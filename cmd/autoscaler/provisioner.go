package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
)

type Provisioner struct {
	config    *Config
	tracker   *Tracker
	router    *Router
	github    *GitHubClient
	semaphore chan struct{}
	logger    *slog.Logger
}

func NewProvisioner(cfg *Config, tracker *Tracker, router *Router, gh *GitHubClient, logger *slog.Logger) *Provisioner {
	// Total capacity across all backends
	totalCap := 0
	for _, b := range router.backends {
		totalCap += b.MaxRunners()
	}
	sem := make(chan struct{}, totalCap)
	return &Provisioner{
		config:    cfg,
		tracker:   tracker,
		router:    router,
		github:    gh,
		semaphore: sem,
		logger:    logger,
	}
}

func (p *Provisioner) vmName(jobID int64) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%d", jobID)))
	return fmt.Sprintf("exeunt-%x", h[:3])
}

func (p *Provisioner) Provision(ctx context.Context, event WorkflowJobEvent) {
	jobID := event.WorkflowJob.ID
	repo := event.Repository.FullName
	vmName := p.vmName(jobID)
	log := p.logger.With("job_id", jobID, "vm", vmName, "repo", repo)

	// Dedup check
	if p.tracker.HasJob(jobID) {
		log.Warn("job already tracked, skipping")
		return
	}

	// Route to best backend
	backend := p.router.SelectBackend(event.WorkflowJob.Labels)
	if backend == nil {
		log.Warn("no backend available for labels", "labels", event.WorkflowJob.Labels)
		return
	}

	log = log.With("backend", backend.Name(), "backend_type", backend.Type())

	// Acquire semaphore
	select {
	case p.semaphore <- struct{}{}:
	case <-ctx.Done():
		log.Warn("context cancelled waiting for semaphore")
		return
	}

	p.tracker.Add(jobID, vmName, repo, backend.Name(), event.WorkflowJob.Labels)
	log.Info("provisioning runner")

	if err := p.provision(ctx, log, jobID, vmName, repo, event.WorkflowJob.Labels, backend); err != nil {
		log.Error("provisioning failed", "error", err)
		_ = backend.DestroyRunner(ctx, vmName)
		p.tracker.Remove(jobID)
		<-p.semaphore
	}
}

func (p *Provisioner) provision(ctx context.Context, log *slog.Logger, jobID int64, vmName, repo string, labels []string, backend Backend) error {
	log.Info("creating runner")
	if err := backend.CreateRunner(ctx, vmName, p.config.RunnerImage); err != nil {
		return fmt.Errorf("create runner: %w", err)
	}

	log.Info("generating JIT config")
	jitConfig, err := p.github.GenerateJITConfig(ctx, repo, vmName, labels)
	if err != nil {
		return fmt.Errorf("generate JIT config: %w", err)
	}

	log.Info("starting runner")
	if err := backend.StartRunner(ctx, vmName, jitConfig); err != nil {
		return fmt.Errorf("start runner: %w", err)
	}

	p.tracker.Update(jobID, StatusReady)
	log.Info("runner ready")
	return nil
}

func (p *Provisioner) Destroy(ctx context.Context, event WorkflowJobEvent) {
	jobID := event.WorkflowJob.ID
	log := p.logger.With("job_id", jobID)

	record, ok := p.tracker.Get(jobID)
	if !ok {
		log.Debug("job not tracked, ignoring completed event")
		return
	}

	vmName := record.VMName
	log = log.With("vm", vmName, "backend", record.Backend)
	log.Info("destroying runner")

	p.tracker.Update(jobID, StatusDestroying)

	// Find the backend that provisioned this runner
	var backend Backend
	for _, b := range p.router.backends {
		if b.Name() == record.Backend {
			backend = b
			break
		}
	}

	if backend == nil {
		log.Error("backend not found for runner", "backend", record.Backend)
		p.tracker.Remove(jobID)
		<-p.semaphore
		return
	}

	if err := backend.DestroyRunner(ctx, vmName); err != nil {
		log.Error("failed to destroy runner", "error", err)
		return
	}

	p.tracker.Remove(jobID)
	<-p.semaphore
	log.Info("runner destroyed")
}
