package tui

import (
	"testing"
)

// TestOAuthProviders_IncludesGrok verifies that the Grok (xAI) provider is
// present in the oauthProviders list used by the TUI OAuth tab.
func TestOAuthProviders_IncludesGrok(t *testing.T) {
	found := false
	for _, p := range oauthProviders {
		if p.name == "Grok (xAI)" {
			found = true
			if p.apiPath != "grok-auth-url" {
				t.Errorf("expected apiPath %q, got %q", "grok-auth-url", p.apiPath)
			}
			break
		}
	}
	if !found {
		t.Error("oauthProviders does not contain a 'Grok (xAI)' entry")
	}
}
