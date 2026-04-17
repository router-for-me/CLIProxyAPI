package executor

import (
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	clipproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

// testOAuthKey is a fake non-functional key used only to trigger the OAuth token code path.
// isClaudeOAuthToken checks for "sk-ant-oat" substring; this value is deliberately
// malformed and cannot be used against the Anthropic API.
const testOAuthKey = "TEST-sk-ant-oat-FAKE-NOT-A-REAL-KEY"

// TestApplyCloaking_SanitizeDisabled_PreservesClientSystem verifies that when
// OAuthSanitizeSystemPrompt is explicitly set to false, the original client system
// prompt passes through to the first user message verbatim instead of being replaced
// by the stub text.
func TestApplyCloaking_SanitizeDisabled_PreservesClientSystem(t *testing.T) {
	disable := false
	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{{
			APIKey: testOAuthKey,
			Cloak: &config.CloakConfig{
				Mode:                      "always",
				OAuthSanitizeSystemPrompt: &disable,
			},
		}},
	}
	auth := &clipproxyauth.Auth{Attributes: map[string]string{"api_key": testOAuthKey}}
	payload := []byte(`{"system":"My custom system prompt","messages":[{"role":"user","content":"hello"}]}`)

	out := applyCloaking(context.Background(), cfg, auth, payload, "claude-3-5-sonnet-20241022", testOAuthKey)

	// The first user message should contain the original system text, not the stub.
	firstUserContent := gjson.GetBytes(out, "messages.0.content").String()
	if !strings.Contains(firstUserContent, "My custom system prompt") {
		t.Errorf("expected original system prompt in user message, got: %s", firstUserContent)
	}
	// Must NOT contain the sanitize stub.
	if strings.Contains(firstUserContent, "Use the available tools when needed") {
		t.Errorf("sanitize stub should not be present when SanitizeSystemPrompt=false, got: %s", firstUserContent)
	}
}

// TestApplyCloaking_SanitizeEnabled_SameAsHEAD verifies that the default behavior
// (nil OAuthSanitizeSystemPrompt) still sanitizes the system prompt for OAuth tokens,
// matching legacy HEAD behavior.
func TestApplyCloaking_SanitizeEnabled_SameAsHEAD(t *testing.T) {
	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{{
			APIKey: testOAuthKey,
			Cloak: &config.CloakConfig{
				Mode: "always",
				// OAuthSanitizeSystemPrompt intentionally nil (default = sanitize).
			},
		}},
	}
	auth := &clipproxyauth.Auth{Attributes: map[string]string{"api_key": testOAuthKey}}
	payload := []byte(`{"system":"My detailed custom system prompt with lots of info","messages":[{"role":"user","content":"hello"}]}`)

	out := applyCloaking(context.Background(), cfg, auth, payload, "claude-3-5-sonnet-20241022", testOAuthKey)

	// The first user message should contain the sanitize stub, not the original text.
	firstUserContent := gjson.GetBytes(out, "messages.0.content").String()
	if strings.Contains(firstUserContent, "My detailed custom system prompt") {
		t.Errorf("original system prompt should be replaced by stub, got: %s", firstUserContent)
	}
	if !strings.Contains(firstUserContent, "Use the available tools when needed") {
		t.Errorf("expected sanitize stub in user message, got: %s", firstUserContent)
	}
}
