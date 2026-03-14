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
	setEnv(t, "AUTOSCALER_REPO", "metcalfc/exeunt")
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
	if cfg.MaxVMs != 5 {
		t.Errorf("max_vms = %d, want 5", cfg.MaxVMs)
	}
	if cfg.RunnerImage != "ghcr.io/metcalfc/exeunt-runner:latest" {
		t.Errorf("image = %q, want default", cfg.RunnerImage)
	}
	if len(cfg.RunnerLabels) != 1 || cfg.RunnerLabels[0] != "exe" {
		t.Errorf("labels = %v, want [exe]", cfg.RunnerLabels)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("log_level = %q, want info", cfg.LogLevel)
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	setRequiredEnv(t)
	setEnv(t, "AUTOSCALER_PORT", "9090")
	setEnv(t, "AUTOSCALER_MAX_VMS", "3")
	setEnv(t, "AUTOSCALER_RUNNER_IMAGE", "custom:latest")
	setEnv(t, "AUTOSCALER_RUNNER_LABELS", "exe,large")
	setEnv(t, "AUTOSCALER_LOG_LEVEL", "debug")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Port != 9090 {
		t.Errorf("port = %d, want 9090", cfg.Port)
	}
	if cfg.MaxVMs != 3 {
		t.Errorf("max_vms = %d, want 3", cfg.MaxVMs)
	}
	if cfg.RunnerImage != "custom:latest" {
		t.Errorf("image = %q, want custom:latest", cfg.RunnerImage)
	}
	if len(cfg.RunnerLabels) != 2 {
		t.Errorf("labels = %v, want [exe large]", cfg.RunnerLabels)
	}
}

func TestLoadConfigMissingRequired(t *testing.T) {
	tests := []struct {
		name  string
		unset string
	}{
		{"missing webhook secret", "AUTOSCALER_WEBHOOK_SECRET"},
		{"missing github token", "AUTOSCALER_GITHUB_TOKEN"},
		{"missing repo", "AUTOSCALER_REPO"},
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

func TestLoadConfigInvalidMaxVMs(t *testing.T) {
	setRequiredEnv(t)
	setEnv(t, "AUTOSCALER_MAX_VMS", "abc")

	_, err := LoadConfig()
	if err == nil {
		t.Error("expected error for invalid max_vms")
	}
}
