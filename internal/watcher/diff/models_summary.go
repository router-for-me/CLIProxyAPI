package diff

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
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
		hash:  hashJoined(keys),
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
		hash:  hashJoined(keys),
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
		hash:  hashJoined(keys),
		count: len(keys),
	}
}

// SummarizeVertexModels hashes Vertex-compatible model aliases for change detection.
func SummarizeVertexModels(models []config.VertexCompatModel) VertexModelsSummary {
	if len(models) == 0 {
		return VertexModelsSummary{}
	}
	names := make([]string, 0, len(models))
	for _, model := range models {
		name := strings.TrimSpace(model.Name)
		alias := strings.TrimSpace(model.Alias)
		if name == "" && alias == "" {
			continue
		}
		if alias != "" {
			name = alias
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return VertexModelsSummary{}
	}
	sort.Strings(names)
	sum := sha256.Sum256([]byte(strings.Join(names, "|")))
	return VertexModelsSummary{
		hash:  hex.EncodeToString(sum[:]),
		count: len(names),
	}
}
