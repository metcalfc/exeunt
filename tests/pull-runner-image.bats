#!/usr/bin/env bats

# Tests for scripts/pull-runner-image.sh
# Uses a mock docker command to test the image freshness logic
# without requiring real Docker image pulls.

SCRIPT="$BATS_TEST_DIRNAME/../scripts/pull-runner-image.sh"

setup() {
  MOCK_BIN="$(mktemp -d)"
  BATS_STATE="$(mktemp -d)"
  export PATH="$MOCK_BIN:$PATH"
}

teardown() {
  rm -rf "$MOCK_BIN" "$BATS_STATE"
}

@test "pull-runner-image: reports updated when digest changes" {
  cat > "$MOCK_BIN/docker" <<MOCK
#!/usr/bin/env bash
if [[ "\$1" == "inspect" ]]; then
  if [[ -f "$BATS_STATE/pull-done" ]]; then
    echo "img@sha256:newdigest"
  else
    echo "img@sha256:olddigest"
  fi
elif [[ "\$1" == "pull" ]]; then
  touch "$BATS_STATE/pull-done"
fi
MOCK
  chmod +x "$MOCK_BIN/docker"

  run bash "$SCRIPT" testimage:latest
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
  cat > "$MOCK_BIN/docker" <<MOCK
#!/usr/bin/env bash
if [[ "\$1" == "inspect" ]]; then
  echo "img@sha256:digest"
elif [[ "\$1" == "pull" ]]; then
  echo "\${*: -1}" > "$BATS_STATE/pulled-image"
fi
MOCK
  chmod +x "$MOCK_BIN/docker"

  run bash "$SCRIPT"
  [ "$status" -eq 0 ]
  pulled=$(cat "$BATS_STATE/pulled-image" 2>/dev/null)
  [[ "$pulled" == "ghcr.io/metcalfc/exeunt-runner:latest" ]]
}

@test "pull-runner-image: handles missing local image" {
  cat > "$MOCK_BIN/docker" <<MOCK
#!/usr/bin/env bash
if [[ "\$1" == "inspect" ]]; then
  if [[ -f "$BATS_STATE/pull-done" ]]; then
    echo "img@sha256:freshdigest"
  else
    echo "Error: No such image" >&2
    exit 1
  fi
elif [[ "\$1" == "pull" ]]; then
  touch "$BATS_STATE/pull-done"
fi
MOCK
  chmod +x "$MOCK_BIN/docker"

  run bash "$SCRIPT" newimage:v1
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
