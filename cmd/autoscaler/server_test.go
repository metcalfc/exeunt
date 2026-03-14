package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// MockSSHExecutor records calls and returns configurable results.
type MockSSHExecutor struct {
	mu         sync.Mutex
	NewVMCalls []string
	RemoveCalls []string
	RunOnCalls []struct{ Name, Script string }
	ListResult []VMInfo
	NewVMErr   error
	RemoveErr  error
	RunOnErr   error
	WaitErr    error
}

func (m *MockSSHExecutor) NewVM(_ context.Context, name, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.NewVMCalls = append(m.NewVMCalls, name)
	return m.NewVMErr
}

func (m *MockSSHExecutor) RemoveVM(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.RemoveCalls = append(m.RemoveCalls, name)
	return m.RemoveErr
}

func (m *MockSSHExecutor) ListVMs(_ context.Context) ([]VMInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ListResult, nil
}

func (m *MockSSHExecutor) WaitForSSH(_ context.Context, _ string) error {
	return m.WaitErr
}

func (m *MockSSHExecutor) RunOnVM(_ context.Context, name, script string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.RunOnCalls = append(m.RunOnCalls, struct{ Name, Script string }{name, script})
	return "Runner connected", m.RunOnErr
}

func newTestServer(t *testing.T, mockSSH *MockSSHExecutor) (*Server, *Config) {
	t.Helper()
	dir := t.TempDir()
	cfg := &Config{
		WebhookSecret: "test-secret",
		GitHubToken:   "ghp_test",
		Repo:          "metcalfc/exeunt",
		Port:          0,
		MaxVMs:        5,
		RunnerImage:   "ghcr.io/metcalfc/exeunt-runner:latest",
		RunnerLabels:  []string{"exe"},
		StateFile:     filepath.Join(dir, "state.json"),
		LogLevel:      "error",
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	tracker := NewTracker(cfg.StateFile, logger)
	gh := NewGitHubClient(cfg.GitHubToken)
	provisioner := NewProvisioner(cfg, tracker, mockSSH, gh, logger)
	server := NewServer(cfg, provisioner, tracker, logger)
	return server, cfg
}

func makeWebhookRequest(t *testing.T, event WorkflowJobEvent, secret string) *http.Request {
	t.Helper()
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	req := httptest.NewRequest("POST", "/webhook", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "workflow_job")
	req.Header.Set("X-Hub-Signature-256", computeSignature(payload, []byte(secret)))
	return req
}

func TestHealthz(t *testing.T) {
	server, _ := newTestServer(t, &MockSSHExecutor{})

	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["status"] != "ok" {
		t.Errorf("status = %v, want ok", resp["status"])
	}
}

func TestStatus(t *testing.T) {
	server, _ := newTestServer(t, &MockSSHExecutor{})

	req := httptest.NewRequest("GET", "/status", nil)
	w := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["count"].(float64) != 0 {
		t.Errorf("count = %v, want 0", resp["count"])
	}
}

func TestWebhookInvalidSignature(t *testing.T) {
	server, _ := newTestServer(t, &MockSSHExecutor{})

	event := WorkflowJobEvent{Action: "queued"}
	payload, _ := json.Marshal(event)

	req := httptest.NewRequest("POST", "/webhook", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "workflow_job")
	req.Header.Set("X-Hub-Signature-256", "sha256=invalid")

	w := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestWebhookIgnoresNonWorkflowJobEvents(t *testing.T) {
	server, _ := newTestServer(t, &MockSSHExecutor{})

	payload := []byte(`{"action":"created"}`)
	req := httptest.NewRequest("POST", "/webhook", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-Hub-Signature-256", computeSignature(payload, []byte("test-secret")))

	w := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestWebhookIgnoresJobsWithoutExeLabel(t *testing.T) {
	server, _ := newTestServer(t, &MockSSHExecutor{})

	event := WorkflowJobEvent{Action: "queued"}
	event.WorkflowJob.ID = 100
	event.WorkflowJob.Labels = []string{"self-hosted", "ubuntu-latest"}
	event.Repository.FullName = "metcalfc/exeunt"

	req := makeWebhookRequest(t, event, "test-secret")
	w := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestWebhookIgnoresExeBuilderLabel(t *testing.T) {
	mockSSH := &MockSSHExecutor{}
	server, _ := newTestServer(t, mockSSH)

	event := WorkflowJobEvent{Action: "queued"}
	event.WorkflowJob.ID = 100
	event.WorkflowJob.Labels = []string{"self-hosted", "exe-builder"}
	event.Repository.FullName = "metcalfc/exeunt"

	req := makeWebhookRequest(t, event, "test-secret")
	w := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	// Give goroutine time to run (it shouldn't)
	time.Sleep(50 * time.Millisecond)

	mockSSH.mu.Lock()
	defer mockSSH.mu.Unlock()
	if len(mockSSH.NewVMCalls) > 0 {
		t.Error("should not provision for exe-builder label")
	}
}

func TestWebhookQueuedProvisions(t *testing.T) {
	mockSSH := &MockSSHExecutor{}
	server, _ := newTestServer(t, mockSSH)

	event := WorkflowJobEvent{Action: "queued"}
	event.WorkflowJob.ID = 100
	event.WorkflowJob.Labels = []string{"self-hosted", "exe"}
	event.Repository.FullName = "metcalfc/exeunt"

	req := makeWebhookRequest(t, event, "test-secret")
	w := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	// Give the provisioning goroutine time to call NewVM
	// (it will fail at JIT config since we have a fake token, but should get to NewVM)
	time.Sleep(200 * time.Millisecond)

	mockSSH.mu.Lock()
	defer mockSSH.mu.Unlock()
	if len(mockSSH.NewVMCalls) == 0 {
		t.Error("expected NewVM to be called")
	}
}

func TestWebhookCompletedDestroys(t *testing.T) {
	mockSSH := &MockSSHExecutor{}
	server, _ := newTestServer(t, mockSSH)

	// Pre-populate tracker
	server.tracker.Add(100, "exeunt-abc123", "metcalfc/exeunt", []string{"exe"})
	server.tracker.Update(100, StatusReady)
	// Put a token in the semaphore so Destroy can release it
	server.provisioner.semaphore <- struct{}{}

	event := WorkflowJobEvent{Action: "completed"}
	event.WorkflowJob.ID = 100
	event.Repository.FullName = "metcalfc/exeunt"

	req := makeWebhookRequest(t, event, "test-secret")
	w := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	// Give the destroy goroutine time to run
	time.Sleep(100 * time.Millisecond)

	mockSSH.mu.Lock()
	defer mockSSH.mu.Unlock()
	if len(mockSSH.RemoveCalls) == 0 {
		t.Error("expected RemoveVM to be called")
	}
	if mockSSH.RemoveCalls[0] != "exeunt-abc123" {
		t.Errorf("removed %q, want %q", mockSSH.RemoveCalls[0], "exeunt-abc123")
	}

	if server.tracker.Count() != 0 {
		t.Errorf("tracker count = %d after destroy, want 0", server.tracker.Count())
	}
}

func TestWebhookCompletedIgnoresUntracked(t *testing.T) {
	mockSSH := &MockSSHExecutor{}
	server, _ := newTestServer(t, mockSSH)

	event := WorkflowJobEvent{Action: "completed"}
	event.WorkflowJob.ID = 999
	event.Repository.FullName = "metcalfc/exeunt"

	req := makeWebhookRequest(t, event, "test-secret")
	w := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	time.Sleep(50 * time.Millisecond)

	mockSSH.mu.Lock()
	defer mockSSH.mu.Unlock()
	if len(mockSSH.RemoveCalls) > 0 {
		t.Error("should not remove untracked VM")
	}
}

func TestWebhookDuplicateQueued(t *testing.T) {
	mockSSH := &MockSSHExecutor{}
	server, _ := newTestServer(t, mockSSH)

	// Pre-populate tracker to simulate already-provisioned
	server.tracker.Add(100, "exeunt-abc123", "metcalfc/exeunt", []string{"exe"})

	event := WorkflowJobEvent{Action: "queued"}
	event.WorkflowJob.ID = 100
	event.WorkflowJob.Labels = []string{"self-hosted", "exe"}
	event.Repository.FullName = "metcalfc/exeunt"

	req := makeWebhookRequest(t, event, "test-secret")
	w := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(w, req)

	time.Sleep(100 * time.Millisecond)

	mockSSH.mu.Lock()
	defer mockSSH.mu.Unlock()
	if len(mockSSH.NewVMCalls) > 0 {
		t.Error("should not provision duplicate job")
	}
}

func TestReconcile(t *testing.T) {
	mockSSH := &MockSSHExecutor{
		ListResult: []VMInfo{
			{VMName: "exeunt-exists"},
			{VMName: "exebuilder"},
		},
	}

	dir := t.TempDir()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	tracker := NewTracker(filepath.Join(dir, "state.json"), logger)

	// Add a VM that still exists and one that doesn't
	tracker.Add(1, "exeunt-exists", "metcalfc/exeunt", []string{"exe"})
	tracker.Add(2, "exeunt-gone", "metcalfc/exeunt", []string{"exe"})

	reconcile(context.Background(), tracker, mockSSH, logger)

	if tracker.Count() != 1 {
		t.Fatalf("count after reconcile = %d, want 1", tracker.Count())
	}

	if !tracker.HasJob(1) {
		t.Error("expected job 1 (exeunt-exists) to survive reconcile")
	}
	if tracker.HasJob(2) {
		t.Error("expected job 2 (exeunt-gone) to be removed by reconcile")
	}
}
