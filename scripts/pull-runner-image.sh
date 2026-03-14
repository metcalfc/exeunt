#!/usr/bin/env bash
set -euo pipefail

# Pre-pull the runner image so new containers start instantly.
# Run via cron on Docker hosts: 0 * * * * /usr/local/bin/pull-runner-image.sh

IMAGE="${1:-ghcr.io/metcalfc/exeunt-runner:latest}"

LOCAL_DIGEST=$(docker inspect --format='{{index .RepoDigests 0}}' "$IMAGE" 2>/dev/null | cut -d@ -f2 || echo "none")
docker pull -q "$IMAGE"
NEW_DIGEST=$(docker inspect --format='{{index .RepoDigests 0}}' "$IMAGE" 2>/dev/null | cut -d@ -f2 || echo "unknown")

if [[ "$LOCAL_DIGEST" != "$NEW_DIGEST" ]]; then
  echo "$(date -Iseconds) updated $IMAGE ($NEW_DIGEST)"
else
  echo "$(date -Iseconds) $IMAGE up to date"
fi
