#!/usr/bin/env bats

# Tests for deploy configs, Makefile targets, and workflow validation.

REPO_ROOT="$BATS_TEST_DIRNAME/.."

@test "deploy/config.json is valid JSON" {
  run jq . "$REPO_ROOT/deploy/config.json"
  [ "$status" -eq 0 ]
}

@test "deploy/config.json has required fields" {
  run jq -e '.repos' "$REPO_ROOT/deploy/config.json"
  [ "$status" -eq 0 ]

  run jq -e '.backends' "$REPO_ROOT/deploy/config.json"
  [ "$status" -eq 0 ]
}

@test "deploy/config.json backends field is valid" {
  # config.json is a template — backends may be empty.
  # If backends are present, validate their structure.
  local count
  count=$(jq '.backends | length' "$REPO_ROOT/deploy/config.json")
  [ "$count" -ge 0 ]  # must be a valid array

  if [ "$count" -gt 0 ]; then
    while IFS= read -r backend; do
      name=$(echo "$backend" | jq -r '.name')
      type=$(echo "$backend" | jq -r '.type')
      [ -n "$name" ] && [ "$name" != "null" ]
      [ -n "$type" ] && [ "$type" != "null" ]
      [[ "$type" == "exedev" || "$type" == "docker" ]]
    done < <(jq -c '.backends[]' "$REPO_ROOT/deploy/config.json")
  fi
}

@test "deploy/config.json docker backends have host" {
  local docker_backends
  docker_backends=$(jq -c '.backends[] | select(.type == "docker")' "$REPO_ROOT/deploy/config.json" 2>/dev/null)
  if [ -n "$docker_backends" ]; then
    while IFS= read -r backend; do
      host=$(echo "$backend" | jq -r '.host')
      [ -n "$host" ] && [ "$host" != "null" ]
    done <<< "$docker_backends"
  else
    skip "no docker backends in config"
  fi
}

@test "deploy/exeunt-autoscaler.service is valid systemd unit" {
  local service="$REPO_ROOT/deploy/exeunt-autoscaler.service"
  [ -f "$service" ]

  # Must have [Unit], [Service], [Install] sections
  grep -q '^\[Unit\]' "$service"
  grep -q '^\[Service\]' "$service"
  grep -q '^\[Install\]' "$service"

  # ExecStart must reference the binary
  grep -q 'ExecStart=' "$service"
}

@test "deploy/exeunt-autoscaler.service runs as non-root" {
  local service="$REPO_ROOT/deploy/exeunt-autoscaler.service"
  # Should have User= set to a non-root user
  run grep 'User=' "$service"
  [ "$status" -eq 0 ]
  [[ "$output" != *"User=root"* ]]
}

@test "actionlint passes on all workflows" {
  if ! command -v actionlint &>/dev/null; then
    skip "actionlint not installed"
  fi
  run actionlint -color "$REPO_ROOT/.github/workflows/"*.yml
  echo "$output"
  [ "$status" -eq 0 ]
}

@test "Makefile: build target compiles" {
  cd "$REPO_ROOT"
  run make build
  [ "$status" -eq 0 ]
  # Binary should exist
  [ -f "$REPO_ROOT/exeunt-autoscaler" ]
  rm -f "$REPO_ROOT/exeunt-autoscaler"
}

@test "Makefile: test target runs" {
  cd "$REPO_ROOT"
  run make test
  [ "$status" -eq 0 ]
  [[ "$output" == *"ok"* ]]
}

@test "Makefile: clean target works" {
  cd "$REPO_ROOT"
  # Create the binary first
  touch "$REPO_ROOT/exeunt-autoscaler"
  run make clean
  [ "$status" -eq 0 ]
  [ ! -f "$REPO_ROOT/exeunt-autoscaler" ]
}

@test "composite actions: setup-exe-runner has required inputs" {
  local action="$REPO_ROOT/setup-exe-runner/action.yml"
  [ -f "$action" ]

  # Should have name and description
  grep -q '^name:' "$action"
  grep -q '^description:' "$action"

  # Should have inputs section
  grep -q 'inputs:' "$action"

  # Should have ssh-key input
  grep -q 'ssh-key:' "$action"
}

@test "composite actions: teardown-exe-runner has required inputs" {
  local action="$REPO_ROOT/teardown-exe-runner/action.yml"
  [ -f "$action" ]

  grep -q '^name:' "$action"
  grep -q '^description:' "$action"
  grep -q 'inputs:' "$action"
}

@test "composite actions: shell scripts use set -euo pipefail" {
  # Check all run: blocks in composite actions for proper error handling
  for action in "$REPO_ROOT/setup-exe-runner/action.yml" "$REPO_ROOT/teardown-exe-runner/action.yml"; do
    [ -f "$action" ]
    # Every shell: bash step should have set -euo pipefail in its run block
    # Extract run blocks and check
    local has_bash_steps=false
    if grep -q 'shell: bash' "$action"; then
      has_bash_steps=true
      # Check that set -euo pipefail appears in the file
      grep -q 'set -euo pipefail' "$action"
    fi
  done
}

@test "go.mod exists and is valid" {
  [ -f "$REPO_ROOT/cmd/autoscaler/go.mod" ]
  run grep '^module ' "$REPO_ROOT/cmd/autoscaler/go.mod"
  [ "$status" -eq 0 ]
  [[ "$output" == *"github.com/metcalfc/exeunt"* ]]
}

@test "scripts have shebang and pipefail" {
  for script in "$REPO_ROOT"/scripts/*.sh; do
    [ -f "$script" ]
    head -1 "$script" | grep -qE '^#!/usr/bin/env bash|^#!/bin/bash'
    grep -q 'set -euo pipefail' "$script"
  done
}

@test "no secrets or tokens in tracked files" {
  cd "$REPO_ROOT"
  # Check for common secret patterns in non-test, non-doc files
  run grep -rn --include='*.go' --include='*.sh' --include='*.yml' --include='*.json' \
    -E '(ghp_[a-zA-Z0-9]{36}|github_pat_|sk-[a-zA-Z0-9]{48}|AKIA[0-9A-Z]{16})' \
    --exclude-dir=.git .
  [ "$status" -ne 0 ]  # grep should find nothing (exit 1)
}
