package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		slog.Error("load config", "error", err)
		os.Exit(1)
	}

	var level slog.Level
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	tracker := NewTracker(cfg.StateFile, logger)
	if err := tracker.Load(); err != nil {
		logger.Error("load state", "error", err)
		os.Exit(1)
	}

	// Build backends from config
	ssh := &RealSSHExecutor{}
	backends, err := buildBackends(cfg, ssh, logger)
	if err != nil {
		logger.Error("build backends", "error", err)
		os.Exit(1)
	}

	router := NewRouter(backends, tracker, logger)
	gh := NewGitHubClient(cfg.GitHubToken)
	provisioner := NewProvisioner(cfg, tracker, router, gh, logger)
	server := NewServer(cfg, provisioner, tracker, logger)

	// Start reconciliation loop
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go reconcileLoop(ctx, tracker, backends, provisioner.semaphore, logger)

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		if err := server.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	// Log backend summary
	for _, b := range backends {
		logger.Info("backend registered",
			"name", b.Name(),
			"type", b.Type(),
			"max_runners", b.MaxRunners(),
			"labels", b.Labels(),
			"priority", b.Priority(),
		)
	}

	logger.Info("autoscaler started",
		"port", cfg.Port,
		"repos", cfg.Repos,
		"backends", len(backends),
		"image", cfg.RunnerImage,
	)

	sig := <-sigCh
	logger.Info("shutting down", "signal", sig.String())

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", "error", err)
	}

	cancel()
	logger.Info("autoscaler stopped")
}

func buildBackends(cfg *Config, ssh SSHExecutor, logger *slog.Logger) ([]Backend, error) {
	var backends []Backend
	for _, bc := range cfg.Backends {
		switch bc.Type {
		case "exedev":
			backends = append(backends, NewExeDevBackend(bc, cfg.RunnerImage, ssh, logger))
		case "docker":
			if bc.Host == "" {
				return nil, fmt.Errorf("backend %q: docker backend requires host", bc.Name)
			}
			backends = append(backends, NewDockerBackend(bc, cfg.RunnerImage, logger))
		default:
			return nil, fmt.Errorf("backend %q: unknown type %q", bc.Name, bc.Type)
		}
	}
	if len(backends) == 0 {
		return nil, fmt.Errorf("no backends configured")
	}
	return backends, nil
}

func reconcileLoop(ctx context.Context, tracker *Tracker, backends []Backend, semaphore chan struct{}, logger *slog.Logger) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcile(ctx, tracker, backends, semaphore, logger)
		}
	}
}

func reconcile(ctx context.Context, tracker *Tracker, backends []Backend, semaphore chan struct{}, logger *slog.Logger) {
	// Build set of existing runners across all backends.
	// Track which backends failed so we don't garbage-collect their VMs
	// when we simply couldn't reach the backend.
	existing := make(map[string]bool)          // vm name → exists in backend
	backendByVM := make(map[string]Backend)    // vm name → backend that owns it
	failedBackends := make(map[string]bool)
	for _, b := range backends {
		runners, err := b.ListRunners(ctx)
		if err != nil {
			logger.Error("reconcile: list runners", "backend", b.Name(), "error", err)
			failedBackends[b.Name()] = true
			continue
		}
		for _, name := range runners {
			existing[name] = true
			backendByVM[name] = b
		}
	}

	// Snapshot tracked VMs once — used for both forward and reverse passes.
	activeVMs := tracker.ActiveVMs()

	// Build set of tracked VM names for reverse lookup.
	tracked := make(map[string]bool)
	for _, record := range activeVMs {
		tracked[record.VMName] = true
	}

	// releaseAndRemove removes a tracked entry and releases its semaphore slot.
	// Reconcile bypasses the Provisioner, so it must release the semaphore
	// directly to avoid deadlocking future provisioning.
	releaseAndRemove := func(jobID int64) {
		tracker.Remove(jobID)
		select {
		case <-semaphore:
		default:
		}
	}

	// Forward: remove tracker entries for runners that no longer exist,
	// but skip VMs on backends we couldn't reach.
	const staleTimeout = 10 * time.Minute
	now := time.Now().UTC()
	for _, record := range activeVMs {
		if failedBackends[record.Backend] {
			logger.Debug("reconcile: skipping VM on unreachable backend",
				"vm", record.VMName, "backend", record.Backend)
			continue
		}
		if !existing[record.VMName] {
			logger.Warn("reconcile: runner no longer exists, removing from tracker",
				"vm", record.VMName, "job_id", record.JobID, "backend", record.Backend)
			releaseAndRemove(record.JobID)
			continue
		}

		// Stale check: if a runner has been in "ready" status for too long,
		// the runner process likely exited (completed a different job or crashed)
		// while the container is still alive. Destroy it.
		if record.Status == StatusReady {
			created, err := time.Parse(time.RFC3339, record.CreatedAt)
			if err == nil && now.Sub(created) > staleTimeout {
				logger.Warn("reconcile: runner stuck in ready, destroying",
					"vm", record.VMName, "job_id", record.JobID,
					"age", now.Sub(created).Round(time.Second))
				b := backendByVM[record.VMName]
				if b != nil {
					if err := b.DestroyRunner(ctx, record.VMName); err != nil {
						logger.Error("reconcile: failed to destroy stale runner",
							"vm", record.VMName, "error", err)
					}
				}
				releaseAndRemove(record.JobID)
			}
		}
	}

	// Reverse: destroy orphaned VMs that exist in the backend but aren't tracked.
	// These leak from failed provisioning, tracker state loss, or autoscaler restarts.
	for vmName, b := range backendByVM {
		if tracked[vmName] {
			continue
		}
		logger.Warn("reconcile: destroying orphaned runner",
			"vm", vmName, "backend", b.Name())
		if err := b.DestroyRunner(ctx, vmName); err != nil {
			logger.Error("reconcile: failed to destroy orphan",
				"vm", vmName, "error", err)
		}
	}
}
