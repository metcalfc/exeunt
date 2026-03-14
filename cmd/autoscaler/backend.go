package main

import (
	"context"
	"log/slog"
)

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

// Router selects the best backend for a given set of job labels.
type Router struct {
	backends []Backend
	tracker  *Tracker
	logger   *slog.Logger
}

func NewRouter(backends []Backend, tracker *Tracker, logger *slog.Logger) *Router {
	return &Router{
		backends: backends,
		tracker:  tracker,
		logger:   logger,
	}
}

// SelectBackend picks the best available backend for the given labels.
// Returns nil if no backend can handle the job.
func (r *Router) SelectBackend(labels []string) Backend {
	type candidate struct {
		backend Backend
		count   int
	}

	var candidates []candidate
	for _, b := range r.backends {
		if !labelsMatch(b.Labels(), labels) {
			continue
		}
		count := r.tracker.CountByBackend(b.Name())
		if count >= b.MaxRunners() {
			r.logger.Debug("backend at capacity", "backend", b.Name(), "count", count, "max", b.MaxRunners())
			continue
		}
		candidates = append(candidates, candidate{b, count})
	}

	if len(candidates) == 0 {
		return nil
	}

	// Pick by priority (lower wins), then by available capacity
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.backend.Priority() < best.backend.Priority() {
			best = c
		} else if c.backend.Priority() == best.backend.Priority() && c.count < best.count {
			best = c
		}
	}

	return best.backend
}

// SelectBackendExcluding picks the best backend, skipping any in the exclude set.
// Used for fallback when a backend fails during provisioning.
func (r *Router) SelectBackendExcluding(labels []string, exclude map[string]bool) Backend {
	type candidate struct {
		backend Backend
		count   int
	}

	var candidates []candidate
	for _, b := range r.backends {
		if exclude[b.Name()] {
			continue
		}
		if !labelsMatch(b.Labels(), labels) {
			continue
		}
		count := r.tracker.CountByBackend(b.Name())
		if count >= b.MaxRunners() {
			continue
		}
		candidates = append(candidates, candidate{b, count})
	}

	if len(candidates) == 0 {
		return nil
	}

	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.backend.Priority() < best.backend.Priority() {
			best = c
		} else if c.backend.Priority() == best.backend.Priority() && c.count < best.count {
			best = c
		}
	}

	return best.backend
}

// labelsMatch returns true if the backend handles at least one of the job's labels.
func labelsMatch(backendLabels, jobLabels []string) bool {
	for _, bl := range backendLabels {
		for _, jl := range jobLabels {
			if bl == jl {
				return true
			}
		}
	}
	return false
}
