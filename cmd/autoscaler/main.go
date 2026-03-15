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
	"sync"
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

	backend := backends[0]

	logger.Info("backend registered",
		"name", backend.Name(),
		"type", backend.Type(),
		"max_runners", backend.MaxRunners(),
		"labels", backend.Labels(),
	)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	hostname, err := os.Hostname()
	if err != nil {
		hostname = "exeunt-autoscaler"
	}

	// Per-runner capacity is split evenly across scale sets.
	maxPerSet := backend.MaxRunners() / len(cfg.ScaleSets)
	if maxPerSet < 1 {
		maxPerSet = 1
	}

	// Register all scale sets and build scalers.
	type scaleSetInstance struct {
		scaler         *Scaler
		listener       *listener.Listener
		scalesetClient *scaleset.Client
		sessionClient  *scaleset.MessageSessionClient
		scaleSet       *scaleset.RunnerScaleSet
	}

	var instances []scaleSetInstance
	for _, ssCfg := range cfg.ScaleSets {
		ssLog := logger.With("scale_set", ssCfg.Name, "url", ssCfg.RegistrationURL)

		client, err := scaleset.NewClientWithPersonalAccessToken(
			scaleset.NewClientWithPersonalAccessTokenConfig{
				GitHubConfigURL:     ssCfg.RegistrationURL,
				PersonalAccessToken: cfg.GitHubToken,
			},
		)
		if err != nil {
			ssLog.Error("create scaleset client", "error", err)
			os.Exit(1)
		}

		labels := make([]scaleset.Label, len(ssCfg.Labels))
		for i, name := range ssCfg.Labels {
			labels[i] = scaleset.Label{Name: name}
		}

		ss, err := client.CreateRunnerScaleSet(ctx, &scaleset.RunnerScaleSet{
			Name:          ssCfg.Name,
			RunnerGroupID: 1,
			Labels:        labels,
			RunnerSetting: scaleset.RunnerSetting{
				DisableUpdate: true,
			},
		})
		if err != nil {
			ssLog.Error("create runner scale set", "error", err)
			os.Exit(1)
		}

		ssLog.Info("scale set registered", "id", ss.ID)

		session, err := client.MessageSessionClient(ctx, ss.ID, hostname)
		if err != nil {
			ssLog.Error("create message session", "error", err)
			os.Exit(1)
		}

		l, err := listener.New(session, listener.Config{
			ScaleSetID: ss.ID,
			MaxRunners: maxPerSet,
			Logger:     ssLog.WithGroup("listener"),
		})
		if err != nil {
			ssLog.Error("create listener", "error", err)
			os.Exit(1)
		}

		scaler := &Scaler{
			logger: ssLog.WithGroup("scaler"),
			runners: runnerState{
				idle: make(map[string]struct{}),
				busy: make(map[string]struct{}),
			},
			maxRunners:     maxPerSet,
			runnerImage:    cfg.RunnerImage,
			scaleSetID:     ss.ID,
			scalesetClient: client,
			backend:        backend,
		}

		instances = append(instances, scaleSetInstance{
			scaler:         scaler,
			listener:       l,
			scalesetClient: client,
			sessionClient:  session,
			scaleSet:       ss,
		})
	}

	// Cleanup on shutdown
	defer func() {
		shutdownCtx := context.WithoutCancel(ctx)
		for _, inst := range instances {
			inst.scaler.shutdown(shutdownCtx)
			inst.sessionClient.Close(shutdownCtx)
			logger.Info("deleting runner scale set", "id", inst.scaleSet.ID, "name", inst.scaleSet.Name)
			if err := inst.scalesetClient.DeleteRunnerScaleSet(shutdownCtx, inst.scaleSet.ID); err != nil {
				logger.Error("failed to delete runner scale set", "id", inst.scaleSet.ID, "error", err)
			}
		}
	}()

	// Health/status HTTP server
	startTime := time.Now()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		total := 0
		for _, inst := range instances {
			total += inst.scaler.runners.count()
		}
		resp := map[string]any{
			"status":      "ok",
			"uptime":      time.Since(startTime).String(),
			"active_vms":  total,
			"max_vms":     backend.MaxRunners(),
			"scale_sets":  len(instances),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		sets := make([]map[string]any, len(instances))
		for i, inst := range instances {
			inst.scaler.runners.mu.Lock()
			idle := len(inst.scaler.runners.idle)
			busy := len(inst.scaler.runners.busy)
			inst.scaler.runners.mu.Unlock()
			sets[i] = map[string]any{
				"name":  inst.scaleSet.Name,
				"id":    inst.scaleSet.ID,
				"idle":  idle,
				"busy":  busy,
				"total": idle + busy,
				"max":   inst.scaler.maxRunners,
			}
		}
		resp := map[string]any{
			"scale_sets": sets,
			"backend":    backend.Name(),
			"max_vms":    backend.MaxRunners(),
			"uptime":     time.Since(startTime).String(),
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
		"scale_sets", len(instances),
		"max_runners", backend.MaxRunners(),
		"image", cfg.RunnerImage,
	)

	// Run all listeners concurrently. If any exits with an error, cancel all.
	var wg sync.WaitGroup
	for _, inst := range instances {
		wg.Add(1)
		go func(inst scaleSetInstance) {
			defer wg.Done()
			if err := inst.listener.Run(ctx, inst.scaler); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("listener error", "scale_set", inst.scaleSet.Name, "error", err)
				cancel()
			}
		}(inst)
	}
	wg.Wait()

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
