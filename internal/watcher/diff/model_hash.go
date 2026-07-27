package diff

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/modelconfig"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// ComputeOpenAICompatModelsHash returns a stable hash for OpenAI-compat models.
// Used to detect model list changes during hot reload.
func ComputeOpenAICompatModelsHash(models []config.OpenAICompatibilityModel) string {
	keys := normalizeModelPairs(func(out func(key string)) {
		for _, model := range models {
			name := strings.TrimSpace(model.Name)
			alias := strings.TrimSpace(model.Alias)
			if name == "" && alias == "" {
				continue
			}
			out(configuredModelHashKey(name, alias, model.DisplayName, configuredModelHashMetadata{
				Thinking:         model.Thinking,
				Image:            model.Image,
				InputModalities:  model.InputModalities,
				OutputModalities: model.OutputModalities,
			}))
		}
	})
	return hashJoined(keys)
}

// ComputeVertexCompatModelsHash returns a stable hash for Vertex-compatible models.
func ComputeVertexCompatModelsHash(models []config.VertexCompatModel) string {
	keys := normalizeModelPairs(func(out func(key string)) {
		for _, model := range models {
			name := strings.TrimSpace(model.Name)
			alias := strings.TrimSpace(model.Alias)
			if name == "" && alias == "" {
				continue
			}
			out(configuredModelHashKey(name, alias, model.DisplayName, configuredModelHashMetadata{Thinking: model.Thinking}))
		}
	})
	return hashJoined(keys)
}

// ComputeClaudeModelsHash returns a stable hash for Claude model aliases.
func ComputeClaudeModelsHash(models []config.ClaudeModel) string {
	keys := normalizeModelPairs(func(out func(key string)) {
		for _, model := range models {
			name := strings.TrimSpace(model.Name)
			alias := strings.TrimSpace(model.Alias)
			if name == "" && alias == "" {
				continue
			}
			out(configuredModelHashKey(name, alias, model.DisplayName, configuredModelHashMetadata{Thinking: model.Thinking}))
		}
	})
	return hashJoined(keys)
}

// ComputeCodexModelsHash returns a stable hash for Codex model aliases.
func ComputeCodexModelsHash(models []config.CodexModel) string {
	keys := normalizeModelPairs(func(out func(key string)) {
		for _, model := range models {
			name := strings.TrimSpace(model.Name)
			alias := strings.TrimSpace(model.Alias)
			if name == "" && alias == "" {
				continue
			}
			out(configuredModelHashKey(name, alias, model.DisplayName, configuredModelHashMetadata{
				Thinking:     model.Thinking,
				ForceMapping: model.ForceMapping,
			}))
		}
	})
	return hashJoined(keys)
}

// ComputeGeminiModelsHash returns a stable hash for Gemini model aliases.
func ComputeGeminiModelsHash(models []config.GeminiModel) string {
	keys := normalizeModelPairs(func(out func(key string)) {
		for _, model := range models {
			name := strings.TrimSpace(model.Name)
			alias := strings.TrimSpace(model.Alias)
			if name == "" && alias == "" {
				continue
			}
			out(configuredModelHashKey(name, alias, model.DisplayName, configuredModelHashMetadata{Thinking: model.Thinking}))
		}
	})
	return hashJoined(keys)
}

type configuredModelHashMetadata struct {
	Thinking         *registry.ThinkingSupport
	Image            bool
	ForceMapping     bool
	InputModalities  []string
	OutputModalities []string
}

func configuredModelHashKey(name, alias, displayName string, metadata configuredModelHashMetadata) string {
	payload := struct {
		Name             string                    `json:"name"`
		Alias            string                    `json:"alias"`
		DisplayName      string                    `json:"display_name,omitempty"`
		Thinking         *registry.ThinkingSupport `json:"thinking,omitempty"`
		Image            bool                      `json:"image,omitempty"`
		ForceMapping     bool                      `json:"force_mapping,omitempty"`
		InputModalities  []string                  `json:"input_modalities,omitempty"`
		OutputModalities []string                  `json:"output_modalities,omitempty"`
	}{
		Name:             strings.ToLower(strings.TrimSpace(name)),
		Alias:            strings.ToLower(strings.TrimSpace(alias)),
		DisplayName:      strings.TrimSpace(displayName),
		Thinking:         metadata.Thinking,
		Image:            metadata.Image,
		ForceMapping:     metadata.ForceMapping,
		InputModalities:  modelconfig.NormalizeModalities(metadata.InputModalities),
		OutputModalities: modelconfig.NormalizeModalities(metadata.OutputModalities),
	}
	data, _ := json.Marshal(payload)
	return string(data)
}

// ComputeExcludedModelsHash returns a normalized hash for excluded model lists.
func ComputeExcludedModelsHash(excluded []string) string {
	if len(excluded) == 0 {
		return ""
	}
	normalized := make([]string, 0, len(excluded))
	for _, entry := range excluded {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			normalized = append(normalized, strings.ToLower(trimmed))
		}
	}
	if len(normalized) == 0 {
		return ""
	}
	sort.Strings(normalized)
	data, _ := json.Marshal(normalized)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func normalizeModelPairs(collect func(out func(key string))) []string {
	seen := make(map[string]struct{})
	keys := make([]string, 0)
	collect(func(key string) {
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	})
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	return keys
}

func hashJoined(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(keys, "\n")))
	return hex.EncodeToString(sum[:])
}
