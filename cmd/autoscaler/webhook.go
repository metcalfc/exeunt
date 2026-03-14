package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

type WorkflowJobEvent struct {
	Action      string `json:"action"`
	WorkflowJob struct {
		ID         int64    `json:"id"`
		RunID      int64    `json:"run_id"`
		Labels     []string `json:"labels"`
		Name       string   `json:"name"`
		RunnerName string   `json:"runner_name"`
	} `json:"workflow_job"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

func validateSignature(payload []byte, signature string, secret []byte) bool {
	if len(secret) == 0 {
		return false
	}
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	sigHex := strings.TrimPrefix(signature, "sha256=")
	sigBytes, err := hex.DecodeString(sigHex)
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	expected := mac.Sum(nil)
	return hmac.Equal(sigBytes, expected)
}

func parseWorkflowJobEvent(payload []byte) (*WorkflowJobEvent, error) {
	var event WorkflowJobEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil, fmt.Errorf("parse webhook payload: %w", err)
	}
	return &event, nil
}

func hasLabel(labels []string, target string) bool {
	for _, l := range labels {
		if l == target {
			return true
		}
	}
	return false
}
