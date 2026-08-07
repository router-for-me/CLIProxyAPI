package modelconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// ComputeOpenAICompatModelsHash returns a stable hash for OpenAI-compatible models.
func ComputeOpenAICompatModelsHash(models []config.OpenAICompatibilityModel) string {
	keys := modelRoutingKeys(func(out func(key string)) {
		for _, model := range models {
			name := strings.TrimSpace(model.Name)
			alias := strings.TrimSpace(model.Alias)
			if name == "" && alias == "" {
				continue
			}
			out(strings.ToLower(name) + "|" + strings.ToLower(alias) + "|" + strings.TrimSpace(model.DisplayName) + "|" + fmt.Sprintf("image=%t", model.Image) + "|" + fmt.Sprintf("force-mapping=%t", model.ForceMapping) + "|" + fmt.Sprintf("is-compat=%t", model.IsCompat) + "|input=" + strings.Join(normalizeModalities(model.InputModalities), ",") + "|output=" + strings.Join(normalizeModalities(model.OutputModalities), ",") + thinkingHashSuffix(model.Thinking))
		}
	})
	return hashJoined(keys)
}

// ComputeVertexCompatModelsHash returns a stable hash for Vertex-compatible models.
func ComputeVertexCompatModelsHash(models []config.VertexCompatModel) string {
	keys := modelRoutingKeys(func(out func(key string)) {
		for _, model := range models {
			name := strings.TrimSpace(model.Name)
			alias := strings.TrimSpace(model.Alias)
			if name == "" && alias == "" {
				continue
			}
			out(strings.ToLower(name) + "|" + strings.ToLower(alias) + "|" + strings.TrimSpace(model.DisplayName) + "|" + fmt.Sprintf("force-mapping=%t", model.ForceMapping) + thinkingHashSuffix(model.Thinking))
		}
	})
	return hashJoined(keys)
}

// ComputeClaudeModelsHash returns a stable hash for Claude model aliases.
func ComputeClaudeModelsHash(models []config.ClaudeModel) string {
	keys := modelRoutingKeys(func(out func(key string)) {
		for _, model := range models {
			name := strings.TrimSpace(model.Name)
			alias := strings.TrimSpace(model.Alias)
			if name == "" && alias == "" {
				continue
			}
			out(strings.ToLower(name) + "|" + strings.ToLower(alias) + "|" + strings.TrimSpace(model.DisplayName) + "|" + fmt.Sprintf("force-mapping=%t", model.ForceMapping) + "|" + fmt.Sprintf("is-compat=%t", model.IsCompat) + thinkingHashSuffix(model.Thinking))
		}
	})
	return hashJoined(keys)
}

// ComputeCodexModelsHash returns a stable hash for Codex model aliases.
func ComputeCodexModelsHash(models []config.CodexModel) string {
	keys := modelRoutingKeys(func(out func(key string)) {
		for _, model := range models {
			name := strings.TrimSpace(model.Name)
			alias := strings.TrimSpace(model.Alias)
			if name == "" && alias == "" {
				continue
			}
			out(strings.ToLower(name) + "|" + strings.ToLower(alias) + "|" + strings.TrimSpace(model.DisplayName) + "|" + fmt.Sprintf("force-mapping=%t", model.ForceMapping) + "|" + fmt.Sprintf("is-compat=%t", model.IsCompat) + thinkingHashSuffix(model.Thinking))
		}
	})
	return hashJoined(keys)
}

// ComputeGeminiModelsHash returns a stable hash for Gemini model aliases.
func ComputeGeminiModelsHash(models []config.GeminiModel) string {
	keys := modelRoutingKeys(func(out func(key string)) {
		for _, model := range models {
			name := strings.TrimSpace(model.Name)
			alias := strings.TrimSpace(model.Alias)
			if name == "" && alias == "" {
				continue
			}
			out(strings.ToLower(name) + "|" + strings.ToLower(alias) + "|" + strings.TrimSpace(model.DisplayName) + "|" + fmt.Sprintf("force-mapping=%t", model.ForceMapping) + "|" + fmt.Sprintf("is-compat=%t", model.IsCompat) + thinkingHashSuffix(model.Thinking))
		}
	})
	return hashJoined(keys)
}

func normalizeModalities(raw []string) []string {
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, value := range raw {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func thinkingHashSuffix(support *registry.ThinkingSupport) string {
	data, _ := json.Marshal(support)
	return "|thinking=" + string(data)
}

func modelRoutingKeys(collect func(out func(key string))) []string {
	keys := make([]string, 0)
	collect(func(key string) {
		keys = append(keys, key)
	})
	return keys
}

func hashJoined(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(keys, "\n")))
	return hex.EncodeToString(sum[:])
}
