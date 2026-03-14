package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	WebhookSecret string
	GitHubToken   string
	Repo          string
	Port          int
	MaxVMs        int
	RunnerImage   string
	RunnerLabels  []string
	StateFile     string
	LogLevel      string
}

func LoadConfig() (*Config, error) {
	c := &Config{
		Port:         8080,
		MaxVMs:       5,
		RunnerImage:  "ghcr.io/metcalfc/exeunt-runner:latest",
		RunnerLabels: []string{"exe"},
		StateFile:    "/var/lib/exeunt-autoscaler/state.json",
		LogLevel:     "info",
	}

	c.WebhookSecret = os.Getenv("AUTOSCALER_WEBHOOK_SECRET")
	if c.WebhookSecret == "" {
		return nil, fmt.Errorf("AUTOSCALER_WEBHOOK_SECRET is required")
	}

	c.GitHubToken = os.Getenv("AUTOSCALER_GITHUB_TOKEN")
	if c.GitHubToken == "" {
		return nil, fmt.Errorf("AUTOSCALER_GITHUB_TOKEN is required")
	}

	c.Repo = os.Getenv("AUTOSCALER_REPO")
	if c.Repo == "" {
		return nil, fmt.Errorf("AUTOSCALER_REPO is required")
	}

	if v := os.Getenv("AUTOSCALER_PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("AUTOSCALER_PORT: %w", err)
		}
		c.Port = p
	}

	if v := os.Getenv("AUTOSCALER_MAX_VMS"); v != "" {
		m, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("AUTOSCALER_MAX_VMS: %w", err)
		}
		c.MaxVMs = m
	}

	if v := os.Getenv("AUTOSCALER_RUNNER_IMAGE"); v != "" {
		c.RunnerImage = v
	}

	if v := os.Getenv("AUTOSCALER_RUNNER_LABELS"); v != "" {
		c.RunnerLabels = strings.Split(v, ",")
	}

	if v := os.Getenv("AUTOSCALER_STATE_FILE"); v != "" {
		c.StateFile = v
	}

	if v := os.Getenv("AUTOSCALER_LOG_LEVEL"); v != "" {
		c.LogLevel = v
	}

	return c, nil
}
