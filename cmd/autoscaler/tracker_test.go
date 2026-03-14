package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func newTestTracker(t *testing.T) *Tracker {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewTracker(path, logger)
}

func TestTrackerAddGetRemove(t *testing.T) {
	tr := newTestTracker(t)

	tr.Add(100, "exeunt-abc123", "metcalfc/exeunt", "test", []string{"exe"})

	if tr.Count() != 1 {
		t.Fatalf("count = %d, want 1", tr.Count())
	}

	record, ok := tr.Get(100)
	if !ok {
		t.Fatal("expected to find job 100")
	}
	if record.VMName != "exeunt-abc123" {
		t.Errorf("vm name = %q, want %q", record.VMName, "exeunt-abc123")
	}
	if record.Status != StatusProvisioning {
		t.Errorf("status = %q, want %q", record.Status, StatusProvisioning)
	}
	if record.Repo != "metcalfc/exeunt" {
		t.Errorf("repo = %q, want %q", record.Repo, "metcalfc/exeunt")
	}

	tr.Remove(100)
	if tr.Count() != 0 {
		t.Fatalf("count = %d after remove, want 0", tr.Count())
	}
	_, ok = tr.Get(100)
	if ok {
		t.Error("expected job 100 to be removed")
	}
}

func TestTrackerUpdate(t *testing.T) {
	tr := newTestTracker(t)

	tr.Add(200, "exeunt-def456", "metcalfc/exeunt", "test", []string{"exe"})
	tr.Update(200, StatusReady)

	record, _ := tr.Get(200)
	if record.Status != StatusReady {
		t.Errorf("status = %q, want %q", record.Status, StatusReady)
	}

	tr.Update(200, StatusRunning)
	record, _ = tr.Get(200)
	if record.Status != StatusRunning {
		t.Errorf("status = %q, want %q", record.Status, StatusRunning)
	}
}

func TestTrackerUpdateNonexistent(t *testing.T) {
	tr := newTestTracker(t)
	// Should not panic
	tr.Update(999, StatusReady)
}

func TestTrackerHasJob(t *testing.T) {
	tr := newTestTracker(t)

	if tr.HasJob(100) {
		t.Error("expected HasJob to return false for nonexistent job")
	}

	tr.Add(100, "exeunt-abc123", "metcalfc/exeunt", "test", []string{"exe"})
	if !tr.HasJob(100) {
		t.Error("expected HasJob to return true")
	}
}

func TestTrackerActiveVMs(t *testing.T) {
	tr := newTestTracker(t)

	tr.Add(1, "exeunt-a", "metcalfc/exeunt", "test", []string{"exe"})
	tr.Add(2, "exeunt-b", "metcalfc/exeunt", "test", []string{"exe"})
	tr.Add(3, "exeunt-c", "metcalfc/exeunt", "test", []string{"exe"})

	vms := tr.ActiveVMs()
	if len(vms) != 3 {
		t.Fatalf("active VMs = %d, want 3", len(vms))
	}
}

func TestTrackerPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// Write state
	tr1 := NewTracker(path, logger)
	tr1.Add(100, "exeunt-abc123", "metcalfc/exeunt", "test", []string{"exe"})
	tr1.Add(200, "exeunt-def456", "metcalfc/exeunt", "test", []string{"exe"})
	tr1.Update(200, StatusReady)

	// Load into new tracker
	tr2 := NewTracker(path, logger)
	if err := tr2.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	if tr2.Count() != 2 {
		t.Fatalf("count after load = %d, want 2", tr2.Count())
	}

	record, ok := tr2.Get(100)
	if !ok {
		t.Fatal("expected to find job 100 after load")
	}
	if record.VMName != "exeunt-abc123" {
		t.Errorf("vm name = %q, want %q", record.VMName, "exeunt-abc123")
	}

	record, ok = tr2.Get(200)
	if !ok {
		t.Fatal("expected to find job 200 after load")
	}
	if record.Status != StatusReady {
		t.Errorf("status = %q after load, want %q", record.Status, StatusReady)
	}
}

func TestTrackerLoadMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.json")
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	tr := NewTracker(path, logger)
	if err := tr.Load(); err != nil {
		t.Fatalf("load from missing file should not error: %v", err)
	}
	if tr.Count() != 0 {
		t.Errorf("count = %d, want 0", tr.Count())
	}
}

func TestTrackerLoadCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	os.WriteFile(path, []byte("not json"), 0o644)

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	tr := NewTracker(path, logger)
	if err := tr.Load(); err == nil {
		t.Error("expected error loading corrupt state file")
	}
}

func TestTrackerCountByBackend(t *testing.T) {
	tr := newTestTracker(t)

	tr.Add(1, "vm-a", "repo", "backend-a", []string{"exe"})
	tr.Add(2, "vm-b", "repo", "backend-a", []string{"exe"})
	tr.Add(3, "vm-c", "repo", "backend-b", []string{"exe"})

	if got := tr.CountByBackend("backend-a"); got != 2 {
		t.Errorf("CountByBackend(backend-a) = %d, want 2", got)
	}
	if got := tr.CountByBackend("backend-b"); got != 1 {
		t.Errorf("CountByBackend(backend-b) = %d, want 1", got)
	}
	if got := tr.CountByBackend("nonexistent"); got != 0 {
		t.Errorf("CountByBackend(nonexistent) = %d, want 0", got)
	}
}
