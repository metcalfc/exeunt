#!/usr/bin/env bats

# Tests for scripts/pull-runner-image.sh
# Uses a mock docker command to test the image freshness logic
# without requiring real Docker image pulls.

SCRIPT="$BATS_TEST_DIRNAME/../scripts/pull-runner-image.sh"

setup() {
  MOCK_BIN="$(mktemp -d)"
  export PATH="$MOCK_BIN:$PATH"
}

teardown() {
  rm -rf "$MOCK_BIN"
}

@test "pull-runner-image: reports updated when digest changes" {
  local call_count=0
  cat > "$MOCK_BIN/docker" <<'MOCK'
#!/usr/bin/env bash
if [[ "$1" == "inspect" ]]; then
  # First call returns old digest, second returns new
  if [[ -f /tmp/bats-pull-done ]]; then
    echo "img@sha256:newdigest"
  else
    echo "img@sha256:olddigest"
  fi
elif [[ "$1" == "pull" ]]; then
  touch /tmp/bats-pull-done
fi
MOCK
  chmod +x "$MOCK_BIN/docker"
  rm -f /tmp/bats-pull-done

  run bash "$SCRIPT" testimage:latest
  rm -f /tmp/bats-pull-done
  [ "$status" -eq 0 ]
  [[ "$output" == *"updated"*"testimage:latest"* ]]
}

@test "pull-runner-image: reports up to date when digest unchanged" {
  cat > "$MOCK_BIN/docker" <<'MOCK'
#!/usr/bin/env bash
if [[ "$1" == "inspect" ]]; then
  echo "img@sha256:samedigest"
elif [[ "$1" == "pull" ]]; then
  true
fi
MOCK
  chmod +x "$MOCK_BIN/docker"

  run bash "$SCRIPT" testimage:latest
  [ "$status" -eq 0 ]
  [[ "$output" == *"up to date"* ]]
}

@test "pull-runner-image: uses default image when no arg given" {
  cat > "$MOCK_BIN/docker" <<'MOCK'
#!/usr/bin/env bash
if [[ "$1" == "inspect" ]]; then
  echo "img@sha256:digest"
elif [[ "$1" == "pull" ]]; then
  # Capture the last arg (image name, after -q flag)
  echo "${*: -1}" > /tmp/bats-pulled-image
fi
MOCK
  chmod +x "$MOCK_BIN/docker"
  rm -f /tmp/bats-pulled-image

  run bash "$SCRIPT"
  [ "$status" -eq 0 ]
  pulled=$(cat /tmp/bats-pulled-image 2>/dev/null)
  [[ "$pulled" == "ghcr.io/metcalfc/exeunt-runner:latest" ]]
  rm -f /tmp/bats-pulled-image
}

@test "pull-runner-image: handles missing local image" {
  cat > "$MOCK_BIN/docker" <<'MOCK'
#!/usr/bin/env bash
if [[ "$1" == "inspect" ]]; then
  if [[ -f /tmp/bats-pull-done ]]; then
    echo "img@sha256:freshdigest"
  else
    # Image not found locally
    echo "Error: No such image" >&2
    exit 1
  fi
elif [[ "$1" == "pull" ]]; then
  touch /tmp/bats-pull-done
fi
MOCK
  chmod +x "$MOCK_BIN/docker"
  rm -f /tmp/bats-pull-done

  run bash "$SCRIPT" newimage:v1
  rm -f /tmp/bats-pull-done
  [ "$status" -eq 0 ]
  [[ "$output" == *"updated"*"newimage:v1"* ]]
}

@test "pull-runner-image: output includes ISO timestamp" {
  cat > "$MOCK_BIN/docker" <<'MOCK'
#!/usr/bin/env bash
if [[ "$1" == "inspect" ]]; then
  echo "img@sha256:d"
elif [[ "$1" == "pull" ]]; then
  true
fi
MOCK
  chmod +x "$MOCK_BIN/docker"

  run bash "$SCRIPT" img:latest
  [ "$status" -eq 0 ]
  # ISO 8601 timestamp like 2026-03-14T...
  [[ "$output" =~ [0-9]{4}-[0-9]{2}-[0-9]{2}T ]]
}
