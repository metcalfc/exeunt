package main

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

func TestVmName(t *testing.T) {
	logger := newTestLogger()
	tracker := newTestTracker(t)
	router := NewRouter(nil, tracker, logger)
	gh := &GitHubClient{Token: "test"}
	p := NewProvisioner(&Config{}, tracker, router, gh, logger)

	// Deterministic: same input → same output
	name1 := p.vmName(12345)
	name2 := p.vmName(12345)
	if name1 != name2 {
		t.Errorf("vmName not deterministic: %q != %q", name1, name2)
	}

	// Different inputs → different outputs
	name3 := p.vmName(99999)
	if name1 == name3 {
		t.Errorf("expected different names for different job IDs, both got %q", name1)
	}

	// Starts with prefix and has 16 hex chars (8 bytes)
	if len(name1) != 23 || name1[:7] != "exeunt-" {
		t.Errorf("expected 'exeunt-' + 16 hex chars (23 total), got %q (len %d)", name1, len(name1))
	}
}

func newProvisionerWithMocks(t *testing.T, backends ...Backend) (*Provisioner, *Tracker) {
	t.Helper()
	logger := newTestLogger()
	tracker := newTestTracker(t)
	router := NewRouter(backends, tracker, logger)
	gh := &GitHubClient{Token: "test"}
	cfg := &Config{RunnerImage: "test:latest"}
	p := NewProvisioner(cfg, tracker, router, gh, logger)
	return p, tracker
}

func TestProvisionDedup(t *testing.T) {
	backend := &MockBackend{name: "test", labels: []string{"exe"}, priority: 1, maxRunners: 5}
	p, tracker := newProvisionerWithMocks(t, backend)

	// Pre-add job to tracker
	tracker.Add(100, "vm-100", "repo", "test", []string{"exe"})

	event := WorkflowJobEvent{Action: "queued"}
	event.WorkflowJob.ID = 100
	event.WorkflowJob.Labels = []string{"exe"}
	event.Repository.FullName = "metcalfc/exeunt"

	p.Provision(context.Background(), event)

	// Should still have only 1 entry — no duplicate
	if tracker.Count() != 1 {
		t.Errorf("count = %d, want 1 (dedup should prevent re-add)", tracker.Count())
	}
}

func TestProvisionNoBackend(t *testing.T) {
	// No backends match the labels
	backend := &MockBackend{name: "test", labels: []string{"gpu"}, priority: 1, maxRunners: 5}
	p, tracker := newProvisionerWithMocks(t, backend)

	event := WorkflowJobEvent{Action: "queued"}
	event.WorkflowJob.ID = 200
	event.WorkflowJob.Labels = []string{"exe"}
	event.Repository.FullName = "metcalfc/exeunt"

	p.Provision(context.Background(), event)

	if tracker.Count() != 0 {
		t.Errorf("count = %d, want 0 (no backend should match)", tracker.Count())
	}
}

func TestProvisionFallback(t *testing.T) {
	// First backend fails on create, second succeeds.
	primary := &MockBackend{
		name:       "primary",
		labels:     []string{"exe"},
		priority:   1,
		maxRunners: 5,
		CreateErr:  fmt.Errorf("ssh timeout"),
	}
	fallback := &MockBackend{
		name:       "fallback",
		labels:     []string{"exe"},
		priority:   10,
		maxRunners: 5,
	}

	logger := newTestLogger()
	tracker := newTestTracker(t)
	router := NewRouter([]Backend{primary, fallback}, tracker, logger)

	// Mock GitHub API that returns a valid JIT config
	ghServer := newMockGitHubServer(t, 201, `{"encoded_jit_config":"abc123","runner":{"id":1}}`)
	defer ghServer.Close()

	gh := &GitHubClient{
		Token: "test",
		HTTPClient: &http.Client{Transport: &urlRewriteTransport{
			base:    ghServer.Client().Transport,
			baseURL: ghServer.URL,
		}},
	}

	cfg := &Config{RunnerImage: "test:latest"}
	p := NewProvisioner(cfg, tracker, router, gh, logger)

	event := WorkflowJobEvent{Action: "queued"}
	event.WorkflowJob.ID = 300
	event.WorkflowJob.Labels = []string{"exe"}
	event.Repository.FullName = "metcalfc/exeunt"

	p.Provision(context.Background(), event)

	// Primary fails (CreateErr), provisioner falls back to fallback which succeeds.
	if !tracker.HasJob(300) {
		t.Fatal("expected job 300 to be tracked after fallback succeeds")
	}
	record, _ := tracker.Get(300)
	if record.Backend != "fallback" {
		t.Errorf("backend = %q, want %q", record.Backend, "fallback")
	}
	if record.Status != StatusReady {
		t.Errorf("status = %q, want %q", record.Status, StatusReady)
	}
}

