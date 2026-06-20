package executor

import (
	"net/http"
	"sync"
	"testing"

	zaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/zai"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// TestZAIExecutorCloneAuthWithBaseURLNoRace verifies that resolving the base URL
// never mutates the shared Auth object: it must deep-copy Attributes so that
// concurrent requests sharing a single credential cannot trigger a fatal
// "concurrent map read and map write" panic. Run with -race to catch regressions.
func TestZAIExecutorCloneAuthWithBaseURLNoRace(t *testing.T) {
	e := NewZAIExecutor(nil)
	shared := &cliproxyauth.Auth{
		ID:         "zai-test",
		Provider:   "zai",
		Attributes: map[string]string{"path": "/tmp/zai.json"},
		Metadata:   map[string]any{"access_token": "tok"},
	}

	const n = 64
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			cloned := e.cloneAuthWithBaseURL(shared)
			if cloned == shared {
				t.Error("expected a distinct Auth clone, got the shared pointer")
				return
			}
			if cloned.Attributes["base_url"] != zaiauth.ZAIAPIBaseURL {
				t.Errorf("base_url not set on clone: %q", cloned.Attributes["base_url"])
			}
			if cloned.Attributes["path"] != "/tmp/zai.json" {
				t.Error("clone lost original attributes")
			}
		}()
	}
	wg.Wait()

	if _, mutated := shared.Attributes["base_url"]; mutated {
		t.Fatal("shared auth Attributes were mutated; the clone should be modified instead")
	}
}

// TestZAIExecutorCloneAuthWithBaseURLPrefersExisting verifies that an existing
// base URL (e.g. an operator override) is preserved rather than overwritten.
func TestZAIExecutorCloneAuthWithBaseURLPrefersExisting(t *testing.T) {
	e := NewZAIExecutor(nil)
	const custom = "https://example.test/api/anthropic"
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": custom}}

	cloned := e.cloneAuthWithBaseURL(auth)
	if cloned.Attributes["base_url"] != custom {
		t.Fatalf("expected base_url %q, got %q", custom, cloned.Attributes["base_url"])
	}
}

// TestZAIExecutorCloneAuthForcesXAPIKey verifies the Z.AI path requests x-api-key
// authentication. The coding-plan endpoint answers Authorization: Bearer with a
// captcha challenge (HTTP 403, code 3007), so the token must be sent via x-api-key.
func TestZAIExecutorCloneAuthForcesXAPIKey(t *testing.T) {
	e := NewZAIExecutor(nil)
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"access_token": "tok"}}

	cloned := e.cloneAuthWithBaseURL(auth)
	if got := cloned.Attributes["anthropic_auth_scheme"]; got != "x-api-key" {
		t.Fatalf("anthropic_auth_scheme = %q, want x-api-key", got)
	}
}

// TestClaudeExecutorXAPIKeySchemeSendsHeader verifies that when an auth requests
// the x-api-key scheme, ClaudeExecutor sends the token via the x-api-key header
// (not Authorization: Bearer) even for non-Anthropic hosts like the Z.AI endpoint.
func TestClaudeExecutorXAPIKeySchemeSendsHeader(t *testing.T) {
	e := NewClaudeExecutor(nil)
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"base_url":              "https://zcode.z.ai/api/v1/zcode-plan/anthropic",
			"anthropic_auth_scheme": "x-api-key",
		},
		Metadata: map[string]any{"access_token": "tok-123"},
	}
	req, err := http.NewRequest(http.MethodPost, "https://zcode.z.ai/api/v1/zcode-plan/anthropic/v1/messages", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if errPrep := e.PrepareRequest(req, auth); errPrep != nil {
		t.Fatalf("PrepareRequest: %v", errPrep)
	}
	if got := req.Header.Get("x-api-key"); got != "tok-123" {
		t.Fatalf("x-api-key = %q, want tok-123", got)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization should be empty, got %q", got)
	}
}

// TestZAIExecutorProviderIdentity verifies the Z.AI path is attributed to the
// "zai" provider (for usage/logging and thinking model lookups) even though it
// reuses the Claude wire format, while a plain ClaudeExecutor stays "claude".
func TestZAIExecutorProviderIdentity(t *testing.T) {
	zai := NewZAIExecutor(nil)
	if got := zai.Identifier(); got != "zai" {
		t.Fatalf("ZAI Identifier() = %q, want zai", got)
	}
	if got := zai.ProviderKey(); got != "zai" {
		t.Fatalf("ZAI ProviderKey() = %q, want zai", got)
	}
	if got := NewClaudeExecutor(nil).ProviderKey(); got != "claude" {
		t.Fatalf("Claude ProviderKey() = %q, want claude", got)
	}
}

// TestZAIExecutorCloneDisablesCloak verifies the clone turns Claude "cloak mode"
// off. Z.AI / BigModel are not Anthropic, so ClaudeExecutor's default "auto"
// cloaking (Claude Code system-prompt injection, fake user IDs) must never run
// against GLM prompts.
func TestZAIExecutorCloneDisablesCloak(t *testing.T) {
	e := NewZAIExecutor(nil)
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"access_token": "tok"}}
	if got := e.cloneAuthWithBaseURL(auth).Attributes["cloak_mode"]; got != "never" {
		t.Fatalf("cloak_mode = %q, want never", got)
	}
}

// TestZAIExecutorCloneRespectsCloakOverride verifies an explicit operator-configured
// cloak_mode is preserved rather than forced off.
func TestZAIExecutorCloneRespectsCloakOverride(t *testing.T) {
	e := NewZAIExecutor(nil)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"cloak_mode": "auto"}}
	if got := e.cloneAuthWithBaseURL(auth).Attributes["cloak_mode"]; got != "auto" {
		t.Fatalf("cloak_mode = %q, want auto (operator override must win)", got)
	}
}

// TestZAIExecutorPrepareRequestForcesXAPIKey verifies the overridden PrepareRequest
// routes raw SDK requests (Manager.PrepareHttpRequest / NewHttpRequest / HttpRequest)
// through cloneAuthWithBaseURL, so the token is sent via x-api-key - not Claude's
// default Authorization: Bearer - even when the saved auth carries no explicit scheme.
func TestZAIExecutorPrepareRequestForcesXAPIKey(t *testing.T) {
	e := NewZAIExecutor(nil)
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"access_token": "tok-xyz"}}
	req, err := http.NewRequest(http.MethodPost, "https://api.z.ai/api/anthropic/v1/messages", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if errPrep := e.PrepareRequest(req, auth); errPrep != nil {
		t.Fatalf("PrepareRequest: %v", errPrep)
	}
	if got := req.Header.Get("x-api-key"); got != "tok-xyz" {
		t.Fatalf("x-api-key = %q, want tok-xyz", got)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization should be empty, got %q", got)
	}
}
