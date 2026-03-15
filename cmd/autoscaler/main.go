package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/actions/scaleset"
	"github.com/actions/scaleset/listener"
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

	// Build backends from config
	ssh := &RealSSHExecutor{}
	backends, err := buildBackends(cfg, ssh, logger)
	if err != nil {
		logger.Error("build backends", "error", err)
		os.Exit(1)
	}

	// Use the first backend (single-backend for now; multi-backend
	// routing can be added later when we have multiple hosts with
	// different labels).
	backend := backends[0]

	logger.Info("backend registered",
		"name", backend.Name(),
		"type", backend.Type(),
		"max_runners", backend.MaxRunners(),
		"labels", backend.Labels(),
	)

	// Create scaleset client
	scalesetClient, err := createScalesetClient(cfg)
	if err != nil {
		logger.Error("create scaleset client", "error", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// Create or get the runner scale set
	scaleSet, err := scalesetClient.CreateRunnerScaleSet(ctx, &scaleset.RunnerScaleSet{
		Name:          cfg.ScaleSetName,
		RunnerGroupID: 1, // default runner group
		Labels:        buildScaleSetLabels(cfg),
		RunnerSetting: scaleset.RunnerSetting{
			DisableUpdate: true,
		},
	})
	if err != nil {
		logger.Error("create runner scale set", "error", err)
		os.Exit(1)
	}

	logger.Info("scale set registered",
		"id", scaleSet.ID,
		"name", scaleSet.Name,
	)

	defer func() {
		logger.Info("deleting runner scale set", "id", scaleSet.ID)
		if err := scalesetClient.DeleteRunnerScaleSet(context.WithoutCancel(ctx), scaleSet.ID); err != nil {
			logger.Error("failed to delete runner scale set", "id", scaleSet.ID, "error", err)
		}
	}()

	// Create message session
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "exeunt-autoscaler"
	}
	sessionClient, err := scalesetClient.MessageSessionClient(ctx, scaleSet.ID, hostname)
	if err != nil {
		logger.Error("create message session", "error", err)
		os.Exit(1)
	}
	defer sessionClient.Close(context.Background())

	// Build the scaler
	scaler := &Scaler{
		logger: logger.WithGroup("scaler"),
		runners: runnerState{
			idle: make(map[string]struct{}),
			busy: make(map[string]struct{}),
		},
		maxRunners:     backend.MaxRunners(),
		runnerImage:    cfg.RunnerImage,
		scaleSetID:     scaleSet.ID,
		scalesetClient: scalesetClient,
		backend:        backend,
	}

	defer scaler.shutdown(context.WithoutCancel(ctx))

	// Start health/status HTTP server
	startTime := time.Now()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		resp := map[string]any{
			"status":     "ok",
			"uptime":     time.Since(startTime).String(),
			"active_vms": scaler.runners.count(),
			"max_vms":    scaler.maxRunners,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		scaler.runners.mu.Lock()
		idle := len(scaler.runners.idle)
		busy := len(scaler.runners.busy)
		scaler.runners.mu.Unlock()
		resp := map[string]any{
			"idle":       idle,
			"busy":       busy,
			"total":      idle + busy,
			"max_vms":    scaler.maxRunners,
			"uptime":     time.Since(startTime).String(),
			"scale_set":  scaleSet.Name,
			"backend":    backend.Name(),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server error", "error", err)
		}
	}()
	defer httpServer.Shutdown(context.WithoutCancel(ctx))

	logger.Info("autoscaler started",
		"port", cfg.Port,
		"scale_set", cfg.ScaleSetName,
		"max_runners", backend.MaxRunners(),
		"image", cfg.RunnerImage,
	)

	// Run the listener — this blocks until ctx is cancelled
	l, err := listener.New(sessionClient, listener.Config{
		ScaleSetID: scaleSet.ID,
		MaxRunners: backend.MaxRunners(),
		Logger:     logger.WithGroup("listener"),
	})
	if err != nil {
		logger.Error("create listener", "error", err)
		os.Exit(1)
	}

	if err := l.Run(ctx, scaler); !errors.Is(err, context.Canceled) {
		logger.Error("listener error", "error", err)
		os.Exit(1)
	}

	logger.Info("autoscaler stopped")
}

func createScalesetClient(cfg *Config) (*scaleset.Client, error) {
	return scaleset.NewClientWithPersonalAccessToken(
		scaleset.NewClientWithPersonalAccessTokenConfig{
			GitHubConfigURL:     cfg.RegistrationURL,
			PersonalAccessToken: cfg.GitHubToken,
		},
	)
}

func buildScaleSetLabels(cfg *Config) []scaleset.Label {
	labels := make([]scaleset.Label, len(cfg.ScaleSetLabels))
	for i, name := range cfg.ScaleSetLabels {
		labels[i] = scaleset.Label{Name: name}
	}
	return labels
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
