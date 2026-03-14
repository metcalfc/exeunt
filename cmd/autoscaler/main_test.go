package main

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildBackends(t *testing.T) {
	logger := newTestLogger()
	ssh := &RealSSHExecutor{} // Real executor — won't be called during construction

	t.Run("exedev backend", func(t *testing.T) {
		cfg := &Config{
			RunnerImage: "test:latest",
			Backends: []BackendConfig{
				{Name: "my-exedev", Type: "exedev", MaxRunners: 3, Labels: []string{"exe"}, Priority: 1},
			},
		}
		backends, err := buildBackends(cfg, ssh, logger)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(backends) != 1 {
			t.Fatalf("got %d backends, want 1", len(backends))
		}
		if backends[0].Name() != "my-exedev" {
			t.Errorf("Name() = %q, want %q", backends[0].Name(), "my-exedev")
		}
		if backends[0].Type() != "exedev" {
			t.Errorf("Type() = %q, want %q", backends[0].Type(), "exedev")
		}
		if backends[0].Priority() != 1 {
			t.Errorf("Priority() = %d, want %d", backends[0].Priority(), 1)
		}
	})

	t.Run("docker backend", func(t *testing.T) {
		cfg := &Config{
			RunnerImage: "test:latest",
			Backends: []BackendConfig{
				{Name: "my-docker", Type: "docker", Host: "docker-host", MaxRunners: 5},
			},
		}
		backends, err := buildBackends(cfg, ssh, logger)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(backends) != 1 {
			t.Fatalf("got %d backends, want 1", len(backends))
		}
		if backends[0].Type() != "docker" {
			t.Errorf("Type() = %q, want %q", backends[0].Type(), "docker")
		}
	})

	t.Run("docker backend missing host", func(t *testing.T) {
		cfg := &Config{
			RunnerImage: "test:latest",
			Backends: []BackendConfig{
				{Name: "bad-docker", Type: "docker"},
			},
		}
		_, err := buildBackends(cfg, ssh, logger)
		if err == nil {
			t.Fatal("expected error for docker backend without host")
		}
	})

	t.Run("unknown type", func(t *testing.T) {
		cfg := &Config{
			RunnerImage: "test:latest",
			Backends: []BackendConfig{
				{Name: "weird", Type: "kubernetes"},
			},
		}
		_, err := buildBackends(cfg, ssh, logger)
		if err == nil {
			t.Fatal("expected error for unknown backend type")
		}
	})

	t.Run("empty backends", func(t *testing.T) {
		cfg := &Config{
			RunnerImage: "test:latest",
			Backends:    []BackendConfig{},
		}
		_, err := buildBackends(cfg, ssh, logger)
		if err == nil {
			t.Fatal("expected error for empty backends")
		}
	})

	t.Run("mixed backends", func(t *testing.T) {
		cfg := &Config{
			RunnerImage: "test:latest",
			Backends: []BackendConfig{
				{Name: "exe", Type: "exedev", MaxRunners: 3, Labels: []string{"exe"}},
				{Name: "dock", Type: "docker", Host: "host1", MaxRunners: 5, Labels: []string{"exe"}},
			},
		}
		backends, err := buildBackends(cfg, ssh, logger)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(backends) != 2 {
			t.Fatalf("got %d backends, want 2", len(backends))
		}
	})
}

