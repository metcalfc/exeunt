package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type GitHubClient struct {
	Token      string
	HTTPClient *http.Client
}

type jitConfigRequest struct {
	Name          string   `json:"name"`
	RunnerGroupID int      `json:"runner_group_id"`
	Labels        []string `json:"labels"`
	WorkFolder    string   `json:"work_folder"`
}

type jitConfigResponse struct {
	EncodedJITConfig string `json:"encoded_jit_config"`
	Runner           struct {
		ID int64 `json:"id"`
	} `json:"runner"`
}

func NewGitHubClient(token string) *GitHubClient {
	return &GitHubClient{
		Token:      token,
		HTTPClient: &http.Client{},
	}
}

// GenerateJITConfig creates an ephemeral runner registration in GitHub and
// returns the encoded JIT config string and the runner ID. The caller must
// call RemoveRunner if StartRunner fails, otherwise the registration leaks
// and causes 409 "Already exists" errors on subsequent attempts.
func (c *GitHubClient) GenerateJITConfig(ctx context.Context, repo, vmName string, labels []string) (string, int64, error) {
	// Always include self-hosted — JIT runners don't auto-add it
	allLabels := []string{"self-hosted"}
	seen := map[string]bool{"self-hosted": true}
	for _, l := range labels {
		if !seen[l] {
			allLabels = append(allLabels, l)
			seen[l] = true
		}
	}

	body, err := json.Marshal(jitConfigRequest{
		Name:          vmName,
		RunnerGroupID: 1,
		Labels:        allLabels,
		WorkFolder:    "_work",
	})
	if err != nil {
		return "", 0, fmt.Errorf("marshal jit request: %w", err)
	}

	url := fmt.Sprintf("https://api.github.com/repos/%s/actions/runners/generate-jitconfig", repo)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return "", 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("jit config request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated {
		return "", 0, fmt.Errorf("jit config API returned %d: %s", resp.StatusCode, string(respBody))
	}

	var jitResp jitConfigResponse
	if err := json.Unmarshal(respBody, &jitResp); err != nil {
		return "", 0, fmt.Errorf("parse jit response: %w", err)
	}

	if jitResp.EncodedJITConfig == "" {
		return "", 0, fmt.Errorf("empty jit config in response: %s", string(respBody))
	}

	return jitResp.EncodedJITConfig, jitResp.Runner.ID, nil
}

// CleanOfflineRunners removes stale offline exeunt-* runner registrations
// from the repo. These cause job stealing: GitHub assigns a new JIT runner
// to an old queued job that was waiting for the stale registration.
func (c *GitHubClient) CleanOfflineRunners(ctx context.Context, repo string, log *slog.Logger) int {
	if c.HTTPClient == nil {
		return 0
	}
	// Use a short timeout so this doesn't delay provisioning significantly.
	cleanCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	url := fmt.Sprintf("https://api.github.com/repos/%s/actions/runners", repo)
	req, err := http.NewRequestWithContext(cleanCtx, "GET", url, nil)
	if err != nil {
		return 0
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0
	}

	var result struct {
		Runners []struct {
			ID     int64  `json:"id"`
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"runners"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return 0
	}

	cleaned := 0
	for _, r := range result.Runners {
		if r.Status == "offline" && strings.HasPrefix(r.Name, "exeunt-") {
			if err := c.RemoveRunner(ctx, repo, r.ID); err != nil {
				log.Warn("failed to clean offline runner", "name", r.Name, "id", r.ID, "error", err)
			} else {
				log.Info("cleaned offline runner", "name", r.Name, "id", r.ID)
				cleaned++
			}
		}
	}
	return cleaned
}

// RemoveRunner deletes a runner registration from GitHub. This must be called
// when provisioning fails after GenerateJITConfig to prevent stale registrations.
func (c *GitHubClient) RemoveRunner(ctx context.Context, repo string, runnerID int64) error {
	url := fmt.Sprintf("https://api.github.com/repos/%s/actions/runners/%d", repo, runnerID)
	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("remove runner request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("remove runner API returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}
