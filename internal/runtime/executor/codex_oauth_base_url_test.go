package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestCodexCredsUsesGlobalBaseURLWhenAuthHasNoBaseURL(t *testing.T) {
	apiKey, baseURL := codexCredsWithConfig(&config.Config{CodexBaseURL: "http://127.0.0.1:18082"}, &cliproxyauth.Auth{
		Metadata: map[string]any{"access_token": "oauth-token"},
	})
	if apiKey != "oauth-token" {
		t.Fatalf("apiKey = %q, want oauth-token", apiKey)
	}
	if baseURL != "http://127.0.0.1:18082" {
		t.Fatalf("baseURL = %q, want global override", baseURL)
	}
}

func TestCodexCredsAuthBaseURLPrecedesGlobalBaseURL(t *testing.T) {
	_, baseURL := codexCredsWithConfig(&config.Config{CodexBaseURL: "http://127.0.0.1:18082"}, &cliproxyauth.Auth{
		Attributes: map[string]string{"base_url": "http://127.0.0.1:18102"},
		Metadata:   map[string]any{"access_token": "oauth-token"},
	})
	if baseURL != "http://127.0.0.1:18102" {
		t.Fatalf("baseURL = %q, want auth override", baseURL)
	}
}

func TestCodexCredsMetadataBaseURLPrecedesGlobalBaseURL(t *testing.T) {
	_, baseURL := codexCredsWithConfig(&config.Config{CodexBaseURL: "http://127.0.0.1:18082"}, &cliproxyauth.Auth{
		Metadata: map[string]any{"access_token": "oauth-token", "base_url": "http://127.0.0.1:18104"},
	})
	if baseURL != "http://127.0.0.1:18104" {
		t.Fatalf("baseURL = %q, want metadata override", baseURL)
	}
}

func TestCodexCredsDoesNotApplyGlobalBaseURLToAPIKeyAuth(t *testing.T) {
	_, baseURL := codexCredsWithConfig(&config.Config{CodexBaseURL: "http://127.0.0.1:18082"}, &cliproxyauth.Auth{
		Attributes: map[string]string{"api_key": "sk-codex-test"},
	})
	if baseURL != "" {
		t.Fatalf("baseURL = %q, want empty for API-key auth without per-key base_url", baseURL)
	}
}
