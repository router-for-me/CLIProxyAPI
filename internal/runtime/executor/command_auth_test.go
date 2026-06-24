package executor

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestParseCommandAuthBearerToken(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{name: "bearer", output: "\nBearer test-token\n", want: "test-token"},
		{name: "leading noise", output: "warning: cached token stale\nBearer fresh-token\n", want: "fresh-token"},
		{name: "fallback", output: "plain-token\n", want: "plain-token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token, err := helps.ParseCommandAuthBearerToken([]byte(tt.output))
			if err != nil {
				t.Fatalf("ParseCommandAuthBearerToken error: %v", err)
			}
			if token != tt.want {
				t.Fatalf("token = %q, want %q", token, tt.want)
			}
		})
	}
}

func TestOpenAICompatPrepareRequestUsesCommandAuthMetadata(t *testing.T) {
	exec := NewOpenAICompatExecutor("openai-compatibility", nil)
	req, err := http.NewRequest(http.MethodGet, "https://example.com/v1/models", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{"base_url": "https://example.com/v1"},
		Metadata:   map[string]any{"access_token": "dynamic-token"},
	}
	if err := exec.PrepareRequest(req, auth); err != nil {
		t.Fatalf("PrepareRequest error: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer dynamic-token" {
		t.Fatalf("Authorization = %q, want Bearer dynamic-token", got)
	}
}

func TestProviderPrepareRequestUsesCommandAuthMetadata(t *testing.T) {
	tests := []struct {
		name string
		exec interface {
			PrepareRequest(*http.Request, *cliproxyauth.Auth) error
		}
		url        string
		wantHeader string
		wantValue  string
	}{
		{
			name:       "gemini",
			exec:       NewGeminiExecutor(nil),
			url:        "https://generativelanguage.googleapis.com/v1beta/models",
			wantHeader: "x-goog-api-key",
			wantValue:  "dynamic-token",
		},
		{
			name:       "claude",
			exec:       NewClaudeExecutor(nil),
			url:        "https://api.anthropic.com/v1/messages",
			wantHeader: "Authorization",
			wantValue:  "Bearer dynamic-token",
		},
		{
			name:       "vertex",
			exec:       NewGeminiVertexExecutor(nil),
			url:        "https://aiplatform.googleapis.com/v1/publishers/google/models/gemini:generateContent",
			wantHeader: "x-goog-api-key",
			wantValue:  "dynamic-token",
		},
		{
			name:       "kimi",
			exec:       NewKimiExecutor(nil),
			url:        "https://api.kimi.com/coding/v1/chat/completions",
			wantHeader: "Authorization",
			wantValue:  "Bearer dynamic-token",
		},
		{
			name:       "xai",
			exec:       NewXAIExecutor(nil),
			url:        "https://api.x.ai/v1/responses",
			wantHeader: "Authorization",
			wantValue:  "Bearer dynamic-token",
		},
		{
			name:       "aistudio",
			exec:       NewAIStudioExecutor(nil, "aistudio", nil),
			url:        "https://generativelanguage.googleapis.com/v1beta/models",
			wantHeader: "Authorization",
			wantValue:  "Bearer dynamic-token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, tt.url, nil)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			auth := &cliproxyauth.Auth{
				Attributes: map[string]string{
					"base_url":                  "https://example.com/v1",
					cliproxyauth.AttrAuthKind:   cliproxyauth.AttrAuthKindAPIKey,
					cliproxyauth.AttrAuthSource: cliproxyauth.AttrAuthSourceCommand,
				},
				Metadata: map[string]any{"access_token": "dynamic-token"},
			}
			if err := tt.exec.PrepareRequest(req, auth); err != nil {
				t.Fatalf("PrepareRequest error: %v", err)
			}
			if got := req.Header.Get(tt.wantHeader); got != tt.wantValue {
				t.Fatalf("%s = %q, want %q", tt.wantHeader, got, tt.wantValue)
			}
		})
	}
}

