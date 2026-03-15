package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	GitHubToken     string
	RegistrationURL string // e.g., https://github.com/org or https://github.com/org/repo
	ScaleSetName    string
	ScaleSetLabels  []string
	Port            int
	RunnerImage     string
	LogLevel        string
	Backends        []BackendConfig
}

// ConfigFile is the JSON config file format.
type ConfigFile struct {
	RegistrationURL string          `json:"registration_url"`
	ScaleSetName    string          `json:"scale_set_name"`
	ScaleSetLabels  []string        `json:"scale_set_labels"`
	Port            int             `json:"port"`
	RunnerImage     string          `json:"runner_image"`
	LogLevel        string          `json:"log_level"`
	Backends        []BackendConfig `json:"backends"`
}

func LoadConfig() (*Config, error) {
	c := &Config{
		Port:        8080,
		RunnerImage: "ghcr.io/metcalfc/exeunt-runner:latest",
		LogLevel:    "info",
	}

	// Token always from env
	c.GitHubToken = os.Getenv("AUTOSCALER_GITHUB_TOKEN")
	if c.GitHubToken == "" {
		return nil, fmt.Errorf("AUTOSCALER_GITHUB_TOKEN is required")
	}

	// Load config file if present
	configPath := os.Getenv("AUTOSCALER_CONFIG")
	if configPath == "" {
		configPath = "/etc/exeunt-autoscaler/config.json"
	}

	if data, err := os.ReadFile(configPath); err == nil {
		var cf ConfigFile
		if err := json.Unmarshal(data, &cf); err != nil {
			return nil, fmt.Errorf("parse config file %s: %w", configPath, err)
		}
		c.RegistrationURL = cf.RegistrationURL
		c.ScaleSetName = cf.ScaleSetName
		c.ScaleSetLabels = cf.ScaleSetLabels
		if cf.Port != 0 {
			c.Port = cf.Port
		}
		if cf.RunnerImage != "" {
			c.RunnerImage = cf.RunnerImage
		}
		if cf.LogLevel != "" {
			c.LogLevel = cf.LogLevel
		}
		c.Backends = cf.Backends
	}

	// Env vars override config file
	if v := os.Getenv("AUTOSCALER_REGISTRATION_URL"); v != "" {
		c.RegistrationURL = v
	}
	if c.RegistrationURL == "" {
		return nil, fmt.Errorf("registration_url is required (AUTOSCALER_REGISTRATION_URL env or registration_url in config file)")
	}

	if v := os.Getenv("AUTOSCALER_SCALE_SET_NAME"); v != "" {
		c.ScaleSetName = v
	}
	if c.ScaleSetName == "" {
		return nil, fmt.Errorf("scale_set_name is required (AUTOSCALER_SCALE_SET_NAME env or scale_set_name in config file)")
	}

	if v := os.Getenv("AUTOSCALER_SCALE_SET_LABELS"); v != "" {
		c.ScaleSetLabels = strings.Split(v, ",")
	}
	if len(c.ScaleSetLabels) == 0 {
		// Default label is the scale set name
		c.ScaleSetLabels = []string{c.ScaleSetName}
	}

	if v := os.Getenv("AUTOSCALER_PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("AUTOSCALER_PORT: %w", err)
		}
		c.Port = p
	}

	if v := os.Getenv("AUTOSCALER_RUNNER_IMAGE"); v != "" {
		c.RunnerImage = v
	}

	if v := os.Getenv("AUTOSCALER_LOG_LEVEL"); v != "" {
		c.LogLevel = v
	}

	// Default backend if none configured
	if len(c.Backends) == 0 {
		c.Backends = []BackendConfig{
			{
				Name:       "exe.dev",
				Type:       "exedev",
				MaxRunners: 5,
				Labels:     []string{"exe"},
				Priority:   10,
			},
		}
	}

	return c, nil
}
