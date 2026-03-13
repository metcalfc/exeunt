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
OUR_DOCKERFILE="$(cd "$(dirname "$0")/.." && pwd)/runner-image/Dockerfile"
IMAGE="ghcr.io/metcalfc/exeunt-runner"

ERRORS=0
WARNINGS=0

info()  { echo "  $*"; }
ok()    { echo "  ✓ $*"; }
warn()  { echo "  ! $*"; WARNINGS=$((WARNINGS + 1)); }
fail()  { echo "  ✗ $*"; ERRORS=$((ERRORS + 1)); }
debug() { $VERBOSE && echo "  … $*" || true; }

echo "=== Upstream exeuntu compatibility ==="

UPSTREAM=$(curl -fsSL "$UPSTREAM_RAW" 2>/dev/null) || {
  fail "Could not fetch upstream Dockerfile from ${UPSTREAM_RAW}"
  UPSTREAM=""
}

if [[ -n "$UPSTREAM" ]]; then
  # Check base image — our FROM must match upstream's final FROM
  UPSTREAM_BASE=$(echo "$UPSTREAM" | grep -E '^FROM ' | tail -1 | awk '{print $2}')
  OUR_BASE=$(grep -E '^FROM ' "$OUR_DOCKERFILE" | head -1 | awk '{print $2}')
  if [[ "$OUR_BASE" == "$UPSTREAM_REPO" ]] || [[ "$OUR_BASE" == "docker.io/$UPSTREAM_REPO" ]]; then
    ok "FROM references upstream image ($OUR_BASE)"
  else
    warn "FROM mismatch: ours=$OUR_BASE, expected=$UPSTREAM_REPO"
  fi

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
  # The install list spans many lines, so just check if curl appears anywhere
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

LATEST_RUNNER=$(curl -fsSL "https://api.github.com/repos/actions/runner/releases/latest" 2>/dev/null \
  | jq -r '.tag_name' | sed 's/^v//') || {
  fail "Could not fetch latest runner version from GitHub"
  LATEST_RUNNER=""
}

if [[ -n "$LATEST_RUNNER" ]]; then
  info "Latest runner version: v${LATEST_RUNNER}"

  # Check what we've published to GHCR
  if docker manifest inspect "${IMAGE}:v${LATEST_RUNNER}" > /dev/null 2>&1; then
    ok "GHCR has image tagged v${LATEST_RUNNER}"
  else
    warn "GHCR missing v${LATEST_RUNNER} — run build-runner-image workflow"
  fi

  # Check latest tag
  if docker manifest inspect "${IMAGE}:latest" > /dev/null 2>&1; then
    ok "GHCR has :latest tag"
  else
    warn "GHCR missing :latest tag"
  fi
fi

echo ""
echo "=== Local Dockerfile check ==="

if [[ -f "$OUR_DOCKERFILE" ]]; then
  ok "runner-image/Dockerfile exists"

  # Verify it doesn't override CMD (exe.dev needs base image's init)
  if grep -qE '^CMD ' "$OUR_DOCKERFILE"; then
    fail "Dockerfile overrides CMD — exe.dev requires the base image's systemd init"
  else
    ok "No CMD override (inherits base image init)"
  fi

  # Verify it doesn't override ENTRYPOINT
  if grep -qE '^ENTRYPOINT ' "$OUR_DOCKERFILE"; then
    fail "Dockerfile overrides ENTRYPOINT — exe.dev requires the base image's init"
  else
    ok "No ENTRYPOINT override"
  fi

  # Verify runner path matches what setup-exe-runner expects
  if grep -q '/home/exedev/actions-runner' "$OUR_DOCKERFILE"; then
    ok "Runner path matches setup action (~/actions-runner)"
  else
    fail "Runner install path doesn't match setup-exe-runner expectation"
  fi

  # Verify ownership is set to exedev
  if grep -q 'chown.*exedev:exedev.*actions-runner' "$OUR_DOCKERFILE"; then
    ok "Runner directory owned by exedev"
  else
    warn "Runner directory ownership may not be set correctly"
  fi
else
  fail "runner-image/Dockerfile not found"
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