func TestProviderRequestAuthPreparersRunCommandAuth(t *testing.T) {
	script := writeTokenScript(t, "prepared-provider-token")
	tests := []struct {
		name     string
		metadata map[string]any
		exec     interface {
			ShouldPrepareRequestAuth(*cliproxyauth.Auth) bool
			PrepareRequestAuth(context.Context, *cliproxyauth.Auth) (*cliproxyauth.Auth, error)
		}
	}{
		{name: "gemini", exec: NewGeminiExecutor(nil)},
		{name: "claude", exec: NewClaudeExecutor(nil)},
		{name: "codex", exec: NewCodexExecutor(nil)},
		{name: "openai-compat", exec: NewOpenAICompatExecutor("openai-compatibility", nil)},
		{name: "vertex", exec: NewGeminiVertexExecutor(nil)},
		{name: "kimi", exec: NewKimiExecutor(nil)},
		{name: "xai", exec: NewXAIExecutor(nil)},
		{name: "xai-auto", exec: NewXAIAutoExecutor(nil)},
		{name: "aistudio", exec: NewAIStudioExecutor(nil, "aistudio", nil)},
		{name: "antigravity", metadata: map[string]any{"project_id": "test-project"}, exec: NewAntigravityExecutor(nil)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth := &cliproxyauth.Auth{
				ID:         tt.name + "-command-auth",
				Provider:   tt.name,
				Attributes: commandAuthAttrs(script, 60000),
				Metadata:   tt.metadata,
			}
			if !tt.exec.ShouldPrepareRequestAuth(auth) {
				t.Fatal("expected command auth to require preparation")
			}
			updated, err := tt.exec.PrepareRequestAuth(context.Background(), auth)
			if err != nil {
				t.Fatalf("PrepareRequestAuth error: %v", err)
			}
			if got, _ := updated.Metadata["access_token"].(string); got != "prepared-provider-token" {
				t.Fatalf("access_token = %q, want prepared-provider-token", got)
			}
			if tt.exec.ShouldPrepareRequestAuth(updated) {
				t.Fatal("expected prepared command auth to skip preparation")
			}
		})
	}
}

func TestProviderRefreshRunsDueCommandAuth(t *testing.T) {
	script := writeTokenScript(t, "refreshed-provider-token")
	tests := []struct {
		name     string
		metadata map[string]any
		exec     interface {
			Refresh(context.Context, *cliproxyauth.Auth) (*cliproxyauth.Auth, error)
		}
	}{
		{name: "gemini", exec: NewGeminiExecutor(nil)},
		{name: "claude", exec: NewClaudeExecutor(nil)},
		{name: "codex", exec: NewCodexExecutor(nil)},
		{name: "openai-compat", exec: NewOpenAICompatExecutor("openai-compatibility", nil)},
		{name: "vertex", exec: NewGeminiVertexExecutor(nil)},
		{name: "kimi", exec: NewKimiExecutor(nil)},
		{name: "xai", exec: NewXAIExecutor(nil)},
		{name: "xai-auto", exec: NewXAIAutoExecutor(nil)},
		{name: "aistudio", exec: NewAIStudioExecutor(nil, "aistudio", nil)},
		{name: "antigravity", metadata: map[string]any{"project_id": "test-project"}, exec: NewAntigravityExecutor(nil)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth := &cliproxyauth.Auth{
				ID:         tt.name + "-command-auth",
				Provider:   tt.name,
				Attributes: commandAuthAttrs(script, 60000),
				Metadata:   tt.metadata,
			}
			if auth.Metadata == nil {
				auth.Metadata = make(map[string]any)
			}
			auth.Metadata["access_token"] = "stale-provider-token"
			auth.NextRefreshAfter = time.Now().Add(-time.Second)

			updated, err := tt.exec.Refresh(context.Background(), auth)
			if err != nil {
				t.Fatalf("Refresh error: %v", err)
			}
			if got, _ := updated.Metadata["access_token"].(string); got != "refreshed-provider-token" {
				t.Fatalf("access_token = %q, want refreshed-provider-token", got)
			}
			if !updated.NextRefreshAfter.After(time.Now()) {
				t.Fatalf("NextRefreshAfter = %v, want future", updated.NextRefreshAfter)
			}
		})
	}
}

