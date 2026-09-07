package executor

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestOpencodeSessionHeaderAdded(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		auth     *cliproxyauth.Auth
		wantSet  bool
	}{
		{"opencode sets header", "opencode", nil, true},
		{"opencode-go sets header", "opencode-go", nil, true},
		{"openai-compat with opencode in name sets header", "openai-compatible-ocd-account", nil, true},
		{"openrouter does not set header", "openrouter", nil, false},
		{"kilo does not set header", "kilo", nil, false},
		{"unknown provider with opencode base_url sets header", "openai-compatible-unknown", &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://opencode.ai/zen/v1"}}, true},
		{"unknown provider with openrouter base_url does not set header", "openai-compatible-unknown-2", &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://openrouter.ai/api/v1"}}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := NewOpenAICompatExecutor(tt.provider, &config.Config{})
			req, err := http.NewRequest("POST", "https://example.com/v1/chat/completions", nil)
			if err != nil {
				t.Fatal(err)
			}

			opts := cliproxyexecutor.Options{}
			executor.addOpencodeSessionHeader(req, &opts, tt.auth)

			sessionID := req.Header.Get("x-opencode-session")
			if tt.wantSet && sessionID == "" {
				t.Errorf("expected x-opencode-session header to be set for %s, but it was empty", tt.provider)
			}
			if !tt.wantSet && sessionID != "" {
				t.Errorf("expected x-opencode-session header NOT to be set for %s, but got: %s", tt.provider, sessionID)
			}
		})
	}
}

func TestOpencodeSessionHeaderPreservesExisting(t *testing.T) {
	executor := NewOpenAICompatExecutor("opencode", &config.Config{})

	// Test 1: Session ID from opts.Headers
	req1, _ := http.NewRequest("POST", "https://example.com", nil)
	opts1 := cliproxyexecutor.Options{
		Headers: http.Header{"X-Opencode-Session": []string{"custom-session-123"}},
	}
	executor.addOpencodeSessionHeader(req1, &opts1, nil)
	if got := req1.Header.Get("x-opencode-session"); got != "custom-session-123" {
		t.Errorf("expected custom-session-123, got %s", got)
	}

	// Test 2: Session ID from request header
	req2, _ := http.NewRequest("POST", "https://example.com", nil)
	req2.Header.Set("X-Opencode-Session", "existing-session-456")
	executor.addOpencodeSessionHeader(req2, &cliproxyexecutor.Options{}, nil)
	if got := req2.Header.Get("x-opencode-session"); got != "existing-session-456" {
		t.Errorf("expected existing-session-456, got %s", got)
	}

	// Test 3: Generates new UUID when no session ID provided
	req3, _ := http.NewRequest("POST", "https://example.com", nil)
	executor.addOpencodeSessionHeader(req3, &cliproxyexecutor.Options{}, nil)
	sessionID := req3.Header.Get("x-opencode-session")
	if sessionID == "" {
		t.Error("expected a generated session ID, got empty string")
	}
	// Basic UUID format check (8-4-4-4-12)
	if len(sessionID) != 36 {
		t.Errorf("expected UUID format (36 chars), got %d chars: %s", len(sessionID), sessionID)
	}
}

func TestIsOpenCodeProvider(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		baseURL  string
		want     bool
	}{
		{"opencode by name", "opencode", "", true},
		{"opencode-go by name", "opencode-go", "", true},
		{"openai-compat with opencode in name", "openai-compatible-ocd-account", "", true},
		{"openai by name", "openai", "", false},
		{"openrouter by name", "openrouter", "", false},
		{"auth base_url opencode", "openai-compatible-unknown", "https://opencode.ai/zen/v1", true},
		{"auth base_url opencode-go", "openai-compatible-unknown", "https://opencode.ai/zen/go/v1", true},
		{"auth base_url openrouter", "openai-compatible-unknown", "https://openrouter.ai/api/v1", false},
		{"auth base_url kilo", "openai-compatible-unknown", "https://api.kilo.ai/api/gateway", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var auth *cliproxyauth.Auth
			if tt.baseURL != "" {
				auth = &cliproxyauth.Auth{
					Attributes: map[string]string{"base_url": tt.baseURL},
				}
			}
			if got := isOpenCodeProvider(tt.provider, auth); got != tt.want {
				t.Errorf("isOpenCodeProvider(%q, baseURL=%q) = %v, want %v", tt.provider, tt.baseURL, got, tt.want)
			}
		})
	}
}
