package main

import (
	"context"
	"errors"
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

	ssh := &RealSSHExecutor{}
	gh := NewGitHubClient(cfg.GitHubToken)
	provisioner := NewProvisioner(cfg, tracker, ssh, gh, logger)
	server := NewServer(cfg, provisioner, tracker, logger)

	// Start reconciliation loop
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go reconcileLoop(ctx, tracker, ssh, logger)

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		if err := server.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	logger.Info("autoscaler started",
		"port", cfg.Port,
		"repo", cfg.Repo,
		"max_vms", cfg.MaxVMs,
		"image", cfg.RunnerImage,
	)

	sig := <-sigCh
	logger.Info("shutting down", "signal", sig.String())

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", "error", err)
	}

	cancel() // Stop reconciliation loop
	logger.Info("autoscaler stopped")
}

func reconcileLoop(ctx context.Context, tracker *Tracker, ssh SSHExecutor, logger *slog.Logger) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcile(ctx, tracker, ssh, logger)
		}
	}
}

func reconcile(ctx context.Context, tracker *Tracker, ssh SSHExecutor, logger *slog.Logger) {
	vms, err := ssh.ListVMs(ctx)
	if err != nil {
		logger.Error("reconcile: list VMs", "error", err)
		return
	}

	// Build set of existing VM names
	existing := make(map[string]bool, len(vms))
	for _, vm := range vms {
		existing[vm.VMName] = true
	}

	// Remove tracker entries for VMs that no longer exist
	for _, record := range tracker.ActiveVMs() {
		if !existing[record.VMName] {
			logger.Warn("reconcile: VM no longer exists, removing from tracker",
				"vm", record.VMName, "job_id", record.JobID)
			tracker.Remove(record.JobID)
		}
	}
}
