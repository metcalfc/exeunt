package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
)

type Config struct {
	WebhookSecret string
	GitHubToken   string
	Repos         []string
	Port          int
	RunnerImage   string
	StateFile     string
	LogLevel      string
	Backends      []BackendConfig
}

// ConfigFile is the JSON config file format.
type ConfigFile struct {
	Repos       []string        `json:"repos"`
	Port        int             `json:"port"`
	RunnerImage string          `json:"runner_image"`
	StateFile   string          `json:"state_file"`
	LogLevel    string          `json:"log_level"`
	Backends    []BackendConfig `json:"backends"`
}

// RepoAllowed checks if a repo matches any configured pattern.
// Patterns support path.Match glob syntax (e.g., "abrihq/*").
func (c *Config) RepoAllowed(repo string) bool {
	for _, pattern := range c.Repos {
		if pattern == repo {
			return true
		}
		if matched, _ := path.Match(pattern, repo); matched {
			return true
		}
	}
	return false
}

func LoadConfig() (*Config, error) {
	c := &Config{
		Port:        8080,
		RunnerImage: "ghcr.io/metcalfc/exeunt-runner:latest",
		StateFile:   "/var/lib/exeunt-autoscaler/state.json",
		LogLevel:    "info",
	}

	// Secrets always from env
	c.WebhookSecret = os.Getenv("AUTOSCALER_WEBHOOK_SECRET")
	if c.WebhookSecret == "" {
		return nil, fmt.Errorf("AUTOSCALER_WEBHOOK_SECRET is required")
	}

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
		c.Repos = cf.Repos
		if cf.Port != 0 {
			c.Port = cf.Port
		}
		if cf.RunnerImage != "" {
			c.RunnerImage = cf.RunnerImage
		}
		if cf.StateFile != "" {
			c.StateFile = cf.StateFile
		}
		if cf.LogLevel != "" {
			c.LogLevel = cf.LogLevel
		}
		c.Backends = cf.Backends
	}

	// Env vars override config file
	if v := os.Getenv("AUTOSCALER_REPOS"); v != "" {
		c.Repos = strings.Split(v, ",")
	}
	if len(c.Repos) == 0 {
		return nil, fmt.Errorf("repos is required (AUTOSCALER_REPOS env or repos in config file)")
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

	if v := os.Getenv("AUTOSCALER_STATE_FILE"); v != "" {
		c.StateFile = v
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
