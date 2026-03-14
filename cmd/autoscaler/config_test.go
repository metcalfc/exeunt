package main

import (
	"os"
	"testing"
)

func setEnv(t *testing.T, key, value string) {
	t.Helper()
	old, existed := os.LookupEnv(key)
	os.Setenv(key, value)
	t.Cleanup(func() {
		if existed {
			os.Setenv(key, old)
		} else {
			os.Unsetenv(key)
		}
	})
}

func unsetEnv(t *testing.T, key string) {
	t.Helper()
	old, existed := os.LookupEnv(key)
	os.Unsetenv(key)
	t.Cleanup(func() {
		if existed {
			os.Setenv(key, old)
		}
	})
}

func setRequiredEnv(t *testing.T) {
	t.Helper()
	setEnv(t, "AUTOSCALER_WEBHOOK_SECRET", "test-secret")
	setEnv(t, "AUTOSCALER_GITHUB_TOKEN", "ghp_test")
	setEnv(t, "AUTOSCALER_REPOS", "metcalfc/exeunt")
	// Point to nonexistent config file so it uses defaults
	setEnv(t, "AUTOSCALER_CONFIG", "/tmp/nonexistent-autoscaler-config.json")
}

func TestLoadConfigDefaults(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Port != 8080 {
		t.Errorf("port = %d, want 8080", cfg.Port)
	}
	if cfg.RunnerImage != "ghcr.io/metcalfc/exeunt-runner:latest" {
		t.Errorf("image = %q, want default", cfg.RunnerImage)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("log_level = %q, want info", cfg.LogLevel)
	}
	// Default backend
	if len(cfg.Backends) != 1 {
		t.Fatalf("backends = %d, want 1", len(cfg.Backends))
	}
	if cfg.Backends[0].Type != "exedev" {
		t.Errorf("default backend type = %q, want exedev", cfg.Backends[0].Type)
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	setRequiredEnv(t)
	setEnv(t, "AUTOSCALER_PORT", "9090")
	setEnv(t, "AUTOSCALER_RUNNER_IMAGE", "custom:latest")
	setEnv(t, "AUTOSCALER_LOG_LEVEL", "debug")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Port != 9090 {
		t.Errorf("port = %d, want 9090", cfg.Port)
	}
	if cfg.RunnerImage != "custom:latest" {
		t.Errorf("image = %q, want custom:latest", cfg.RunnerImage)
	}
}

func TestLoadConfigMissingRequired(t *testing.T) {
	tests := []struct {
		name  string
		unset string
	}{
		{"missing webhook secret", "AUTOSCALER_WEBHOOK_SECRET"},
		{"missing github token", "AUTOSCALER_GITHUB_TOKEN"},
		{"missing repos", "AUTOSCALER_REPOS"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setRequiredEnv(t)
			unsetEnv(t, tt.unset)

			_, err := LoadConfig()
			if err == nil {
				t.Errorf("expected error when %s is missing", tt.unset)
			}
		})
	}
}

func TestLoadConfigInvalidPort(t *testing.T) {
	setRequiredEnv(t)
	setEnv(t, "AUTOSCALER_PORT", "not-a-number")

	_, err := LoadConfig()
	if err == nil {
		t.Error("expected error for invalid port")
	}
}

func TestRepoAllowed(t *testing.T) {
	cfg := &Config{
		Repos: []string{"metcalfc/exeunt", "abrihq/*"},
	}

	tests := []struct {
		name    string
		repo    string
		allowed bool
	}{
		{"exact match", "metcalfc/exeunt", true},
		{"glob match", "abrihq/frontend", true},
		{"glob match another", "abrihq/backend-api", true},
		{"no match", "other/repo", false},
		{"partial match not glob", "metcalfc/other", false},
		{"empty repo", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cfg.RepoAllowed(tt.repo); got != tt.allowed {
				t.Errorf("RepoAllowed(%q) = %v, want %v", tt.repo, got, tt.allowed)
			}
		})
	}
}

