package auth

import "strings"

// Antigravity quota groups.
//
// Antigravity meters quota per group, not per model: the upstream
// retrieveUserQuotaSummary endpoint reports separate windows for
//   - "Gemini Models"       (model names starting with "gemini-")
//   - "Claude and GPT models" (model names starting with "claude-" or "gpt-oss")
//
// A QUOTA_EXHAUSTED 429 on one model therefore exhausts its whole group on that
// credential: every sibling model in the group stays unusable until the same
// reset deadline, while the other group is unaffected.
//
// Group keys are namespaced so a group key can never collide with a model key,
// which lets both live in ModelStates.

// AntigravityQuotaGroupGemini covers the upstream "Gemini Models" window.
const AntigravityQuotaGroupGemini = "antigravity:gemini"

// AntigravityQuotaGroupClaudeGPT covers the upstream "Claude and GPT models" window.
const AntigravityQuotaGroupClaudeGPT = "antigravity:claude-gpt"

// antigravityQuotaGroupPrefixes maps the upstream group names to the model name
// prefixes that fall inside them. Keep in sync with retrieveUserQuotaSummary.
var antigravityQuotaGroupPrefixes = []struct {
	group    string
	prefixes []string
}{
	{AntigravityQuotaGroupGemini, []string{"gemini-"}},
	{AntigravityQuotaGroupClaudeGPT, []string{"claude-", "gpt-oss"}},
}

// AntigravityQuotaGroup returns the quota group a model belongs to, or "" when the
// model name matches no known group. An empty result means "no group": callers keep
// the pre-group behavior of cooling only the failing model.
func AntigravityQuotaGroup(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return ""
	}
	for _, entry := range antigravityQuotaGroupPrefixes {
		for _, prefix := range entry.prefixes {
			if strings.HasPrefix(model, prefix) {
				return entry.group
			}
		}
	}
	return ""
}
