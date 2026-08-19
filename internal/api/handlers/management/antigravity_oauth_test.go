package management

import (
	"testing"

	antigravityauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/antigravity"
)

func TestAntigravityOAuthClientValueFallsBackToDefaults(t *testing.T) {
	t.Setenv(antigravityOAuthClientIDEnv, "")
	t.Setenv(antigravityOAuthClientSecretEnv, "")

	if got := antigravityOAuthClientValue(nil, "client_id", antigravityOAuthClientIDEnv); got != antigravityauth.DefaultClientID {
		t.Fatalf("client_id = %q, want default", got)
	}
	if got := antigravityOAuthClientValue(nil, "client_secret", antigravityOAuthClientSecretEnv); got != antigravityauth.DefaultClientSecret {
		t.Fatalf("client_secret = %q, want default", got)
	}
}
