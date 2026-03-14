package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// skipWithoutDocker skips if docker is not available locally.
func skipWithoutDocker(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping Docker integration test in short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "docker", "info").Run(); err != nil {
		t.Skipf("skipping: docker not available: %v", err)
	}
}

// setupLocalTailscaleShim creates a fake "tailscale" binary that runs
// the SSH command's arguments locally via bash -c. This lets DockerBackend
// execute its real Docker commands against the local daemon without
// needing an actual Tailscale SSH connection.
func setupLocalTailscaleShim(t *testing.T) string {
	t.Helper()
	shimDir := t.TempDir()
	shimPath := filepath.Join(shimDir, "tailscale")
	// The shim intercepts "tailscale ssh USER@HOST COMMAND" and runs
	// "bash -c COMMAND" locally, discarding the SSH target.
	// For local test images, docker pull will fail since they're not in
	// a registry. We make pull of test images a no-op.
	script := `#!/usr/bin/env bash
set -euo pipefail
# tailscale ssh USER@HOST COMMAND...
# Skip "ssh" and the target, run the rest as a local command
shift  # "ssh"
shift  # "USER@HOST"
cmd="$*"
# Make docker pull of local test images a no-op
cmd="${cmd/docker pull exeunt-test-image:latest && /}"
bash -c "$cmd"
`
	if err := os.WriteFile(shimPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write tailscale shim: %v", err)
	}
	return shimDir
}

// newLocalDockerBackend creates a DockerBackend that runs against the
// local Docker daemon by prepending the tailscale shim to PATH.
func newLocalDockerBackend(t *testing.T, shimDir string) *DockerBackend {
	t.Helper()
	// Prepend shim dir to PATH so DockerBackend's exec.Command("tailscale", ...)
	// finds our shim instead of the real tailscale binary
	t.Setenv("PATH", shimDir+":"+os.Getenv("PATH"))

	return NewDockerBackend(BackendConfig{
		Name:       "local-docker",
		Type:       "docker",
		Host:       "localhost",
		User:       "testuser",
		MaxRunners: 5,
		Labels:     []string{"exe"},
		Priority:   1,
	}, dockerTestImage, newTestLogger())
}

const dockerTestContainer = "exeunt-dockertest"
const dockerTestImage = "exeunt-test-image:latest"

// buildTestImage creates a minimal Docker image with an exedev user,
// matching the production runner image's user setup.
func buildTestImage(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "build", "-t", dockerTestImage, "-f", "-", ".")
	cmd.Stdin = strings.NewReader(`FROM alpine:latest
RUN adduser -D -h /home/exedev exedev
USER exedev
WORKDIR /home/exedev
`)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build test image: %v\n%s", err, out)
	}
}

func cleanupContainer(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()
}

func TestDockerBackendCreateAndDestroy(t *testing.T) {
	skipWithoutDocker(t)
	buildTestImage(t)
	shimDir := setupLocalTailscaleShim(t)
	backend := newLocalDockerBackend(t, shimDir)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Clean up any leftover
	cleanupContainer(dockerTestContainer)
	defer cleanupContainer(dockerTestContainer)

	t.Log("CreateRunner...")
	if err := backend.CreateRunner(ctx, dockerTestContainer, dockerTestImage); err != nil {
		t.Fatalf("CreateRunner: %v", err)
	}

	// Verify container is running
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.State.Running}}", dockerTestContainer).Output()
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if strings.TrimSpace(string(out)) != "true" {
		t.Errorf("container not running: %s", out)
	}

	// DestroyRunner
	t.Log("DestroyRunner...")
	if err := backend.DestroyRunner(ctx, dockerTestContainer); err != nil {
		t.Fatalf("DestroyRunner: %v", err)
	}

	// Verify container is gone
	err = exec.CommandContext(ctx, "docker", "inspect", dockerTestContainer).Run()
	if err == nil {
		t.Error("container still exists after DestroyRunner")
	}
}

