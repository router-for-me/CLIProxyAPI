package auth

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestKiroAuthenticatorProviderAndRefreshLead(t *testing.T) {
	authenticator := NewKiroAuthenticator()
	if authenticator.Provider() != "kiro" {
		t.Fatalf("Provider() = %q, want kiro", authenticator.Provider())
	}
	lead := authenticator.RefreshLead()
	if lead == nil || *lead != 20*time.Minute {
		t.Fatalf("RefreshLead() = %v, want 20m", lead)
	}
}

func TestExtractKiroIdentifier(t *testing.T) {
	tests := []struct {
		name        string
		accountName string
		profileArn  string
		clientID    string
		want        string
	}{
		{
			name:        "account name priority",
			accountName: "user@example.com",
			profileArn:  "arn:aws:codewhisperer:us-east-1:123456789012:profile/P123",
			clientID:    "client-abc",
			want:        "user@example.com",
		},
		{
			name:       "profile ARN second priority",
			profileArn: "arn:aws:codewhisperer:us-east-1:123456789012:profile/P123",
			clientID:   "client-abc",
			want:       "P123",
		},
		{
			name:     "client ID third priority",
			clientID: "client-abc",
			want:     "client-abc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractKiroIdentifier(tt.accountName, tt.profileArn, tt.clientID)
			if got != tt.want {
				t.Errorf("extractKiroIdentifier() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestKiroAuthenticatorLoginNilConfigFails(t *testing.T) {
	authenticator := NewKiroAuthenticator()
	_, err := authenticator.Login(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected error with nil config, got nil")
	}
}

func TestKiroAuthenticatorRefreshValidation(t *testing.T) {
	authenticator := NewKiroAuthenticator()
	cfg := &config.Config{}
	_, err := authenticator.Refresh(context.Background(), cfg, nil)
	if err == nil {
		t.Fatal("expected error with nil auth, got nil")
	}
}
