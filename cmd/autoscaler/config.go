package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

type ScaleSetConfig struct {
	RegistrationURL string   `json:"registration_url"`
	Name            string   `json:"name"`
	Labels          []string `json:"labels"`
}

type Config struct {
	GitHubToken string
	ScaleSets   []ScaleSetConfig
	Port        int
	RunnerImage string
	LogLevel    string
	Backends    []BackendConfig
}

type ConfigFile struct {
	ScaleSets   []ScaleSetConfig `json:"scale_sets"`
	Port        int              `json:"port"`
	RunnerImage string           `json:"runner_image"`
	LogLevel    string           `json:"log_level"`
	Backends    []BackendConfig  `json:"backends"`
}

func LoadConfig() (*Config, error) {
	c := &Config{
		Port:        8080,
		RunnerImage: "ghcr.io/metcalfc/exeunt-runner:latest",
		LogLevel:    "info",
	}

	c.GitHubToken = os.Getenv("AUTOSCALER_GITHUB_TOKEN")
	if c.GitHubToken == "" {
		return nil, fmt.Errorf("AUTOSCALER_GITHUB_TOKEN is required")
	}

	configPath := os.Getenv("AUTOSCALER_CONFIG")
	if configPath == "" {
		configPath = "/etc/exeunt-autoscaler/config.json"
	}

	if data, err := os.ReadFile(configPath); err == nil {
		var cf ConfigFile
		if err := json.Unmarshal(data, &cf); err != nil {
			return nil, fmt.Errorf("parse config file %s: %w", configPath, err)
		}
		c.ScaleSets = cf.ScaleSets
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

	if len(c.ScaleSets) == 0 {
		return nil, fmt.Errorf("scale_sets is required in config file")
	}

	for i, ss := range c.ScaleSets {
		if ss.RegistrationURL == "" {
			return nil, fmt.Errorf("scale_sets[%d]: registration_url is required", i)
		}
		if ss.Name == "" {
			return nil, fmt.Errorf("scale_sets[%d]: name is required", i)
		}
		if len(ss.Labels) == 0 {
			c.ScaleSets[i].Labels = []string{ss.Name}
		}
	}

	if len(c.Backends) == 0 {
		return nil, fmt.Errorf("at least one backend is required")
	}

	return c, nil
}
