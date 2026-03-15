package main

import (
	"context"
	"strings"
)

// shellQuote wraps a value in single quotes with proper escaping,
// preventing shell injection when interpolating into shell commands.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// Backend provisions and destroys runners on a specific infrastructure.
type Backend interface {
	Name() string
	Type() string
	Labels() []string
	Priority() int
	MaxRunners() int

	CreateRunner(ctx context.Context, name, image string) error
	StartRunner(ctx context.Context, name, jitConfig string) error
	DestroyRunner(ctx context.Context, name string) error
	ListRunners(ctx context.Context) ([]string, error)
}

// BackendConfig is the JSON representation of a backend in the config file.
type BackendConfig struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"`       // "exedev" or "docker"
	Host       string   `json:"host"`       // SSH host for docker backends
	User       string   `json:"user"`       // SSH user for docker backends
	MaxRunners int      `json:"max_runners"`
	Labels     []string `json:"labels"`
	Priority   int      `json:"priority"` // lower = preferred
	Image      string   `json:"image"`    // override default runner image
}
