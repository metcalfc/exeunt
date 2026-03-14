package main

import (
	"context"
	"fmt"
	"log/slog"
)

// ExeDevBackend provisions runners as exe.dev VMs.
type ExeDevBackend struct {
	name       string
	maxRunners int
	labels     []string
	priority   int
	image      string
	ssh        SSHExecutor
	logger     *slog.Logger
}

func NewExeDevBackend(cfg BackendConfig, defaultImage string, ssh SSHExecutor, logger *slog.Logger) *ExeDevBackend {
	image := cfg.Image
	if image == "" {
		image = defaultImage
	}
	return &ExeDevBackend{
		name:       cfg.Name,
		maxRunners: cfg.MaxRunners,
		labels:     cfg.Labels,
		priority:   cfg.Priority,
		image:      image,
		ssh:        ssh,
		logger:     logger,
	}
}

func (b *ExeDevBackend) Name() string       { return b.name }
func (b *ExeDevBackend) Type() string       { return "exedev" }
func (b *ExeDevBackend) Labels() []string   { return b.labels }
func (b *ExeDevBackend) Priority() int      { return b.priority }
func (b *ExeDevBackend) MaxRunners() int    { return b.maxRunners }

func (b *ExeDevBackend) CreateRunner(ctx context.Context, name, _ string) error {
	b.logger.Info("creating exe.dev VM", "name", name)
	if err := b.ssh.NewVM(ctx, name, b.image); err != nil {
		return fmt.Errorf("create VM: %w", err)
	}
	if err := b.ssh.WaitForSSH(ctx, name); err != nil {
		return fmt.Errorf("wait for SSH: %w", err)
	}
	return nil
}

func (b *ExeDevBackend) StartRunner(ctx context.Context, name, jitConfig string) error {
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

	b.logger.Info("starting runner on VM", "name", name)
	if _, err := b.ssh.RunOnVM(ctx, name, script); err != nil {
		return fmt.Errorf("start runner: %w", err)
	}
	return nil
}

func (b *ExeDevBackend) DestroyRunner(ctx context.Context, name string) error {
	b.logger.Info("destroying exe.dev VM", "name", name)
	return b.ssh.RemoveVM(ctx, name)
}

func (b *ExeDevBackend) ListRunners(ctx context.Context) ([]string, error) {
	vms, err := b.ssh.ListVMs(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, vm := range vms {
		names = append(names, vm.VMName)
	}
	return names, nil
}
