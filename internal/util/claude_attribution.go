package util

import (
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
