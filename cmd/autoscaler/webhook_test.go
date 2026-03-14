package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func computeSignature(payload, secret []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestValidateSignature(t *testing.T) {
	secret := []byte("test-secret")
	payload := []byte(`{"action":"queued"}`)

	t.Run("valid signature", func(t *testing.T) {
		sig := computeSignature(payload, secret)
		if !validateSignature(payload, sig, secret) {
			t.Error("expected valid signature")
		}
	})

	t.Run("wrong secret", func(t *testing.T) {
		sig := computeSignature(payload, []byte("wrong-secret"))
		if validateSignature(payload, sig, secret) {
			t.Error("expected invalid signature with wrong secret")
		}
	})

	t.Run("tampered payload", func(t *testing.T) {
		sig := computeSignature(payload, secret)
		tampered := []byte(`{"action":"completed"}`)
		if validateSignature(tampered, sig, secret) {
			t.Error("expected invalid signature with tampered payload")
		}
	})

	t.Run("missing prefix", func(t *testing.T) {
		mac := hmac.New(sha256.New, secret)
		mac.Write(payload)
		sig := hex.EncodeToString(mac.Sum(nil)) // no sha256= prefix
		if validateSignature(payload, sig, secret) {
			t.Error("expected invalid signature without prefix")
		}
	})

	t.Run("empty signature", func(t *testing.T) {
		if validateSignature(payload, "", secret) {
			t.Error("expected invalid for empty signature")
		}
	})

	t.Run("invalid hex", func(t *testing.T) {
		if validateSignature(payload, "sha256=not-valid-hex!", secret) {
			t.Error("expected invalid for bad hex")
		}
	})
}

func TestParseWorkflowJobEvent(t *testing.T) {
	t.Run("queued event", func(t *testing.T) {
		event := WorkflowJobEvent{
			Action: "queued",
		}
		event.WorkflowJob.ID = 12345
		event.WorkflowJob.RunID = 67890
		event.WorkflowJob.Labels = []string{"self-hosted", "exe"}
		event.Repository.FullName = "metcalfc/exeunt"

		payload, _ := json.Marshal(event)
		parsed, err := parseWorkflowJobEvent(payload)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if parsed.Action != "queued" {
			t.Errorf("action = %q, want %q", parsed.Action, "queued")
		}
		if parsed.WorkflowJob.ID != 12345 {
			t.Errorf("job ID = %d, want %d", parsed.WorkflowJob.ID, 12345)
		}
		if parsed.Repository.FullName != "metcalfc/exeunt" {
			t.Errorf("repo = %q, want %q", parsed.Repository.FullName, "metcalfc/exeunt")
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		_, err := parseWorkflowJobEvent([]byte("not json"))
		if err == nil {
			t.Error("expected error for invalid JSON")
		}
	})
}

func TestHasLabel(t *testing.T) {
	labels := []string{"self-hosted", "exe", "linux"}

	if !hasLabel(labels, "exe") {
		t.Error("expected to find exe label")
	}
	if !hasLabel(labels, "self-hosted") {
		t.Error("expected to find self-hosted label")
	}
	if hasLabel(labels, "exe-builder") {
		t.Error("did not expect to find exe-builder label")
	}
	if hasLabel(nil, "exe") {
		t.Error("did not expect to find label in nil slice")
	}
	if hasLabel([]string{}, "exe") {
		t.Error("did not expect to find label in empty slice")
	}
}
