package executor

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
)

// Two credential Anthropic-Beta overrides that differ only by case must resolve
// to one value, so the main-cache-ttl-1h gate and the sent header agree.
func TestClaudeExecutor_MainCacheTTL1hCaseVariantOverridesAgreeWithSentHeader(t *testing.T) {
	cfg := &config.Config{}
	cfg.ClaudeCode.MainCacheTTL1h = true
	executor := NewClaudeExecutor(cfg)
	auth := mainCacheTTLTestAuth(mainCacheTTLTestGateway, map[string]string{
		"header:Anthropic-Beta": "claude-code-20250219,oauth-2025-04-20," + claudeExtendedCacheTTLBeta,
		"header:anthropic-beta": "claude-code-20250219,oauth-2025-04-20",
	})
	payload := mainCacheTTLTestPayload("")
	for _, stream := range []bool{false, true} {
		for i := 0; i < 50; i++ {
			seen, err := runMainCacheTTLNativeOAuth(t, executor, auth, payload, stream, "msg_case")
			if err != nil {
				t.Fatalf("stream=%t iteration %d: error = %v", stream, i, err)
			}
			has1h := helps.ClaudePayloadHas1hTTL(seen.body)
			betas := strings.Join(seen.header.Values("Anthropic-Beta"), ",")
			if has1h != strings.Contains(betas, claudeExtendedCacheTTLBeta) {
				t.Fatalf("stream=%t iteration %d: body 1h=%t but Anthropic-Beta=%q", stream, i, has1h, betas)
			}
			if !has1h {
				t.Fatalf("stream=%t iteration %d: want the 1h upgrade from the canonical override; Anthropic-Beta=%q", stream, i, betas)
			}
		}
	}
}

// A credential override whose attribute name has spaces around the header name
// is still sent, so the main-cache-ttl-1h gate must see it too.
func TestClaudeExecutor_MainCacheTTL1hSpacedOverrideNameAgreesWithSentHeader(t *testing.T) {
	cfg := &config.Config{}
	cfg.ClaudeCode.MainCacheTTL1h = true
	executor := NewClaudeExecutor(cfg)
	auth := mainCacheTTLTestAuth(mainCacheTTLTestGateway, map[string]string{
		"header: Anthropic-Beta ": "claude-code-20250219,oauth-2025-04-20",
	})
	payload := mainCacheTTLTestPayload("")
	for _, stream := range []bool{false, true} {
		seen, err := runMainCacheTTLNativeOAuth(t, executor, auth, payload, stream, "msg_spaced")
		if err != nil {
			t.Fatalf("stream=%t: error = %v", stream, err)
		}
		has1h := helps.ClaudePayloadHas1hTTL(seen.body)
		betas := strings.Join(seen.header.Values("Anthropic-Beta"), ",")
		if has1h || strings.Contains(betas, claudeExtendedCacheTTLBeta) {
			t.Fatalf("stream=%t: body 1h=%t, Anthropic-Beta=%q; want no 1h upgrade, since the override omits the beta", stream, has1h, betas)
		}
	}
}
