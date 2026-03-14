#!/usr/bin/env bash
set -euo pipefail

# Check runner image compatibility against upstream exeuntu Dockerfile
# and report on runner version freshness.
#
# Usage: scripts/check-runner-image.sh [--verbose]

VERBOSE=false
if [[ "${1:-}" == "--verbose" || "${1:-}" == "-v" ]]; then
  VERBOSE=true
fi

UPSTREAM_REPO="boldsoftware/exeuntu"
UPSTREAM_RAW="https://raw.githubusercontent.com/${UPSTREAM_REPO}/main/Dockerfile"
FORK_REPO="metcalfc/exeuntu"
FORK_RAW="https://raw.githubusercontent.com/${FORK_REPO}/main/Dockerfile"
IMAGE="ghcr.io/metcalfc/exeunt-runner"

ERRORS=0
WARNINGS=0

info()  { echo "  $*"; }
ok()    { echo "  ✓ $*"; }
warn()  { echo "  ! $*"; WARNINGS=$((WARNINGS + 1)); }
fail()  { echo "  ✗ $*"; ERRORS=$((ERRORS + 1)); }
debug() { $VERBOSE && echo "  … $*" || true; }

# Retry a command up to N times with backoff. Usage: retry <attempts> <cmd...>
retry() {
  local attempts=$1; shift
  local delay=2
  for i in $(seq 1 "$attempts"); do
    if "$@"; then
      return 0
    fi
    if [[ $i -lt $attempts ]]; then
      debug "Attempt $i/$attempts failed, retrying in ${delay}s..."
      sleep "$delay"
      delay=$((delay * 2))
    fi
  done
  return 1
}

# Fetch a URL with retries. Output goes to stdout.
fetch() {
  retry 3 curl -fsSL --connect-timeout 10 --max-time 30 "$1"
}

echo "=== Upstream exeuntu compatibility ==="

UPSTREAM=$(fetch "$UPSTREAM_RAW" 2>/dev/null) || {
  warn "Could not fetch upstream Dockerfile from ${UPSTREAM_RAW}"
  UPSTREAM=""
}

if [[ -n "$UPSTREAM" ]]; then
  # Check user still exists and is named exedev
  if echo "$UPSTREAM" | grep -q 'usermod -l exedev'; then
    ok "Upstream still creates 'exedev' user"
  else
    fail "Upstream may have renamed the 'exedev' user — check Dockerfile"
  fi

  # Check home directory is /home/exedev
  if echo "$UPSTREAM" | grep -q '/home/exedev'; then
    ok "Upstream still uses /home/exedev"
  else
    fail "Upstream may have moved the home directory — check Dockerfile"
  fi

  # Check curl is installed (we need it for runner download in Dockerfile)
  if echo "$UPSTREAM" | grep -qw 'curl'; then
    ok "Upstream installs curl"
  else
    warn "Upstream may not install curl anymore — runner download could break"
  fi

  # Check for libicu (required by .NET-based runner)
  if echo "$UPSTREAM" | grep -qiE 'libicu|ubuntu-server'; then
    ok "Upstream likely includes libicu (via ubuntu-server or explicit)"
  else
    warn "libicu may be missing — GitHub Actions runner requires it"
  fi

  # Check upstream base image version
  if echo "$UPSTREAM" | grep -qE '^FROM ubuntu:24\.04'; then
    ok "Upstream base is ubuntu:24.04"
  else
    UPSTREAM_UBUNTU=$(echo "$UPSTREAM" | grep -E '^FROM ubuntu:' | tail -1 | awk '{print $2}')
    warn "Upstream base changed: $UPSTREAM_UBUNTU (was ubuntu:24.04)"
  fi

  # Check systemd init (exe.dev expects it)
  if echo "$UPSTREAM" | grep -q 'systemd'; then
    ok "Upstream includes systemd"
    debug "Our image inherits systemd — no CMD override needed"
  else
    warn "Upstream may have dropped systemd"
  fi
fi

echo ""
echo "=== Runner version status ==="

LATEST_RUNNER=$(fetch "https://api.github.com/repos/actions/runner/releases/latest" 2>/dev/null \
  | jq -r '.tag_name' | sed 's/^v//') || {
  warn "Could not fetch latest runner version from GitHub"
  LATEST_RUNNER=""
}

if [[ -n "$LATEST_RUNNER" ]]; then
  info "Latest runner version: v${LATEST_RUNNER}"

  # Check what we've published to GHCR (retry manifest inspect too)
  if retry 3 docker manifest inspect "${IMAGE}:v${LATEST_RUNNER}" > /dev/null 2>&1; then
    ok "GHCR has image tagged v${LATEST_RUNNER}"
  else
    warn "GHCR missing v${LATEST_RUNNER} — run build-runner-image workflow"
  fi

  if retry 3 docker manifest inspect "${IMAGE}:latest" > /dev/null 2>&1; then
    ok "GHCR has :latest tag"
  else
    warn "GHCR missing :latest tag"
  fi
fi

echo ""
echo "=== Fork Dockerfile check ==="

FORK=$(fetch "$FORK_RAW" 2>/dev/null) || {
  fail "Could not fetch fork Dockerfile from ${FORK_RAW}"
  FORK=""
}

if [[ -n "$FORK" ]]; then
  ok "Fork Dockerfile fetched from ${FORK_REPO}"

  # Check fork base matches upstream base
  FORK_BASE=$(echo "$FORK" | grep -E '^FROM ubuntu:' | head -1 | awk '{print $2}')
  UPSTREAM_UBUNTU=$(echo "$UPSTREAM" | grep -E '^FROM ubuntu:' | tail -1 | awk '{print $2}')
  if [[ -n "$UPSTREAM_UBUNTU" ]] && [[ "$FORK_BASE" == "$UPSTREAM_UBUNTU" ]]; then
    ok "Fork base matches upstream ($FORK_BASE)"
  elif [[ -n "$UPSTREAM_UBUNTU" ]]; then
    warn "Fork base ($FORK_BASE) differs from upstream ($UPSTREAM_UBUNTU) — sync needed"
  fi

  # Verify runner installation is present
  if echo "$FORK" | grep -q '/home/exedev/actions-runner'; then
    ok "Runner path matches setup action (~/actions-runner)"
  else
    fail "Runner install path not found in fork Dockerfile"
  fi

  # Verify ownership is set to exedev
  if echo "$FORK" | grep -q 'chown.*exedev:exedev.*actions-runner'; then
    ok "Runner directory owned by exedev"
  else
    warn "Runner directory ownership may not be set correctly"
  fi

  # Verify RUNNER_VERSION build arg exists
  if echo "$FORK" | grep -q 'ARG RUNNER_VERSION'; then
    ok "RUNNER_VERSION build arg present"
  else
    fail "RUNNER_VERSION build arg missing from fork Dockerfile"
  fi
fi

echo ""
if [[ $ERRORS -gt 0 ]]; then
  echo "Result: ${ERRORS} error(s), ${WARNINGS} warning(s)"
  exit 1
elif [[ $WARNINGS -gt 0 ]]; then
  echo "Result: ${WARNINGS} warning(s), no errors"
  exit 0
else
  echo "Result: all checks passed"
  exit 0
fi
