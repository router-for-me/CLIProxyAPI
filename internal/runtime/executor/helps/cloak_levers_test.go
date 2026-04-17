package helps

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// TestResolveOAuthLevers_DefaultsTrue verifies that nil cfg and nil fields both
// default to the legacy behavior (all levers enabled).
func TestResolveOAuthLevers_DefaultsTrue(t *testing.T) {
	t.Run("nil cfg", func(t *testing.T) {
		levers := ResolveOAuthLevers(nil, nil)
		if !levers.SanitizeSystemPrompt {
			t.Error("SanitizeSystemPrompt should default to true")
		}
		if !levers.RemapToolNames {
			t.Error("RemapToolNames should default to true")
		}
		if !levers.InjectBillingHeader {
			t.Error("InjectBillingHeader should default to true")
		}
	})

	t.Run("empty cfg (all nil pointers)", func(t *testing.T) {
		cfg := &config.CloakConfig{}
		levers := ResolveOAuthLevers(cfg, nil)
		if !levers.SanitizeSystemPrompt {
			t.Error("SanitizeSystemPrompt should default to true")
		}
		if !levers.RemapToolNames {
			t.Error("RemapToolNames should default to true")
		}
		if !levers.InjectBillingHeader {
			t.Error("InjectBillingHeader should default to true")
		}
	})
}

// TestResolveOAuthLevers_HeaderOptOutWithoutToken_Ignored verifies that the opt-out
// header is ignored when no matching token is presented.
func TestResolveOAuthLevers_HeaderOptOutWithoutToken_Ignored(t *testing.T) {
	cfg := &config.CloakConfig{
		OAuthDisableHeader: "supersecret",
	}
	hdr := http.Header{}
	hdr.Set("X-Cliproxy-Cloak-Opt-Out", "all")
	// No X-Cliproxy-Cloak-Token presented
	levers := ResolveOAuthLevers(cfg, hdr)
	if !levers.SanitizeSystemPrompt {
		t.Error("SanitizeSystemPrompt should remain true when token is missing")
	}
	if !levers.RemapToolNames {
		t.Error("RemapToolNames should remain true when token is missing")
	}
	if !levers.InjectBillingHeader {
		t.Error("InjectBillingHeader should remain true when token is missing")
	}
}

// TestResolveOAuthLevers_HeaderOptOut_WithValidToken_DisablesSpecified verifies that
// individual opt-out directives are applied when the token matches.
func TestResolveOAuthLevers_HeaderOptOut_WithValidToken_DisablesSpecified(t *testing.T) {
	cfg := &config.CloakConfig{
		OAuthDisableHeader: "supersecret",
	}
	hdr := http.Header{}
	hdr.Set("X-Cliproxy-Cloak-Token", "supersecret")
	hdr.Set("X-Cliproxy-Cloak-Opt-Out", "sanitize")
	levers := ResolveOAuthLevers(cfg, hdr)
	if levers.SanitizeSystemPrompt {
		t.Error("SanitizeSystemPrompt should be false after sanitize opt-out")
	}
	// Other levers remain enabled
	if !levers.RemapToolNames {
		t.Error("RemapToolNames should remain true")
	}
	if !levers.InjectBillingHeader {
		t.Error("InjectBillingHeader should remain true")
	}
}

// TestResolveOAuthLevers_HeaderOptOut_All_DisablesEverything verifies that the "all"
// directive disables every lever in one shot.
func TestResolveOAuthLevers_HeaderOptOut_All_DisablesEverything(t *testing.T) {
	cfg := &config.CloakConfig{
		OAuthDisableHeader: "supersecret",
	}
	hdr := http.Header{}
	hdr.Set("X-Cliproxy-Cloak-Token", "supersecret")
	hdr.Set("X-Cliproxy-Cloak-Opt-Out", "all")
	levers := ResolveOAuthLevers(cfg, hdr)
	if levers.SanitizeSystemPrompt {
		t.Error("SanitizeSystemPrompt should be false")
	}
	if levers.RemapToolNames {
		t.Error("RemapToolNames should be false")
	}
	if levers.InjectBillingHeader {
		t.Error("InjectBillingHeader should be false")
	}
}

// TestResolveOAuthLevers_HeaderOptOut_EmptySecret_AlwaysIgnoresHeader verifies that
// the opt-out mechanism is disabled entirely when OAuthDisableHeader is empty (fails closed).
func TestResolveOAuthLevers_HeaderOptOut_EmptySecret_AlwaysIgnoresHeader(t *testing.T) {
	cfg := &config.CloakConfig{
		OAuthDisableHeader: "", // no secret configured
	}
	hdr := http.Header{}
	hdr.Set("X-Cliproxy-Cloak-Token", "anything")
	hdr.Set("X-Cliproxy-Cloak-Opt-Out", "all")
	levers := ResolveOAuthLevers(cfg, hdr)
	if !levers.SanitizeSystemPrompt {
		t.Error("SanitizeSystemPrompt should remain true when secret is empty")
	}
	if !levers.RemapToolNames {
		t.Error("RemapToolNames should remain true when secret is empty")
	}
	if !levers.InjectBillingHeader {
		t.Error("InjectBillingHeader should remain true when secret is empty")
	}
}
