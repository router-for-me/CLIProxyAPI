package cliproxy

import (
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// Before the server listens, only stored credentials with a registered
// executor publish models. Config API keys are already registered, and a
// plugin-backed credential gets its executor only after the server starts.
func TestServableStoredCredentials(t *testing.T) {
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(mockBatchTestExecutor{provider: "claude"})
	service := &Service{cfg: &config.Config{}, coreManager: manager}

	stored := &coreauth.Auth{ID: "stored", Provider: "claude", Attributes: map[string]string{
		coreauth.AttributeAuthKind: coreauth.AuthKindOAuth, coreauth.AttributePath: "/auths/stored.json",
	}}
	configKey := &coreauth.Auth{ID: "config-key", Provider: "claude", Attributes: map[string]string{
		coreauth.AttributeAuthKind: coreauth.AuthKindAPIKey, coreauth.AttributeSource: "config:claude[0]",
	}}
	pluginBacked := &coreauth.Auth{ID: "plugin", Provider: "plugin-provider", Attributes: map[string]string{
		coreauth.AttributeAuthKind: coreauth.AuthKindOAuth, coreauth.AttributePath: "/auths/plugin.json",
	}}

	got := service.servableStoredCredentials([]*coreauth.Auth{stored, configKey, pluginBacked, nil})
	if len(got) != 1 || got[0].ID != "stored" {
		ids := make([]string, 0, len(got))
		for _, auth := range got {
			ids = append(ids, auth.ID)
		}
		t.Fatalf("servableStoredCredentials() = %v, want [stored]", ids)
	}
}
