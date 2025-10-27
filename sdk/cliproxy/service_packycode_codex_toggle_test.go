package cliproxy

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// Test that when Packycode is enabled, codex auth.json entries are disabled;
// when Packycode is disabled again, previously disabled-by-packycode codex auths are re-enabled.
func TestEnforceCodexToggle_WithPackycode(t *testing.T) {
	cfg := &config.Config{}
	s := &Service{cfg: cfg}
	s.coreManager = coreauth.NewManager(sdkAuth.GetTokenStore(), nil, nil)

	// Seed a codex auth entry
	a := &coreauth.Auth{
		ID:       "codex:test",
		Provider: "codex",
		Label:    "codex",
		Status:   coreauth.StatusActive,
	}
	if _, err := s.coreManager.Register(context.Background(), a); err != nil {
		t.Fatalf("register auth failed: %v", err)
	}

	// Enable Packycode -> codex auths should be disabled
	cfg.Packycode.Enabled = true
	s.enforceCodexToggle(cfg)
	got, ok := s.coreManager.GetByID("codex:test")
	if !ok || got == nil {
		t.Fatalf("expected auth present after toggle")
	}
	if !got.Disabled {
		t.Fatalf("expected codex auth to be disabled when packycode enabled")
	}

	// Disable Packycode -> codex auths previously disabled-by-packycode should be re-enabled
	cfg.Packycode.Enabled = false
	s.enforceCodexToggle(cfg)
	got2, ok2 := s.coreManager.GetByID("codex:test")
	if !ok2 || got2 == nil {
		t.Fatalf("expected auth present after re-enable")
	}
	if got2.Disabled {
		t.Fatalf("expected codex auth to be enabled when packycode disabled")
	}
}
