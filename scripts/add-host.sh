#!/usr/bin/env bash
set -euo pipefail

# Probe a host via SSH, gather hardware stats, and output a backend
# config block for deploy/config.local.json.
#
# Usage: scripts/add-host.sh <hostname> [--user USER] [--labels LABELS] [--priority N] [--reserve PCT]
#
# The host must be reachable via tailscale ssh.

HOST=""
USER="metcalfc"
LABELS="exe"
PRIORITY=1
RESERVE=25  # percent reserved for host overhead

usage() {
  cat >&2 <<EOF
Usage: $(basename "$0") <hostname> [OPTIONS]

Probe a Docker host and generate an autoscaler backend config.

Options:
  --user USER       SSH user (default: metcalfc)
  --labels LABELS   Comma-separated runner labels (default: exe)
  --priority N      Backend priority, lower = preferred (default: 1)
  --reserve PCT     Percent of resources reserved for host (default: 25)
  -h, --help        Show this help
EOF
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --user)     USER="$2"; shift 2 ;;
    --labels)   LABELS="$2"; shift 2 ;;
    --priority) PRIORITY="$2"; shift 2 ;;
    --reserve)  RESERVE="$2"; shift 2 ;;
    -h|--help)  usage ;;
    -*)         echo "Unknown option: $1" >&2; usage ;;
    *)          HOST="$1"; shift ;;
  esac
done

if [[ -z "$HOST" ]]; then
  echo "Error: hostname required" >&2
  usage
fi

echo "Probing $HOST via tailscale ssh..."
echo ""

# --- Gather stats ---
ssh_cmd() {
  tailscale ssh "${USER}@${HOST}" "$1" 2>/dev/null
}

cpus=$(ssh_cmd "nproc")
mem_total_kb=$(ssh_cmd "grep MemTotal /proc/meminfo | awk '{print \$2}'")
mem_total_gb=$(( mem_total_kb / 1024 / 1024 ))

disk_info=$(ssh_cmd "df / --output=size,avail,pcent | tail -1")
disk_total=$(echo "$disk_info" | awk '{print $1}')
disk_avail=$(echo "$disk_info" | awk '{print $2}')
disk_pct=$(echo "$disk_info" | awk '{print $3}' | tr -d '%')
disk_total_gb=$(( disk_total / 1024 / 1024 ))
disk_avail_gb=$(( disk_avail / 1024 / 1024 ))

gpu_info=$(ssh_cmd "nvidia-smi --query-gpu=name,memory.total --format=csv,noheader 2>/dev/null" || echo "none")

docker_version=$(ssh_cmd "docker version --format '{{.Server.Version}}'" || echo "not installed")

# --- Calculate runner capacity ---
# Reserve RESERVE% for the host, use the rest for runners.
# Each runner gets ~2 CPU cores and ~2GB RAM (Go builds, linting).
available_pct=$(( 100 - RESERVE ))
available_cpus=$(( cpus * available_pct / 100 ))
available_mem_gb=$(( mem_total_gb * available_pct / 100 ))

# Runners are CPU-bound (Go builds). 2 cores per runner is comfortable.
max_by_cpu=$(( available_cpus / 2 ))
# Memory: 2GB per runner for Go builds + test suites.
max_by_mem=$(( available_mem_gb / 2 ))

# Take the lower of CPU and memory limits.
if [[ "$max_by_cpu" -lt "$max_by_mem" ]]; then
  max_runners=$max_by_cpu
else
  max_runners=$max_by_mem
fi

# Floor at 1.
if [[ "$max_runners" -lt 1 ]]; then
  max_runners=1
fi

# --- Build labels JSON ---
IFS=',' read -ra label_arr <<< "$LABELS"
labels_json=$(printf '%s\n' "${label_arr[@]}" | jq -R . | jq -s .)

# --- Report ---
echo "=== Host: $HOST ==="
echo ""
echo "  CPU:       $cpus cores"
echo "  Memory:    ${mem_total_gb}GB"
echo "  Disk:      ${disk_total_gb}GB total, ${disk_avail_gb}GB free (${disk_pct}% used)"
echo "  GPU:       $gpu_info"
echo "  Docker:    $docker_version"
echo ""
echo "=== Capacity (${RESERVE}% reserved) ==="
echo ""
echo "  Available: $available_cpus cores, ${available_mem_gb}GB RAM"
echo "  Per runner: ~2 cores, ~2GB RAM"
echo "  Max runners: $max_runners (limited by $([ "$max_by_cpu" -lt "$max_by_mem" ] && echo "CPU" || echo "memory"))"
echo ""

# --- Output config block ---
has_gpu="false"
gpu_labels=""
if [[ "$gpu_info" != "none" ]]; then
  has_gpu="true"
  # Add exe-gpu label if GPU is present and not already in labels.
  if [[ ! "$LABELS" == *"exe-gpu"* ]]; then
    labels_json=$(echo "$labels_json" | jq '. + ["exe-gpu"]')
  fi
fi

config=$(jq -n \
  --arg name "$HOST" \
  --arg host "$HOST" \
  --arg user "$USER" \
  --argjson max "$max_runners" \
  --argjson labels "$labels_json" \
  --argjson priority "$PRIORITY" \
  '{
    name: $name,
    type: "docker",
    host: $host,
    user: $user,
    max_runners: $max,
    labels: $labels,
    priority: $priority
  }')

echo "=== Config block ==="
echo ""
echo "$config" | jq .

# --- Update config file if it exists ---
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
CONFIG_FILE="$REPO_ROOT/deploy/config.local.json"
if [[ ! -f "$CONFIG_FILE" ]]; then
  CONFIG_FILE="$REPO_ROOT/deploy/config.json"
fi

if [[ -f "$CONFIG_FILE" ]]; then
  existing=$(jq --arg name "$HOST" '.backends[] | select(.name == $name)' "$CONFIG_FILE" 2>/dev/null || true)
  if [[ -n "$existing" ]]; then
    echo "Updating existing entry for '$HOST' in $CONFIG_FILE"
    jq --arg name "$HOST" --argjson new "$config" \
      '.backends = [.backends[] | if .name == $name then $new else . end]' \
      "$CONFIG_FILE" > "${CONFIG_FILE}.tmp" && mv "${CONFIG_FILE}.tmp" "$CONFIG_FILE"
  else
    echo "Adding new entry for '$HOST' to $CONFIG_FILE"
    jq --argjson new "$config" '.backends += [$new]' \
      "$CONFIG_FILE" > "${CONFIG_FILE}.tmp" && mv "${CONFIG_FILE}.tmp" "$CONFIG_FILE"
  fi
  echo "Updated $CONFIG_FILE"
else
  echo ""
  echo "No config file found. Add this to the 'backends' array in deploy/config.local.json"
fi

if [[ "$has_gpu" == "true" ]]; then
  echo ""
  echo "Note: GPU detected ($gpu_info). Added 'exe-gpu' label."
fi
