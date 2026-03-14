package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestTrackerConcurrentAccess(t *testing.T) {
	tr := newTestTracker(t)

	var wg sync.WaitGroup

	// Concurrent adds
	for i := int64(0); i < 50; i++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			tr.Add(id, fmt.Sprintf("vm-%d", id), "repo", "backend", []string{"exe"})
		}(i)
	}
	wg.Wait()

	if tr.Count() != 50 {
		t.Errorf("count after concurrent adds = %d, want 50", tr.Count())
	}

	// Concurrent updates
	for i := int64(0); i < 50; i++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			tr.Update(id, StatusRunning)
		}(i)
	}
	wg.Wait()

	// Concurrent reads
	for i := int64(0); i < 50; i++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			record, ok := tr.Get(id)
			if !ok {
				t.Errorf("expected to find job %d", id)
			}
			if record.Status != StatusRunning {
				t.Errorf("job %d status = %q, want %q", id, record.Status, StatusRunning)
			}
		}(i)
	}
	wg.Wait()

	// Concurrent removes
	for i := int64(0); i < 50; i++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			tr.Remove(id)
		}(i)
	}
	wg.Wait()

	if tr.Count() != 0 {
		t.Errorf("count after concurrent removes = %d, want 0", tr.Count())
	}
}

func TestTrackerPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	logger := newTestLogger()

	// Create tracker, add records with various states
	tr1 := NewTracker(path, logger)
	tr1.Add(1, "exeunt-aaa", "org/repo1", "backend-a", []string{"exe", "linux"})
	tr1.Add(2, "exeunt-bbb", "org/repo2", "backend-b", []string{"exe"})
	tr1.Update(1, StatusRunning)
	tr1.Update(2, StatusReady)
	tr1.Add(3, "exeunt-ccc", "org/repo1", "backend-a", []string{"exe"})
	tr1.Remove(3) // Remove one to verify it's gone after reload

	// Verify the state file exists and is valid JSON
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	var records []json.RawMessage
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatalf("state file is not valid JSON: %v", err)
	}
	if len(records) != 2 {
		t.Errorf("state file has %d records, want 2", len(records))
	}

	// Load into new tracker and verify all state
	tr2 := NewTracker(path, logger)
	if err := tr2.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	if tr2.Count() != 2 {
		t.Fatalf("count after load = %d, want 2", tr2.Count())
	}

	r1, ok := tr2.Get(1)
	if !ok {
		t.Fatal("expected job 1 after load")
	}
	if r1.VMName != "exeunt-aaa" || r1.Repo != "org/repo1" || r1.Backend != "backend-a" || r1.Status != StatusRunning {
		t.Errorf("job 1 = %+v, unexpected field values", r1)
	}
	if len(r1.Labels) != 2 || r1.Labels[0] != "exe" || r1.Labels[1] != "linux" {
		t.Errorf("job 1 labels = %v, want [exe, linux]", r1.Labels)
	}
	if r1.CreatedAt == "" {
		t.Error("job 1 CreatedAt should not be empty")
	}

	r2, ok := tr2.Get(2)
	if !ok {
		t.Fatal("expected job 2 after load")
	}
	if r2.Status != StatusReady {
		t.Errorf("job 2 status = %q, want %q", r2.Status, StatusReady)
	}

	// Job 3 was removed — should not exist
	if tr2.HasJob(3) {
		t.Error("job 3 should not exist after load (was removed)")
	}
}

func TestTrackerGetReturnsCopy(t *testing.T) {
	tr := newTestTracker(t)
	tr.Add(1, "exeunt-aaa", "repo", "backend", []string{"exe"})

	// Get returns a value copy, so modifying it should not affect the tracker
	record, _ := tr.Get(1)
	record.Status = StatusDestroying
	record.VMName = "modified"

	// Re-get should show original values
	original, _ := tr.Get(1)
	if original.Status != StatusProvisioning {
		t.Errorf("status = %q, want %q (Get should return copy)", original.Status, StatusProvisioning)
	}
	if original.VMName != "exeunt-aaa" {
		t.Errorf("VMName = %q, want %q (Get should return copy)", original.VMName, "exeunt-aaa")
	}
}

