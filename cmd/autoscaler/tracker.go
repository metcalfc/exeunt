package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type VMStatus string

const (
	StatusProvisioning VMStatus = "provisioning"
	StatusReady        VMStatus = "ready"
	StatusRunning      VMStatus = "running"
	StatusDestroying   VMStatus = "destroying"
)

type VMRecord struct {
	JobID     int64    `json:"job_id"`
	VMName    string   `json:"vm_name"`
	Repo      string   `json:"repo"`
	Status    VMStatus `json:"status"`
	Labels    []string `json:"labels"`
	Backend   string   `json:"backend"`
	CreatedAt string   `json:"created_at"`
}

type Tracker struct {
	mu       sync.RWMutex
	vms      map[int64]*VMRecord
	filePath string
	logger   *slog.Logger
}

func NewTracker(filePath string, logger *slog.Logger) *Tracker {
	// Create state directory once at init, not on every save.
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logger.Error("create state dir", "error", err, "path", dir)
	}
	return &Tracker{
		vms:      make(map[int64]*VMRecord),
		filePath: filePath,
		logger:   logger,
	}
}

func (t *Tracker) Add(jobID int64, vmName, repo, backend string, labels []string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.vms[jobID] = &VMRecord{
		JobID:     jobID,
		VMName:    vmName,
		Repo:      repo,
		Status:    StatusProvisioning,
		Labels:    labels,
		Backend:   backend,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := t.saveLocked(); err != nil {
		t.logger.Error("persist state after add", "error", err, "job_id", jobID)
	}
}

func (t *Tracker) CountByBackend(backend string) int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	count := 0
	for _, r := range t.vms {
		if r.Backend == backend {
			count++
		}
	}
	return count
}

func (t *Tracker) Get(jobID int64) (VMRecord, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	r, ok := t.vms[jobID]
	if !ok {
		return VMRecord{}, false
	}
	return *r, ok
}

func (t *Tracker) Update(jobID int64, status VMStatus) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if r, ok := t.vms[jobID]; ok {
		r.Status = status
		if err := t.saveLocked(); err != nil {
			t.logger.Error("persist state after update", "error", err, "job_id", jobID)
		}
	}
}

func (t *Tracker) Remove(jobID int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.vms, jobID)
	if err := t.saveLocked(); err != nil {
		t.logger.Error("persist state after remove", "error", err, "job_id", jobID)
	}
}

func (t *Tracker) Count() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.vms)
}

func (t *Tracker) ActiveVMs() []VMRecord {
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := make([]VMRecord, 0, len(t.vms))
	for _, r := range t.vms {
		result = append(result, *r)
	}
	return result
}

func (t *Tracker) HasJob(jobID int64) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	_, ok := t.vms[jobID]
	return ok
}

func (t *Tracker) Load() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	data, err := os.ReadFile(t.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read state file: %w", err)
	}

	var records []*VMRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return fmt.Errorf("parse state file: %w", err)
	}

	t.vms = make(map[int64]*VMRecord, len(records))
	for _, r := range records {
		t.vms[r.JobID] = r
	}

	t.logger.Info("loaded state", "vms", len(t.vms))
	return nil
}

// saveLocked writes state to disk. Caller must hold t.mu.
func (t *Tracker) saveLocked() error {
	records := make([]*VMRecord, 0, len(t.vms))
	for _, r := range t.vms {
		records = append(records, r)
	}

	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	tmp := t.filePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	if err := os.Rename(tmp, t.filePath); err != nil {
		return fmt.Errorf("rename state file: %w", err)
	}
	return nil
}
