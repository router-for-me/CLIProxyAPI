package thinking

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// NormalizeCodexEffortLevel maps OpenAI "minimal" effort to Codex's lowest
// supported effort when the target model does not advertise "minimal".
func NormalizeCodexEffortLevel(level ThinkingLevel, modelInfo *registry.ModelInfo) ThinkingLevel {
	if !strings.EqualFold(strings.TrimSpace(string(level)), string(LevelMinimal)) {
		return level
	}

	if modelInfo != nil && modelInfo.Thinking != nil {
		levels := modelInfo.Thinking.Levels
		if HasLevel(levels, string(LevelMinimal)) {
			return level
		}
		if len(levels) > 0 && !HasLevel(levels, string(LevelLow)) {
			return level
		}
	}

	return LevelLow
}
