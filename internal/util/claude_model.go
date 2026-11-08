package util

import "strings"

// IsClaudeModel reports whether the model name identifies a Claude model.
func IsClaudeModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "claude")
}

// IsClaudeThinkingModel checks if the model is a Claude thinking model
// that requires the interleaved-thinking beta header.
func IsClaudeThinkingModel(model string) bool {
	lower := strings.ToLower(model)
	return strings.Contains(lower, "claude") && strings.Contains(lower, "thinking")
}

// claudeLegacySystemReminderModels lists the official Anthropic model IDs and
// aliases that reject a mid-conversation role=system message. Entries mirror the
// "claude" provider in internal/registry/models/models.json plus Anthropic's own
// bare and "-latest" aliases. Other providers' synthetic IDs do not belong here.
var claudeLegacySystemReminderModels = map[string]struct{}{
	"claude-3-5-haiku-20241022":  {},
	"claude-3-5-haiku-latest":    {},
	"claude-3-7-sonnet-20250219": {},
	"claude-3-7-sonnet-latest":   {},
	"claude-haiku-4-5":           {},
	"claude-haiku-4-5-20251001":  {},
	"claude-opus-4":              {},
	"claude-opus-4-20250514":     {},
	"claude-opus-4-1":            {},
	"claude-opus-4-1-20250805":   {},
	"claude-opus-4-5":            {},
	"claude-opus-4-5-20251101":   {},
	"claude-opus-4-6":            {},
	"claude-opus-4-7":            {},
	"claude-sonnet-4":            {},
	"claude-sonnet-4-20250514":   {},
	"claude-sonnet-4-5":          {},
	"claude-sonnet-4-5-20250929": {},
	"claude-sonnet-4-6":          {},
}

// IsClaudeLegacySystemReminderModel reports whether model is a known Claude
// model that rejects a mid-conversation role=system message. Matching ignores
// case, surrounding whitespace and any provider prefix. Unknown and future IDs
// are not listed, so callers treat them as accepting the turn.
func IsClaudeLegacySystemReminderModel(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if slash := strings.LastIndexByte(normalized, '/'); slash >= 0 {
		normalized = normalized[slash+1:]
	}
	_, legacy := claudeLegacySystemReminderModels[normalized]
	return legacy
}