func TestCommandAuthPrepareExecutesCommandAndCaches(t *testing.T) {
	script := writeTokenScript(t, "Bearer dynamic-token")
	auth := &cliproxyauth.Auth{
		ID:         "command-auth",
		Provider:   "openai-compatibility",
		Attributes: commandAuthAttrs(script, 60000),
	}

	if !helps.ShouldPrepareCommandAuth(auth) {
		t.Fatal("expected command auth to require preparation")
	}
	updated, err := helps.PrepareCommandAuth(context.Background(), auth)
	if err != nil {
		t.Fatalf("PrepareCommandAuth error: %v", err)
	}
	if got, _ := updated.Metadata["access_token"].(string); got != "dynamic-token" {
		t.Fatalf("access_token = %q, want dynamic-token", got)
	}
	if !updated.NextRefreshAfter.After(time.Now()) {
		t.Fatalf("NextRefreshAfter = %v, want future", updated.NextRefreshAfter)
	}
	if helps.ShouldPrepareCommandAuth(updated) {
		t.Fatal("expected cached command auth to skip preparation")
	}
}

func TestCommandAuthPrepareFindsUserNPMGlobalBin(t *testing.T) {
	home := t.TempDir()
	binDir := filepath.Join(home, ".npm-global", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin dir: %v", err)
	}
	script := filepath.Join(binDir, "command-auth-token")
	body := "#!/bin/sh\nprintf '%s\\n' 'Bearer user-bin-token'\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("write token script: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")

	auth := &cliproxyauth.Auth{
		ID:         "command-auth",
		Provider:   "openai-compatibility",
		Attributes: commandAuthAttrs("command-auth-token", 60000),
	}
	updated, err := helps.PrepareCommandAuth(context.Background(), auth)
	if err != nil {
		t.Fatalf("PrepareCommandAuth error: %v", err)
	}
	if got, _ := updated.Metadata["access_token"].(string); got != "user-bin-token" {
		t.Fatalf("access_token = %q, want user-bin-token", got)
	}
}

func TestManagerPrepareHttpRequestRunsCommandAuth(t *testing.T) {
	script := writeTokenScript(t, "manager-token")
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(NewOpenAICompatExecutor("openai-compatibility", nil))

	auth := &cliproxyauth.Auth{
		ID:         "command-auth",
		Provider:   "openai-compatibility",
		Attributes: commandAuthAttrs(script, 60000),
	}
	if _, err := manager.Register(cliproxyauth.WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, "https://example.com/v1/models", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if err := manager.PrepareHttpRequest(context.Background(), auth, req); err != nil {
		t.Fatalf("PrepareHttpRequest error: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer manager-token" {
		t.Fatalf("Authorization = %q, want Bearer manager-token", got)
	}
	current, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatal("expected auth in manager")
	}
	if got, _ := current.Metadata["access_token"].(string); got != "manager-token" {
		t.Fatalf("manager access_token = %q, want manager-token", got)
	}
}

func writeTokenScript(t *testing.T, token string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "token.sh")
	body := "#!/bin/sh\nprintf '%s\\n' " + shellSingleQuote(token) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatalf("write token script: %v", err)
	}
	return path
}

func commandAuthAttrs(command string, refreshMS int) map[string]string {
	return map[string]string{
		"base_url":                                "https://example.com/v1",
		cliproxyauth.AttrAuthKind:                 cliproxyauth.AttrAuthKindAPIKey,
		cliproxyauth.AttrAuthSource:               cliproxyauth.AttrAuthSourceCommand,
		cliproxyauth.AttrAuthCommand:              command,
		cliproxyauth.AttrAuthArgsJSON:             "[]",
		cliproxyauth.AttrAuthTimeoutMS:            "5000",
		cliproxyauth.AttrAuthRefreshIntervalMS:    strconv.Itoa(refreshMS),
		cliproxyauth.AttrAuthInvalidatesOnNext401: "true",
	}
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
