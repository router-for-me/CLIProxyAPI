package util

import "strings"

// StripLeadingCLIIdentityPrefix removes only the Agent SDK identity sentence.
// The suffix, including any whitespace, is preserved exactly.
func StripLeadingCLIIdentityPrefix(text string) string {
	return strings.TrimPrefix(text, "You are a Claude agent, built on Anthropic's Claude Agent SDK.")
}
