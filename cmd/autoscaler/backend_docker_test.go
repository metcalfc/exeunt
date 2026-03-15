package main

import (
	"testing"
)

func TestNewDockerBackend(t *testing.T) {
	logger := newTestLogger()

	t.Run("defaults", func(t *testing.T) {
		cfg := BackendConfig{
			Name:       "docker-host",
			Type:       "docker",
			Host:       "myhost",
			MaxRunners: 3,
			Labels:     []string{"exe"},
			Priority:   5,
		}
		b := NewDockerBackend(cfg, "default:latest", logger)

		if b.Name() != "docker-host" {
			t.Errorf("Name() = %q, want %q", b.Name(), "docker-host")
		}
		if b.Type() != "docker" {
			t.Errorf("Type() = %q, want %q", b.Type(), "docker")
		}
		if b.MaxRunners() != 3 {
			t.Errorf("MaxRunners() = %d, want %d", b.MaxRunners(), 3)
		}
		if b.Priority() != 5 {
			t.Errorf("Priority() = %d, want %d", b.Priority(), 5)
		}
		if len(b.Labels()) != 1 || b.Labels()[0] != "exe" {
			t.Errorf("Labels() = %v, want [exe]", b.Labels())
		}
		// Default image should be the fallback
		if b.image != "default:latest" {
			t.Errorf("image = %q, want %q", b.image, "default:latest")
		}
		// Default user should be exedev (non-root policy)
		if b.user != "exedev" {
			t.Errorf("user = %q, want %q", b.user, "exedev")
		}
	})

	t.Run("custom image overrides default", func(t *testing.T) {
		cfg := BackendConfig{
			Name:  "docker-host",
			Type:  "docker",
			Host:  "myhost",
			Image: "custom:v2",
		}
		b := NewDockerBackend(cfg, "default:latest", logger)
		if b.image != "custom:v2" {
			t.Errorf("image = %q, want %q", b.image, "custom:v2")
		}
	})

	t.Run("custom user overrides default", func(t *testing.T) {
		cfg := BackendConfig{
			Name: "docker-host",
			Type: "docker",
			Host: "myhost",
			User: "exedev",
		}
		b := NewDockerBackend(cfg, "default:latest", logger)
		if b.user != "exedev" {
			t.Errorf("user = %q, want %q", b.user, "exedev")
		}
	})
}

func TestDockerBackendSSHTarget(t *testing.T) {
	logger := newTestLogger()

	t.Run("user@host", func(t *testing.T) {
		b := NewDockerBackend(BackendConfig{
			Name: "test",
			Host: "myhost",
			User: "admin",
		}, "img:latest", logger)
		if got := b.sshTarget(); got != "admin@myhost" {
			t.Errorf("sshTarget() = %q, want %q", got, "admin@myhost")
		}
	})

	t.Run("default user exedev@host", func(t *testing.T) {
		b := NewDockerBackend(BackendConfig{
			Name: "test",
			Host: "myhost",
		}, "img:latest", logger)
		if got := b.sshTarget(); got != "exedev@myhost" {
			t.Errorf("sshTarget() = %q, want %q", got, "exedev@myhost")
		}
	})

	t.Run("empty user returns host only", func(t *testing.T) {
		// Construct directly to bypass NewDockerBackend's default
		b := &DockerBackend{host: "bare-host"}
		if got := b.sshTarget(); got != "bare-host" {
			t.Errorf("sshTarget() = %q, want %q", got, "bare-host")
		}
	})
}
