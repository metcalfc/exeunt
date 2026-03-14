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
	ssh       SSHExecutor
	github    *GitHubClient
	semaphore chan struct{}
	logger    *slog.Logger
}

func NewProvisioner(cfg *Config, tracker *Tracker, ssh SSHExecutor, gh *GitHubClient, logger *slog.Logger) *Provisioner {
	sem := make(chan struct{}, cfg.MaxVMs)
	return &Provisioner{
		config:    cfg,
		tracker:   tracker,
		ssh:       ssh,
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

	// Capacity check
	if p.tracker.Count() >= p.config.MaxVMs {
		log.Warn("at VM capacity, cannot provision", "max", p.config.MaxVMs, "current", p.tracker.Count())
		return
	}

	// Acquire semaphore
	select {
	case p.semaphore <- struct{}{}:
	case <-ctx.Done():
		log.Warn("context cancelled waiting for semaphore")
		return
	}

	p.tracker.Add(jobID, vmName, repo, event.WorkflowJob.Labels)
	log.Info("provisioning VM")

	// From here, any failure must clean up
	if err := p.provision(ctx, log, jobID, vmName, repo, event.WorkflowJob.Labels); err != nil {
		log.Error("provisioning failed", "error", err)
		// Try to destroy the VM if it was created
		_ = p.ssh.RemoveVM(ctx, vmName)
		p.tracker.Remove(jobID)
		<-p.semaphore
	}
}

func (p *Provisioner) provision(ctx context.Context, log *slog.Logger, jobID int64, vmName, repo string, labels []string) error {
	// Create VM
	log.Info("creating exe.dev VM")
	if err := p.ssh.NewVM(ctx, vmName, p.config.RunnerImage); err != nil {
		return fmt.Errorf("create VM: %w", err)
	}

	// Wait for SSH
	log.Info("waiting for SSH")
	if err := p.ssh.WaitForSSH(ctx, vmName); err != nil {
		return fmt.Errorf("wait for SSH: %w", err)
	}

	// Generate JIT config
	log.Info("generating JIT config")
	jitConfig, err := p.github.GenerateJITConfig(ctx, repo, vmName, labels)
	if err != nil {
		return fmt.Errorf("generate JIT config: %w", err)
	}

	// Start runner on VM using systemd-run so the process survives SSH disconnect.
	// nohup/disown is not enough — systemd logind kills processes in the session scope.
	log.Info("starting runner")
	script := fmt.Sprintf(`set -euo pipefail
JIT_CONFIG="%s"
RUNNER_DIR=/home/exedev/actions-runner
RUNNER_LOG="$RUNNER_DIR/runner.log"

systemd-run --unit=actions-runner \
  --property=User=exedev \
  --property=Group=exedev \
  --property=WorkingDirectory="$RUNNER_DIR" \
  --property=StandardOutput=file:"$RUNNER_LOG" \
  --property=StandardError=file:"$RUNNER_LOG" \
  "$RUNNER_DIR/run.sh" --jitconfig "$JIT_CONFIG"

for i in $(seq 1 30); do
  if grep -q "Listening for Jobs" "$RUNNER_LOG" 2>/dev/null; then
    echo "Runner connected"
    exit 0
  fi
  sleep 1
done
echo "Runner did not connect within 30s" >&2
cat "$RUNNER_LOG" >&2
exit 1`, jitConfig)

	if _, err := p.ssh.RunOnVM(ctx, vmName, script); err != nil {
		return fmt.Errorf("start runner: %w", err)
	}

	p.tracker.Update(jobID, StatusReady)
	log.Info("VM ready, runner listening")
	return nil
}
