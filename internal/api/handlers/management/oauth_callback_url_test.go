package management

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestOAuthCallbackURLBuilder_DefaultFallback(t *testing.T) {
	cfg := &config.Config{}
	got, err := BuildOAuthRedirectURI(cfg, "codex", "http://localhost:1455/auth/callback")
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://localhost:1455/auth/callback" {
		t.Fatalf("got %s", got)
	}
}

func TestOAuthCallbackURLBuilder_ExternalMode(t *testing.T) {
	cfg := &config.Config{}
	cfg.OAuthCallback.Enabled = true
	cfg.OAuthCallback.ExternalBaseURL = "https://cliproxy.example.com"
	cfg.OAuthCallback.ProviderPaths = map[string]string{"codex": "/oauth/callback/codex"}

	got, err := BuildOAuthRedirectURI(cfg, "codex", "http://localhost:1455/auth/callback")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://cliproxy.example.com/oauth/callback/codex" {
		t.Fatal(got)
	}
}
