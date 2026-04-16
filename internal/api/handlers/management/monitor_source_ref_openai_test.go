package management

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestMonitorSourceResolver_OpenAIKeyDisabledState(t *testing.T) {
	resolver := newMonitorSourceResolver(&config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "demo",
				Prefix:  "team",
				BaseURL: "https://example.com/v1",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "sk-disabled", Disabled: true},
					{APIKey: "sk-enabled", Disabled: false},
				},
			},
		},
	}, nil)

	disabledRef := resolver.Resolve("sk-disabled", "")
	enabledRef := resolver.Resolve("sk-enabled", "")
	providerRef := resolver.Resolve("demo", "")

	if !disabledRef.Disabled {
		t.Fatal("expected disabled key source ref to be disabled")
	}
	if enabledRef.Disabled {
		t.Fatal("expected enabled key source ref to be enabled")
	}
	if providerRef.Disabled {
		t.Fatal("expected provider source ref to remain enabled when at least one key is enabled")
	}
}
