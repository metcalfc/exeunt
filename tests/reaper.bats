#!/usr/bin/env bats

# Tests for scripts/reaper.sh argument parsing and validation.
# These tests exercise the real script with mocked external commands
# (ssh, curl) to test argument parsing, validation, and output without
# requiring real infrastructure.

SCRIPT="$BATS_TEST_DIRNAME/../scripts/reaper.sh"

setup() {
  # Create a temp dir for mock commands
  MOCK_BIN="$(mktemp -d)"
  export PATH="$MOCK_BIN:$PATH"

  # Create mock ssh that returns empty VM list
  cat > "$MOCK_BIN/ssh" <<'MOCK'
#!/usr/bin/env bash
# Return empty VM list for "ls --json"
if [[ "$*" == *"ls --json"* ]]; then
  echo '{"vms":[]}'
  exit 0
fi
exit 0
MOCK
  chmod +x "$MOCK_BIN/ssh"

  # Create mock curl that returns empty runners
  cat > "$MOCK_BIN/curl" <<'MOCK'
#!/usr/bin/env bash
# Parse -o flag to write response body
output="/dev/stdout"
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o) output="$2"; shift 2 ;;
    *) shift ;;
  esac
done
echo '{"runners":[]}' > "$output"
# Write HTTP 200 to stdout (for -w '%{http_code}')
printf '200'
MOCK
  chmod +x "$MOCK_BIN/curl"
}

teardown() {
  rm -rf "$MOCK_BIN"
}

@test "reaper: --help shows usage" {
  run bash "$SCRIPT" --help
  [ "$status" -eq 1 ]
  [[ "$output" == *"Usage:"* ]]
  [[ "$output" == *"--repo"* ]]
  [[ "$output" == *"--dry-run"* ]]
}

@test "reaper: -h shows usage" {
  run bash "$SCRIPT" -h
  [ "$status" -eq 1 ]
  [[ "$output" == *"Usage:"* ]]
}

@test "reaper: missing --repo errors" {
  unset GITHUB_REPOSITORY 2>/dev/null || true
  run bash "$SCRIPT" --token fake-token
  [ "$status" -eq 1 ]
  [[ "$output" == *"--repo"* ]]
}

@test "reaper: missing --token errors" {
  unset GITHUB_TOKEN 2>/dev/null || true
  run bash "$SCRIPT" --repo owner/repo
  [ "$status" -eq 1 ]
  [[ "$output" == *"--token"* ]]
}

@test "reaper: unknown option errors" {
  run bash "$SCRIPT" --bogus
  [ "$status" -eq 1 ]
  [[ "$output" == *"Unknown option"* ]]
}

@test "reaper: accepts --repo and --token" {
  run bash "$SCRIPT" --repo owner/repo --token fake-token
  [ "$status" -eq 0 ]
  [[ "$output" == *"Repo: owner/repo"* ]]
}

@test "reaper: GITHUB_REPOSITORY env var works" {
  GITHUB_REPOSITORY=env/repo GITHUB_TOKEN=fake run bash "$SCRIPT"
  [ "$status" -eq 0 ]
  [[ "$output" == *"Repo: env/repo"* ]]
}

@test "reaper: --dry-run flag is set" {
  run bash "$SCRIPT" --repo owner/repo --token fake-token --dry-run
  [ "$status" -eq 0 ]
  [[ "$output" == *"Dry run: true"* ]]
}

@test "reaper: no exeunt VMs exits cleanly" {
  run bash "$SCRIPT" --repo owner/repo --token fake-token
  [ "$status" -eq 0 ]
  [[ "$output" == *"No exeunt-* VMs found"* ]]
}

