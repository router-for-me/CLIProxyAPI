package cliproxy

import (
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestCodexWarmURLUsesAuthBaseURL(t *testing.T) {
	t.Parallel()

	auth := &coreauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"base_url": "https://example.com/backend-api/codex",
		},
	}

	if got := codexWarmURL(auth); got != "https://example.com/backend-api/codex/responses" {
		t.Fatalf("codexWarmURL() = %q", got)
	}
}

func TestReserveCodexWarmupUsesTTL(t *testing.T) {
	t.Parallel()

	service := &Service{}
	now := time.Now()
	if !service.reserveCodexWarmup("auth-1", now) {
		t.Fatalf("first reserveCodexWarmup() = false")
	}
	if service.reserveCodexWarmup("auth-1", now.Add(time.Minute)) {
		t.Fatalf("reserveCodexWarmup() within ttl = true")
	}
	if !service.reserveCodexWarmup("auth-1", now.Add(codexWarmupTTL+time.Second)) {
		t.Fatalf("reserveCodexWarmup() after ttl = false")
	}
}
