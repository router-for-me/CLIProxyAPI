package util

import (
	"regexp"
	"strings"
)

// OpenAI-compatible tool/function names are commonly constrained to:
//   ^[a-zA-Z0-9_-]+$
// Some upstream gateways enforce this strictly (e.g. for input[].name).
var openaiCompatNameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// SanitizeOpenAICompatName coerces a name to a safe OpenAI-compat tool/function name.
// - trims whitespace
// - replaces invalid chars with '_'
// - trims leading/trailing '_'
// - max length 64
// - fallback to "tool" when empty after sanitization
func SanitizeOpenAICompatName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}

	name = openaiCompatNameSanitizer.ReplaceAllString(name, "_")
	name = strings.Trim(name, "_")
	if name == "" {
		name = "tool"
	}
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}
