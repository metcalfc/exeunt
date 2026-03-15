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

func writeTestConfig(t *testing.T, json string) string {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/config.json"
	os.WriteFile(path, []byte(json), 0o644)
	setEnv(t, "AUTOSCALER_CONFIG", path)
	return path
}

const minimalConfig = `{
	"scale_sets": [
		{"registration_url": "https://github.com/test/repo", "name": "exe"}
	],
	"backends": [
		{"name": "test", "type": "docker", "host": "testhost", "max_runners": 2, "labels": ["exe"]}
	]
}`

func setRequiredEnv(t *testing.T) {
	t.Helper()
	setEnv(t, "AUTOSCALER_GITHUB_TOKEN", "ghp_test")
	writeTestConfig(t, minimalConfig)
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
	if len(cfg.ScaleSets) != 1 {
		t.Fatalf("scale_sets = %d, want 1", len(cfg.ScaleSets))
	}
	if cfg.ScaleSets[0].Name != "exe" {
		t.Errorf("scale_set name = %q, want exe", cfg.ScaleSets[0].Name)
	}
	// Default label = scale set name
	if len(cfg.ScaleSets[0].Labels) != 1 || cfg.ScaleSets[0].Labels[0] != "exe" {
		t.Errorf("labels = %v, want [exe]", cfg.ScaleSets[0].Labels)
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

func TestLoadConfigMissingToken(t *testing.T) {
	setRequiredEnv(t)
	unsetEnv(t, "AUTOSCALER_GITHUB_TOKEN")

	_, err := LoadConfig()
	if err == nil {
		t.Error("expected error when token is missing")
	}
}

func TestLoadConfigMissingScaleSets(t *testing.T) {
	setEnv(t, "AUTOSCALER_GITHUB_TOKEN", "ghp_test")
	writeTestConfig(t, `{
		"backends": [{"name": "b", "type": "docker", "host": "h", "max_runners": 1, "labels": ["exe"]}]
	}`)

	_, err := LoadConfig()
	if err == nil {
		t.Error("expected error when scale_sets is empty")
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

func TestLoadConfigMultipleScaleSets(t *testing.T) {
	setEnv(t, "AUTOSCALER_GITHUB_TOKEN", "ghp_test")
	writeTestConfig(t, `{
		"scale_sets": [
			{"registration_url": "https://github.com/org1/repo1", "name": "exe", "labels": ["exe", "exe-gpu"]},
			{"registration_url": "https://github.com/org2/repo2", "name": "exe", "labels": ["exe"]}
		],
		"backends": [
			{"name": "b", "type": "docker", "host": "h", "max_runners": 6, "labels": ["exe"]}
		]
	}`)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.ScaleSets) != 2 {
		t.Fatalf("scale_sets = %d, want 2", len(cfg.ScaleSets))
	}
	if cfg.ScaleSets[0].RegistrationURL != "https://github.com/org1/repo1" {
		t.Errorf("url = %q", cfg.ScaleSets[0].RegistrationURL)
	}
	if len(cfg.ScaleSets[0].Labels) != 2 {
		t.Errorf("labels = %v, want 2", cfg.ScaleSets[0].Labels)
	}
}

func TestLoadConfigScaleSetValidation(t *testing.T) {
	setEnv(t, "AUTOSCALER_GITHUB_TOKEN", "ghp_test")

	t.Run("missing registration_url", func(t *testing.T) {
		writeTestConfig(t, `{
			"scale_sets": [{"name": "exe"}],
			"backends": [{"name": "b", "type": "docker", "host": "h", "max_runners": 1, "labels": ["exe"]}]
		}`)
		_, err := LoadConfig()
		if err == nil {
			t.Error("expected error")
		}
	})

	t.Run("missing name", func(t *testing.T) {
		writeTestConfig(t, `{
			"scale_sets": [{"registration_url": "https://github.com/o/r"}],
			"backends": [{"name": "b", "type": "docker", "host": "h", "max_runners": 1, "labels": ["exe"]}]
		}`)
		_, err := LoadConfig()
		if err == nil {
			t.Error("expected error")
		}
	})
}
