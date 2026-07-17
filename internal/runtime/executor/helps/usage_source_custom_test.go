package helps

import (
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestResolveUsageSourcePrefersBaseURLNotAPIKey(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider: "openai-compat",
		Attributes: map[string]string{
			"api_key":  "sk-proj-secret-key-must-not-appear",
			"base_url": "https://api.provider.example/v1",
		},
	}
	got := resolveUsageSource(auth, "sk-frontend-client-key")
	if got != "https://api.provider.example/v1" {
		t.Fatalf("source = %q, want base_url", got)
	}
}

func TestResolveUsageSourceNeverReturnsAPIKeyAccountInfo(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider: "openai",
		Attributes: map[string]string{
			"api_key": "sk-ant-leaked-secret-value-here",
		},
	}
	got := resolveUsageSource(auth, "sk-another-secret")
	if got != "https://api.openai.com/v1" {
		t.Fatalf("source = %q, want default openai endpoint", got)
	}
	if strings.Contains(got, "sk-") {
		t.Fatalf("source leaked key material: %q", got)
	}
}

func TestResolveUsageSourceCodexDefaultEndpoint(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"api_key": "sk-codex-secret-key-value",
		},
	}
	got := resolveUsageSource(auth, "")
	if got != "https://chatgpt.com/backend-api/codex" {
		t.Fatalf("source = %q, want codex default endpoint", got)
	}
}

func TestResolveUsageSourceKeepsOAuthEmail(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider: "claude",
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{
			"email": "user@example.com",
		},
	}
	got := resolveUsageSource(auth, "")
	if got != "user@example.com" {
		t.Fatalf("source = %q, want oauth email", got)
	}
}
