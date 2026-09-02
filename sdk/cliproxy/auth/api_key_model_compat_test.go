package auth

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestResolvedAPIKeyModelInfoPropagatesIsCompat(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{ClaudeKey: []internalconfig.ClaudeKey{{
		APIKey: "compat-key",
		Prefix: "tenant",
		Models: []internalconfig.ClaudeModel{{
			Name:     "compat-upstream-model",
			Alias:    "compat-alias",
			IsCompat: true,
		}},
	}}})
	auth := configuredCapabilityTestAuth("compat-auth", "compat-key")
	registerCapabilityTestAuth(t, manager, auth)

	req := manager.attachResolvedAPIKeyModelInfo(cliproxyexecutor.Request{}, auth, "tenant/compat-alias", "compat-upstream-model")
	info, ok := ResolvedAPIKeyModelInfo(req)
	if !ok || info == nil || !info.IsCompat {
		t.Fatalf("ResolvedAPIKeyModelInfo() = (%+v, %t), want IsCompat=true", info, ok)
	}
	opts := withSelectedModelCompatibility(cliproxyexecutor.Options{}, req)
	if compatible, _ := opts.Metadata[cliproxyexecutor.SelectedModelCompatibilityMetadataKey].(bool); !compatible {
		t.Fatalf("SelectedModelCompatibilityMetadataKey = %#v, want true", opts.Metadata[cliproxyexecutor.SelectedModelCompatibilityMetadataKey])
	}
}
