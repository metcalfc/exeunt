#!/usr/bin/env bash
set -euo pipefail

# Reaper: cross-references exe.dev VMs against GitHub runners to find and
# destroy orphaned exeunt-* VMs that leaked due to failed teardowns.

REPO="${GITHUB_REPOSITORY:-}"
TOKEN="${GITHUB_TOKEN:-}"
DRY_RUN=false

usage() {
  cat >&2 <<EOF
Usage: $(basename "$0") [OPTIONS]

Reap orphaned exeunt-* VMs by cross-referencing exe.dev against GitHub runners.

Options:
  --repo OWNER/REPO   GitHub repository (default: \$GITHUB_REPOSITORY)
  --token TOKEN        GitHub token (default: \$GITHUB_TOKEN)
  --dry-run            Log what would be reaped without destroying anything
  -h, --help           Show this help
EOF
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo)   REPO="$2"; shift 2 ;;
    --token)  TOKEN="$2"; shift 2 ;;
    --dry-run) DRY_RUN=true; shift ;;
    -h|--help) usage ;;
    *) echo "Unknown option: $1" >&2; usage ;;
  esac
done

if [[ -z "$REPO" ]]; then
  echo "Error: --repo or \$GITHUB_REPOSITORY required" >&2
  exit 1
fi

if [[ -z "$TOKEN" ]]; then
  echo "Error: --token or \$GITHUB_TOKEN required" >&2
  exit 1
fi

# --- SSH setup (CI only) ---
setup_ssh() {
  if [[ "${GITHUB_ACTIONS:-}" != "true" ]]; then
    return
  fi

  local key="${EXE_SSH_KEY:-}"
  if [[ -z "$key" ]]; then
    echo "Error: \$EXE_SSH_KEY required in CI" >&2
    exit 1
  fi

  mkdir -p ~/.ssh
  chmod 700 ~/.ssh
  printf '%s\n' "$key" > ~/.ssh/exe_dev_key
  chmod 600 ~/.ssh/exe_dev_key
  {
    echo "Host exe.dev *.exe.xyz"
    echo "  IdentitiesOnly yes"
    echo "  IdentityFile ~/.ssh/exe_dev_key"
    echo "  StrictHostKeyChecking accept-new"
    echo "  UserKnownHostsFile ~/.ssh/known_hosts"
  } >> ~/.ssh/config
  chmod 600 ~/.ssh/config
}

# --- GitHub API helper ---
gh_api() {
  local endpoint="$1"
  local method="${2:-GET}"

  local http_code body
  body=$(mktemp)
  http_code=$(curl -s -o "$body" -w '%{http_code}' -X "$method" \
    -H "Authorization: Bearer $TOKEN" \
    -H "Accept: application/vnd.github+json" \
    -H "X-GitHub-Api-Version: 2022-11-28" \
    "https://api.github.com${endpoint}")

  if [[ "$http_code" != "2"* ]]; then
    echo "API error (HTTP $http_code) on $method $endpoint:" >&2
    cat "$body" >&2
    rm -f "$body"
    return 1
  fi

  cat "$body"
  rm -f "$body"
}

# --- Main ---
setup_ssh

echo "=== Exeunt VM Reaper ==="
echo "Repo: $REPO"
echo "Dry run: $DRY_RUN"
echo ""

# 1. List exe.dev VMs matching exeunt-*
echo "Fetching exe.dev VMs..."
VM_JSON=$(ssh -n exe.dev ls --json) || {
  echo "Error: failed to list exe.dev VMs" >&2
  exit 1
}

EXEUNT_VMS=$(echo "$VM_JSON" | jq -r '.vms[]? | select(.vm_name | startswith("exeunt-")) | .vm_name')

if [[ -z "$EXEUNT_VMS" ]]; then
  echo "No exeunt-* VMs found. Nothing to do."
  exit 0
fi

VM_COUNT=$(echo "$EXEUNT_VMS" | wc -l | tr -d ' ')
echo "Found $VM_COUNT exeunt-* VM(s)"

# 2. Fetch GitHub runners matching exeunt-*
echo "Fetching GitHub runners..."
RUNNERS_JSON=$(gh_api "/repos/${REPO}/actions/runners") || {
  echo "Error: failed to list GitHub runners" >&2
  exit 1
}

# Build a lookup: runner_name -> "id:status"
declare -A RUNNER_MAP
while IFS=$'\t' read -r name id status; do
  [[ -n "$name" ]] && RUNNER_MAP["$name"]="${id}:${status}"
done < <(echo "$RUNNERS_JSON" | jq -r '.runners[]? | select(.name | startswith("exeunt-")) | [.name, (.id | tostring), .status] | @tsv')

# 3. Classify each VM
ACTIVE=0
REAPED=0
FAILED=0

echo ""
while IFS= read -r vm; do
  runner_info="${RUNNER_MAP[$vm]:-}"

  if [[ -n "$runner_info" ]]; then
    runner_id="${runner_info%%:*}"
    runner_status="${runner_info##*:}"

    if [[ "$runner_status" == "online" ]]; then
      echo "[ACTIVE]  $vm (runner online)"
      ACTIVE=$((ACTIVE + 1))
      continue
    fi

    # Runner exists but offline → orphaned
    echo "[ORPHAN]  $vm (runner offline, id=$runner_id)"
  else
    # No runner registered at all → orphaned
    echo "[ORPHAN]  $vm (no runner registered)"
    runner_id=""
  fi

  if [[ "$DRY_RUN" == "true" ]]; then
    echo "  → would reap (dry-run)"
    REAPED=$((REAPED + 1))
    continue
  fi

  # Reap: destroy VM
  if ssh -n exe.dev rm "$vm"; then
    echo "  → VM destroyed"
  else
    echo "  → WARNING: failed to destroy VM" >&2
    FAILED=$((FAILED + 1))
    # Continue to try runner cleanup even if VM destroy failed
  fi

  # Reap: delete stale runner registration
  if [[ -n "$runner_id" ]]; then
    if gh_api "/repos/${REPO}/actions/runners/${runner_id}" DELETE >/dev/null; then
      echo "  → runner registration deleted"
    else
      echo "  → WARNING: failed to delete runner registration" >&2
    fi
  fi

  REAPED=$((REAPED + 1))
done <<< "$EXEUNT_VMS"

# 4. Summary
echo ""
echo "=== Summary ==="
echo "Active: $ACTIVE"
echo "Reaped: $REAPED"
echo "Failed: $FAILED"

if [[ "$FAILED" -gt 0 ]]; then
  exit 1
fi
