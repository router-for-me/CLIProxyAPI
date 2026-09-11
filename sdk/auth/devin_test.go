package auth

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestDevinAuthenticatorProviderAndRefreshLead(t *testing.T) {
	authenticator := NewDevinAuthenticator()
	if authenticator.Provider() != "devin" {
		t.Fatalf("Provider() = %q, want devin", authenticator.Provider())
	}
	lead := authenticator.RefreshLead()
	if lead != nil {
		t.Fatalf("RefreshLead() = %v, want nil for permanent tokens", lead)
	}
}

func TestDevinAuthenticatorHeadlessManualTokenLogin(t *testing.T) {
	authenticator := NewDevinAuthenticator()
	cfg := &config.Config{}

	mockPrompt := func(prompt string) (string, error) {
		return "devin-session-token$eyJmock.session.token", nil
	}

	opts := &LoginOptions{
		NoBrowser: true,
		Prompt:    mockPrompt,
	}

	auth, err := authenticator.Login(context.Background(), cfg, opts)
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}

	if auth.Provider != "devin" {
		t.Errorf("auth.Provider = %q, want devin", auth.Provider)
	}
	if auth.Attributes["api_key"] != "devin-session-token$eyJmock.session.token" {
		t.Errorf("api_key = %q, want expected", auth.Attributes["api_key"])
	}
}
