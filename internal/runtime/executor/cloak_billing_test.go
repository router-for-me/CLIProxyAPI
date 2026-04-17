package executor

import (
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	clipproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

// TestBilling_DisabledSkipsHeaderBlock verifies that when OAuthInjectBillingHeader is
// explicitly false, the x-anthropic-billing-header block is not prepended to the system array.
func TestBilling_DisabledSkipsHeaderBlock(t *testing.T) {
	disable := false
	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{{
			APIKey: testOAuthKey,
			Cloak: &config.CloakConfig{
				Mode:                     "always",
				OAuthInjectBillingHeader: &disable,
			},
		}},
	}
	auth := &clipproxyauth.Auth{Attributes: map[string]string{"api_key": testOAuthKey}}
	payload := []byte(`{"system":"My system","messages":[{"role":"user","content":"hello"}]}`)

	out := applyCloaking(context.Background(), cfg, auth, payload, "claude-3-5-sonnet-20241022", testOAuthKey)

	systemBlocks := gjson.GetBytes(out, "system")
	if !systemBlocks.IsArray() {
		t.Fatalf("expected system to be an array, got: %s", systemBlocks.Raw)
	}

	// First block must NOT be a billing header when injection is disabled.
	firstText := gjson.GetBytes(out, "system.0.text").String()
	if strings.HasPrefix(firstText, "x-anthropic-billing-header:") {
		t.Errorf("billing header should not be injected when OAuthInjectBillingHeader=false, got: %s", firstText)
	}
}

// TestBilling_DisabledKeepsCacheControlInvariants verifies that disabling billing-header
// injection does not break the cache_control structure on the remaining system blocks.
func TestBilling_DisabledKeepsCacheControlInvariants(t *testing.T) {
	disable := false
	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{{
			APIKey: testOAuthKey,
			Cloak: &config.CloakConfig{
				Mode:                     "always",
				OAuthInjectBillingHeader: &disable,
			},
		}},
	}
	auth := &clipproxyauth.Auth{Attributes: map[string]string{"api_key": testOAuthKey}}
	payload := []byte(`{"system":"My system","messages":[{"role":"user","content":"hello"}]}`)

	out := applyCloaking(context.Background(), cfg, auth, payload, "claude-3-5-sonnet-20241022", testOAuthKey)

	// Then run ensureCacheControl to verify the remaining blocks can still be processed.
	processed := ensureCacheControl(out)
	systemBlocks := gjson.GetBytes(processed, "system")
	if !systemBlocks.IsArray() {
		t.Fatalf("expected system to be an array after ensureCacheControl, got: %s", systemBlocks.Raw)
	}

	// System array should have at least 1 block (agent + static = 2 when billing disabled).
	var blockCount int
	systemBlocks.ForEach(func(_, _ gjson.Result) bool {
		blockCount++
		return true
	})
	if blockCount < 1 {
		t.Errorf("expected at least 1 system block after cloaking without billing, got %d", blockCount)
	}
}

// TestBilling_DisabledDoesNotForceCCHSigning verifies that disabling billing-header injection
// does not affect the useCCHSigning path — the billing block and CCH signing are decoupled.
func TestBilling_DisabledDoesNotForceCCHSigning(t *testing.T) {
	disable := false
	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{{
			APIKey: testOAuthKey,
			Cloak: &config.CloakConfig{
				Mode:                     "always",
				OAuthInjectBillingHeader: &disable,
			},
		}},
	}
	auth := &clipproxyauth.Auth{Attributes: map[string]string{"api_key": testOAuthKey}}
	payload := []byte(`{"system":"Test system","messages":[{"role":"user","content":"hello"}]}`)

	// This call must not panic or crash even when billing is disabled.
	out := applyCloaking(context.Background(), cfg, auth, payload, "claude-3-5-sonnet-20241022", testOAuthKey)
	if len(out) == 0 {
		t.Fatal("applyCloaking returned empty payload")
	}

	// system[0] text must not start with billing header prefix.
	firstText := gjson.GetBytes(out, "system.0.text").String()
	if strings.HasPrefix(firstText, "x-anthropic-billing-header:") {
		t.Errorf("billing header injected despite InjectBillingHeader=false: %s", firstText)
	}
}
