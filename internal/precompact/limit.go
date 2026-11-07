// Package precompact shrinks requests that do not fit the selected upstream
// model's context window by summarizing older turns with an auxiliary model.
package precompact

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/tidwall/gjson"
)

// ModelWindow returns the context window for model, falling back to the
// Claude default when the registry has no explicit value.
func ModelWindow(reg *registry.ModelRegistry, model string) int {
	if reg != nil {
		if info := reg.GetModelInfo(model, ""); info != nil {
			switch {
			case info.MaxContextLength > 0:
				return info.MaxContextLength
			case info.ContextLength > 0:
				return info.ContextLength
			case info.InputTokenLimit > 0:
				return info.InputTokenLimit
			}
		}
	}
	return registry.DefaultClaudeMaxInputTokens
}

// Budget is the input-token budget for a request: window * threshold minus the
// requested max_tokens (so the reply fits too).
func Budget(window int, threshold float64, body []byte) int {
	if threshold <= 0 || threshold > 1 {
		threshold = 0.85
	}
	budget := int(float64(window) * threshold)
	maxTokens := int(gjson.GetBytes(body, "max_tokens").Int())
	if maxTokens <= 0 {
		maxTokens = int(gjson.GetBytes(body, "max_completion_tokens").Int())
	}
	if maxTokens > 0 && maxTokens < window {
		budget -= maxTokens
	}
	if budget < 1 {
		budget = 1
	}
	return budget
}
