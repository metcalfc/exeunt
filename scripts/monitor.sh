#!/usr/bin/env bash
set -euo pipefail

# Exeunt infrastructure monitor. Runs on a timer, checks autoscaler
# and fleet health, emails on failure via exe.dev gateway.

ALERT_EMAIL="${ALERT_EMAIL:-metcalfc@gmail.com}"
GATEWAY="http://169.254.169.254/gateway/email/send"
STATE_DIR="/var/lib/exeunt-monitor"
AUTOSCALER_URL="http://localhost:8080"
HOST=$(hostname)
TIMESTAMP=$(date -u '+%Y-%m-%d %H:%M UTC')

# Managed repos
REPOS=("metcalfc/exeunt" "metcalfc/exeuntu" "abrihq/den")

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
  local state_file="$STATE_DIR/$key"
  if [[ -f "$state_file" ]]; then
    alert "[exeunt] RESOLVED: ${key//-/ }" \
"Previously firing alert has cleared.

  Host:     $HOST
  Resolved: $TIMESTAMP

To investigate, forward this email to: alerts@exeunt.exe.xyz"
    rm -f "$state_file"
  fi
}

# --- Gather context once for richer alerts ---
uptime_info=$(uptime 2>/dev/null | sed 's/^ *//' || echo "unknown")
healthz=$(curl -sf --max-time 5 "$AUTOSCALER_URL/healthz" 2>/dev/null || echo '{}')
active_vms=$(echo "$healthz" | jq -r '.active_vms // "?"')
max_vms=$(echo "$healthz" | jq -r '.max_vms // "?"')
autoscaler_uptime=$(echo "$healthz" | jq -r '.uptime // "?"')

tracker_json=$(cat /var/lib/exeunt-autoscaler/state.json 2>/dev/null || echo '[]')
tracker_count=$(echo "$tracker_json" | jq 'length' 2>/dev/null || echo "0")

problems=()

# =========================================================================
# Check 1: Autoscaler process
# =========================================================================
if systemctl is-active --quiet exeunt-autoscaler; then
  clear_alert "autoscaler-down"
else
  problems+=("autoscaler is down")
  recent_logs=$(journalctl -u exeunt-autoscaler --no-pager -n 10 2>/dev/null || echo "no logs available")
  alert_once "autoscaler-down" \
    "[exeunt] CRITICAL: autoscaler is down" \
"The exeunt-autoscaler service is not running. No runners will be
provisioned until this is resolved.

  Host:   $HOST
  Time:   $TIMESTAMP
  System: $uptime_info

RECENT LOGS:
$recent_logs

TO FIX:
  ssh exeunt.exe.xyz 'sudo systemctl restart exeunt-autoscaler'

Or forward this email to: alerts@exeunt.exe.xyz"
fi

# =========================================================================
# Check 2: Autoscaler responding
# =========================================================================
if [[ "$healthz" != "{}" ]]; then
  clear_alert "autoscaler-unhealthy"
else
  problems+=("autoscaler not responding")
  alert_once "autoscaler-unhealthy" \
    "[exeunt] CRITICAL: autoscaler not responding" \
"The autoscaler process may be running but is not responding to
HTTP requests. It may be hung or crashed.

  Host:   $HOST
  Time:   $TIMESTAMP
  URL:    $AUTOSCALER_URL/healthz

TO FIX:
  ssh exeunt.exe.xyz 'sudo systemctl restart exeunt-autoscaler'

Or forward this email to: alerts@exeunt.exe.xyz"
fi

# =========================================================================
# Check 3: Error rate spike
# =========================================================================
error_count=$(journalctl -u exeunt-autoscaler --since "10 min ago" --no-pager 2>/dev/null \
  | grep -c '"level":"ERROR"' || true)
if [[ "$error_count" -gt 10 ]]; then
  problems+=("$error_count errors in last 10 min")
  recent_errors=$(journalctl -u exeunt-autoscaler --since '10 min ago' --no-pager 2>/dev/null \
    | grep '"level":"ERROR"' | tail -5 | while read -r line; do
      msg=$(echo "$line" | grep -o '"msg":"[^"]*"' | head -1)
      err=$(echo "$line" | grep -o '"error":"[^"]*"' | head -1)
      echo "  - $msg $err"
    done)
  alert_once "autoscaler-errors" \
    "[exeunt] WARNING: error spike ($error_count errors in 10 min)" \
"The autoscaler is generating errors at a high rate.

  Host:      $HOST
  Time:      $TIMESTAMP
  Errors:    $error_count in last 10 minutes
  Uptime:    $autoscaler_uptime
  Active:    $active_vms / $max_vms runners

RECENT ERRORS:
$recent_errors

Or forward this email to: alerts@exeunt.exe.xyz"
else
  clear_alert "autoscaler-errors"
fi

# =========================================================================
# Check 4: Backend connectivity (boxloader)
# =========================================================================
if tailscale ssh metcalfc@boxloader "true" 2>/dev/null; then
  clear_alert "boxloader-unreachable"

  # --- Check 5: Boxloader disk space ---
  disk_pct=$(tailscale ssh metcalfc@boxloader "df / --output=pcent | tail -1 | tr -d ' %'" 2>/dev/null || echo "0")
  docker_df=$(tailscale ssh metcalfc@boxloader "docker system df --format 'table {{.Type}}\t{{.Size}}\t{{.Reclaimable}}'" 2>/dev/null || echo "unavailable")
  if [[ "$disk_pct" -gt 85 ]]; then
    problems+=("boxloader disk at ${disk_pct}%")
    alert_once "boxloader-disk" \
      "[exeunt] WARNING: boxloader disk at ${disk_pct}%" \