func TestReconcileEmptyBackend(t *testing.T) {
	logger := newTestLogger()
	dir := t.TempDir()
	tracker := NewTracker(filepath.Join(dir, "state.json"), logger)

	tracker.Add(1, "exeunt-abc", "repo", "empty-backend", []string{"exe"})
	tracker.Add(2, "exeunt-def", "repo", "ok-backend", []string{"exe"})

	// One backend returns empty list (VM gone), the other has the VM
	emptySSH := &MockSSHExecutor{} // ListVMs returns empty
	emptyBackend := NewExeDevBackend(BackendConfig{
		Name: "empty-backend", Type: "exedev", MaxRunners: 5, Labels: []string{"exe"},
	}, "test:latest", emptySSH, logger)

	okSSH := &MockSSHExecutor{ListResult: []VMInfo{{VMName: "exeunt-def"}}}
	okBackend := NewExeDevBackend(BackendConfig{
		Name: "ok-backend", Type: "exedev", MaxRunners: 5, Labels: []string{"exe"},
	}, "test:latest", okSSH, logger)

	reconcile(context.Background(), tracker, []Backend{emptyBackend, okBackend}, logger)

	// exeunt-abc was on empty-backend which returned no VMs → removed
	if tracker.HasJob(1) {
		t.Error("expected job 1 to be removed (VM not found on backend)")
	}
	// exeunt-def exists in okBackend's list
	if !tracker.HasJob(2) {
		t.Error("expected job 2 to survive (VM exists on ok-backend)")
	}
}

func TestReconcileListRunnerError(t *testing.T) {
	logger := newTestLogger()
	dir := t.TempDir()
	tracker := NewTracker(filepath.Join(dir, "state.json"), logger)

	tracker.Add(1, "exeunt-abc", "repo", "failing-backend", []string{"exe"})
	tracker.Add(2, "exeunt-def", "repo", "ok-backend", []string{"exe"})

	// One backend fails ListRunners (SSH error), the other succeeds
	failSSH := &MockSSHExecutor{ListErr: fmt.Errorf("ssh timeout")}
	failBackend := NewExeDevBackend(BackendConfig{
		Name: "failing-backend", Type: "exedev", MaxRunners: 5, Labels: []string{"exe"},
	}, "test:latest", failSSH, logger)

	okSSH := &MockSSHExecutor{ListResult: []VMInfo{{VMName: "exeunt-def"}}}
	okBackend := NewExeDevBackend(BackendConfig{
		Name: "ok-backend", Type: "exedev", MaxRunners: 5, Labels: []string{"exe"},
	}, "test:latest", okSSH, logger)

	reconcile(context.Background(), tracker, []Backend{failBackend, okBackend}, logger)

	// exeunt-abc is on failing-backend whose ListRunners errored.
	// Reconcile must NOT remove it — we couldn't reach the backend.
	if !tracker.HasJob(1) {
		t.Error("expected job 1 to SURVIVE (backend was unreachable, not empty)")
	}
	// exeunt-def exists in okBackend's list
	if !tracker.HasJob(2) {
		t.Error("expected job 2 to survive (VM exists on ok-backend)")
	}
}

func TestReconcileLoopCancellation(t *testing.T) {
	logger := newTestLogger()
	dir := t.TempDir()
	tracker := NewTracker(filepath.Join(dir, "state.json"), logger)

	ssh := &MockSSHExecutor{}
	backend := NewExeDevBackend(BackendConfig{
		Name: "test", Type: "exedev", MaxRunners: 5, Labels: []string{"exe"},
	}, "test:latest", ssh, logger)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		reconcileLoop(ctx, tracker, []Backend{backend}, logger)
		close(done)
	}()

	// Cancel immediately — reconcileLoop should exit
	cancel()
	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("reconcileLoop did not exit after context cancellation")
	}
}

func TestReconcileEmptyTracker(t *testing.T) {
	logger := newTestLogger()
	dir := t.TempDir()
	tracker := NewTracker(filepath.Join(dir, "state.json"), logger)

	ssh := &MockSSHExecutor{ListResult: []VMInfo{{VMName: "exeunt-orphan"}}}
	backend := NewExeDevBackend(BackendConfig{
		Name: "test", Type: "exedev", MaxRunners: 5, Labels: []string{"exe"},
	}, "test:latest", ssh, logger)

	// Empty tracker + backends with VMs — reconcile should not crash
	reconcile(context.Background(), tracker, []Backend{backend}, logger)

	if tracker.Count() != 0 {
		t.Errorf("count = %d, want 0 (no tracked VMs to reconcile)", tracker.Count())
	}
}
