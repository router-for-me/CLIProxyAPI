package diff

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

type GeminiModelsSummary struct {
	hash  string
	count int
}

type ClaudeModelsSummary struct {
	hash  string
	count int
}

type CodexModelsSummary struct {
	hash  string
	count int
}

type VertexModelsSummary struct {
	hash  string
	count int
}

// SummarizeGeminiModels hashes Gemini model aliases for change detection.
func SummarizeGeminiModels(models []config.GeminiModel) GeminiModelsSummary {
	if len(models) == 0 {
		return GeminiModelsSummary{}
	}
	keys := normalizeModelPairs(func(out func(key string)) {
		for _, model := range models {
			name := strings.TrimSpace(model.Name)
			alias := strings.TrimSpace(model.Alias)
			if name == "" && alias == "" {
				continue
			}
			out(strings.ToLower(name) + "|" + strings.ToLower(alias))
		}
	})
	return GeminiModelsSummary{
		hash:  ComputeGeminiModelsHash(models),
		count: len(keys),
	}
}

// SummarizeClaudeModels hashes Claude model aliases for change detection.
func SummarizeClaudeModels(models []config.ClaudeModel) ClaudeModelsSummary {
	if len(models) == 0 {
		return ClaudeModelsSummary{}
	}
	keys := normalizeModelPairs(func(out func(key string)) {
		for _, model := range models {
			name := strings.TrimSpace(model.Name)
			alias := strings.TrimSpace(model.Alias)
			if name == "" && alias == "" {
				continue
			}
			out(strings.ToLower(name) + "|" + strings.ToLower(alias))
		}
	})
	return ClaudeModelsSummary{
		hash:  ComputeClaudeModelsHash(models),
		count: len(keys),
	}
}

// SummarizeCodexModels hashes Codex model aliases for change detection.
func SummarizeCodexModels(models []config.CodexModel) CodexModelsSummary {
	if len(models) == 0 {
		return CodexModelsSummary{}
	}
	keys := normalizeModelPairs(func(out func(key string)) {
		for _, model := range models {
			name := strings.TrimSpace(model.Name)
			alias := strings.TrimSpace(model.Alias)
			if name == "" && alias == "" {
				continue
			}
			out(strings.ToLower(name) + "|" + strings.ToLower(alias))
		}
	})
	return CodexModelsSummary{
		hash:  ComputeCodexModelsHash(models),
		count: len(keys),
	}
}

// SummarizeVertexModels hashes Vertex-compatible model aliases for change detection.
func SummarizeVertexModels(models []config.VertexCompatModel) VertexModelsSummary {
	if len(models) == 0 {
		return VertexModelsSummary{}
	}
	keys := normalizeModelPairs(func(out func(key string)) {
		for _, model := range models {
			name := strings.TrimSpace(model.Name)
			alias := strings.TrimSpace(model.Alias)
			if name == "" && alias == "" {
				continue
			}
			out(strings.ToLower(name) + "|" + strings.ToLower(alias))
		}
	})
	if len(keys) == 0 {
		return VertexModelsSummary{}
	}
	return VertexModelsSummary{
		hash:  ComputeVertexCompatModelsHash(models),
		count: len(keys),
	}
}
