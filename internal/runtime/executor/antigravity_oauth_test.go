package executor

import (
	"testing"

	antigravityauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/antigravity"
)

func TestAntigravityOAuthClientValueFallsBackToDefaults(t *testing.T) {
	t.Setenv(antigravityClientIDEnv, "")
	t.Setenv(antigravityClientSecretEnv, "")

	if got := antigravityOAuthClientValue(nil, "client_id", antigravityClientIDEnv); got != antigravityauth.DefaultClientID {
		t.Fatalf("client_id = %q, want default", got)
	}
	if got := antigravityOAuthClientValue(nil, "client_secret", antigravityClientSecretEnv); got != antigravityauth.DefaultClientSecret {
		t.Fatalf("client_secret = %q, want default", got)
	}
}