func TestDestroyUntracked(t *testing.T) {
	backend := &MockBackend{name: "test", labels: []string{"exe"}, priority: 1, maxRunners: 5}
	p, tracker := newProvisionerWithMocks(t, backend)

	event := WorkflowJobEvent{Action: "completed"}
	event.WorkflowJob.ID = 999
	event.Repository.FullName = "metcalfc/exeunt"

	// Should not panic and should not change tracker
	p.Destroy(context.Background(), event)

	if tracker.Count() != 0 {
		t.Errorf("count = %d, want 0", tracker.Count())
	}
}

func TestDestroySuccess(t *testing.T) {
	backend := &MockBackend{name: "test", labels: []string{"exe"}, priority: 1, maxRunners: 5}
	p, tracker := newProvisionerWithMocks(t, backend)

	// Pre-populate tracker and semaphore
	tracker.Add(400, "exeunt-abc", "metcalfc/exeunt", "test", []string{"exe"})
	p.semaphore <- struct{}{} // simulate acquired semaphore

	event := WorkflowJobEvent{Action: "completed"}
	event.WorkflowJob.ID = 400
	event.Repository.FullName = "metcalfc/exeunt"

	p.Destroy(context.Background(), event)

	if tracker.HasJob(400) {
		t.Error("expected job 400 to be removed from tracker")
	}
	if tracker.Count() != 0 {
		t.Errorf("count = %d, want 0", tracker.Count())
	}
}

func TestDestroyFailureReleasesResources(t *testing.T) {
	backend := &MockBackend{
		name:       "test",
		labels:     []string{"exe"},
		priority:   1,
		maxRunners: 5,
		DestroyErr: fmt.Errorf("ssh timeout"),
	}
	p, tracker := newProvisionerWithMocks(t, backend)

	tracker.Add(600, "exeunt-fail", "metcalfc/exeunt", "test", []string{"exe"})
	p.semaphore <- struct{}{} // simulate acquired semaphore

	event := WorkflowJobEvent{Action: "completed"}
	event.WorkflowJob.ID = 600
	event.Repository.FullName = "metcalfc/exeunt"

	p.Destroy(context.Background(), event)

	// Even though destroy failed, tracker and semaphore must be cleaned up
	if tracker.HasJob(600) {
		t.Error("expected job 600 to be removed from tracker even on destroy failure")
	}

	// Verify semaphore was released by checking we can acquire it
	select {
	case p.semaphore <- struct{}{}:
		<-p.semaphore // put it back
	default:
		t.Error("semaphore was not released after destroy failure")
	}
}

func TestDestroyBackendNotFound(t *testing.T) {
	// Backend in tracker doesn't exist in router
	backend := &MockBackend{name: "other", labels: []string{"exe"}, priority: 1, maxRunners: 5}
	p, tracker := newProvisionerWithMocks(t, backend)

	// Track a job on a backend that doesn't exist in the router
	tracker.Add(500, "exeunt-xyz", "metcalfc/exeunt", "vanished-backend", []string{"exe"})
	p.semaphore <- struct{}{}

	event := WorkflowJobEvent{Action: "completed"}
	event.WorkflowJob.ID = 500
	event.Repository.FullName = "metcalfc/exeunt"

	p.Destroy(context.Background(), event)

	// Should clean up tracker even when backend not found
	if tracker.HasJob(500) {
		t.Error("expected job 500 to be removed from tracker even when backend not found")
	}
}
