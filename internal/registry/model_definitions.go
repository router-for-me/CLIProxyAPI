// Package registry provides model definitions and lookup helpers for various AI providers.
// Static model metadata is loaded from the embedded models.json file and can be refreshed from network.
package registry

import (
	"strings"
)

const (
	codexBuiltinImage15ModelID      = "gpt-image-1.5"
	codexBuiltinImageModelID        = "gpt-image-2"
	xaiBuiltinImageModelID          = "grok-imagine-image"
	xaiBuiltinImageQualityModelID   = "grok-imagine-image-quality"
	xaiBuiltinVideoModelID          = "grok-imagine-video"
	xaiBuiltinVideo15PreviewModelID = "grok-imagine-video-1.5-preview"
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
	Qoder       []*ModelInfo `json:"qoder"`
	Antigravity []*ModelInfo `json:"antigravity"`
	XAI         []*ModelInfo `json:"xai"`
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

// GetGeminiCLIModels returns model definitions for the Gemini CLI.
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

// GetAntigravityModels returns the standard Antigravity model definitions.
func GetAntigravityModels() []*ModelInfo {
	return cloneModelInfos(getModels().Antigravity)
}

// AntigravityWebSearchModelFor returns the Antigravity model that should run a
// native web search request for modelID.
func AntigravityWebSearchModelFor(modelID string) string {
	modelID = normalizeAntigravityCapabilityModelID(modelID)
	if modelID == "" {
		return ""
	}
	for _, model := range GetGlobalRegistry().GetAvailableModelsByProvider("antigravity") {
		if model == nil {
			continue
		}
		currentModelID := normalizeAntigravityCapabilityModelID(model.ID)
		if currentModelID == "" {
			continue
		}
		if currentModelID == modelID {
			if model.SupportsWebSearch {
				return currentModelID
			}
			return ""
		}
	}
	return ""
}

// GetXAIModels returns the standard xAI Grok model definitions.
func GetXAIModels() []*ModelInfo {
	return WithXAIBuiltins(cloneModelInfos(getModels().XAI))
}

// WithCodexBuiltins injects hard-coded Codex-only model definitions that should
// not depend on remote models.json updates. Built-ins replace any matching IDs
// already present in the provided slice.
func WithCodexBuiltins(models []*ModelInfo) []*ModelInfo {
	return upsertModelInfos(models, codexBuiltinImage15ModelInfo(), codexBuiltinImageModelInfo())
}

// WithXAIBuiltins injects hard-coded xAI image/video model definitions that should
// not depend on remote models.json updates.
func WithXAIBuiltins(models []*ModelInfo) []*ModelInfo {
	return upsertModelInfos(models, xaiBuiltinImageModelInfo(), xaiBuiltinImageQualityModelInfo(), xaiBuiltinVideoModelInfo(), xaiBuiltinVideo15PreviewModelInfo())
}

func normalizeAntigravityCapabilityModelID(modelID string) string {
	modelID = strings.ToLower(strings.TrimSpace(modelID))
	if open := strings.LastIndex(modelID, "("); open >= 0 && strings.HasSuffix(modelID, ")") {
		modelID = strings.TrimSpace(modelID[:open])
	}
	return modelID
}

func codexBuiltinImage15ModelInfo() *ModelInfo {
	return &ModelInfo{
		ID:          codexBuiltinImage15ModelID,
		Object:      "model",
		Created:     1704067200, // 2024-01-01
		OwnedBy:     "openai",
		Type:        "openai",
		DisplayName: "GPT Image 1.5",
		Version:     codexBuiltinImage15ModelID,
	}
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

func xaiBuiltinImageModelInfo() *ModelInfo {
	return &ModelInfo{
		ID:          xaiBuiltinImageModelID,
		Object:      "model",
		Created:     1735689600, // 2025-01-01
		OwnedBy:     "xai",
		Type:        "xai",
		DisplayName: "Grok Imagine Image",
		Name:        xaiBuiltinImageModelID,
		Description: "xAI Grok image generation model.",
	}
}

func xaiBuiltinImageQualityModelInfo() *ModelInfo {
	return &ModelInfo{
		ID:          xaiBuiltinImageQualityModelID,
		Object:      "model",
		Created:     1735689600, // 2025-01-01
		OwnedBy:     "xai",
		Type:        "xai",
		DisplayName: "Grok Imagine Image Quality",
		Name:        xaiBuiltinImageQualityModelID,
		Description: "xAI Grok higher-fidelity image generation model.",
	}
}

func xaiBuiltinVideoModelInfo() *ModelInfo {
	return &ModelInfo{
		ID:          xaiBuiltinVideoModelID,
		Object:      "model",
		Created:     1735689600, // 2025-01-01
		OwnedBy:     "xai",
		Type:        "xai",
		DisplayName: "Grok Imagine Video",
		Name:        xaiBuiltinVideoModelID,
		Description: "xAI Grok video generation model.",
	}
}

func xaiBuiltinVideo15PreviewModelInfo() *ModelInfo {
	return &ModelInfo{
		ID:          xaiBuiltinVideo15PreviewModelID,
		Object:      "model",
		Created:     1735689600, // 2025-01-01
		OwnedBy:     "xai",
		Type:        "xai",
		DisplayName: "Grok Imagine Video 1.5 Preview",
		Name:        xaiBuiltinVideo15PreviewModelID,
		Description: "xAI Grok preview video generation model.",
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
//   - antigravity
//   - xai
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
	case "github-copilot":
		return GetGitHubCopilotModels()
	case "kiro":
		return GetKiroModels()
	case "kilo":
		return GetKiloModels()
	case "amazonq":
		return GetAmazonQModels()
	case "antigravity":
		return GetAntigravityModels()
	case "xai", "x-ai", "grok":
		return GetXAIModels()
	case "qoder":
		return GetQoderModels()
	default:
		return nil
	}
}

// GetCursorModels returns the fallback Cursor model definitions.
func GetCursorModels() []*ModelInfo {
	return []*ModelInfo{
		{ID: "composer-2", Object: "model", OwnedBy: "cursor", Type: "cursor", DisplayName: "Composer 2", ContextLength: 200000, MaxCompletionTokens: 64000, Thinking: &ThinkingSupport{Max: 50000, DynamicAllowed: true}},
		{ID: "claude-4-sonnet", Object: "model", OwnedBy: "cursor", Type: "cursor", DisplayName: "Claude 4 Sonnet", ContextLength: 200000, MaxCompletionTokens: 64000, Thinking: &ThinkingSupport{Max: 50000, DynamicAllowed: true}},
		{ID: "claude-3.5-sonnet", Object: "model", OwnedBy: "cursor", Type: "cursor", DisplayName: "Claude 3.5 Sonnet", ContextLength: 200000, MaxCompletionTokens: 8192},
		{ID: "gpt-4o", Object: "model", OwnedBy: "cursor", Type: "cursor", DisplayName: "GPT-4o", ContextLength: 128000, MaxCompletionTokens: 16384},
		{ID: "cursor-small", Object: "model", OwnedBy: "cursor", Type: "cursor", DisplayName: "Cursor Small", ContextLength: 200000, MaxCompletionTokens: 64000},
		{ID: "gemini-2.5-pro", Object: "model", OwnedBy: "cursor", Type: "cursor", DisplayName: "Gemini 2.5 Pro", ContextLength: 1000000, MaxCompletionTokens: 65536, Thinking: &ThinkingSupport{Max: 50000, DynamicAllowed: true}},
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
		data.CodexPro,
		data.Kimi,
		data.Antigravity,
		data.XAI,
		data.Qoder,
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

// defaultCopilotClaudeContextLength is the conservative prompt token limit for
// Claude models accessed via the GitHub Copilot API. Individual accounts are
// capped at 128K; business accounts at 168K. When the dynamic /models API fetch
// succeeds, the real per-account limit overrides this value. This constant is
// only used as a safe fallback.
const defaultCopilotClaudeContextLength = 128000

// GetGitHubCopilotModels returns the available models for GitHub Copilot.
// These models are available through the GitHub Copilot API at api.githubcopilot.com.
func GetGitHubCopilotModels() []*ModelInfo {
	now := int64(1732752000) // 2024-11-27
	copilotClaudeEndpoints := []string{"/chat/completions", "/messages"}
	gpt4oEntries := []struct {
		ID          string
		DisplayName string
		Description string
	}{
		{ID: "gpt-4o-2024-11-20", DisplayName: "GPT-4o (2024-11-20)", Description: "OpenAI GPT-4o 2024-11-20 via GitHub Copilot"},
		{ID: "gpt-4o-2024-08-06", DisplayName: "GPT-4o (2024-08-06)", Description: "OpenAI GPT-4o 2024-08-06 via GitHub Copilot"},
		{ID: "gpt-4o-2024-05-13", DisplayName: "GPT-4o (2024-05-13)", Description: "OpenAI GPT-4o 2024-05-13 via GitHub Copilot"},
		{ID: "gpt-4o", DisplayName: "GPT-4o", Description: "OpenAI GPT-4o via GitHub Copilot"},
		{ID: "gpt-4-o-preview", DisplayName: "GPT-4-o Preview", Description: "OpenAI GPT-4-o Preview via GitHub Copilot"},
	}

	models := []*ModelInfo{
		{
			ID:                  "gpt-4.1",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "GPT-4.1",
			Description:         "OpenAI GPT-4.1 via GitHub Copilot",
			ContextLength:       128000,
			MaxCompletionTokens: 16384,
			SupportedEndpoints:  []string{"/chat/completions", "/responses"},
		},
	}

	for _, entry := range gpt4oEntries {
		models = append(models, &ModelInfo{
			ID:                  entry.ID,
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         entry.DisplayName,
			Description:         entry.Description,
			ContextLength:       128000,
			MaxCompletionTokens: 16384,
			SupportedEndpoints:  []string{"/chat/completions", "/responses"},
		})
	}

	return append(models, []*ModelInfo{
		{
			ID:                  "gpt-5",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "GPT-5",
			Description:         "OpenAI GPT-5 via GitHub Copilot",
			ContextLength:       200000,
			MaxCompletionTokens: 32768,
			SupportedEndpoints:  []string{"/chat/completions", "/responses"},
			Thinking:            &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
		{
			ID:                  "gpt-5-mini",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "GPT-5 Mini",
			Description:         "OpenAI GPT-5 Mini via GitHub Copilot",
			ContextLength:       128000,
			MaxCompletionTokens: 16384,
			SupportedEndpoints:  []string{"/chat/completions", "/responses"},
			Thinking:            &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
		{
			ID:                  "gpt-5-codex",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "GPT-5 Codex",
			Description:         "OpenAI GPT-5 Codex via GitHub Copilot",
			ContextLength:       200000,
			MaxCompletionTokens: 32768,
			SupportedEndpoints:  []string{"/responses"},
			Thinking:            &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
		{
			ID:                  "gpt-5.1",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "GPT-5.1",
			Description:         "OpenAI GPT-5.1 via GitHub Copilot",
			ContextLength:       200000,
			MaxCompletionTokens: 32768,
			SupportedEndpoints:  []string{"/chat/completions", "/responses"},
			Thinking:            &ThinkingSupport{Levels: []string{"none", "low", "medium", "high"}},
		},
		{
			ID:                  "gpt-5.1-codex",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "GPT-5.1 Codex",
			Description:         "OpenAI GPT-5.1 Codex via GitHub Copilot",
			ContextLength:       200000,
			MaxCompletionTokens: 32768,
			SupportedEndpoints:  []string{"/responses"},
			Thinking:            &ThinkingSupport{Levels: []string{"none", "low", "medium", "high"}},
		},
		{
			ID:                  "gpt-5.1-codex-mini",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "GPT-5.1 Codex Mini",
			Description:         "OpenAI GPT-5.1 Codex Mini via GitHub Copilot",
			ContextLength:       128000,
			MaxCompletionTokens: 16384,
			SupportedEndpoints:  []string{"/responses"},
			Thinking:            &ThinkingSupport{Levels: []string{"none", "low", "medium", "high"}},
		},
		{
			ID:                  "gpt-5.1-codex-max",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "GPT-5.1 Codex Max",
			Description:         "OpenAI GPT-5.1 Codex Max via GitHub Copilot",
			ContextLength:       200000,
			MaxCompletionTokens: 32768,
			SupportedEndpoints:  []string{"/responses"},
			Thinking:            &ThinkingSupport{Levels: []string{"none", "low", "medium", "high", "xhigh"}},
		},
		{
			ID:                  "gpt-5.2",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "GPT-5.2",
			Description:         "OpenAI GPT-5.2 via GitHub Copilot",
			ContextLength:       200000,
			MaxCompletionTokens: 32768,
			SupportedEndpoints:  []string{"/chat/completions", "/responses"},
			Thinking:            &ThinkingSupport{Levels: []string{"none", "low", "medium", "high", "xhigh"}},
		},
		{
			ID:                  "gpt-5.2-codex",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "GPT-5.2 Codex",
			Description:         "OpenAI GPT-5.2 Codex via GitHub Copilot",
			ContextLength:       200000,
			MaxCompletionTokens: 32768,
			SupportedEndpoints:  []string{"/responses"},
			Thinking:            &ThinkingSupport{Levels: []string{"none", "low", "medium", "high", "xhigh"}},
		},
		{
			ID:                  "gpt-5.3-codex",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "GPT-5.3 Codex",
			Description:         "OpenAI GPT-5.3 Codex via GitHub Copilot",
			ContextLength:       200000,
			MaxCompletionTokens: 32768,
			SupportedEndpoints:  []string{"/responses"},
			Thinking:            &ThinkingSupport{Levels: []string{"none", "low", "medium", "high", "xhigh"}},
		},
		{
			ID:                  "gpt-5.4",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "GPT-5.4",
			Description:         "OpenAI GPT-5.4 via GitHub Copilot",
			ContextLength:       200000,
			MaxCompletionTokens: 32768,
			SupportedEndpoints:  []string{"/responses"},
			Thinking:            &ThinkingSupport{Levels: []string{"none", "low", "medium", "high", "xhigh"}},
		},
		{
			ID:                  "gpt-5.4-mini",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "GPT-5.4 mini",
			Description:         "OpenAI GPT-5.4 mini via GitHub Copilot",
			ContextLength:       200000,
			MaxCompletionTokens: 32768,
			SupportedEndpoints:  []string{"/responses"},
			Thinking:            &ThinkingSupport{Levels: []string{"none", "low", "medium", "high", "xhigh"}},
		},
		{
			ID:                  "claude-haiku-4.5",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "Claude Haiku 4.5",
			Description:         "Anthropic Claude Haiku 4.5 via GitHub Copilot",
			ContextLength:       defaultCopilotClaudeContextLength,
			MaxCompletionTokens: 64000,
			SupportedEndpoints:  copilotClaudeEndpoints,
		},
		{
			ID:                  "claude-opus-4.1",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "Claude Opus 4.1",
			Description:         "Anthropic Claude Opus 4.1 via GitHub Copilot",
			ContextLength:       defaultCopilotClaudeContextLength,
			MaxCompletionTokens: 32000,
			SupportedEndpoints:  copilotClaudeEndpoints,
		},
		{
			ID:                  "claude-opus-4.5",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "Claude Opus 4.5",
			Description:         "Anthropic Claude Opus 4.5 via GitHub Copilot",
			ContextLength:       defaultCopilotClaudeContextLength,
			MaxCompletionTokens: 64000,
			SupportedEndpoints:  copilotClaudeEndpoints,
			Thinking:            &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
		{
			ID:                  "claude-opus-4.6",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "Claude Opus 4.6",
			Description:         "Anthropic Claude Opus 4.6 via GitHub Copilot",
			ContextLength:       defaultCopilotClaudeContextLength,
			MaxCompletionTokens: 64000,
			SupportedEndpoints:  copilotClaudeEndpoints,
			Thinking:            &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
		{
			ID:                  "claude-sonnet-4",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "Claude Sonnet 4",
			Description:         "Anthropic Claude Sonnet 4 via GitHub Copilot",
			ContextLength:       defaultCopilotClaudeContextLength,
			MaxCompletionTokens: 64000,
			SupportedEndpoints:  copilotClaudeEndpoints,
			Thinking:            &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
		{
			ID:                  "claude-sonnet-4.5",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "Claude Sonnet 4.5",
			Description:         "Anthropic Claude Sonnet 4.5 via GitHub Copilot",
			ContextLength:       defaultCopilotClaudeContextLength,
			MaxCompletionTokens: 64000,
			SupportedEndpoints:  copilotClaudeEndpoints,
			Thinking:            &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
		{
			ID:                  "claude-sonnet-4.6",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "Claude Sonnet 4.6",
			Description:         "Anthropic Claude Sonnet 4.6 via GitHub Copilot",
			ContextLength:       defaultCopilotClaudeContextLength,
			MaxCompletionTokens: 64000,
			SupportedEndpoints:  copilotClaudeEndpoints,
			Thinking:            &ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
		{
			ID:                  "gemini-2.5-pro",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "Gemini 2.5 Pro",
			Description:         "Google Gemini 2.5 Pro via GitHub Copilot",
			ContextLength:       1048576,
			MaxCompletionTokens: 65536,
			SupportedEndpoints:  []string{"/chat/completions"},
		},
		{
			ID:                  "gemini-3-pro-preview",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "Gemini 3 Pro (Preview)",
			Description:         "Google Gemini 3 Pro Preview via GitHub Copilot",
			ContextLength:       1048576,
			MaxCompletionTokens: 65536,
			SupportedEndpoints:  []string{"/chat/completions"},
		},
		{
			ID:                  "gemini-3.1-pro-preview",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "Gemini 3.1 Pro (Preview)",
			Description:         "Google Gemini 3.1 Pro Preview via GitHub Copilot",
			ContextLength:       173000,
			MaxCompletionTokens: 65536,
			SupportedEndpoints:  []string{"/chat/completions"},
		},
		{
			ID:                  "gemini-3-flash-preview",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "Gemini 3 Flash (Preview)",
			Description:         "Google Gemini 3 Flash Preview via GitHub Copilot",
			ContextLength:       173000,
			MaxCompletionTokens: 65536,
			SupportedEndpoints:  []string{"/chat/completions"},
		},
		{
			ID:                  "grok-code-fast-1",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "Grok Code Fast 1",
			Description:         "xAI Grok Code Fast 1 via GitHub Copilot",
			ContextLength:       128000,
			MaxCompletionTokens: 16384,
		},
		{
			ID:                  "oswe-vscode-prime",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "Raptor mini (Preview)",
			Description:         "Raptor mini via GitHub Copilot",
			ContextLength:       128000,
			MaxCompletionTokens: 16384,
			SupportedEndpoints:  []string{"/chat/completions", "/responses"},
		},
	}...)
}

// GetKiroModels returns Kiro-hosted fallback model definitions.
//
// Dynamic Kiro discovery remains the primary source of truth. This small
// fallback set mirrors the built-in Kiro OAuth aliases and prevents a valid
// Kiro auth from registering zero models when model discovery is temporarily
// unavailable.
func GetKiroModels() []*ModelInfo {
	now := int64(1732752000) // 2024-11-27
	endpoints := []string{"/chat/completions", "/messages"}
	entries := []struct {
		id          string
		displayName string
		description string
	}{
		{
			id:          "kiro-claude-sonnet-4-6",
			displayName: "Claude Sonnet 4.6",
			description: "Claude Sonnet 4.6 via Kiro",
		},
		{
			id:          "kiro-claude-sonnet-4-5",
			displayName: "Claude Sonnet 4.5",
			description: "Claude Sonnet 4.5 via Kiro",
		},
		{
			id:          "kiro-claude-sonnet-4",
			displayName: "Claude Sonnet 4",
			description: "Claude Sonnet 4 via Kiro",
		},
		{
			id:          "kiro-claude-opus-4-7",
			displayName: "Claude Opus 4.7",
			description: "Claude Opus 4.7 via Kiro",
		},
		{
			id:          "kiro-claude-opus-4-6",
			displayName: "Claude Opus 4.6",
			description: "Claude Opus 4.6 via Kiro",
		},
		{
			id:          "kiro-claude-opus-4-5",
			displayName: "Claude Opus 4.5",
			description: "Claude Opus 4.5 via Kiro",
		},
		{
			id:          "kiro-claude-haiku-4-5",
			displayName: "Claude Haiku 4.5",
			description: "Claude Haiku 4.5 via Kiro",
		},
	}

	models := make([]*ModelInfo, 0, len(entries))
	for _, entry := range entries {
		models = append(models, &ModelInfo{
			ID:                  entry.id,
			Object:              "model",
			Created:             now,
			OwnedBy:             "kiro",
			Type:                "kiro",
			DisplayName:         entry.displayName,
			Description:         entry.description,
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			SupportedEndpoints:  endpoints,
			Thinking:            cloneThinkingSupport(DefaultKiroThinkingSupport),
		})
	}
	return models
}

// GetAmazonQModels returns the Amazon Q (AWS CodeWhisperer) model definitions.
// These models use the same API as Kiro and share the same executor.
func GetAmazonQModels() []*ModelInfo {
	return []*ModelInfo{
		{
			ID:                  "amazonq-auto",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro", // Uses Kiro executor - same API
			DisplayName:         "Amazon Q Auto",
			Description:         "Automatic model selection by Amazon Q",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
		},
		{
			ID:                  "amazonq-claude-opus-4.5",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Amazon Q Claude Opus 4.5",
			Description:         "Claude Opus 4.5 via Amazon Q (2.2x credit)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
		},
		{
			ID:                  "amazonq-claude-sonnet-4.5",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Amazon Q Claude Sonnet 4.5",
			Description:         "Claude Sonnet 4.5 via Amazon Q (1.3x credit)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
		},
		{
			ID:                  "amazonq-claude-sonnet-4",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Amazon Q Claude Sonnet 4",
			Description:         "Claude Sonnet 4 via Amazon Q (1.3x credit)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
		},
		{
			ID:                  "amazonq-claude-haiku-4.5",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Amazon Q Claude Haiku 4.5",
			Description:         "Claude Haiku 4.5 via Amazon Q (0.4x credit)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
		},
	}
}

// GetQoderModels returns the Qoder model definitions.
func GetQoderModels() []*ModelInfo {
	return cloneModelInfos(getModels().Qoder)
}
