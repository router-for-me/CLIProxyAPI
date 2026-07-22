package auth

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestCodexIntegrationAliasesResolveAndForceResponseMapping(t *testing.T) {
	cfg := &internalconfig.Config{SDKConfig: internalconfig.SDKConfig{CodexIntegration: internalconfig.DefaultCodexIntegrationConfig()}}
	cfg.CodexIntegration.Enabled = true
	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(cfg)
	mgr.SetOAuthModelAlias(cfg.EffectiveOAuthModelAlias())

	result := mgr.resolveOAuthModelAliasWithResult(&Auth{Provider: "antigravity"}, "antigravity/gemini-3.1-pro(high)")
	if result.UpstreamModel != "gemini-pro-agent(high)" {
		t.Fatalf("UpstreamModel = %q, want gemini-pro-agent(high)", result.UpstreamModel)
	}
	if !result.ForceMapping || result.OriginalAlias != "antigravity/gemini-3.1-pro" {
		t.Fatalf("alias result = %#v, want forced stable response mapping", result)
	}
}
