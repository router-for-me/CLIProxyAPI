// Package registry provides model definitions and lookup helpers for various AI providers.
// Static model metadata is stored in model_definitions_static_data.go.
package registry

import (
	"sort"
	"strings"
)

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
//   - qwen
//   - iflow
//   - kimi
//   - kiro
//   - kilo
//   - github-copilot
//   - amazonq
//   - cursor (via cursor-api; use dedicated cursor: block)
//   - minimax (use dedicated minimax: block; api.minimax.io)
//   - roo (use dedicated roo: block; api.roocode.com)
//   - kilo (use dedicated kilo: block; api.kilo.ai)
//   - antigravity (returns static overrides only)
func GetStaticModelDefinitionsByChannel(channel string) []*ModelInfo {
	key := strings.ToLower(strings.TrimSpace(channel))
	switch key {
	case "openai":
		return GetOpenAIModels()
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
		return GetOpenAIModels()
	case "qwen":
		return GetQwenModels()
	case "iflow":
		return GetIFlowModels()
	case "kimi":
		return GetKimiModels()
	case "github-copilot":
		return GetGitHubCopilotModels()
	case "kiro":
		return GetKiroModels()
	case "amazonq":
		return GetAmazonQModels()
	case "cursor":
		return GetCursorModels()
	case "minimax":
		return GetMiniMaxModels()
	case "roo":
		return GetRooModels()
	case "kilo":
		return GetKiloModels()
	case "deepseek":
		return GetDeepSeekModels()
	case "groq":
		return GetGroqModels()
	case "mistral":
		return GetMistralModels()
	case "siliconflow":
		return GetSiliconFlowModels()
	case "openrouter":
		return GetOpenRouterModels()
	case "together":
		return GetTogetherModels()
	case "fireworks":
		return GetFireworksModels()
	case "novita":
		return GetNovitaModels()
	case "antigravity":
		cfg := GetAntigravityModelConfig()
		if len(cfg) == 0 {
			return nil
		}
		models := make([]*ModelInfo, 0, len(cfg))
		for modelID, entry := range cfg {
			if modelID == "" || entry == nil {
				continue
			}
			models = append(models, &ModelInfo{
				ID:                  modelID,
				Object:              "model",
				OwnedBy:             "antigravity",
				Type:                "antigravity",
				Thinking:            entry.Thinking,
				MaxCompletionTokens: entry.MaxCompletionTokens,
			})
		}
		sort.Slice(models, func(i, j int) bool {
			return strings.ToLower(models[i].ID) < strings.ToLower(models[j].ID)
		})
		return models
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

	allModels := [][]*ModelInfo{
		GetClaudeModels(),
		GetGeminiModels(),
		GetGeminiVertexModels(),
		GetGeminiCLIModels(),
		GetAIStudioModels(),
		GetOpenAIModels(),
		GetQwenModels(),
		GetIFlowModels(),
		GetKimiModels(),
		GetGitHubCopilotModels(),
		GetKiroModels(),
		GetKiloModels(),
		GetAmazonQModels(),
		GetCursorModels(),
		GetMiniMaxModels(),
		GetRooModels(),
		GetKiloModels(),
		GetDeepSeekModels(),
		GetGroqModels(),
		GetMistralModels(),
		GetSiliconFlowModels(),
		GetOpenRouterModels(),
		GetTogetherModels(),
		GetFireworksModels(),
		GetNovitaModels(),
	}
	for _, models := range allModels {
		for _, m := range models {
			if m != nil && m.ID == modelID {
				return m
			}
		}
	}

	// Check Antigravity static config
	if cfg := GetAntigravityModelConfig()[modelID]; cfg != nil {
		return &ModelInfo{
			ID:                  modelID,
			Thinking:            cfg.Thinking,
			MaxCompletionTokens: cfg.MaxCompletionTokens,
		}
	}

	return nil
}

// GetGitHubCopilotModels returns the available models for GitHub Copilot.
// These models are available through the GitHub Copilot API at api.githubcopilot.com.
func GetGitHubCopilotModels() []*ModelInfo {
	now := int64(1732752000) // 2024-11-27
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
		})
	}

	models = append(models, []*ModelInfo{
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
			ID:                  "claude-haiku-4.5",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "Claude Haiku 4.5",
			Description:         "Anthropic Claude Haiku 4.5 via GitHub Copilot",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			SupportedEndpoints:  []string{"/chat/completions"},
		},
		{
			ID:                  "claude-opus-4.1",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "Claude Opus 4.1",
			Description:         "Anthropic Claude Opus 4.1 via GitHub Copilot",
			ContextLength:       200000,
			MaxCompletionTokens: 32000,
			SupportedEndpoints:  []string{"/chat/completions"},
		},
		{
			ID:                  "claude-opus-4.5",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "Claude Opus 4.5",
			Description:         "Anthropic Claude Opus 4.5 via GitHub Copilot",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			SupportedEndpoints:  []string{"/chat/completions"},
		},
		{
			ID:                  "claude-opus-4.6",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "Claude Opus 4.6",
			Description:         "Anthropic Claude Opus 4.6 via GitHub Copilot",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			SupportedEndpoints:  []string{"/chat/completions"},
		},
		{
			ID:                  "claude-sonnet-4",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "Claude Sonnet 4",
			Description:         "Anthropic Claude Sonnet 4 via GitHub Copilot",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			SupportedEndpoints:  []string{"/chat/completions"},
		},
		{
			ID:                  "claude-sonnet-4.5",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "Claude Sonnet 4.5",
			Description:         "Anthropic Claude Sonnet 4.5 via GitHub Copilot",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			SupportedEndpoints:  []string{"/chat/completions"},
		},
		{
			ID:                  "claude-sonnet-4.6",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "Claude Sonnet 4.6",
			Description:         "Anthropic Claude Sonnet 4.6 via GitHub Copilot",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			SupportedEndpoints:  []string{"/chat/completions"},
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
		},
		{
			ID:                  "gemini-3.1-pro-preview",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "Gemini 3.1 Pro (Preview)",
			Description:         "Google Gemini 3.1 Pro Preview via GitHub Copilot",
			ContextLength:       1048576,
			MaxCompletionTokens: 65536,
		},
		{
			ID:                  "gemini-3-flash-preview",
			Object:              "model",
			Created:             now,
			OwnedBy:             "github-copilot",
			Type:                "github-copilot",
			DisplayName:         "Gemini 3 Flash (Preview)",
			Description:         "Google Gemini 3 Flash Preview via GitHub Copilot",
			ContextLength:       1048576,
			MaxCompletionTokens: 65536,
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

	// GitHub Copilot currently exposes a uniform 128K context window across registered models.
	for _, model := range models {
		if model != nil {
			model.ContextLength = 128000
		}
	}

	return models
}

// GetKiroModels returns the Kiro (AWS CodeWhisperer) model definitions
func GetKiroModels() []*ModelInfo {
	return []*ModelInfo{
		// --- Base Models ---
		{
			ID:                  "kiro-auto",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro Auto",
			Description:         "Automatic model selection by Kiro",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "kiro-claude-opus-4-6",
			Object:              "model",
			Created:             1736899200, // 2025-01-15
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro Claude Opus 4.6",
			Description:         "Claude Opus 4.6 via Kiro (2.2x credit)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "kiro-claude-sonnet-4-6",
			Object:              "model",
			Created:             1739836800, // 2025-02-18
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro Claude Sonnet 4.6",
			Description:         "Claude Sonnet 4.6 via Kiro (1.3x credit)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "kiro-claude-opus-4-5",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro Claude Opus 4.5",
			Description:         "Claude Opus 4.5 via Kiro (2.2x credit)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "kiro-claude-sonnet-4-5",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro Claude Sonnet 4.5",
			Description:         "Claude Sonnet 4.5 via Kiro (1.3x credit)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "kiro-claude-sonnet-4",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro Claude Sonnet 4",
			Description:         "Claude Sonnet 4 via Kiro (1.3x credit)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "kiro-claude-haiku-4-5",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro Claude Haiku 4.5",
			Description:         "Claude Haiku 4.5 via Kiro (0.4x credit)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		// --- 第三方模型 (通过 Kiro 接入) ---
		{
			ID:                  "kiro-deepseek-3-2",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro DeepSeek 3.2",
			Description:         "DeepSeek 3.2 via Kiro",
			ContextLength:       128000,
			MaxCompletionTokens: 32768,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "kiro-minimax-m2-1",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro MiniMax M2.1",
			Description:         "MiniMax M2.1 via Kiro",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "kiro-qwen3-coder-next",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro Qwen3 Coder Next",
			Description:         "Qwen3 Coder Next via Kiro",
			ContextLength:       128000,
			MaxCompletionTokens: 32768,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "kiro-gpt-4o",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro GPT-4o",
			Description:         "OpenAI GPT-4o via Kiro",
			ContextLength:       128000,
			MaxCompletionTokens: 16384,
		},
		{
			ID:                  "kiro-gpt-4",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro GPT-4",
			Description:         "OpenAI GPT-4 via Kiro",
			ContextLength:       128000,
			MaxCompletionTokens: 8192,
		},
		{
			ID:                  "kiro-gpt-4-turbo",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro GPT-4 Turbo",
			Description:         "OpenAI GPT-4 Turbo via Kiro",
			ContextLength:       128000,
			MaxCompletionTokens: 16384,
		},
		{
			ID:                  "kiro-gpt-3-5-turbo",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro GPT-3.5 Turbo",
			Description:         "OpenAI GPT-3.5 Turbo via Kiro",
			ContextLength:       16384,
			MaxCompletionTokens: 4096,
		},
		// --- Agentic Variants (Optimized for coding agents with chunked writes) ---
		{
			ID:                  "kiro-claude-opus-4-6-agentic",
			Object:              "model",
			Created:             1736899200, // 2025-01-15
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro Claude Opus 4.6 (Agentic)",
			Description:         "Claude Opus 4.6 optimized for coding agents (chunked writes)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "kiro-claude-sonnet-4-6-agentic",
			Object:              "model",
			Created:             1739836800, // 2025-02-18
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro Claude Sonnet 4.6 (Agentic)",
			Description:         "Claude Sonnet 4.6 optimized for coding agents (chunked writes)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "kiro-claude-opus-4-5-agentic",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro Claude Opus 4.5 (Agentic)",
			Description:         "Claude Opus 4.5 optimized for coding agents (chunked writes)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "kiro-claude-sonnet-4-5-agentic",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro Claude Sonnet 4.5 (Agentic)",
			Description:         "Claude Sonnet 4.5 optimized for coding agents (chunked writes)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "kiro-claude-sonnet-4-agentic",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro Claude Sonnet 4 (Agentic)",
			Description:         "Claude Sonnet 4 optimized for coding agents (chunked writes)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "kiro-claude-haiku-4-5-agentic",
			Object:              "model",
			Created:             1732752000,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         "Kiro Claude Haiku 4.5 (Agentic)",
			Description:         "Claude Haiku 4.5 optimized for coding agents (chunked writes)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
	}
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

// GetCursorModels returns model definitions for Cursor via cursor-api (wisdgod).
// Use dedicated cursor: block in config (token-file, cursor-api-url).
func GetCursorModels() []*ModelInfo {
	now := int64(1732752000)
	return []*ModelInfo{
		{
			ID:                  "claude-4.5-opus-high-thinking",
			Object:              "model",
			Created:             now,
			OwnedBy:             "cursor",
			Type:                "cursor",
			DisplayName:         "Claude 4.5 Opus High Thinking",
			Description:         "Anthropic Claude 4.5 Opus via Cursor (cursor-api)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
		},
		{
			ID:                  "claude-4.5-opus-high",
			Object:              "model",
			Created:             now,
			OwnedBy:             "cursor",
			Type:                "cursor",
			DisplayName:         "Claude 4.5 Opus High",
			Description:         "Anthropic Claude 4.5 Opus via Cursor (cursor-api)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
		},
		{
			ID:                  "claude-4.5-sonnet-thinking",
			Object:              "model",
			Created:             now,
			OwnedBy:             "cursor",
			Type:                "cursor",
			DisplayName:         "Claude 4.5 Sonnet Thinking",
			Description:         "Anthropic Claude 4.5 Sonnet via Cursor (cursor-api)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
		},
		{
			ID:                  "claude-4-sonnet",
			Object:              "model",
			Created:             now,
			OwnedBy:             "cursor",
			Type:                "cursor",
			DisplayName:         "Claude 4 Sonnet",
			Description:         "Anthropic Claude 4 Sonnet via Cursor (cursor-api)",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
		},
		{
			ID:                  "gpt-4o",
			Object:              "model",
			Created:             now,
			OwnedBy:             "cursor",
			Type:                "cursor",
			DisplayName:         "GPT-4o",
			Description:         "OpenAI GPT-4o via Cursor (cursor-api)",
			ContextLength:       128000,
			MaxCompletionTokens: 16384,
		},
		{
			ID:                  "gpt-5.1-codex",
			Object:              "model",
			Created:             now,
			OwnedBy:             "cursor",
			Type:                "cursor",
			DisplayName:         "GPT-5.1 Codex",
			Description:         "OpenAI GPT-5.1 Codex via Cursor (cursor-api)",
			ContextLength:       200000,
			MaxCompletionTokens: 32768,
		},
		{
			ID:                  "default",
			Object:              "model",
			Created:             now,
			OwnedBy:             "cursor",
			Type:                "cursor",
			DisplayName:         "Default",
			Description:         "Cursor server-selected default model",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
		},
	}
}

// GetMiniMaxModels returns model definitions for MiniMax (api.minimax.chat).
// Use dedicated minimax: block in config (OAuth token-file or api-key).
func GetMiniMaxModels() []*ModelInfo {
	now := int64(1758672000)
	return []*ModelInfo{
		{
			ID:                  "minimax-m2",
			Object:              "model",
			Created:             now,
			OwnedBy:             "minimax",
			Type:                "minimax",
			DisplayName:         "MiniMax M2",
			Description:         "MiniMax M2 via api.minimax.chat",
			ContextLength:       128000,
			MaxCompletionTokens: 32768,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "minimax-m2.1",
			Object:              "model",
			Created:             1766448000,
			OwnedBy:             "minimax",
			Type:                "minimax",
			DisplayName:         "MiniMax M2.1",
			Description:         "MiniMax M2.1 via api.minimax.chat",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
		{
			ID:                  "minimax-m2.5",
			Object:              "model",
			Created:             1770825600,
			OwnedBy:             "minimax",
			Type:                "minimax",
			DisplayName:         "MiniMax M2.5",
			Description:         "MiniMax M2.5 via api.minimax.chat",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
	}
}

// GetRooModels returns model definitions for Roo Code (RooCodeInc).
// Use dedicated roo: block in config (token-file or api-key).
func GetRooModels() []*ModelInfo {
	now := int64(1758672000)
	return []*ModelInfo{
		{
			ID:                  "roo-default",
			Object:              "model",
			Created:             now,
			OwnedBy:             "roo",
			Type:                "roo",
			DisplayName:         "Roo Default",
			Description:         "Roo Code default model via api.roocode.com",
			ContextLength:       128000,
			MaxCompletionTokens: 32768,
		},
	}
}

// GetDeepSeekModels returns static model definitions for DeepSeek.
func GetDeepSeekModels() []*ModelInfo {
	now := int64(1738672000)
	return []*ModelInfo{
		{
			ID:                  "deepseek-chat",
			Object:              "model",
			Created:             now,
			OwnedBy:             "deepseek",
			Type:                "deepseek",
			DisplayName:         "DeepSeek V3",
			Description:         "DeepSeek-V3 chat model",
			ContextLength:       64000,
			MaxCompletionTokens: 8192,
		},
		{
			ID:                  "deepseek-reasoner",
			Object:              "model",
			Created:             now,
			OwnedBy:             "deepseek",
			Type:                "deepseek",
			DisplayName:         "DeepSeek R1",
			Description:         "DeepSeek-R1 reasoning model",
			ContextLength:       64000,
			MaxCompletionTokens: 8192,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
	}
}

// GetGroqModels returns static model definitions for Groq.
func GetGroqModels() []*ModelInfo {
	now := int64(1738672000)
	return []*ModelInfo{
		{
			ID:                  "llama-3.3-70b-versatile",
			Object:              "model",
			Created:             now,
			OwnedBy:             "groq",
			Type:                "groq",
			DisplayName:         "Llama 3.3 70B (Groq)",
			Description:         "Llama 3.3 70B via Groq LPU",
			ContextLength:       128000,
			MaxCompletionTokens: 32768,
		},
		{
			ID:                  "llama-3.1-8b-instant",
			Object:              "model",
			Created:             now,
			OwnedBy:             "groq",
			Type:                "groq",
			DisplayName:         "Llama 3.1 8B (Groq)",
			Description:         "Llama 3.1 8B via Groq LPU",
			ContextLength:       128000,
			MaxCompletionTokens: 32768,
		},
	}
}

// GetMistralModels returns static model definitions for Mistral AI.
func GetMistralModels() []*ModelInfo {
	now := int64(1738672000)
	return []*ModelInfo{
		{
			ID:                  "mistral-large-latest",
			Object:              "model",
			Created:             now,
			OwnedBy:             "mistral",
			Type:                "mistral",
			DisplayName:         "Mistral Large",
			Description:         "Mistral Large latest model",
			ContextLength:       128000,
			MaxCompletionTokens: 32768,
		},
		{
			ID:                  "codestral-latest",
			Object:              "model",
			Created:             now,
			OwnedBy:             "mistral",
			Type:                "mistral",
			DisplayName:         "Codestral",
			Description:         "Mistral code-specialized model",
			ContextLength:       32000,
			MaxCompletionTokens: 32768,
		},
	}
}

// GetSiliconFlowModels returns static model definitions for SiliconFlow.
func GetSiliconFlowModels() []*ModelInfo {
	now := int64(1738672000)
	return []*ModelInfo{
		{
			ID:                  "deepseek-ai/DeepSeek-V3",
			Object:              "model",
			Created:             now,
			OwnedBy:             "siliconflow",
			Type:                "siliconflow",
			DisplayName:         "DeepSeek V3 (SiliconFlow)",
			Description:         "DeepSeek-V3 via SiliconFlow",
			ContextLength:       64000,
			MaxCompletionTokens: 8192,
		},
		{
			ID:                  "deepseek-ai/DeepSeek-R1",
			Object:              "model",
			Created:             now,
			OwnedBy:             "siliconflow",
			Type:                "siliconflow",
			DisplayName:         "DeepSeek R1 (SiliconFlow)",
			Description:         "DeepSeek-R1 via SiliconFlow",
			ContextLength:       64000,
			MaxCompletionTokens: 8192,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
	}
}

// GetOpenRouterModels returns static model definitions for OpenRouter.
func GetOpenRouterModels() []*ModelInfo {
	now := int64(1738672000)
	return []*ModelInfo{
		{
			ID:                  "anthropic/claude-3.5-sonnet",
			Object:              "model",
			Created:             now,
			OwnedBy:             "openrouter",
			Type:                "openrouter",
			DisplayName:         "Claude 3.5 Sonnet (OpenRouter)",
			ContextLength:       200000,
			MaxCompletionTokens: 8192,
		},
		{
			ID:                  "google/gemini-2.0-flash-001",
			Object:              "model",
			Created:             now,
			OwnedBy:             "openrouter",
			Type:                "openrouter",
			DisplayName:         "Gemini 2.0 Flash (OpenRouter)",
			ContextLength:       1000000,
			MaxCompletionTokens: 8192,
		},
	}
}

// GetTogetherModels returns static model definitions for Together AI.
func GetTogetherModels() []*ModelInfo {
	now := int64(1738672000)
	return []*ModelInfo{
		{
			ID:                  "deepseek-ai/DeepSeek-V3",
			Object:              "model",
			Created:             now,
			OwnedBy:             "together",
			Type:                "together",
			DisplayName:         "DeepSeek V3 (Together)",
			ContextLength:       64000,
			MaxCompletionTokens: 8192,
		},
		{
			ID:                  "deepseek-ai/DeepSeek-R1",
			Object:              "model",
			Created:             now,
			OwnedBy:             "together",
			Type:                "together",
			DisplayName:         "DeepSeek R1 (Together)",
			ContextLength:       64000,
			MaxCompletionTokens: 8192,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
	}
}

// GetFireworksModels returns static model definitions for Fireworks AI.
func GetFireworksModels() []*ModelInfo {
	now := int64(1738672000)
	return []*ModelInfo{
		{
			ID:                  "accounts/fireworks/models/deepseek-v3",
			Object:              "model",
			Created:             now,
			OwnedBy:             "fireworks",
			Type:                "fireworks",
			DisplayName:         "DeepSeek V3 (Fireworks)",
			ContextLength:       64000,
			MaxCompletionTokens: 8192,
		},
		{
			ID:                  "accounts/fireworks/models/deepseek-r1",
			Object:              "model",
			Created:             now,
			OwnedBy:             "fireworks",
			Type:                "fireworks",
			DisplayName:         "DeepSeek R1 (Fireworks)",
			ContextLength:       64000,
			MaxCompletionTokens: 8192,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
	}
}

// GetNovitaModels returns static model definitions for Novita AI.
func GetNovitaModels() []*ModelInfo {
	now := int64(1738672000)
	return []*ModelInfo{
		{
			ID:                  "deepseek/deepseek-v3",
			Object:              "model",
			Created:             now,
			OwnedBy:             "novita",
			Type:                "novita",
			DisplayName:         "DeepSeek V3 (Novita)",
			ContextLength:       64000,
			MaxCompletionTokens: 8192,
		},
		{
			ID:                  "deepseek/deepseek-r1",
			Object:              "model",
			Created:             now,
			OwnedBy:             "novita",
			Type:                "novita",
			DisplayName:         "DeepSeek R1 (Novita)",
			ContextLength:       64000,
			MaxCompletionTokens: 8192,
			Thinking:            &ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		},
	}
}
