package auth

import (
	"bytes"
	"net/http"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
)

// Upstream prompt caches expire after a period without reads. In cache-aware
// affinity mode a session binding lives exactly as long as the cache it protects
// (plus a small grace period), so an idle session is released for rebalancing
// as soon as moving it no longer costs a cache rebuild.
const (
	promptCacheTTLGrace = 30 * time.Second
	// Anthropic's default ephemeral cache lifetime.
	claudePromptCacheTTL5m = 5 * time.Minute
	// Anthropic's extended cache lifetime (cache_control ttl "1h").
	claudePromptCacheTTL1h = time.Hour
	// OpenAI-style implicit prefix caches usually survive 5-10 minutes of
	// inactivity. Use the upper end so a binding is never released early.
	defaultPromptCacheTTL = 10 * time.Minute
)

// maxPromptCacheBindingTTL bounds every TTL estimatePromptCacheTTL returns. It
// is used as the retention limit for stores that cannot vary TTL per entry.
const maxPromptCacheBindingTTL = claudePromptCacheTTL1h + promptCacheTTLGrace

// estimatePromptCacheTTL predicts how long the upstream prompt cache for this
// request stays warm after the request is served.
//
// The selector runs before the executor rewrites cache_control, so the estimate
// errs long whenever the final TTL is not visible in the client request: holding
// a binding a little too long only delays rebalancing, while releasing it early
// throws away a warm cache.
//
// Claude:
//   - An explicit ttl "1h" breakpoint in the client body means a 1h cache.
//   - Native Claude Code owns its own breakpoints and sends ttl "1h" whenever it
//     selected the 1h pool, so a native request without it uses the 5m default.
//   - Any other caller has its breakpoints managed by CPA, which upgrades them to
//     1h for OAuth credentials, so assume 1h.
//
// "mixed" may resolve to Claude, so it follows the Claude rules.
func estimatePromptCacheTTL(provider string, headers http.Header, body []byte) time.Duration {
	if requestHasClaude1hCacheControl(body) {
		return claudePromptCacheTTL1h + promptCacheTTLGrace
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude", "anthropic", "mixed":
		if isNativeClaudeCodeUserAgent(headers) {
			return claudePromptCacheTTL5m + promptCacheTTLGrace
		}
		return claudePromptCacheTTL1h + promptCacheTTLGrace
	default:
		return defaultPromptCacheTTL + promptCacheTTLGrace
	}
}

func isNativeClaudeCodeUserAgent(headers http.Header) bool {
	if headers == nil {
		return false
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(headers.Get("User-Agent"))), "claude-cli/")
}

// requestHasClaude1hCacheControl reports whether any Anthropic cache breakpoint
// (tools, system, or message content) requests the 1h TTL.
func requestHasClaude1hCacheControl(body []byte) bool {
	if len(body) == 0 || !bytes.Contains(body, []byte(`"ttl"`)) {
		return false
	}
	found := false
	visit := func(block ojson.Result) bool {
		if block.Get("cache_control.ttl").String() == "1h" {
			found = true
			return false
		}
		return true
	}
	if tools := ojson.GetBytes(body, "tools"); tools.IsArray() {
		tools.ForEach(func(_, tool ojson.Result) bool { return visit(tool) })
	}
	if system := ojson.GetBytes(body, "system"); !found && system.IsArray() {
		system.ForEach(func(_, block ojson.Result) bool { return visit(block) })
	}
	if messages := ojson.GetBytes(body, "messages"); !found && messages.IsArray() {
		messages.ForEach(func(_, message ojson.Result) bool {
			if !visit(message) {
				return false
			}
			if content := message.Get("content"); content.IsArray() {
				content.ForEach(func(_, block ojson.Result) bool { return visit(block) })
			}
			return !found
		})
	}
	return found
}
