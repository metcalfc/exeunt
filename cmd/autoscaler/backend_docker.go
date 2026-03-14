package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

// DockerBackend provisions runners as Docker containers on a remote host via SSH.
type DockerBackend struct {
	name       string
	host       string
	user       string
	maxRunners int
	labels     []string
	priority   int
	image      string
	logger     *slog.Logger
}

func NewDockerBackend(cfg BackendConfig, defaultImage string, logger *slog.Logger) *DockerBackend {
	image := cfg.Image
	if image == "" {
		image = defaultImage
	}
	user := cfg.User
	if user == "" {
		user = "root"
	}
	return &DockerBackend{
		name:       cfg.Name,
		host:       cfg.Host,
		user:       user,
		maxRunners: cfg.MaxRunners,
		labels:     cfg.Labels,
		priority:   cfg.Priority,
		image:      image,
		logger:     logger,
	}
}

func (b *DockerBackend) Name() string       { return b.name }
func (b *DockerBackend) Type() string       { return "docker" }
func (b *DockerBackend) Labels() []string   { return b.labels }
func (b *DockerBackend) Priority() int      { return b.priority }
func (b *DockerBackend) MaxRunners() int    { return b.maxRunners }

func (b *DockerBackend) sshTarget() string {
	if b.user != "" {
		return b.user + "@" + b.host
	}
	return b.host
}

func (b *DockerBackend) sshRun(ctx context.Context, command string) (string, error) {
	cmd := exec.CommandContext(ctx, "ssh", "-o", "ConnectTimeout=10", b.sshTarget(), command)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("ssh %s: %w: %s", b.host, err, stderr.String())
	}
	return stdout.String(), nil
}

func (b *DockerBackend) CreateRunner(ctx context.Context, name, image string) error {
	if image == "" {
		image = b.image
	}
	// Pull latest image, then run container with the runner entrypoint waiting for JIT config.
	// The container starts with a sleep; StartRunner will exec the actual runner.
	cmd := fmt.Sprintf(
		"docker pull %s && docker run -d --name %s --hostname %s %s sleep infinity",
		image, name, name, image,
	)
	b.logger.Info("creating docker runner", "host", b.host, "name", name)
	if _, err := b.sshRun(ctx, cmd); err != nil {
		return fmt.Errorf("create container: %w", err)
	}
	return nil
}

func (b *DockerBackend) StartRunner(ctx context.Context, name, jitConfig string) error {
	// Exec the runner inside the already-running container.
	// Use docker exec to start the runner as exedev with JIT config.
	script := fmt.Sprintf(
		`docker exec -d %s bash -c 'cd /home/exedev/actions-runner && sudo -u exedev ./run.sh --jitconfig "%s" > /home/exedev/actions-runner/runner.log 2>&1'`,
		name, jitConfig,
	)
	b.logger.Info("starting runner in container", "host", b.host, "name", name)
	if _, err := b.sshRun(ctx, script); err != nil {
		return fmt.Errorf("start runner: %w", err)
	}

	// Wait for runner to connect
	checkCmd := fmt.Sprintf(
		`for i in $(seq 1 30); do docker exec %s grep -q "Listening for Jobs" /home/exedev/actions-runner/runner.log 2>/dev/null && echo connected && exit 0; sleep 1; done; echo timeout; exit 1`,
		name,
	)
	out, err := b.sshRun(ctx, checkCmd)
	if err != nil {
		// Grab the log for debugging
		logCmd := fmt.Sprintf("docker exec %s cat /home/exedev/actions-runner/runner.log 2>&1", name)
		logOut, _ := b.sshRun(ctx, logCmd)
		return fmt.Errorf("runner did not connect: %w\nrunner log: %s", err, logOut)
	}
	if strings.TrimSpace(out) != "connected" {
		return fmt.Errorf("unexpected runner status: %s", out)
	}
	return nil
}

func (b *DockerBackend) DestroyRunner(ctx context.Context, name string) error {
	cmd := fmt.Sprintf("docker rm -f %s", name)
	b.logger.Info("destroying docker runner", "host", b.host, "name", name)
	if _, err := b.sshRun(ctx, cmd); err != nil {
		return fmt.Errorf("destroy container: %w", err)
	}
	return nil
}

func (b *DockerBackend) ListRunners(ctx context.Context) ([]string, error) {
	cmd := `docker ps --filter "name=exeunt-" --format "{{.Names}}"`
	out, err := b.sshRun(ctx, cmd)
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}
