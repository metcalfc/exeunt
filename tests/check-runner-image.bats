#!/usr/bin/env bats

# Tests for scripts/check-runner-image.sh
# Tests argument parsing, helper functions, and Dockerfile analysis logic
# using mocked external commands (curl, docker, jq).

SCRIPT="$BATS_TEST_DIRNAME/../scripts/check-runner-image.sh"

setup() {
  MOCK_BIN="$(mktemp -d)"
  export PATH="$MOCK_BIN:$PATH"

  # Default mock jq — pass through real jq
  ln -s "$(which jq)" "$MOCK_BIN/jq" 2>/dev/null || true

  # Default mock docker — manifest inspect always fails
  cat > "$MOCK_BIN/docker" <<'MOCK'
#!/usr/bin/env bash
exit 1
MOCK
  chmod +x "$MOCK_BIN/docker"
}

teardown() {
  rm -rf "$MOCK_BIN"
}

make_curl_mock() {
  # Args: upstream_dockerfile fork_dockerfile runner_version
  local upstream="${1:-}"
  local fork="${2:-}"
  local runner_version="${3:-2.325.0}"

  cat > "$MOCK_BIN/curl" <<MOCK
#!/usr/bin/env bash
url="\${*: -1}"  # last argument is the URL
case "\$url" in
  *boldsoftware/exeuntu/main/Dockerfile*)
    if [ -n "$upstream" ]; then
      cat <<'DOCKERFILE'
${upstream}
DOCKERFILE
    else
      exit 1
    fi
    ;;
  *metcalfc/exeuntu/main/Dockerfile*)
    if [ -n "$fork" ]; then
      cat <<'DOCKERFILE'
${fork}
DOCKERFILE
    else
      exit 1
    fi
    ;;
  *api.github.com*runner*releases*latest*)
    echo '{"tag_name":"v${runner_version}"}'
    ;;
  *)
    exit 1
    ;;
esac
MOCK
  chmod +x "$MOCK_BIN/curl"
}

GOOD_UPSTREAM='FROM ubuntu:24.04
RUN apt-get install -y curl libicu-dev
RUN usermod -l exedev ubuntu
RUN mkdir -p /home/exedev
RUN apt-get install -y systemd'

GOOD_FORK='FROM ubuntu:24.04
ARG RUNNER_VERSION
RUN mkdir -p /home/exedev/actions-runner
RUN chown -R exedev:exedev /home/exedev/actions-runner'

@test "check-runner-image: all checks pass with good Dockerfiles" {
  make_curl_mock "$GOOD_UPSTREAM" "$GOOD_FORK" "2.325.0"

  run bash "$SCRIPT"
  [ "$status" -eq 0 ]
  [[ "$output" == *"exedev"* ]]
  [[ "$output" == *"all checks passed"* ]] || [[ "$output" == *"warning"* ]]
}

@test "check-runner-image: --verbose flag works" {
  make_curl_mock "$GOOD_UPSTREAM" "$GOOD_FORK"

  run bash "$SCRIPT" --verbose
  [ "$status" -eq 0 ]
}

@test "check-runner-image: -v flag works" {
  make_curl_mock "$GOOD_UPSTREAM" "$GOOD_FORK"

  run bash "$SCRIPT" -v
  [ "$status" -eq 0 ]
}

@test "check-runner-image: detects missing exedev user" {
  local bad_upstream='FROM ubuntu:24.04
RUN apt-get install -y curl
RUN mkdir -p /home/runner
RUN apt-get install -y systemd'

  make_curl_mock "$bad_upstream" "$GOOD_FORK"

  run bash "$SCRIPT"
  [[ "$output" == *"renamed the"*"exedev"* ]] || [[ "$output" == *"error"* ]]
}

@test "check-runner-image: detects missing home directory" {
  local bad_upstream='FROM ubuntu:24.04
RUN apt-get install -y curl
RUN usermod -l exedev ubuntu
RUN mkdir -p /opt/runner
RUN apt-get install -y systemd'

  make_curl_mock "$bad_upstream" "$GOOD_FORK"

  run bash "$SCRIPT"
  [[ "$output" == *"home directory"* ]] || [[ "$output" == *"error"* ]]
}

