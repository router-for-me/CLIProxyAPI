package codexintegration

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestResolveStableModelGemini31Pro(t *testing.T) {
	policy, err := NewModelPolicy(config.DefaultCodexIntegrationConfig().Models)
	if err != nil {
		t.Fatalf("NewModelPolicy() error = %v", err)
	}

	resolved, ok := policy.Resolve("antigravity/gemini-3.1-pro")
	if !ok {
		t.Fatal("Resolve() ok = false, want true")
	}
	if resolved.Provider != "antigravity" || resolved.UpstreamModel != "gemini-pro-agent" {
		t.Fatalf("Resolve() = %#v, want antigravity/gemini-pro-agent", resolved)
	}
}

func TestModelPolicyRejectsDuplicateSlug(t *testing.T) {
	models := config.DefaultCodexIntegrationConfig().Models
	models = append(models, models[0])
	if _, err := NewModelPolicy(models); err == nil {
		t.Fatal("NewModelPolicy() error = nil, want duplicate slug error")
	}
}

func TestReservedProviderNamespace(t *testing.T) {
	for _, provider := range []string{"aistudio", "antigravity", "claude", "gemini", "kimi", "vertex", "xai"} {
		if !IsReservedProvider(provider) {
			t.Fatalf("IsReservedProvider(%q) = false, want true", provider)
		}
	}
	if IsReservedProvider("teamA") {
		t.Fatal("IsReservedProvider(teamA) = true, want false")
	}
}
