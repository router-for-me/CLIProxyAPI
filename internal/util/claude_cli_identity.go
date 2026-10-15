package util

import "strings"

// cliIdentityPrefixes are the identity sentences an Anthropic CLI client
// prepends to the system prompt. The Antigravity upstream rejects a request
// whose system instruction opens with one of these sentences (observed as
// upstream 429 RESOURCE_EXHAUSTED), so translators for that provider drop the
// leading sentence the same way the Claude Code attribution block is dropped.
var cliIdentityPrefixes = []string{
	"You are a Claude agent, built on Anthropic's Claude Agent SDK.",
	"You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK.",
	"You are Claude Code, Anthropic's official CLI for Claude.",
}

// StripLeadingCLIIdentityPrefix removes a leading Anthropic CLI identity
// sentence and the whitespace that follows it from text. Text that does not
// open with one of the known identity sentences is returned unchanged, and
// the remainder of the system prompt is never modified.
func StripLeadingCLIIdentityPrefix(text string) string {
	for _, prefix := range cliIdentityPrefixes {
		if strings.HasPrefix(text, prefix) {
			return strings.TrimLeft(text[len(prefix):], " \t\r\n")
		}
	}
	return text
}
