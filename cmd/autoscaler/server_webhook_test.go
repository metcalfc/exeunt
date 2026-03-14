package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWebhookInProgressUpdatesTracker(t *testing.T) {
	mockSSH := &MockSSHExecutor{}
	server, _ := newTestServer(t, mockSSH)

	// Pre-populate tracker with a provisioned job
	server.tracker.Add(100, "exeunt-abc123", "metcalfc/exeunt", "test-exedev", []string{"exe"})
	server.tracker.Update(100, StatusReady)

	event := WorkflowJobEvent{Action: "in_progress"}
	event.WorkflowJob.ID = 100
	event.WorkflowJob.Labels = []string{"self-hosted", "exe"}
	event.Repository.FullName = "metcalfc/exeunt"

	req := makeWebhookRequest(t, event, "test-secret")
	w := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	record, ok := server.tracker.Get(100)
	if !ok {
		t.Fatal("expected job 100 to still be tracked")
	}
	if record.Status != StatusRunning {
		t.Errorf("status = %q, want %q", record.Status, StatusRunning)
	}
}

func TestWebhookInProgressUntrackedJob(t *testing.T) {
	mockSSH := &MockSSHExecutor{}
	server, _ := newTestServer(t, mockSSH)

	// in_progress for a job that isn't tracked — should not crash
	event := WorkflowJobEvent{Action: "in_progress"}
	event.WorkflowJob.ID = 999
	event.WorkflowJob.Labels = []string{"self-hosted", "exe"}
	event.Repository.FullName = "metcalfc/exeunt"

	req := makeWebhookRequest(t, event, "test-secret")
	w := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestWebhookRepoNotAllowed(t *testing.T) {
	mockSSH := &MockSSHExecutor{}
	server, _ := newTestServer(t, mockSSH)

	event := WorkflowJobEvent{Action: "queued"}
	event.WorkflowJob.ID = 100
	event.WorkflowJob.Labels = []string{"self-hosted", "exe"}
	event.Repository.FullName = "other-org/other-repo"

	req := makeWebhookRequest(t, event, "test-secret")
	w := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	time.Sleep(50 * time.Millisecond)

	mockSSH.mu.Lock()
	defer mockSSH.mu.Unlock()
	if len(mockSSH.NewVMCalls) > 0 {
		t.Error("should not provision for disallowed repo")
	}
}

func TestWebhookUnhandledAction(t *testing.T) {
	mockSSH := &MockSSHExecutor{}
	server, _ := newTestServer(t, mockSSH)

	event := WorkflowJobEvent{Action: "waiting"}
	event.WorkflowJob.ID = 100
	event.WorkflowJob.Labels = []string{"self-hosted", "exe"}
	event.Repository.FullName = "metcalfc/exeunt"

	req := makeWebhookRequest(t, event, "test-secret")
	w := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestWebhookInvalidJSON(t *testing.T) {
	mockSSH := &MockSSHExecutor{}
	server, _ := newTestServer(t, mockSSH)

	payload := []byte(`not valid json{{{`)
	req := httptest.NewRequest("POST", "/webhook", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "workflow_job")
	req.Header.Set("X-Hub-Signature-256", computeSignature(payload, []byte("test-secret")))

	w := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestServerStartAndShutdown(t *testing.T) {
	mockSSH := &MockSSHExecutor{}
	server, _ := newTestServer(t, mockSSH)

	// Bind to a random port
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close() // Free the port for the server

	server.httpServer.Addr = addr

	// Start server in background
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Start()
	}()

	// Wait for server to be ready
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Make a real HTTP request
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status = %v, want ok", body["status"])
	}

	// Shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	// Server should return ErrServerClosed
	if err := <-errCh; err != http.ErrServerClosed {
		t.Errorf("server error = %v, want ErrServerClosed", err)
	}
}

func TestHealthzMaxVmsReflectsBackendCapacity(t *testing.T) {
	mockSSH := &MockSSHExecutor{}
	server, _ := newTestServer(t, mockSSH)

	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(w, req)

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Config has one backend with MaxRunners=5
	maxVMs := resp["max_vms"].(float64)
	if maxVMs != 5 {
		t.Errorf("max_vms = %v, want 5", maxVMs)
	}
}

func TestStatusWithActiveVMs(t *testing.T) {
	mockSSH := &MockSSHExecutor{}
	server, _ := newTestServer(t, mockSSH)

	// Add some VMs to tracker
	server.tracker.Add(100, "exeunt-aaa", "metcalfc/exeunt", "test-exedev", []string{"exe"})
	server.tracker.Add(200, "exeunt-bbb", "metcalfc/exeunt", "test-exedev", []string{"exe"})
	server.tracker.Update(200, StatusRunning)

	req := httptest.NewRequest("GET", "/status", nil)
	w := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	count := resp["count"].(float64)
	if count != 2 {
		t.Errorf("count = %v, want 2", count)
	}

	activeVMs := resp["active_vms"].([]any)
	if len(activeVMs) != 2 {
		t.Errorf("active_vms len = %d, want 2", len(activeVMs))
	}

	// Verify uptime is a non-empty string
	uptime := resp["uptime"].(string)
	if uptime == "" {
		t.Error("uptime should not be empty")
	}
}

func TestWebhookMethodRouting(t *testing.T) {
	mockSSH := &MockSSHExecutor{}
	server, _ := newTestServer(t, mockSSH)

	// GET /webhook should return 405 (Go 1.22+ pattern-based routing)
	req := httptest.NewRequest("GET", "/webhook", nil)
	w := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(w, req)

	if w.Code == http.StatusOK {
		t.Error("GET /webhook should not return 200")
	}
}

func TestWebhookBodyTooLarge(t *testing.T) {
	mockSSH := &MockSSHExecutor{}
	server, _ := newTestServer(t, mockSSH)

	// Create a body larger than 1MB
	largeBody := strings.Repeat("a", 2<<20)
	req := httptest.NewRequest("POST", "/webhook", strings.NewReader(largeBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "workflow_job")
	req.Header.Set("X-Hub-Signature-256", "sha256=anything")

	w := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d for oversized body", w.Code, http.StatusBadRequest)
	}
}
