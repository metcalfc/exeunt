package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

func (c *GitHubClient) GenerateJITConfig(ctx context.Context, repo, vmName string, labels []string) (string, error) {
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
		return "", fmt.Errorf("marshal jit request: %w", err)
	}

	url := fmt.Sprintf("https://api.github.com/repos/%s/actions/runners/generate-jitconfig", repo)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("jit config request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("jit config API returned %d: %s", resp.StatusCode, string(respBody))
	}

	var jitResp jitConfigResponse
	if err := json.Unmarshal(respBody, &jitResp); err != nil {
		return "", fmt.Errorf("parse jit response: %w", err)
	}

	if jitResp.EncodedJITConfig == "" {
		return "", fmt.Errorf("empty jit config in response: %s", string(respBody))
	}

	return jitResp.EncodedJITConfig, nil
}
