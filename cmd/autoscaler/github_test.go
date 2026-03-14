package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newMockGitHubServer creates an httptest server that responds to JIT config requests.
func newMockGitHubServer(t *testing.T, statusCode int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(statusCode)
		fmt.Fprint(w, body)
	}))
}

// newGitHubClientWithServer creates a GitHubClient that talks to the test server.
// The caller must override the URL in GenerateJITConfig, so we use a custom approach:
// we create a client whose transport rewrites requests to the test server.
func newGitHubClientWithServer(server *httptest.Server) *GitHubClient {
	return &GitHubClient{
		Token:      "test-token",
		HTTPClient: server.Client(),
	}
}

// generateJITConfigWithURL calls the JIT config endpoint using a custom base URL.
func generateJITConfigWithURL(ctx context.Context, client *GitHubClient, baseURL, repo, vmName string, labels []string) (string, error) {
	// We need to replicate GenerateJITConfig but with a different URL.
	// Since we can't modify the struct, we'll use an HTTP test that intercepts all traffic.
	return client.GenerateJITConfig(ctx, repo, vmName, labels)
}

func TestGenerateJITConfig(t *testing.T) {
	respBody := `{"encoded_jit_config":"base64-jit-config-data","runner":{"id":42}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request
		if r.Method != "POST" {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("auth header = %q, want %q", got, "Bearer test-token")
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("accept header = %q, want %q", got, "application/vnd.github+json")
		}

		// Verify request body
		var req jitConfigRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if req.Name != "test-vm" {
			t.Errorf("name = %q, want %q", req.Name, "test-vm")
		}
		if req.WorkFolder != "_work" {
			t.Errorf("work_folder = %q, want %q", req.WorkFolder, "_work")
		}

		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, respBody)
	}))
	defer server.Close()

	// Create client that routes to our test server
	client := &GitHubClient{
		Token:      "test-token",
		HTTPClient: server.Client(),
	}

	// We need to make GenerateJITConfig hit our server. Since it hardcodes the URL,
	// we'll use a transport that redirects requests.
	transport := &urlRewriteTransport{
		base:    server.Client().Transport,
		baseURL: server.URL,
	}
	client.HTTPClient = &http.Client{Transport: transport}

	config, err := client.GenerateJITConfig(context.Background(), "metcalfc/exeunt", "test-vm", []string{"exe"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if config != "base64-jit-config-data" {
		t.Errorf("config = %q, want %q", config, "base64-jit-config-data")
	}
}

func TestGenerateJITConfigLabels(t *testing.T) {
	var receivedLabels []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jitConfigRequest
		json.NewDecoder(r.Body).Decode(&req)
		receivedLabels = req.Labels

		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"encoded_jit_config":"cfg","runner":{"id":1}}`)
	}))
	defer server.Close()

	client := &GitHubClient{
		Token:      "test-token",
		HTTPClient: &http.Client{Transport: &urlRewriteTransport{base: server.Client().Transport, baseURL: server.URL}},
	}

	t.Run("self-hosted auto-added", func(t *testing.T) {
		receivedLabels = nil
		_, err := client.GenerateJITConfig(context.Background(), "org/repo", "vm", []string{"exe"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(receivedLabels) < 2 {
			t.Fatalf("expected at least 2 labels, got %d", len(receivedLabels))
		}
		if receivedLabels[0] != "self-hosted" {
			t.Errorf("first label = %q, want %q", receivedLabels[0], "self-hosted")
		}
	})

	t.Run("self-hosted dedup", func(t *testing.T) {
		receivedLabels = nil
		_, err := client.GenerateJITConfig(context.Background(), "org/repo", "vm", []string{"self-hosted", "exe"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Should have self-hosted only once
		count := 0
		for _, l := range receivedLabels {
			if l == "self-hosted" {
				count++
			}
		}
		if count != 1 {
			t.Errorf("self-hosted appears %d times, want 1", count)
		}
	})
}

func TestGenerateJITConfigError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		fmt.Fprint(w, `{"message":"Validation Failed"}`)
	}))
	defer server.Close()

	client := &GitHubClient{
		Token:      "test-token",
		HTTPClient: &http.Client{Transport: &urlRewriteTransport{base: server.Client().Transport, baseURL: server.URL}},
	}

	_, err := client.GenerateJITConfig(context.Background(), "org/repo", "vm", []string{"exe"})
	if err == nil {
		t.Fatal("expected error for non-201 status")
	}
}

func TestGenerateJITConfigEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"encoded_jit_config":"","runner":{"id":1}}`)
	}))
	defer server.Close()

	client := &GitHubClient{
		Token:      "test-token",
		HTTPClient: &http.Client{Transport: &urlRewriteTransport{base: server.Client().Transport, baseURL: server.URL}},
	}

	_, err := client.GenerateJITConfig(context.Background(), "org/repo", "vm", []string{"exe"})
	if err == nil {
		t.Fatal("expected error for empty jit config")
	}
}

// urlRewriteTransport rewrites request URLs to point to a test server.
type urlRewriteTransport struct {
	base    http.RoundTripper
	baseURL string
}

func (t *urlRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Rewrite the URL to point to our test server
	req.URL.Scheme = "http"
	req.URL.Host = t.baseURL[len("http://"):]
	return t.base.RoundTrip(req)
}