@test "check-runner-image: detects changed base image" {
  local changed_upstream='FROM ubuntu:26.04
RUN usermod -l exedev ubuntu
RUN mkdir -p /home/exedev
RUN apt-get install -y curl systemd'

  make_curl_mock "$changed_upstream" "$GOOD_FORK"

  run bash "$SCRIPT"
  [[ "$output" == *"changed"* ]] || [[ "$output" == *"26.04"* ]]
}

@test "check-runner-image: detects missing systemd" {
  local no_systemd='FROM ubuntu:24.04
RUN usermod -l exedev ubuntu
RUN mkdir -p /home/exedev
RUN apt-get install -y curl'

  make_curl_mock "$no_systemd" "$GOOD_FORK"

  run bash "$SCRIPT"
  [[ "$output" == *"systemd"* ]]
}

@test "check-runner-image: detects missing RUNNER_VERSION in fork" {
  local bad_fork='FROM ubuntu:24.04
RUN mkdir -p /home/exedev/actions-runner
RUN chown -R exedev:exedev /home/exedev/actions-runner'

  make_curl_mock "$GOOD_UPSTREAM" "$bad_fork"

  run bash "$SCRIPT"
  [ "$status" -eq 1 ]
  [[ "$output" == *"RUNNER_VERSION"* ]]
}

@test "check-runner-image: detects missing runner path in fork" {
  local bad_fork='FROM ubuntu:24.04
ARG RUNNER_VERSION
RUN mkdir -p /opt/runner'

  make_curl_mock "$GOOD_UPSTREAM" "$bad_fork"

  run bash "$SCRIPT"
  [ "$status" -eq 1 ]
  [[ "$output" == *"Runner install path"* ]] || [[ "$output" == *"error"* ]]
}

@test "check-runner-image: handles upstream fetch failure" {
  # curl fails for upstream, succeeds for fork and runner version
  cat > "$MOCK_BIN/curl" <<'MOCK'
#!/usr/bin/env bash
url="${*: -1}"
case "$url" in
  *boldsoftware*) exit 1 ;;
  *metcalfc/exeuntu*)
    echo 'FROM ubuntu:24.04'
    echo 'ARG RUNNER_VERSION'
    echo 'RUN mkdir -p /home/exedev/actions-runner'
    echo 'RUN chown -R exedev:exedev /home/exedev/actions-runner'
    ;;
  *api.github.com*) echo '{"tag_name":"v2.325.0"}' ;;
  *) exit 1 ;;
esac
MOCK
  chmod +x "$MOCK_BIN/curl"

  run bash "$SCRIPT"
  [[ "$output" == *"Could not fetch upstream"* ]]
  # Should still continue and check fork
  [[ "$output" == *"Fork Dockerfile"* ]]
}

@test "check-runner-image: handles fork fetch failure" {
  cat > "$MOCK_BIN/curl" <<'MOCK'
#!/usr/bin/env bash
url="${*: -1}"
case "$url" in
  *boldsoftware*)
    echo 'FROM ubuntu:24.04'
    echo 'RUN usermod -l exedev ubuntu'
    echo 'RUN mkdir -p /home/exedev'
    echo 'RUN apt-get install -y curl systemd'
    ;;
  *metcalfc/exeuntu*) exit 1 ;;
  *api.github.com*) echo '{"tag_name":"v2.325.0"}' ;;
  *) exit 1 ;;
esac
MOCK
  chmod +x "$MOCK_BIN/curl"

  run bash "$SCRIPT"
  [ "$status" -eq 1 ]
  [[ "$output" == *"Could not fetch fork"* ]]
}

@test "check-runner-image: reports error count correctly" {
  local bad_fork='FROM ubuntu:24.04
RUN mkdir -p /opt/somewhere-else'

  make_curl_mock "$GOOD_UPSTREAM" "$bad_fork"

  run bash "$SCRIPT"
  [ "$status" -eq 1 ]
  [[ "$output" == *"error(s)"* ]]
}
