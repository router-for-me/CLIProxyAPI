package api

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"gopkg.in/yaml.v3"
)

func TestEffectiveSDKConfigCopiesClientCodexEnableApplyPatch(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		cfg := &config.Config{}
		cfg.Client.Codex.EnableApplyPatch = enabled
		sdkCfg := effectiveSDKConfig(cfg)
		if sdkCfg == nil || sdkCfg.Client.Codex.EnableApplyPatch != enabled {
			t.Fatalf("SDK client EnableApplyPatch does not match %t", enabled)
		}
		cfg.Client.Codex.EnableApplyPatch = !enabled
		if sdkCfg.Client.Codex.EnableApplyPatch != enabled {
			t.Fatal("effective SDK config shares client configuration state")
		}
	}
}

func TestEffectiveSDKConfigCopiesClientCodexOptimizeMultiAgentV2(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		cfg := &config.Config{}
		cfg.Client.Codex.OptimizeMultiAgentV2 = enabled
		sdkCfg := effectiveSDKConfig(cfg)
		if sdkCfg == nil || sdkCfg.Client.Codex.OptimizeMultiAgentV2 != enabled {
			t.Fatalf("SDK client OptimizeMultiAgentV2 does not match %t", enabled)
		}
		cfg.Client.Codex.OptimizeMultiAgentV2 = !enabled
		if sdkCfg.Client.Codex.OptimizeMultiAgentV2 != enabled {
			t.Fatal("effective SDK config shares client configuration state")
		}
	}
}

func TestEffectiveSDKConfigCopiesCodexOrphanDelegationCompatibility(t *testing.T) {
	cfg := &config.Config{Codex: config.CodexConfig{OrphanDelegationCompatibility: true}}

	sdkCfg := effectiveSDKConfig(cfg)
	if sdkCfg == nil || !sdkCfg.CodexOrphanDelegationCompatibility {
		t.Fatalf("CodexOrphanDelegationCompatibility = false, want true")
	}
}

func TestEffectiveSDKConfigCopiesCodexResponseSteering(t *testing.T) {
	cfg := &config.Config{Codex: config.CodexConfig{ResponseSteering: true}}

	sdkCfg := effectiveSDKConfig(cfg)
	if sdkCfg == nil || !sdkCfg.CodexResponseSteering {
		t.Fatalf("CodexResponseSteering = false, want true")
	}
}

func TestCodexResponseSteeringYAMLUnmarshal(t *testing.T) {
	yamlContent := []byte(`
codex:
  response-steering: true
`)
	var cfg config.Config
	if err := yaml.Unmarshal(yamlContent, &cfg); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if !cfg.Codex.ResponseSteering {
		t.Fatalf("cfg.Codex.ResponseSteering = false, want true")
	}
	sdkCfg := effectiveSDKConfig(&cfg)
	if sdkCfg == nil || !sdkCfg.CodexResponseSteering {
		t.Fatalf("sdkCfg.CodexResponseSteering = false, want true")
	}
}
