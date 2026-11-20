package modelconfig

import (
	"slices"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
)

// openRouterLevelOrder sorts OpenRouter's efforts, which it lists from highest to lowest, the way CPA lists levels.
var openRouterLevelOrder = []thinking.ThinkingLevel{
	thinking.LevelMinimal, thinking.LevelLow, thinking.LevelMedium, thinking.LevelHigh, thinking.LevelXHigh, thinking.LevelMax,
}

// OpenRouterThinkingSupport turns the reasoning efforts OpenRouter publishes for a model (reasoning.supported_efforts)
// into thinking support: "none" means reasoning can be turned off, the other efforts become levels from lowest to
// highest. It returns nil when OpenRouter publishes no level, so callers keep their default.
func OpenRouterThinkingSupport(efforts []string) *registry.ThinkingSupport {
	support := &registry.ThinkingSupport{}
	for _, effort := range efforts {
		level := strings.ToLower(strings.TrimSpace(effort))
		switch level {
		case "":
			continue
		case string(thinking.LevelNone):
			support.ZeroAllowed = true
			continue
		}
		support.Levels = append(support.Levels, level)
	}
	if len(support.Levels) == 0 {
		return nil
	}
	rank := func(level string) int {
		if i := slices.Index(openRouterLevelOrder, thinking.ThinkingLevel(level)); i >= 0 {
			return i
		}
		return len(openRouterLevelOrder)
	}
	slices.SortStableFunc(support.Levels, func(a, b string) int { return rank(a) - rank(b) })
	return NormalizeThinkingSupport(support)
}
