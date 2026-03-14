#!/usr/bin/env bash
set -euo pipefail

# Alert responder: watches ~/Maildir/new/ for forwarded alert emails,
# runs claude -p to investigate, and emails the outcome back.

MAILDIR="${HOME}/Maildir/new"
PROCESSED="${HOME}/Maildir/cur"
ALERT_EMAIL="${ALERT_EMAIL:-metcalfc@gmail.com}"
GATEWAY="http://169.254.169.254/gateway/email/send"
WORKDIR="/tmp/alert-responder"

mkdir -p "$PROCESSED" "$WORKDIR"

# Process each new email
for mail in "$MAILDIR"/*; do
  [[ -f "$mail" ]] || continue

  filename=$(basename "$mail")
  subject=$(grep -m1 '^Subject:' "$mail" | sed 's/^Subject: *//' || echo "unknown")
  # Get the body (everything after the first blank line)
  body=$(sed -n '/^$/,$p' "$mail" | tail -n +2 || echo "no body")

  echo "Processing: $subject"

  # Build the prompt
  prompt=$(cat <<PROMPT
You are investigating an infrastructure alert for the exeunt autoscaler.

## Alert
Subject: $subject
Body:
$body

## Your task
1. Investigate the issue using the tools available to you
2. If you can fix it with HIGH CONFIDENCE, do so
3. Report what you found and what (if anything) you fixed

## Safety rules — READ CAREFULLY
- NEVER delete or destroy the exeunt VM (this is the autoscaler host you are running on)
- NEVER run \`ssh exe.dev rm exeunt\`
- NEVER stop the exeunt-autoscaler service unless you are immediately restarting it
- You MAY delete orphaned containers on boxloader (\`tailscale ssh metcalfc@boxloader "docker rm -f <name>"\`)
- You MAY delete stale GitHub runner registrations (\`gh api -X DELETE repos/{owner}/{repo}/actions/runners/{id}\`)
- You MAY restart the autoscaler: \`sudo systemctl restart exeunt-autoscaler\`
- You MAY cancel stuck GitHub Actions runs

## Investigation tools
- Autoscaler status: \`systemctl status exeunt-autoscaler\`
- Autoscaler logs: \`journalctl -u exeunt-autoscaler --since '30 min ago' --no-pager\`
- Tracker state: \`cat /var/lib/exeunt-autoscaler/state.json\`
- Boxloader containers: \`tailscale ssh metcalfc@boxloader "docker ps --filter name=exeunt --format '{{.Names}}\t{{.Status}}'"\`
- GitHub runners: \`gh api repos/{owner}/{repo}/actions/runners\`
- Healthz: \`curl -s http://localhost:8080/healthz\`

## Managed repos
- metcalfc/exeunt
- metcalfc/exeuntu
- abrihq/den

## Output
Write a concise summary of:
- What the alert was about
- What you found
- What you fixed (if anything)
- What needs human attention (if anything)
PROMPT
)

  # Run claude in non-interactive mode
  result_file="$WORKDIR/${filename}.result"
  if timeout 120 claude -p "$prompt" > "$result_file" 2>&1; then
    result=$(cat "$result_file")
  else
    result="Claude timed out or errored investigating this alert.

Raw output:
$(cat "$result_file" 2>/dev/null || echo 'no output')"
  fi

  # Email the result back
  reply_subject="Re: $subject"
  if ! curl -sf -X POST "$GATEWAY" \
    -H "Content-Type: application/json" \
    -d "$(jq -n --arg to "$ALERT_EMAIL" --arg s "$reply_subject" --arg b "$result" \
      '{to: $to, subject: $s, body: $b}')" >/dev/null 2>&1; then
    echo "ERROR: failed to send reply for: $subject" >&2
  fi

  # Move to processed
  mv "$mail" "$PROCESSED/${filename}:2,S"
  rm -f "$result_file"

  echo "Done: $subject"
done