"Disk usage on boxloader is getting high.

  Host:  boxloader
  Time:  $TIMESTAMP
  Disk:  ${disk_pct}% used

DOCKER DISK USAGE:
$docker_df

TO FIX:
  ssh boxloader 'docker system prune -f'
  ssh boxloader 'docker image prune -a -f'

Or forward this email to: alerts@exeunt.exe.xyz"
  else
    clear_alert "boxloader-disk"
  fi

  # --- Check 6: Orphaned containers ---
  tracker_vms=$(echo "$tracker_json" | jq -r '.[].vm_name' 2>/dev/null | sort || true)
  backend_vms=$(tailscale ssh metcalfc@boxloader \
    "docker ps --filter name=exeunt- --format '{{.Names}}\t{{.Status}}'" 2>/dev/null || true)
  backend_names=$(echo "$backend_vms" | awk '{print $1}' | sort || true)

  orphans=""
  if [[ -n "$backend_names" ]]; then
    orphans=$(comm -23 <(echo "$backend_names") <(echo "$tracker_vms") || true)
  fi

  if [[ -n "$orphans" ]]; then
    orphan_count=$(echo "$orphans" | wc -l | tr -d ' ')
    orphan_detail=$(for name in $orphans; do
      echo "$backend_vms" | grep "^$name" || echo "  $name (status unknown)"
    done)
    problems+=("$orphan_count orphaned containers")
    alert_once "orphaned-containers" \
      "[exeunt] WARNING: $orphan_count orphaned container(s) on boxloader" \
"Containers exist on boxloader but are not tracked by the autoscaler.
The reconciler should clean these up within 5 minutes.

  Host:     boxloader
  Time:     $TIMESTAMP
  Tracked:  $tracker_count entries
  Backend:  $(echo "$backend_names" | wc -l | tr -d ' ') containers
  Orphaned: $orphan_count

ORPHANED CONTAINERS:
$orphan_detail

If this persists, the reconciler may be broken.

Forward this email to: alerts@exeunt.exe.xyz"
  else
    clear_alert "orphaned-containers"
  fi
else
  problems+=("boxloader unreachable via tailscale")
  ts_status=$(tailscale status 2>/dev/null | head -5 || echo "tailscale status unavailable")
  alert_once "boxloader-unreachable" \
    "[exeunt] CRITICAL: boxloader unreachable" \
"Cannot SSH to boxloader via tailscale. No runners can be provisioned.

  Host:   $HOST
  Time:   $TIMESTAMP

TAILSCALE STATUS:
$ts_status

POSSIBLE CAUSES:
  - Boxloader machine is offline or rebooting
  - Tailscale ACLs changed (need exeunt -> boxloader SSH)
  - Tailscale service down on boxloader

Or forward this email to: alerts@exeunt.exe.xyz"
fi

# =========================================================================
# Check 7: Stale tracker entries
# =========================================================================
stale_entries=()
if [[ "$tracker_json" != "[]" ]]; then
  now=$(date +%s)
  while IFS= read -r line; do
    created=$(echo "$line" | jq -r '.created_at' 2>/dev/null || true)
    status=$(echo "$line" | jq -r '.status' 2>/dev/null || true)
    vm=$(echo "$line" | jq -r '.vm_name' 2>/dev/null || true)
    repo=$(echo "$line" | jq -r '.repo' 2>/dev/null || true)
    job_id=$(echo "$line" | jq -r '.job_id' 2>/dev/null || true)
    if [[ "$status" == "ready" && -n "$created" ]]; then
      created_epoch=$(date -d "$created" +%s 2>/dev/null || date -j -f "%Y-%m-%dT%H:%M:%SZ" "$created" +%s 2>/dev/null || echo "0")
      age=$(( now - created_epoch ))
      if [[ "$age" -gt 600 ]]; then
        age_min=$(( age / 60 ))
        stale_entries+=("  $vm  repo=$repo  job=$job_id  age=${age_min}m  status=$status")
      fi
    fi
  done < <(jq -c '.[]' <<< "$tracker_json" 2>/dev/null || true)
fi

if [[ ${#stale_entries[@]} -gt 0 ]]; then
  stale_count=${#stale_entries[@]}
  stale_detail=$(printf '%s\n' "${stale_entries[@]}")
  problems+=("$stale_count stale tracker entries")
  alert_once "stale-tracker" \
    "[exeunt] WARNING: $stale_count stale tracker entries" \
"Runner entries stuck in 'ready' for >10 min. The runner process likely
exited but the container is still alive. The reconciler should clean
these up automatically.

  Host:    $HOST
  Time:    $TIMESTAMP
  Tracked: $tracker_count total, $stale_count stale

STALE ENTRIES:
$stale_detail

If this persists, restart the autoscaler:
  ssh exeunt.exe.xyz 'sudo systemctl restart exeunt-autoscaler'

Or forward this email to: alerts@exeunt.exe.xyz"
else
  clear_alert "stale-tracker"
fi

# =========================================================================
# Summary
# =========================================================================
if [[ ${#problems[@]} -eq 0 ]]; then
  echo "OK — all checks passed ($TIMESTAMP)"
  echo "  autoscaler: up $autoscaler_uptime, $active_vms/$max_vms runners"
  echo "  tracker:    $tracker_count entries"
else
  echo "PROBLEMS (${#problems[@]}) at $TIMESTAMP:"
  for p in "${problems[@]}"; do
    echo "  - $p"
  done
  exit 1
fi
