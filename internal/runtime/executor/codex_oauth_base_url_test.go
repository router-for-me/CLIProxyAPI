package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestCodexExecutorOAuthBaseURLDefaultsToStock(t *testing.T) {
	exec := NewCodexExecutor(&config.Config{})
	_, baseURL := exec.codexCreds(&cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"access_token": "oauth-token"},
	})
	if baseURL != codexDefaultBaseURL {
		t.Fatalf("baseURL = %q, want stock %q", baseURL, codexDefaultBaseURL)
	}
}

func TestCodexAutoExecutorConfiguredOAuthBaseUsesHTTPResponsesEndpoint(t *testing.T) {
	var gotPath string
	var gotUpgrade string
	var gotEdgeKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotUpgrade = r.Header.Get("Upgrade")
		gotEdgeKey = r.Header.Get("X-Edgee-Api-Key")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"total_tokens\":0}}}\n\n"))
	}))
	defer server.Close()

	exec := NewCodexAutoExecutor(&config.Config{
		CodexOAuthBaseURL: server.URL,
		CodexOAuthHeaders: map[string]string{"x-edgee-api-key": "edge-key"},
		SDKConfig:         config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll},
	})
	auth := &cliproxyauth.Auth{
		Provider:   "codex",
		Attributes: map[string]string{"websockets": "true"},
		Metadata:   map[string]any{"access_token": "oauth-token"},
	}
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())

	_, errExecute := exec.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"model":"gpt-5-codex","input":"hello"}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if gotPath != "/responses" {
		t.Fatalf("request path = %q, want /responses", gotPath)
	}
	if gotUpgrade != "" {
		t.Fatalf("Upgrade = %q, want plain HTTP request", gotUpgrade)
	}
	if gotEdgeKey != "edge-key" {
		t.Fatalf("X-Edgee-Api-Key = %q, want configured header", gotEdgeKey)
	}
}

func TestCodexExecutorOAuthConfigIsStableAcrossTokenRefreshAndConfigMutation(t *testing.T) {
	cfg := &config.Config{
		CodexOAuthBaseURL: "https://edge.example.com/codex",
		CodexOAuthHeaders: map[string]string{"x-edgee-api-key": "startup-key"},
	}
	exec := NewCodexExecutor(cfg)
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"access_token": "old-token"},
	}

	refreshedAuth, errRefresh := exec.Refresh(context.Background(), auth)
	if errRefresh != nil {
		t.Fatalf("Refresh() error = %v", errRefresh)
	}
	oldToken, oldBaseURL := exec.codexCreds(refreshedAuth)
	auth.Metadata["access_token"] = "refreshed-token"
	cfg.CodexOAuthBaseURL = "https://reload.example.com/codex"
	cfg.CodexOAuthHeaders["x-edgee-api-key"] = "reload-key"
	newToken, newBaseURL := exec.codexCreds(auth)

	if oldToken != "old-token" || newToken != "refreshed-token" {
		t.Fatalf("tokens = %q, %q; want old and refreshed token", oldToken, newToken)
	}
	if oldBaseURL != "https://edge.example.com/codex" || newBaseURL != oldBaseURL {
		t.Fatalf("base URLs = %q, %q; want immutable startup route", oldBaseURL, newBaseURL)
	}
	httpReq, errRequest := http.NewRequest(http.MethodPost, newBaseURL+"/responses", nil)
	if errRequest != nil {
		t.Fatalf("NewRequest() error = %v", errRequest)
	}
	exec.applyCodexOAuthHeaders(httpReq, auth)
	if got := httpReq.Header.Get("X-Edgee-Api-Key"); got != "startup-key" {
		t.Fatalf("X-Edgee-Api-Key = %q, want immutable startup value", got)
	}
}

func TestCodexExecutorHttpRequestRewritesStockOAuthURLToConfiguredBase(t *testing.T) {
	var gotPath string
	var gotEdgeKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotEdgeKey = r.Header.Get("X-Edgee-Api-Key")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	exec := NewCodexExecutor(&config.Config{
		CodexOAuthBaseURL: server.URL + "/edge/codex",
		CodexOAuthHeaders: map[string]string{"x-edgee-api-key": "edge-key"},
	})
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"access_token": "oauth-token"},
	}
	req, errRequest := http.NewRequest(http.MethodPost, codexDefaultBaseURL+"/responses", nil)
	if errRequest != nil {
		t.Fatalf("NewRequest() error = %v", errRequest)
	}

	resp, errHTTP := exec.HttpRequest(context.Background(), auth, req)
	if errHTTP != nil {
		t.Fatalf("HttpRequest() error = %v", errHTTP)
	}
	if _, errRead := io.Copy(io.Discard, resp.Body); errRead != nil {
		t.Fatalf("read response body: %v", errRead)
	}
	if errClose := resp.Body.Close(); errClose != nil {
		t.Fatalf("close response body: %v", errClose)
	}
	if gotPath != "/edge/codex/responses" {
		t.Fatalf("request path = %q, want /edge/codex/responses", gotPath)
	}
	if gotEdgeKey != "edge-key" {
		t.Fatalf("X-Edgee-Api-Key = %q, want configured header", gotEdgeKey)
	}
}

func TestCodexExecutorOAuthHeadersAreNotAppliedWithoutBaseURL(t *testing.T) {
	exec := NewCodexExecutor(&config.Config{
		CodexOAuthHeaders: map[string]string{"x-edgee-api-key": "edge-key"},
	})
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"access_token": "oauth-token"},
	}
	req, errRequest := http.NewRequest(http.MethodPost, codexDefaultBaseURL+"/responses", nil)
	if errRequest != nil {
		t.Fatalf("NewRequest() error = %v", errRequest)
	}

	if errPrepare := exec.PrepareRequest(req, auth); errPrepare != nil {
		t.Fatalf("PrepareRequest() error = %v", errPrepare)
	}
	if got := req.Header.Get("X-Edgee-Api-Key"); got != "" {
		t.Fatalf("X-Edgee-Api-Key = %q, want empty without configured base URL", got)
	}
}

func TestCodexExecutorConfiguredOAuthBaseDoesNotOverrideAPIKeyAuth(t *testing.T) {
	exec := NewCodexExecutor(&config.Config{
		CodexOAuthBaseURL: "https://edge.example.com/codex",
		CodexOAuthHeaders: map[string]string{"x-edgee-api-key": "edge-key"},
	})
	auth := &cliproxyauth.Auth{Provider: "codex", Attributes: map[string]string{
		"api_key":  "sk-test",
		"base_url": "https://api-key.example.com/v1",
	}}
	_, baseURL := exec.codexCreds(auth)
	if baseURL != "https://api-key.example.com/v1" {
		t.Fatalf("baseURL = %q, want API-key route", baseURL)
	}
	req, errRequest := http.NewRequest(http.MethodPost, baseURL+"/responses", nil)
	if errRequest != nil {
		t.Fatalf("NewRequest() error = %v", errRequest)
	}
	exec.applyCodexOAuthHeaders(req, auth)
	if got := req.Header.Get("X-Edgee-Api-Key"); got != "" {
		t.Fatalf("X-Edgee-Api-Key = %q, want empty for API-key auth", got)
	}
}
