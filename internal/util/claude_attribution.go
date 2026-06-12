package util

import (
	"net/http"
	"strings"
	"unicode"
)

const claudeCodeAttributionSystemPrefix = "x-anthropic-billing-header:"

// IsClaudeCodeAttributionSystemText reports whether text is the Claude Code
// attribution block that carries per-request billing and prompt fingerprint data.
func IsClaudeCodeAttributionSystemText(text string) bool {
	text = strings.TrimLeftFunc(text, unicode.IsSpace)
	return strings.HasPrefix(text, claudeCodeAttributionSystemPrefix)
}

// IsSDKEntrypoint reports whether the parsed entrypoint string looks SDK-shaped.
// claude-code stamps values like "sdk-cli", "sdk-py", or "sdk-ts" into the
// x-anthropic-billing-header attribution block when invoked via --print or the
// Anthropic SDK, and Anthropic gates Fast Mode on that marker. The Claude Fast
// Mode spoof in the executor uses this predicate to detect when the upstream
// client is SDK-shaped so the entrypoint can be rewritten to a TTY-style value.
func IsSDKEntrypoint(ep string) bool {
	ep = strings.ToLower(strings.TrimSpace(ep))
	return strings.HasPrefix(ep, "sdk") ||
		ep == "external-sdk" ||
		strings.Contains(ep, "-sdk")
}

// IsForceFastModeHeader reports whether the inbound request carries the opt-in
// "X-CPA-Force-Fast-Mode" header set to a truthy value ("1", "true", "yes",
// "on"; case-insensitive). The Claude Fast Mode spoof in the executor uses
// this predicate as a per-request activation path that complements UA-based
// detection: a downstream gateway can stamp the header to force the spoof
// bundle even when the upstream User-Agent does not look SDK-shaped. The
// header itself must be stripped before forwarding upstream so that Anthropic
// never sees it.
func IsForceFastModeHeader(h http.Header) bool {
	if h == nil {
		return false
	}
	v := strings.ToLower(strings.TrimSpace(h.Get("X-CPA-Force-Fast-Mode")))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}