@test "reaper: classifies orphaned VMs (no runner registered)" {
  # Mock ssh to return VMs with exeunt- prefix
  cat > "$MOCK_BIN/ssh" <<'MOCK'
#!/usr/bin/env bash
if [[ "$*" == *"ls --json"* ]]; then
  echo '{"vms":[{"vm_name":"exeunt-orphan1","status":"running"},{"vm_name":"exeunt-orphan2","status":"running"},{"vm_name":"other-vm","status":"running"}]}'
  exit 0
fi
if [[ "$*" == *"rm "* ]]; then
  echo "Deleting..."
  exit 0
fi
exit 0
MOCK
  chmod +x "$MOCK_BIN/ssh"

  run bash "$SCRIPT" --repo owner/repo --token fake-token --dry-run
  [ "$status" -eq 0 ]
  [[ "$output" == *"Found 2 exeunt-* VM(s)"* ]]
  [[ "$output" == *"[ORPHAN]"*"exeunt-orphan1"* ]]
  [[ "$output" == *"[ORPHAN]"*"exeunt-orphan2"* ]]
  [[ "$output" == *"would reap (dry-run)"* ]]
  # "other-vm" should not appear in orphan list
  [[ "$output" != *"other-vm"* ]]
}

@test "reaper: classifies active VMs (runner online)" {
  cat > "$MOCK_BIN/ssh" <<'MOCK'
#!/usr/bin/env bash
if [[ "$*" == *"ls --json"* ]]; then
  echo '{"vms":[{"vm_name":"exeunt-active","status":"running"}]}'
  exit 0
fi
exit 0
MOCK
  chmod +x "$MOCK_BIN/ssh"

  # Mock curl to return an online runner matching the VM
  cat > "$MOCK_BIN/curl" <<'MOCK'
#!/usr/bin/env bash
output="/dev/stdout"
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o) output="$2"; shift 2 ;;
    *) shift ;;
  esac
done
echo '{"runners":[{"name":"exeunt-active","id":42,"status":"online"}]}' > "$output"
printf '200'
MOCK
  chmod +x "$MOCK_BIN/curl"

  run bash "$SCRIPT" --repo owner/repo --token fake-token
  [ "$status" -eq 0 ]
  [[ "$output" == *"[ACTIVE]"*"exeunt-active"* ]]
  [[ "$output" == *"Active: 1"* ]]
  [[ "$output" == *"Reaped: 0"* ]]
}

@test "reaper: classifies offline runner as orphan" {
  cat > "$MOCK_BIN/ssh" <<'MOCK'
#!/usr/bin/env bash
if [[ "$*" == *"ls --json"* ]]; then
  echo '{"vms":[{"vm_name":"exeunt-stale","status":"running"}]}'
  exit 0
fi
if [[ "$*" == *"rm "* ]]; then
  echo "Deleted"
  exit 0
fi
exit 0
MOCK
  chmod +x "$MOCK_BIN/ssh"

  cat > "$MOCK_BIN/curl" <<'MOCK'
#!/usr/bin/env bash
output="/dev/stdout"
method="GET"
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o) output="$2"; shift 2 ;;
    -X) method="$2"; shift 2 ;;
    *) shift ;;
  esac
done
if [[ "$method" == "DELETE" ]]; then
  echo '{}' > "$output"
  printf '204'
else
  echo '{"runners":[{"name":"exeunt-stale","id":99,"status":"offline"}]}' > "$output"
  printf '200'
fi
MOCK
  chmod +x "$MOCK_BIN/curl"

  run bash "$SCRIPT" --repo owner/repo --token fake-token --dry-run
  [ "$status" -eq 0 ]
  [[ "$output" == *"[ORPHAN]"*"exeunt-stale"*"runner offline"* ]]
}

@test "reaper: ssh failure exits with error" {
  cat > "$MOCK_BIN/ssh" <<'MOCK'
#!/usr/bin/env bash
echo "Connection refused" >&2
exit 255
MOCK
  chmod +x "$MOCK_BIN/ssh"

  run bash "$SCRIPT" --repo owner/repo --token fake-token
  [ "$status" -eq 1 ]
  [[ "$output" == *"failed to list"* ]]
}
