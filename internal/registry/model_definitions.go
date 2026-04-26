// Package registry provides model definitions and lookup helpers for various AI providers.
// Static model metadata is loaded from the embedded models.json file and can be refreshed from network.
package registry

import (
	"strings"
)

const (
	codexBuiltinImageModelID = "gpt-image-2"

	deepSeekV4FlashModelID   = "deepseek-v4-flash"
	deepSeekV4ProModelID     = "deepseek-v4-pro"
	deepSeekChatModelID      = "deepseek-chat"
	deepSeekReasonerModelID  = "deepseek-reasoner"
	deepSeekModelCreatedTime = 1777161600 // 2026-04-26
)

// staticModelsJSON mirrors the top-level structure of models.json.
type staticModelsJSON struct {
	Claude      []*ModelInfo `json:"claude"`
	Gemini      []*ModelInfo `json:"gemini"`
	Vertex      []*ModelInfo `json:"vertex"`
	GeminiCLI   []*ModelInfo `json:"gemini-cli"`
	AIStudio    []*ModelInfo `json:"aistudio"`
	CodexFree   []*ModelInfo `json:"codex-free"`
	CodexTeam   []*ModelInfo `json:"codex-team"`
	CodexPlus   []*ModelInfo `json:"codex-plus"`
	CodexPro    []*ModelInfo `json:"codex-pro"`
	Kimi        []*ModelInfo `json:"kimi"`
	DeepSeek    []*ModelInfo `json:"deepseek"`
	Antigravity []*ModelInfo `json:"antigravity"`
}

// GetClaudeModels returns the standard Claude model definitions.
func GetClaudeModels() []*ModelInfo {
	return cloneModelInfos(getModels().Claude)
}

// GetGeminiModels returns the standard Gemini model definitions.
func GetGeminiModels() []*ModelInfo {
	return cloneModelInfos(getModels().Gemini)
}

// GetGeminiVertexModels returns Gemini model definitions for Vertex AI.
func GetGeminiVertexModels() []*ModelInfo {
	return cloneModelInfos(getModels().Vertex)
}

// GetGeminiCLIModels returns Gemini model definitions for the Gemini CLI.
func GetGeminiCLIModels() []*ModelInfo {
	return cloneModelInfos(getModels().GeminiCLI)
}

// GetAIStudioModels returns model definitions for AI Studio.
func GetAIStudioModels() []*ModelInfo {
	return cloneModelInfos(getModels().AIStudio)
}

// GetCodexFreeModels returns model definitions for the Codex free plan tier.
func GetCodexFreeModels() []*ModelInfo {
	return WithCodexBuiltins(cloneModelInfos(getModels().CodexFree))
}

// GetCodexTeamModels returns model definitions for the Codex team plan tier.
func GetCodexTeamModels() []*ModelInfo {
	return WithCodexBuiltins(cloneModelInfos(getModels().CodexTeam))
}

// GetCodexPlusModels returns model definitions for the Codex plus plan tier.
func GetCodexPlusModels() []*ModelInfo {
	return WithCodexBuiltins(cloneModelInfos(getModels().CodexPlus))
}

// GetCodexProModels returns model definitions for the Codex pro plan tier.
func GetCodexProModels() []*ModelInfo {
	return WithCodexBuiltins(cloneModelInfos(getModels().CodexPro))
}

// GetKimiModels returns the standard Kimi (Moonshot AI) model definitions.
func GetKimiModels() []*ModelInfo {
	return cloneModelInfos(getModels().Kimi)
}

// GetDeepSeekModels returns the standard DeepSeek model definitions.
//
// DeepSeek is also supplied as a built-in overlay so existing remote models.json
// catalogs that predate the deepseek section do not erase first-class provider
// support during startup/periodic refresh.
func GetDeepSeekModels() []*ModelInfo {
	return WithDeepSeekBuiltins(cloneModelInfos(getModels().DeepSeek))
}

// GetAntigravityModels returns the standard Antigravity model definitions.
func GetAntigravityModels() []*ModelInfo {
	return cloneModelInfos(getModels().Antigravity)
}

// WithCodexBuiltins injects hard-coded Codex-only model definitions that should
// not depend on remote models.json updates. Built-ins replace any matching IDs
// already present in the provided slice.
func WithCodexBuiltins(models []*ModelInfo) []*ModelInfo {
	return upsertModelInfos(models, codexBuiltinImageModelInfo())
}

// WithDeepSeekBuiltins injects hard-coded DeepSeek model definitions that should
// not depend on remote models.json updates. Built-ins replace any matching IDs
// already present in the provided slice.
func WithDeepSeekBuiltins(models []*ModelInfo) []*ModelInfo {
	return upsertModelInfos(models,
		deepSeekV4FlashModelInfo(),
		deepSeekV4ProModelInfo(),
		deepSeekChatModelInfo(),
		deepSeekReasonerModelInfo(),
	)
}

func codexBuiltinImageModelInfo() *ModelInfo {
	return &ModelInfo{
		ID:          codexBuiltinImageModelID,
		Object:      "model",
		Created:     1704067200, // 2024-01-01
		OwnedBy:     "openai",
		Type:        "openai",
		DisplayName: "GPT Image 2",
		Version:     codexBuiltinImageModelID,
	}
}