func TestTrackerActiveVMsReturnsCopies(t *testing.T) {
	tr := newTestTracker(t)
	tr.Add(1, "exeunt-aaa", "repo", "backend", []string{"exe"})

	vms := tr.ActiveVMs()
	if len(vms) != 1 {
		t.Fatalf("got %d VMs, want 1", len(vms))
	}

	// Modify the returned slice entry
	vms[0].Status = StatusDestroying
	vms[0].VMName = "modified"

	// Re-get should show original
	original, _ := tr.Get(1)
	if original.Status != StatusProvisioning {
		t.Errorf("status = %q (ActiveVMs should return copies)", original.Status)
	}
}

func TestTrackerSaveFailsGracefully(t *testing.T) {
	// Point tracker at a path where the parent is a file, not a directory
	dir := t.TempDir()
	blockingFile := filepath.Join(dir, "blocker")
	os.WriteFile(blockingFile, []byte("not a directory"), 0o644)

	logger := newTestLogger()
	tr := NewTracker(filepath.Join(blockingFile, "subdir", "state.json"), logger)

	// Add should not panic even though save will fail
	tr.Add(1, "vm", "repo", "backend", []string{"exe"})
	if tr.Count() != 1 {
		t.Errorf("count = %d, want 1 (in-memory state should work even if save fails)", tr.Count())
	}

	// Update should also survive a save failure
	tr.Update(1, StatusRunning)
	record, ok := tr.Get(1)
	if !ok {
		t.Fatal("expected job 1 after update")
	}
	if record.Status != StatusRunning {
		t.Errorf("status = %q, want %q (in-memory update should work)", record.Status, StatusRunning)
	}

	// Remove should also survive a save failure
	tr.Remove(1)
	if tr.Count() != 0 {
		t.Errorf("count = %d, want 0 (in-memory remove should work)", tr.Count())
	}
}

func TestTrackerSaveRenameError(t *testing.T) {
	// Create a valid state dir, then make the state file a directory
	// so the rename from .tmp to the final path fails
	dir := t.TempDir()
	stateFile := filepath.Join(dir, "state.json")

	logger := newTestLogger()
	tr := NewTracker(stateFile, logger)

	// First add works (creates state.json)
	tr.Add(1, "vm-1", "repo", "backend", []string{"exe"})
	if tr.Count() != 1 {
		t.Fatalf("count = %d, want 1", tr.Count())
	}

	// Replace the state file with a directory to cause rename to fail
	os.Remove(stateFile)
	os.Mkdir(stateFile, 0o755)

	// Add should still work in-memory even though rename will fail
	tr.Add(2, "vm-2", "repo", "backend", []string{"exe"})
	if tr.Count() != 2 {
		t.Errorf("count = %d, want 2 (in-memory should work despite rename error)", tr.Count())
	}
}

func TestTrackerCountByBackendAccuracy(t *testing.T) {
	tr := newTestTracker(t)

	tr.Add(1, "vm-1", "repo", "alpha", []string{"exe"})
	tr.Add(2, "vm-2", "repo", "alpha", []string{"exe"})
	tr.Add(3, "vm-3", "repo", "beta", []string{"exe"})
	tr.Add(4, "vm-4", "repo", "alpha", []string{"exe"})

	if got := tr.CountByBackend("alpha"); got != 3 {
		t.Errorf("CountByBackend(alpha) = %d, want 3", got)
	}
	if got := tr.CountByBackend("beta"); got != 1 {
		t.Errorf("CountByBackend(beta) = %d, want 1", got)
	}

	tr.Remove(2)
	if got := tr.CountByBackend("alpha"); got != 2 {
		t.Errorf("CountByBackend(alpha) after remove = %d, want 2", got)
	}
}
