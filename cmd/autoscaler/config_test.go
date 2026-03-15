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
	setEnv(t, "AUTOSCALER_GITHUB_TOKEN", "ghp_test")
	setEnv(t, "AUTOSCALER_REGISTRATION_URL", "https://github.com/metcalfc")
	setEnv(t, "AUTOSCALER_SCALE_SET_NAME", "exe")
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
	if cfg.ScaleSetName != "exe" {
		t.Errorf("scale_set_name = %q, want exe", cfg.ScaleSetName)
	}
	if cfg.RegistrationURL != "https://github.com/metcalfc" {
		t.Errorf("registration_url = %q, want https://github.com/metcalfc", cfg.RegistrationURL)
	}
	// Default labels = scale set name
	if len(cfg.ScaleSetLabels) != 1 || cfg.ScaleSetLabels[0] != "exe" {
		t.Errorf("labels = %v, want [exe]", cfg.ScaleSetLabels)
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	setRequiredEnv(t)
	setEnv(t, "AUTOSCALER_PORT", "9090")
	setEnv(t, "AUTOSCALER_RUNNER_IMAGE", "custom:latest")
	setEnv(t, "AUTOSCALER_LOG_LEVEL", "debug")
	setEnv(t, "AUTOSCALER_SCALE_SET_LABELS", "exe,exe-gpu")

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
	if len(cfg.ScaleSetLabels) != 2 {
		t.Fatalf("labels = %v, want 2", cfg.ScaleSetLabels)
	}
	if cfg.ScaleSetLabels[0] != "exe" || cfg.ScaleSetLabels[1] != "exe-gpu" {
		t.Errorf("labels = %v, want [exe exe-gpu]", cfg.ScaleSetLabels)
	}
}

func TestLoadConfigMissingRequired(t *testing.T) {
	tests := []struct {
		name  string
		unset string
	}{
		{"missing github token", "AUTOSCALER_GITHUB_TOKEN"},
		{"missing registration url", "AUTOSCALER_REGISTRATION_URL"},
		{"missing scale set name", "AUTOSCALER_SCALE_SET_NAME"},
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

func TestLoadConfigFromFile(t *testing.T) {
	setRequiredEnv(t)

	dir := t.TempDir()
	configPath := dir + "/config.json"

	configJSON := `{
		"registration_url": "https://github.com/testorg",
		"scale_set_name": "test-set",
		"scale_set_labels": ["exe", "linux"],
		"port": 3000,
		"runner_image": "custom-from-file:v2",
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
	// Unset env overrides so config file values are used
	unsetEnv(t, "AUTOSCALER_REGISTRATION_URL")
	unsetEnv(t, "AUTOSCALER_SCALE_SET_NAME")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.RegistrationURL != "https://github.com/testorg" {
		t.Errorf("registration_url = %q, want https://github.com/testorg", cfg.RegistrationURL)
	}
	if cfg.ScaleSetName != "test-set" {
		t.Errorf("scale_set_name = %q, want test-set", cfg.ScaleSetName)
	}
	if cfg.Port != 3000 {
		t.Errorf("port = %d, want 3000", cfg.Port)
	}
	if cfg.RunnerImage != "custom-from-file:v2" {
		t.Errorf("image = %q, want custom-from-file:v2", cfg.RunnerImage)
	}
	if len(cfg.Backends) != 1 {
		t.Fatalf("backends = %d, want 1", len(cfg.Backends))
	}
	if cfg.Backends[0].Name != "file-backend" {
		t.Errorf("backend name = %q, want file-backend", cfg.Backends[0].Name)
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