func TestRepoAllowedMultiplePatterns(t *testing.T) {
	cfg := &Config{
		Repos: []string{"metcalfc/*", "abrihq/*", "specific/repo"},
	}

	if !cfg.RepoAllowed("metcalfc/anything") {
		t.Error("expected metcalfc/* to match")
	}
	if !cfg.RepoAllowed("specific/repo") {
		t.Error("expected exact match for specific/repo")
	}
	if cfg.RepoAllowed("unknown/repo") {
		t.Error("expected no match for unknown/repo")
	}
}

func TestLoadConfigFromFile(t *testing.T) {
	setRequiredEnv(t)

	dir := t.TempDir()
	configPath := dir + "/config.json"

	configJSON := `{
		"repos": ["fileorg/filerepo"],
		"port": 3000,
		"runner_image": "custom-from-file:v2",
		"state_file": "/tmp/test-state.json",
		"log_level": "debug",
		"backends": [
			{
				"name": "file-backend",
				"type": "exedev",
				"max_runners": 3,
				"labels": ["exe", "linux"],
				"priority": 5
			}
		]
	}`
	os.WriteFile(configPath, []byte(configJSON), 0o644)

	setEnv(t, "AUTOSCALER_CONFIG", configPath)
	// Unset AUTOSCALER_REPOS so config file repos are used
	unsetEnv(t, "AUTOSCALER_REPOS")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.Repos) != 1 || cfg.Repos[0] != "fileorg/filerepo" {
		t.Errorf("repos = %v, want [fileorg/filerepo]", cfg.Repos)
	}
	if cfg.Port != 3000 {
		t.Errorf("port = %d, want 3000", cfg.Port)
	}
	if cfg.RunnerImage != "custom-from-file:v2" {
		t.Errorf("image = %q, want custom-from-file:v2", cfg.RunnerImage)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("log_level = %q, want debug", cfg.LogLevel)
	}
	if len(cfg.Backends) != 1 {
		t.Fatalf("backends = %d, want 1", len(cfg.Backends))
	}
	if cfg.Backends[0].Name != "file-backend" {
		t.Errorf("backend name = %q, want file-backend", cfg.Backends[0].Name)
	}
	if cfg.Backends[0].MaxRunners != 3 {
		t.Errorf("max_runners = %d, want 3", cfg.Backends[0].MaxRunners)
	}
}

func TestLoadConfigInvalidConfigFile(t *testing.T) {
	setRequiredEnv(t)

	dir := t.TempDir()
	configPath := dir + "/bad-config.json"
	os.WriteFile(configPath, []byte("not valid json{{{"), 0o644)

	setEnv(t, "AUTOSCALER_CONFIG", configPath)

	_, err := LoadConfig()
	if err == nil {
		t.Error("expected error for invalid JSON config file")
	}
}

func TestLoadConfigStateFileOverride(t *testing.T) {
	setRequiredEnv(t)
	setEnv(t, "AUTOSCALER_STATE_FILE", "/custom/state.json")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.StateFile != "/custom/state.json" {
		t.Errorf("state_file = %q, want /custom/state.json", cfg.StateFile)
	}
}

func TestLoadConfigReposGlob(t *testing.T) {
	setRequiredEnv(t)

	dir := t.TempDir()
	configPath := dir + "/config.json"

	configJSON := `{
		"repos": ["metcalfc/*", "abrihq/*"]
	}`
	os.WriteFile(configPath, []byte(configJSON), 0o644)

	setEnv(t, "AUTOSCALER_CONFIG", configPath)
	unsetEnv(t, "AUTOSCALER_REPOS")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.Repos) != 2 {
		t.Fatalf("repos = %d, want 2", len(cfg.Repos))
	}

	// Verify the glob patterns are preserved as-is (used by RepoAllowed)
	if !cfg.RepoAllowed("metcalfc/anything") {
		t.Error("expected metcalfc/* to match")
	}
	if !cfg.RepoAllowed("abrihq/something") {
		t.Error("expected abrihq/* to match")
	}
	if cfg.RepoAllowed("other/repo") {
		t.Error("expected other/repo to not match")
	}
}
