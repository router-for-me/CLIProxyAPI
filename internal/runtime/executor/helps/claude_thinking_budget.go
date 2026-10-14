package helps

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// EnforceClaudeThinkingBudgetBelowMaxTokens restores the Anthropic invariant
// max_tokens > thinking.budget_tokens on a final Claude Messages body.
//
// Thinking application already enforces this invariant, but payload rules run
// afterwards and may lower max_tokens (for example a payload.default rule that
// fills an omitted client limit). Callers must skip this helper when payload
// rules targeted thinking, so configured thinking values are never overwritten.
//
// When the budget can be lowered to max_tokens-1 without falling below the
// model minimum, it is clamped. Otherwise manual thinking cannot be satisfied
// and is disabled so the request remains valid.
func EnforceClaudeThinkingBudgetBelowMaxTokens(body []byte, modelInfo *registry.ModelInfo) []byte {
	if len(body) == 0 || gjson.GetBytes(body, "thinking.type").String() != "enabled" {
		return body
	}
	budget := gjson.GetBytes(body, "thinking.budget_tokens")
	maxTokens := gjson.GetBytes(body, "max_tokens")
	if !budget.Exists() || !maxTokens.Exists() || maxTokens.Int() <= 0 || budget.Int() < maxTokens.Int() {
		return body
	}

	adjusted := maxTokens.Int() - 1
	minBudget := int64(1)
	if modelInfo != nil && modelInfo.Thinking != nil && modelInfo.Thinking.Min > 0 {
		minBudget = int64(modelInfo.Thinking.Min)
	}
	if adjusted >= minBudget {
		if updated, errSet := sjson.SetBytes(body, "thinking.budget_tokens", adjusted); errSet == nil {
			return updated
		}
		return body
	}

	out := body
	if updated, errSet := sjson.SetBytes(out, "thinking.type", "disabled"); errSet == nil {
		out = updated
	}
	for _, path := range []string{"thinking.budget_tokens", "thinking.display"} {
		if updated, errDelete := sjson.DeleteBytes(out, path); errDelete == nil {
			out = updated
		}
	}
	return out
}
