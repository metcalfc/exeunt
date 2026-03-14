package main

import (
	"context"
	"log/slog"
	"os"
	"testing"
)

// MockBackend implements the Backend interface for testing.
type MockBackend struct {
	name       string
	labels     []string
	priority   int
	maxRunners int

	CreateErr  error
	StartErr   error
	DestroyErr error
	Runners    []string
}

func (m *MockBackend) Name() string       { return m.name }
func (m *MockBackend) Type() string       { return "mock" }
func (m *MockBackend) Labels() []string   { return m.labels }
func (m *MockBackend) Priority() int      { return m.priority }
func (m *MockBackend) MaxRunners() int    { return m.maxRunners }

func (m *MockBackend) CreateRunner(_ context.Context, _, _ string) error {
	return m.CreateErr
}

func (m *MockBackend) StartRunner(_ context.Context, _, _ string) error {
	return m.StartErr
}

func (m *MockBackend) DestroyRunner(_ context.Context, _ string) error {
	return m.DestroyErr
}

func (m *MockBackend) ListRunners(_ context.Context) ([]string, error) {
	return m.Runners, nil
}

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestLabelsMatch(t *testing.T) {
	tests := []struct {
		name           string
		backendLabels  []string
		jobLabels      []string
		expectMatch    bool
	}{
		{"match single", []string{"exe"}, []string{"self-hosted", "exe"}, true},
		{"match multiple", []string{"exe", "linux"}, []string{"linux"}, true},
		{"no match", []string{"exe"}, []string{"self-hosted", "ubuntu"}, false},
		{"empty backend labels", []string{}, []string{"exe"}, false},
		{"empty job labels", []string{"exe"}, []string{}, false},
		{"both empty", []string{}, []string{}, false},
		{"nil backend labels", nil, []string{"exe"}, false},
		{"nil job labels", []string{"exe"}, nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := labelsMatch(tt.backendLabels, tt.jobLabels)
			if got != tt.expectMatch {
				t.Errorf("labelsMatch(%v, %v) = %v, want %v", tt.backendLabels, tt.jobLabels, got, tt.expectMatch)
			}
		})
	}
}

func TestSelectBackend(t *testing.T) {
	logger := newTestLogger()
	tracker := newTestTracker(t)

	low := &MockBackend{name: "low-pri", labels: []string{"exe"}, priority: 1, maxRunners: 5}
	high := &MockBackend{name: "high-pri", labels: []string{"exe"}, priority: 10, maxRunners: 5}

	router := NewRouter([]Backend{high, low}, tracker, logger)

	t.Run("priority ordering", func(t *testing.T) {
		b := router.SelectBackend([]string{"exe"})
		if b == nil {
			t.Fatal("expected a backend")
		}
		if b.Name() != "low-pri" {
			t.Errorf("got %q, want %q (lower priority wins)", b.Name(), "low-pri")
		}
	})

	t.Run("no candidates for labels", func(t *testing.T) {
		b := router.SelectBackend([]string{"gpu"})
		if b != nil {
			t.Errorf("expected nil, got %q", b.Name())
		}
	})

	t.Run("at capacity", func(t *testing.T) {
		full := &MockBackend{name: "full", labels: []string{"exe"}, priority: 1, maxRunners: 1}
		r := NewRouter([]Backend{full}, tracker, logger)

		// Fill the backend to capacity
		tracker.Add(999, "vm-full", "repo", "full", []string{"exe"})
		defer tracker.Remove(999)

		b := r.SelectBackend([]string{"exe"})
		if b != nil {
			t.Errorf("expected nil when at capacity, got %q", b.Name())
		}
	})
}

func TestSelectBackendExcluding(t *testing.T) {
	logger := newTestLogger()
	tracker := newTestTracker(t)

	primary := &MockBackend{name: "primary", labels: []string{"exe"}, priority: 1, maxRunners: 5}
	fallback := &MockBackend{name: "fallback", labels: []string{"exe"}, priority: 10, maxRunners: 5}

	router := NewRouter([]Backend{primary, fallback}, tracker, logger)

	t.Run("excludes specified backend", func(t *testing.T) {
		b := router.SelectBackendExcluding([]string{"exe"}, map[string]bool{"primary": true})
		if b == nil {
			t.Fatal("expected a backend")
		}
		if b.Name() != "fallback" {
			t.Errorf("got %q, want %q", b.Name(), "fallback")
		}
	})

	t.Run("all excluded returns nil", func(t *testing.T) {
		b := router.SelectBackendExcluding([]string{"exe"}, map[string]bool{"primary": true, "fallback": true})
		if b != nil {
			t.Errorf("expected nil, got %q", b.Name())
		}
	})

	t.Run("empty exclude behaves like SelectBackend", func(t *testing.T) {
		b := router.SelectBackendExcluding([]string{"exe"}, map[string]bool{})
		if b == nil {
			t.Fatal("expected a backend")
		}
		if b.Name() != "primary" {
			t.Errorf("got %q, want %q (lower priority wins)", b.Name(), "primary")
		}
	})
}

func TestSelectBackendLoadBalancing(t *testing.T) {
	logger := newTestLogger()
	tracker := newTestTracker(t)

	a := &MockBackend{name: "a", labels: []string{"exe"}, priority: 1, maxRunners: 5}
	b := &MockBackend{name: "b", labels: []string{"exe"}, priority: 1, maxRunners: 5}

	router := NewRouter([]Backend{a, b}, tracker, logger)

	// Add load to backend "a"
	tracker.Add(100, "vm-1", "repo", "a", []string{"exe"})
	tracker.Add(101, "vm-2", "repo", "a", []string{"exe"})
	defer tracker.Remove(100)
	defer tracker.Remove(101)

	// Backend "b" has zero load, should be selected
	selected := router.SelectBackend([]string{"exe"})
	if selected == nil {
		t.Fatal("expected a backend")
	}
	if selected.Name() != "b" {
		t.Errorf("got %q, want %q (lower count wins at equal priority)", selected.Name(), "b")
	}
}
