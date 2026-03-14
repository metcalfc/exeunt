#!/usr/bin/env bash
set -euo pipefail

# Exeunt infrastructure monitor. Runs on a timer, checks autoscaler
# and fleet health, emails on failure via exe.dev gateway.

ALERT_EMAIL="${ALERT_EMAIL:-metcalfc@gmail.com}"
GATEWAY="http://169.254.169.254/gateway/email/send"
STATE_DIR="/var/lib/exeunt-monitor"
AUTOSCALER_URL="http://localhost:8080"

# Track alert state to avoid spamming — only alert on transitions.
mkdir -p "$STATE_DIR"

alert() {
  local subject="$1" body="$2"
  curl -sf -X POST "$GATEWAY" \
    -H "Content-Type: application/json" \
    -d "$(jq -n --arg to "$ALERT_EMAIL" --arg s "$subject" --arg b "$body" \
      '{to: $to, subject: $s, body: $b}')" >/dev/null 2>&1 || true
}

# Only alert once per issue. Clear when resolved.
alert_once() {
  local key="$1" subject="$2" body="$3"
  local state_file="$STATE_DIR/$key"
  if [[ ! -f "$state_file" ]]; then
    alert "$subject" "$body"
    date -u > "$state_file"
  fi
}

clear_alert() {
  local key="$1"
  rm -f "$STATE_DIR/$key"
}

problems=()

# --- Check 1: Autoscaler process ---
if systemctl is-active --quiet exeunt-autoscaler; then
  clear_alert "autoscaler-down"
else
  problems+=("autoscaler is down")
  alert_once "autoscaler-down" \
    "[exeunt] autoscaler is down" \
    "exeunt-autoscaler service is not running on $(hostname).
Check: ssh exeunt.exe.xyz 'journalctl -u exeunt-autoscaler -n 50'"
fi

# --- Check 2: Autoscaler responding ---
if health=$(curl -sf --max-time 5 "$AUTOSCALER_URL/healthz" 2>/dev/null); then
  clear_alert "autoscaler-unhealthy"
else
  problems+=("autoscaler not responding on $AUTOSCALER_URL")
  alert_once "autoscaler-unhealthy" \
    "[exeunt] autoscaler not responding" \
    "GET /healthz failed. Service may be hung.
Check: ssh exeunt.exe.xyz 'systemctl status exeunt-autoscaler'"
fi

# --- Check 3: Autoscaler errors in last 10 minutes ---
error_count=$(journalctl -u exeunt-autoscaler --since "10 min ago" --no-pager 2>/dev/null \
  | grep -c '"level":"ERROR"' || true)
if [[ "$error_count" -gt 10 ]]; then
  problems+=("$error_count errors in last 10 min")
  alert_once "autoscaler-errors" \
    "[exeunt] autoscaler error spike ($error_count in 10 min)" \
    "$(journalctl -u exeunt-autoscaler --since '10 min ago' --no-pager 2>/dev/null \
      | grep '"level":"ERROR"' | tail -5)"
else
  clear_alert "autoscaler-errors"
fi

# --- Check 4: Backend connectivity (boxloader) ---
if tailscale ssh metcalfc@boxloader "true" 2>/dev/null; then
  clear_alert "boxloader-unreachable"

  # --- Check 5: Boxloader disk space ---
  disk_pct=$(tailscale ssh metcalfc@boxloader "df / --output=pcent | tail -1 | tr -d ' %'" 2>/dev/null || echo "0")
  if [[ "$disk_pct" -gt 85 ]]; then
    problems+=("boxloader disk at ${disk_pct}%")
    alert_once "boxloader-disk" \
      "[exeunt] boxloader disk at ${disk_pct}%" \
      "Disk usage on boxloader is ${disk_pct}%. Consider pruning docker images:
ssh boxloader 'docker system prune -f'"
  else
    clear_alert "boxloader-disk"
  fi

  # --- Check 6: Orphaned containers ---
  tracker_vms=$(cat /var/lib/exeunt-autoscaler/state.json 2>/dev/null \
    | jq -r '.[].vm_name' 2>/dev/null | sort || true)
  backend_vms=$(tailscale ssh metcalfc@boxloader \
    "docker ps --filter name=exeunt- --format '{{.Names}}'" 2>/dev/null | sort || true)

  orphans=""
  if [[ -n "$backend_vms" ]]; then
    orphans=$(comm -23 <(echo "$backend_vms") <(echo "$tracker_vms") || true)
  fi

  if [[ -n "$orphans" ]]; then
    orphan_count=$(echo "$orphans" | wc -l | tr -d ' ')
    problems+=("$orphan_count orphaned containers")
    alert_once "orphaned-containers" \
      "[exeunt] $orphan_count orphaned container(s) on boxloader" \
      "Containers not in autoscaler tracker:
$orphans

These leak resources. The reconciler should clean them up within 5 minutes.
If this persists, check: ssh exeunt.exe.xyz 'journalctl -u exeunt-autoscaler -n 50'"
  else
    clear_alert "orphaned-containers"
  fi
else
  problems+=("boxloader unreachable via tailscale")
  alert_once "boxloader-unreachable" \
    "[exeunt] boxloader unreachable" \
    "Cannot SSH to boxloader via tailscale. Check:
- Is boxloader online? (tailscale status)
- Tailscale ACLs allowing exeunt -> boxloader?"
fi

# --- Check 7: Stale tracker entries ---
stale_count=0
if [[ -f /var/lib/exeunt-autoscaler/state.json ]]; then
  now=$(date +%s)
  while IFS= read -r line; do
    created=$(echo "$line" | jq -r '.created_at' 2>/dev/null || true)
    status=$(echo "$line" | jq -r '.status' 2>/dev/null || true)
    if [[ "$status" == "ready" && -n "$created" ]]; then
      created_epoch=$(date -d "$created" +%s 2>/dev/null || date -j -f "%Y-%m-%dT%H:%M:%SZ" "$created" +%s 2>/dev/null || echo "0")
      age=$(( now - created_epoch ))
      if [[ "$age" -gt 600 ]]; then
        stale_count=$((stale_count + 1))
      fi
    fi
  done < <(jq -c '.[]' /var/lib/exeunt-autoscaler/state.json 2>/dev/null || true)
fi

if [[ "$stale_count" -gt 0 ]]; then
  problems+=("$stale_count stale tracker entries")
  alert_once "stale-tracker" \
    "[exeunt] $stale_count stale tracker entries" \
    "Entries stuck in 'ready' for >10 min. Runner process likely exited.
The reconciler should clean these up. If this persists, restart:
ssh exeunt.exe.xyz 'sudo systemctl restart exeunt-autoscaler'"
else
  clear_alert "stale-tracker"
fi

# --- Summary ---
if [[ ${#problems[@]} -eq 0 ]]; then
  echo "OK — all checks passed"
else
  echo "PROBLEMS (${#problems[@]}):"
  for p in "${problems[@]}"; do
    echo "  - $p"
  done
  exit 1
fi
