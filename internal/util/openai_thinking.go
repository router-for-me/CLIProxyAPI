package util

// OpenAIThinkingBudgetToEffort maps a numeric thinking budget (tokens)
// into an OpenAI-style reasoning effort level for level-based models.
//
// Ranges:
//   - 0            -> "none"
//   - 1..1024      -> "low"
//   - 1025..8192   -> "medium"
//   - 8193..24576  -> "high"
//   - 24577..      -> highest supported level for the model (defaults to "xhigh")
//
// Negative values (except the dynamic -1 handled elsewhere) are treated as unsupported.
func OpenAIThinkingBudgetToEffort(model string, budget int) (string, bool) {
	switch {
	case budget < 0:
		return "", false
	case budget == 0:
		return "none", true
	case budget > 0 && budget <= 1024:
		return "low", true
	case budget <= 8192:
		return "medium", true
	case budget <= 24576:
		return "high", true
	case budget > 24576:
		if levels := GetModelThinkingLevels(model); len(levels) > 0 {
			return levels[len(levels)-1], true
		}
		return "xhigh", true
	default:
		return "", false
	}
}
