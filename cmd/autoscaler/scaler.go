package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/actions/scaleset"
	"github.com/actions/scaleset/listener"
	"github.com/google/uuid"
)

// Scaler implements listener.Scaler to provision and destroy runners
// on our backends (Docker hosts via tailscale SSH) in response to
// scaling events from the GitHub Actions Scale Set API.
type Scaler struct {
	logger         *slog.Logger
	runners        runnerState
	maxRunners     int
	capacity       *SharedCapacity // shared across all scalers
	runnerImage    string
	scaleSetID     int
	scalesetClient *scaleset.Client
	backend        Backend
}

// SharedCapacity enforces a hard limit on total runners across all scalers.
type SharedCapacity struct {
	mu      sync.Mutex
	current int
	max     int
}

func NewSharedCapacity(max int) *SharedCapacity {
	return &SharedCapacity{max: max}
}

// TryAcquire attempts to reserve n slots. Returns the number actually acquired (0 to n).
func (c *SharedCapacity) TryAcquire(n int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	avail := c.max - c.current
	if avail <= 0 {
		return 0
	}
	got := min(n, avail)
	c.current += got
	return got
}

func (c *SharedCapacity) Release(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current -= n
	if c.current < 0 {
		c.current = 0
	}
}

var _ listener.Scaler = (*Scaler)(nil)

func (s *Scaler) HandleDesiredRunnerCount(ctx context.Context, count int) (int, error) {
	currentCount := s.runners.count()
	targetRunnerCount := min(s.maxRunners, count)

	switch {
	case targetRunnerCount <= currentCount:
		// Scale down is handled by JobCompleted removing runners.
		return currentCount, nil
	default:
		wanted := targetRunnerCount - currentCount
		// Acquire shared capacity — may get fewer slots than wanted
		// if other scale sets are using the backend.
		got := s.capacity.TryAcquire(wanted)
		if got == 0 {
			s.logger.Warn("backend at capacity, cannot scale up",
				slog.Int("wanted", wanted))
			return currentCount, nil
		}

		s.logger.Info("scaling up",
			slog.Int("current", currentCount),
			slog.Int("wanted", wanted),
			slog.Int("acquired", got))

		created := 0
		for range got {
			if _, err := s.startRunner(ctx); err != nil {
				s.logger.Error("failed to start runner", "error", err)
				// Release unused capacity
				s.capacity.Release(got - created)
				return s.runners.count(), nil
			}
			created++
		}
		return s.runners.count(), nil
	}
}

func (s *Scaler) HandleJobStarted(_ context.Context, jobInfo *scaleset.JobStarted) error {
	s.logger.Info("job started",
		slog.String("runner", jobInfo.RunnerName),
		slog.String("job_id", jobInfo.JobID))
	s.runners.markBusy(jobInfo.RunnerName)
	return nil
}

func (s *Scaler) HandleJobCompleted(ctx context.Context, jobInfo *scaleset.JobCompleted) error {
	s.logger.Info("job completed",
		slog.String("runner", jobInfo.RunnerName),
		slog.String("job_id", jobInfo.JobID),
		slog.String("result", jobInfo.Result))

	vmName := s.runners.markDone(jobInfo.RunnerName)
	if vmName == "" {
		s.logger.Warn("completed job for unknown runner", "runner", jobInfo.RunnerName)
		return nil
	}

	if err := s.backend.DestroyRunner(ctx, vmName); err != nil {
		s.logger.Error("failed to destroy runner", "vm", vmName, "error", err)
		// Release capacity even on destroy failure — the runner is done
		// from GitHub's perspective, and the container will be cleaned up
		// by the monitor or manual intervention.
		s.capacity.Release(1)
		return fmt.Errorf("destroy runner %s: %w", vmName, err)
	}
	s.capacity.Release(1)
	return nil
}

func (s *Scaler) startRunner(ctx context.Context) (string, error) {
	name := fmt.Sprintf("exeunt-%s", uuid.NewString()[:8])

	s.logger.Info("creating runner", "vm", name)
	if err := s.backend.CreateRunner(ctx, name, s.runnerImage); err != nil {
		return "", fmt.Errorf("create runner: %w", err)
	}

	jit, err := s.scalesetClient.GenerateJitRunnerConfig(
		ctx,
		&scaleset.RunnerScaleSetJitRunnerSetting{
			Name: name,
		},
		s.scaleSetID,
	)
	if err != nil {
		// Clean up the container we just created
		if cleanupErr := s.backend.DestroyRunner(ctx, name); cleanupErr != nil {
			s.logger.Warn("cleanup after JIT failure", "vm", name, "error", cleanupErr)
		}
		return "", fmt.Errorf("generate JIT config: %w", err)
	}

	s.logger.Info("starting runner", "vm", name)
	if err := s.backend.StartRunner(ctx, name, jit.EncodedJITConfig); err != nil {
		if cleanupErr := s.backend.DestroyRunner(ctx, name); cleanupErr != nil {
			s.logger.Warn("cleanup after start failure", "vm", name, "error", cleanupErr)
		}
		return "", fmt.Errorf("start runner: %w", err)
	}

	s.runners.addIdle(name)
	s.logger.Info("runner ready", "vm", name)
	return name, nil
}

func (s *Scaler) shutdown(ctx context.Context) {
	s.logger.Info("shutting down runners")
	s.runners.mu.Lock()
	defer s.runners.mu.Unlock()

	for name := range s.runners.idle {
		s.logger.Info("removing idle runner", "vm", name)
		if err := s.backend.DestroyRunner(ctx, name); err != nil {
			s.logger.Error("failed to remove idle runner", "vm", name, "error", err)
		}
	}
	s.capacity.Release(len(s.runners.idle))
	clear(s.runners.idle)

	for name := range s.runners.busy {
		s.logger.Info("removing busy runner", "vm", name)
		if err := s.backend.DestroyRunner(ctx, name); err != nil {
			s.logger.Error("failed to remove busy runner", "vm", name, "error", err)
		}
	}
	s.capacity.Release(len(s.runners.busy))
	clear(s.runners.busy)
}

// runnerState tracks runner names and their lifecycle.
// The runner name IS the VM/container name on the backend.
type runnerState struct {
	mu   sync.Mutex
	idle map[string]struct{}
	busy map[string]struct{}
}

func (r *runnerState) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.idle) + len(r.busy)
}

func (r *runnerState) addIdle(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.idle[name] = struct{}{}
}

func (r *runnerState) markBusy(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.idle[name]; ok {
		delete(r.idle, name)
		r.busy[name] = struct{}{}
	}
}

func (r *runnerState) markDone(name string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.busy[name]; ok {
		delete(r.busy, name)
		return name
	}
	if _, ok := r.idle[name]; ok {
		delete(r.idle, name)
		return name
	}
	return ""
}
