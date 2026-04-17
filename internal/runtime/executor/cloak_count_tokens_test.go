package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

// TestCountTokens_MatchesExecuteWhenLeversOverridden iterates all 8 combinations of the
// three OAuth cloaking levers (SanitizeSystemPrompt, InjectBillingHeader, RemapToolNames)
// and verifies that CountTokens produces a request body whose system-block shape matches
// what checkSystemInstructionsWithSigningMode would produce for those lever values.
//
// This is the parity guard ensuring CountTokens and Execute/ExecuteStream stay in sync.
func TestCountTokens_MatchesExecuteWhenLeversOverridden(t *testing.T) {
	for _, tc := range leversAllCombinations() {
		name := leverComboName(tc.sanitize, tc.billing, tc.remap)
		t.Run(name, func(t *testing.T) {
			var capturedBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturedBody, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"input_tokens":10}`))
			}))
			defer server.Close()

			cfg := &config.Config{
				ClaudeKey: []config.ClaudeKey{{
					APIKey: testOAuthKey,
					Cloak: &config.CloakConfig{
						Mode:                      "always",
						OAuthSanitizeSystemPrompt: &tc.sanitize,
						OAuthInjectBillingHeader:  &tc.billing,
						OAuthRemapToolNames:       &tc.remap,
					},
				}},
			}
			exec := NewClaudeExecutor(cfg)
			auth := &cliproxyauth.Auth{Attributes: map[string]string{
				"api_key":  testOAuthKey,
				"base_url": server.URL,
			}}
			payload := []byte(`{"system":"My custom system prompt","messages":[{"role":"user","content":"hello"}]}`)

			_, err := exec.CountTokens(
				context.Background(),
				auth,
				cliproxyexecutor.Request{Model: "claude-3-5-sonnet-20241022", Payload: payload},
				cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")},
			)
			if err != nil {
				t.Fatalf("CountTokens error: %v", err)
			}
			if len(capturedBody) == 0 {
				t.Fatal("no request body captured")
			}

			// Also compute the expected body via direct call to
			// checkSystemInstructionsWithSigningMode so we can compare shapes.
			expected := checkSystemInstructionsWithSigningMode(
				bytes.Clone(payload), false, false, true,
				tc.sanitize, tc.billing, "2.1.63", "", "",
			)

			// --- InjectBillingHeader assertion ---
			firstText := gjson.GetBytes(capturedBody, "system.0.text").String()
			expFirstText := gjson.GetBytes(expected, "system.0.text").String()
			gotBilling := strings.HasPrefix(firstText, "x-anthropic-billing-header:")
			wantBilling := strings.HasPrefix(expFirstText, "x-anthropic-billing-header:")
			if gotBilling != wantBilling {
				t.Errorf("InjectBillingHeader=%v: billing block mismatch — got %v, want %v (system[0]=%q)",
					tc.billing, gotBilling, wantBilling, firstText)
			}

			// --- SanitizeSystemPrompt assertion ---
			// The user's system prompt ends up in messages[0].content (first user turn).
			gotUserContent := gjson.GetBytes(capturedBody, "messages.0.content").String()
			expUserContent := gjson.GetBytes(expected, "messages.0.content").String()
			gotSanitized := strings.Contains(gotUserContent, "Use the available tools when needed")
			expSanitized := strings.Contains(expUserContent, "Use the available tools when needed")
			if gotSanitized != expSanitized {
				t.Errorf("SanitizeSystemPrompt=%v: sanitize mismatch — got sanitized=%v, want sanitized=%v",
					tc.sanitize, gotSanitized, expSanitized)
			}
		})
	}
}

type leverCombo struct {
	sanitize bool
	billing  bool
	remap    bool
}

func leversAllCombinations() []leverCombo {
	var out []leverCombo
	for _, s := range []bool{false, true} {
		for _, b := range []bool{false, true} {
			for _, r := range []bool{false, true} {
				out = append(out, leverCombo{sanitize: s, billing: b, remap: r})
			}
		}
	}
	return out
}

func leverComboName(sanitize, billing, remap bool) string {
	s := map[bool]string{true: "T", false: "F"}
	return "sanitize" + s[sanitize] + "_billing" + s[billing] + "_remap" + s[remap]
}