func deepSeekV4FlashModelInfo() *ModelInfo {
	return deepSeekThinkingModelInfo(
		deepSeekV4FlashModelID,
		"DeepSeek V4 Flash",
		"DeepSeek-V4-Flash: fast DeepSeek V4 model with 1M context and thinking/non-thinking modes.",
	)
}

func deepSeekV4ProModelInfo() *ModelInfo {
	return deepSeekThinkingModelInfo(
		deepSeekV4ProModelID,
		"DeepSeek V4 Pro",
		"DeepSeek-V4-Pro: flagship DeepSeek V4 model with 1M context and thinking/non-thinking modes.",
	)
}

func deepSeekChatModelInfo() *ModelInfo {
	return &ModelInfo{
		ID:                  deepSeekChatModelID,
		Object:              "model",
		Created:             deepSeekModelCreatedTime,
		OwnedBy:             "deepseek",
		Type:                "deepseek",
		DisplayName:         "DeepSeek Chat (legacy)",
		Version:             deepSeekChatModelID,
		Description:         "Legacy compatibility alias for deepseek-v4-flash non-thinking mode; scheduled for DeepSeek deprecation on 2026-07-24.",
		ContextLength:       1000000,
		MaxCompletionTokens: 384000,
		SupportedParameters: []string{"tools", "json_mode"},
	}
}

func deepSeekReasonerModelInfo() *ModelInfo {
	return deepSeekThinkingModelInfo(
		deepSeekReasonerModelID,
		"DeepSeek Reasoner (legacy)",
		"Legacy compatibility alias for deepseek-v4-flash thinking mode; scheduled for DeepSeek deprecation on 2026-07-24.",
	)
}

func deepSeekThinkingModelInfo(id, displayName, description string) *ModelInfo {
	return &ModelInfo{
		ID:                  id,
		Object:              "model",
		Created:             deepSeekModelCreatedTime,
		OwnedBy:             "deepseek",
		Type:                "deepseek",
		DisplayName:         displayName,
		Version:             id,
		Description:         description,
		ContextLength:       1000000,
		MaxCompletionTokens: 384000,
		SupportedParameters: []string{"tools", "json_mode", "reasoning_effort"},
		Thinking: &ThinkingSupport{
			ZeroAllowed:    true,
			DynamicAllowed: true,
			Levels:         []string{"high", "max"},
		},
	}
}

func upsertModelInfos(models []*ModelInfo, extras ...*ModelInfo) []*ModelInfo {
	if len(extras) == 0 {
		return models
	}

	extraIDs := make(map[string]struct{}, len(extras))
	extraList := make([]*ModelInfo, 0, len(extras))
	for _, extra := range extras {
		if extra == nil {
			continue
		}
		id := strings.TrimSpace(extra.ID)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if _, exists := extraIDs[key]; exists {
			continue
		}
		extraIDs[key] = struct{}{}
		extraList = append(extraList, cloneModelInfo(extra))
	}

	if len(extraList) == 0 {
		return models
	}

	filtered := make([]*ModelInfo, 0, len(models)+len(extraList))
	for _, model := range models {
		if model == nil {
			continue
		}
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		if _, exists := extraIDs[strings.ToLower(id)]; exists {
			continue
		}
		filtered = append(filtered, model)
	}

	filtered = append(filtered, extraList...)
	return filtered
}

// cloneModelInfos returns a shallow copy of the slice with each element deep-cloned.
func cloneModelInfos(models []*ModelInfo) []*ModelInfo {
	if len(models) == 0 {
		return nil
	}
	out := make([]*ModelInfo, len(models))
	for i, m := range models {
		out[i] = cloneModelInfo(m)
	}
	return out
}

// GetStaticModelDefinitionsByChannel returns static model definitions for a given channel/provider.
// It returns nil when the channel is unknown.
//
// Supported channels:
//   - claude
//   - gemini
//   - vertex
//   - gemini-cli
//   - aistudio
//   - codex
//   - kimi
//   - deepseek
//   - antigravity
func GetStaticModelDefinitionsByChannel(channel string) []*ModelInfo {
	key := strings.ToLower(strings.TrimSpace(channel))
	switch key {
	case "claude":
		return GetClaudeModels()
	case "gemini":
		return GetGeminiModels()
	case "vertex":
		return GetGeminiVertexModels()
	case "gemini-cli":
		return GetGeminiCLIModels()
	case "aistudio":
		return GetAIStudioModels()
	case "codex":
		return GetCodexProModels()
	case "kimi":
		return GetKimiModels()
	case "deepseek":
		return GetDeepSeekModels()
	case "antigravity":
		return GetAntigravityModels()
	default:
		return nil
	}
}

// LookupStaticModelInfo searches all static model definitions for a model by ID.
// Returns nil if no matching model is found.
func LookupStaticModelInfo(modelID string) *ModelInfo {
	if modelID == "" {
		return nil
	}

	data := getModels()
	allModels := [][]*ModelInfo{
		data.Claude,
		data.Gemini,
		data.Vertex,
		data.GeminiCLI,
		data.AIStudio,
		WithCodexBuiltins(data.CodexFree),
		WithCodexBuiltins(data.CodexTeam),
		WithCodexBuiltins(data.CodexPlus),
		WithCodexBuiltins(data.CodexPro),
		data.Kimi,
		WithDeepSeekBuiltins(data.DeepSeek),
		data.Antigravity,
	}
	for _, models := range allModels {
		for _, m := range models {
			if m != nil && m.ID == modelID {
				return cloneModelInfo(m)
			}
		}
	}

	return nil
}