func TestDockerBackendListRunners(t *testing.T) {
	skipWithoutDocker(t)
	shimDir := setupLocalTailscaleShim(t)
	backend := newLocalDockerBackend(t, shimDir)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Clean slate
	cleanupContainer("exeunt-listtest1")
	cleanupContainer("exeunt-listtest2")
	cleanupContainer("other-container")
	defer cleanupContainer("exeunt-listtest1")
	defer cleanupContainer("exeunt-listtest2")
	defer cleanupContainer("other-container")

	// Create some containers — some with exeunt- prefix, one without
	exec.CommandContext(ctx, "docker", "run", "-d", "--name", "exeunt-listtest1", "alpine:latest", "sleep", "infinity").Run()
	exec.CommandContext(ctx, "docker", "run", "-d", "--name", "exeunt-listtest2", "alpine:latest", "sleep", "infinity").Run()
	exec.CommandContext(ctx, "docker", "run", "-d", "--name", "other-container", "alpine:latest", "sleep", "infinity").Run()

	runners, err := backend.ListRunners(ctx)
	if err != nil {
		t.Fatalf("ListRunners: %v", err)
	}

	// Should find exactly the exeunt- prefixed containers
	found1, found2, foundOther := false, false, false
	for _, name := range runners {
		switch name {
		case "exeunt-listtest1":
			found1 = true
		case "exeunt-listtest2":
			found2 = true
		case "other-container":
			foundOther = true
		}
	}

	if !found1 {
		t.Error("exeunt-listtest1 not found in ListRunners")
	}
	if !found2 {
		t.Error("exeunt-listtest2 not found in ListRunners")
	}
	if foundOther {
		t.Error("other-container should not appear in ListRunners (no exeunt- prefix)")
	}
}

func TestDockerBackendListRunnersEmpty(t *testing.T) {
	skipWithoutDocker(t)
	shimDir := setupLocalTailscaleShim(t)
	backend := newLocalDockerBackend(t, shimDir)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Clean all exeunt- containers first
	out, _ := exec.CommandContext(ctx, "docker", "ps", "--filter", "name=exeunt-", "--format", "{{.Names}}").Output()
	for _, name := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if name != "" {
			cleanupContainer(name)
		}
	}

	runners, err := backend.ListRunners(ctx)
	if err != nil {
		t.Fatalf("ListRunners: %v", err)
	}
	if len(runners) != 0 {
		t.Errorf("expected empty list, got %v", runners)
	}
}

func TestDockerBackendCreateRunnerDefaultImage(t *testing.T) {
	skipWithoutDocker(t)
	buildTestImage(t)
	shimDir := setupLocalTailscaleShim(t)
	backend := newLocalDockerBackend(t, shimDir)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cleanupContainer("exeunt-imgtest")
	defer cleanupContainer("exeunt-imgtest")

	// Pass empty image — should use backend's default (dockerTestImage)
	if err := backend.CreateRunner(ctx, "exeunt-imgtest", ""); err != nil {
		t.Fatalf("CreateRunner with empty image: %v", err)
	}

	// Verify it used the default image
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.Config.Image}}", "exeunt-imgtest").Output()
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if strings.TrimSpace(string(out)) != dockerTestImage {
		t.Errorf("image = %q, want %q", strings.TrimSpace(string(out)), dockerTestImage)
	}
}

func TestDockerBackendSshRunError(t *testing.T) {
	skipWithoutDocker(t)
	shimDir := setupLocalTailscaleShim(t)
	backend := newLocalDockerBackend(t, shimDir)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// ListRunners with a broken command — replace the shim with one that fails
	failShim := filepath.Join(shimDir, "tailscale")
	os.WriteFile(failShim, []byte("#!/bin/bash\necho 'connection refused' >&2\nexit 1\n"), 0o755)

	_, err := backend.ListRunners(ctx)
	if err == nil {
		t.Fatal("expected error when tailscale ssh fails")
	}
	if !strings.Contains(err.Error(), "tailscale ssh") {
		t.Errorf("error = %q, expected to mention 'tailscale ssh'", err)
	}
}
