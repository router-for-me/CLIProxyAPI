// Package registry provides model definitions for various AI service providers.
// This file contains static model definitions that can be used by clients
// when registering their supported models.
package registry

import "time"

// GetClaudeModels returns the standard Claude model definitions
func GetClaudeModels() []*ModelInfo {
	return []*ModelInfo{

		{
			ID:          "claude-haiku-4-5-20251001",
			Object:      "model",
			Created:     1759276800, // 2025-10-01
			OwnedBy:     "anthropic",
			Type:        "claude",
			DisplayName: "Claude 4.5 Haiku",
		},
		{
			ID:          "claude-sonnet-4-5-20250929",
			Object:      "model",
			Created:     1759104000, // 2025-09-29
			OwnedBy:     "anthropic",
			Type:        "claude",
			DisplayName: "Claude 4.5 Sonnet",
		},
		{
			ID:          "claude-opus-4-1-20250805",
			Object:      "model",
			Created:     1722945600, // 2025-08-05
			OwnedBy:     "anthropic",
			Type:        "claude",
			DisplayName: "Claude 4.1 Opus",
		},
		{
			ID:          "claude-opus-4-20250514",
			Object:      "model",
			Created:     1715644800, // 2025-05-14
			OwnedBy:     "anthropic",
			Type:        "claude",
			DisplayName: "Claude 4 Opus",
		},
		{
			ID:          "claude-sonnet-4-20250514",
			Object:      "model",
			Created:     1715644800, // 2025-05-14
			OwnedBy:     "anthropic",
			Type:        "claude",
			DisplayName: "Claude 4 Sonnet",
		},
		{
			ID:          "claude-3-7-sonnet-20250219",
			Object:      "model",
			Created:     1708300800, // 2025-02-19
			OwnedBy:     "anthropic",
			Type:        "claude",
			DisplayName: "Claude 3.7 Sonnet",
		},
		{
			ID:          "claude-3-5-haiku-20241022",
			Object:      "model",
			Created:     1729555200, // 2024-10-22
			OwnedBy:     "anthropic",
			Type:        "claude",
			DisplayName: "Claude 3.5 Haiku",
		},
	}
}

// GetGeminiModels returns the standard Gemini model definitions
func GetGeminiModels() []*ModelInfo {
	return []*ModelInfo{
		{
			ID:                         "gemini-2.5-flash",
			Object:                     "model",
			Created:                    time.Now().Unix(),
			OwnedBy:                    "google",
			Type:                       "gemini",
			Name:                       "models/gemini-2.5-flash",
			Version:                    "001",
			DisplayName:                "Gemini 2.5 Flash",
			Description:                "Stable version of Gemini 2.5 Flash, our mid-size multimodal model that supports up to 1 million tokens, released in June of 2025.",
			InputTokenLimit:            1048576,
			OutputTokenLimit:           65536,
			SupportedGenerationMethods: []string{"generateContent", "countTokens", "createCachedContent", "batchGenerateContent"},
		},
		{
			ID:                         "gemini-2.5-pro",
			Object:                     "model",
			Created:                    time.Now().Unix(),
			OwnedBy:                    "google",
			Type:                       "gemini",
			Name:                       "models/gemini-2.5-pro",
			Version:                    "2.5",
			DisplayName:                "Gemini 2.5 Pro",
			Description:                "Stable release (June 17th, 2025) of Gemini 2.5 Pro",
			InputTokenLimit:            1048576,
			OutputTokenLimit:           65536,
			SupportedGenerationMethods: []string{"generateContent", "countTokens", "createCachedContent", "batchGenerateContent"},
		},
		{
			ID:                         "gemini-2.5-flash-lite",
			Object:                     "model",
			Created:                    time.Now().Unix(),
			OwnedBy:                    "google",
			Type:                       "gemini",
			Name:                       "models/gemini-2.5-flash-lite",
			Version:                    "2.5",
			DisplayName:                "Gemini 2.5 Flash Lite",
			Description:                "Stable release (June 17th, 2025) of Gemini 2.5 Flash Lite",
			InputTokenLimit:            1048576,
			OutputTokenLimit:           65536,
			SupportedGenerationMethods: []string{"generateContent", "countTokens", "createCachedContent", "batchGenerateContent"},
		},
		{
			ID:                         "gemini-2.5-flash-image-preview",
			Object:                     "model",
			Created:                    time.Now().Unix(),
			OwnedBy:                    "google",
			Type:                       "gemini",
			Name:                       "models/gemini-2.5-flash-image-preview",
			Version:                    "2.5",
			DisplayName:                "Gemini 2.5 Flash Image Preview",
			Description:                "State-of-the-art image generation and editing model.",
			InputTokenLimit:            1048576,
			OutputTokenLimit:           8192,
			SupportedGenerationMethods: []string{"generateContent", "countTokens", "createCachedContent", "batchGenerateContent"},
		},
		{
			ID:                         "gemini-2.5-flash-image",
			Object:                     "model",
			Created:                    time.Now().Unix(),
			OwnedBy:                    "google",
			Type:                       "gemini",
			Name:                       "models/gemini-2.5-flash-image",
			Version:                    "2.5",
			DisplayName:                "Gemini 2.5 Flash Image",
			Description:                "State-of-the-art image generation and editing model.",
			InputTokenLimit:            1048576,
			OutputTokenLimit:           8192,
			SupportedGenerationMethods: []string{"generateContent", "countTokens", "createCachedContent", "batchGenerateContent"},
		},
	}
}

// GetGeminiCLIModels returns the standard Gemini model definitions
func GetGeminiCLIModels() []*ModelInfo {
	return []*ModelInfo{
		{
			ID:                         "gemini-2.5-flash",
			Object:                     "model",
			Created:                    time.Now().Unix(),
			OwnedBy:                    "google",
			Type:                       "gemini",
			Name:                       "models/gemini-2.5-flash",
			Version:                    "001",
			DisplayName:                "Gemini 2.5 Flash",
			Description:                "Stable version of Gemini 2.5 Flash, our mid-size multimodal model that supports up to 1 million tokens, released in June of 2025.",
			InputTokenLimit:            1048576,
			OutputTokenLimit:           65536,
			SupportedGenerationMethods: []string{"generateContent", "countTokens", "createCachedContent", "batchGenerateContent"},
		},
		{
			ID:                         "gemini-2.5-pro",
			Object:                     "model",
			Created:                    time.Now().Unix(),
			OwnedBy:                    "google",
			Type:                       "gemini",
			Name:                       "models/gemini-2.5-pro",
			Version:                    "2.5",
			DisplayName:                "Gemini 2.5 Pro",
			Description:                "Stable release (June 17th, 2025) of Gemini 2.5 Pro",
			InputTokenLimit:            1048576,
			OutputTokenLimit:           65536,
			SupportedGenerationMethods: []string{"generateContent", "countTokens", "createCachedContent", "batchGenerateContent"},
		},
		{
			ID:                         "gemini-2.5-flash-lite",
			Object:                     "model",
			Created:                    time.Now().Unix(),
			OwnedBy:                    "google",
			Type:                       "gemini",
			Name:                       "models/gemini-2.5-flash-lite",
			Version:                    "2.5",
			DisplayName:                "Gemini 2.5 Flash Lite",
			Description:                "Our smallest and most cost effective model, built for at scale usage.",
			InputTokenLimit:            1048576,
			OutputTokenLimit:           65536,
			SupportedGenerationMethods: []string{"generateContent", "countTokens", "createCachedContent", "batchGenerateContent"},
		},
		{
			ID:                         "gemini-2.5-flash-image-preview",
			Object:                     "model",
			Created:                    time.Now().Unix(),
			OwnedBy:                    "google",
			Type:                       "gemini",
			Name:                       "models/gemini-2.5-flash-image-preview",
			Version:                    "2.5",
			DisplayName:                "Gemini 2.5 Flash Image Preview",
			Description:                "State-of-the-art image generation and editing model.",
			InputTokenLimit:            1048576,
			OutputTokenLimit:           8192,
			SupportedGenerationMethods: []string{"generateContent", "countTokens", "createCachedContent", "batchGenerateContent"},
		},
		{
			ID:                         "gemini-2.5-flash-image",
			Object:                     "model",
			Created:                    time.Now().Unix(),
			OwnedBy:                    "google",
			Type:                       "gemini",
			Name:                       "models/gemini-2.5-flash-image",
			Version:                    "2.5",
			DisplayName:                "Gemini 2.5 Flash Image",
			Description:                "State-of-the-art image generation and editing model.",
			InputTokenLimit:            1048576,
			OutputTokenLimit:           8192,
			SupportedGenerationMethods: []string{"generateContent", "countTokens", "createCachedContent", "batchGenerateContent"},
		},
	}
}

// GetOpenAIModels returns the standard OpenAI model definitions
func GetOpenAIModels() []*ModelInfo {
	return []*ModelInfo{
		{
			ID:                  "gpt-5",
			Object:              "model",
			Created:             time.Now().Unix(),
			OwnedBy:             "openai",
			Type:                "openai",
			Version:             "gpt-5-2025-08-07",
			DisplayName:         "GPT 5",
			Description:         "Stable version of GPT 5, The best model for coding and agentic tasks across domains.",
			ContextLength:       400000,
			MaxCompletionTokens: 128000,
			SupportedParameters: []string{"tools"},
		},
		{
			ID:                  "gpt-5-minimal",
			Object:              "model",
			Created:             time.Now().Unix(),
			OwnedBy:             "openai",
			Type:                "openai",
			Version:             "gpt-5-2025-08-07",
			DisplayName:         "GPT 5 Minimal",
			Description:         "Stable version of GPT 5, The best model for coding and agentic tasks across domains.",
			ContextLength:       400000,
			MaxCompletionTokens: 128000,
			SupportedParameters: []string{"tools"},
		},
		{
			ID:                  "gpt-5-low",
			Object:              "model",
			Created:             time.Now().Unix(),
			OwnedBy:             "openai",
			Type:                "openai",
			Version:             "gpt-5-2025-08-07",
			DisplayName:         "GPT 5 Low",
			Description:         "Stable version of GPT 5, The best model for coding and agentic tasks across domains.",
			ContextLength:       400000,
			MaxCompletionTokens: 128000,
			SupportedParameters: []string{"tools"},
		},
		{
			ID:                  "gpt-5-medium",
			Object:              "model",
			Created:             time.Now().Unix(),
			OwnedBy:             "openai",
			Type:                "openai",
			Version:             "gpt-5-2025-08-07",
			DisplayName:         "GPT 5 Medium",
			Description:         "Stable version of GPT 5, The best model for coding and agentic tasks across domains.",
			ContextLength:       400000,
			MaxCompletionTokens: 128000,
			SupportedParameters: []string{"tools"},
		},
		{
			ID:                  "gpt-5-high",
			Object:              "model",
			Created:             time.Now().Unix(),
			OwnedBy:             "openai",
			Type:                "openai",
			Version:             "gpt-5-2025-08-07",
			DisplayName:         "GPT 5 High",
			Description:         "Stable version of GPT 5, The best model for coding and agentic tasks across domains.",
			ContextLength:       400000,
			MaxCompletionTokens: 128000,
			SupportedParameters: []string{"tools"},
		},
		{
			ID:                  "gpt-5-codex",
			Object:              "model",
			Created:             time.Now().Unix(),
			OwnedBy:             "openai",
			Type:                "openai",
			Version:             "gpt-5-2025-09-15",
			DisplayName:         "GPT 5 Codex",
			Description:         "Stable version of GPT 5 Codex, The best model for coding and agentic tasks across domains.",
			ContextLength:       400000,
			MaxCompletionTokens: 128000,
			SupportedParameters: []string{"tools"},
		},
		{
			ID:                  "gpt-5-codex-low",
			Object:              "model",
			Created:             time.Now().Unix(),
			OwnedBy:             "openai",
			Type:                "openai",
			Version:             "gpt-5-2025-09-15",
			DisplayName:         "GPT 5 Codex Low",
			Description:         "Stable version of GPT 5 Codex, The best model for coding and agentic tasks across domains.",
			ContextLength:       400000,
			MaxCompletionTokens: 128000,
			SupportedParameters: []string{"tools"},
		},
		{
			ID:                  "gpt-5-codex-medium",
			Object:              "model",
			Created:             time.Now().Unix(),
			OwnedBy:             "openai",
			Type:                "openai",
			Version:             "gpt-5-2025-09-15",
			DisplayName:         "GPT 5 Codex Medium",
			Description:         "Stable version of GPT 5 Codex, The best model for coding and agentic tasks across domains.",
			ContextLength:       400000,
			MaxCompletionTokens: 128000,
			SupportedParameters: []string{"tools"},
		},
		{
			ID:                  "gpt-5-codex-high",
			Object:              "model",
			Created:             time.Now().Unix(),
			OwnedBy:             "openai",
			Type:                "openai",
			Version:             "gpt-5-2025-09-15",
			DisplayName:         "GPT 5 Codex High",
			Description:         "Stable version of GPT 5 Codex, The best model for coding and agentic tasks across domains.",
			ContextLength:       400000,
			MaxCompletionTokens: 128000,
			SupportedParameters: []string{"tools"},
		},
		{
			ID:                  "codex-mini-latest",
			Object:              "model",
			Created:             time.Now().Unix(),
			OwnedBy:             "openai",
			Type:                "openai",
			Version:             "1.0",
			DisplayName:         "Codex Mini",
			Description:         "Lightweight code generation model",
			ContextLength:       4096,
			MaxCompletionTokens: 2048,
			SupportedParameters: []string{"temperature", "max_tokens", "stream", "stop"},
		},
	}
}

// GetQwenModels returns the standard Qwen model definitions
func GetQwenModels() []*ModelInfo {
	return []*ModelInfo{
		{
			ID:                  "qwen3-coder-plus",
			Object:              "model",
			Created:             time.Now().Unix(),
			OwnedBy:             "qwen",
			Type:                "qwen",
			Version:             "3.0",
			DisplayName:         "Qwen3 Coder Plus",
			Description:         "Advanced code generation and understanding model",
			ContextLength:       32768,
			MaxCompletionTokens: 8192,
			SupportedParameters: []string{"temperature", "top_p", "max_tokens", "stream", "stop"},
		},
		{
			ID:                  "qwen3-coder-flash",
			Object:              "model",
			Created:             time.Now().Unix(),
			OwnedBy:             "qwen",
			Type:                "qwen",
			Version:             "3.0",
			DisplayName:         "Qwen3 Coder Flash",
			Description:         "Fast code generation model",
			ContextLength:       8192,
			MaxCompletionTokens: 2048,
			SupportedParameters: []string{"temperature", "top_p", "max_tokens", "stream", "stop"},
		},
	}
}

// GetIFlowModels returns supported models for iFlow OAuth accounts.

func GetIFlowModels() []*ModelInfo {
	created := time.Now().Unix()
	entries := []struct {
		ID          string
		DisplayName string
		Description string
	}{
		{ID: "tstars2.0", DisplayName: "TStars-2.0", Description: "iFlow TStars-2.0 multimodal assistant"},
		{ID: "qwen3-coder-plus", DisplayName: "Qwen3-Coder-Plus", Description: "Qwen3 Coder Plus code generation"},
		{ID: "qwen3-coder", DisplayName: "Qwen3-Coder-480B-A35B", Description: "Qwen3 Coder 480B A35B"},
		{ID: "qwen3-max", DisplayName: "Qwen3-Max", Description: "Qwen3 flagship model"},
		{ID: "qwen3-vl-plus", DisplayName: "Qwen3-VL-Plus", Description: "Qwen3 multimodal vision-language"},
		{ID: "qwen3-max-preview", DisplayName: "Qwen3-Max-Preview", Description: "Qwen3 Max preview build"},
		{ID: "kimi-k2-0905", DisplayName: "Kimi-K2-Instruct-0905", Description: "Moonshot Kimi K2 instruct 0905"},
		{ID: "glm-4.5", DisplayName: "GLM-4.5", Description: "Zhipu GLM 4.5 general model"},
		{ID: "glm-4.6", DisplayName: "GLM-4.6", Description: "Zhipu GLM 4.6 general model"},
		{ID: "kimi-k2", DisplayName: "Kimi-K2", Description: "Moonshot Kimi K2 general model"},
		{ID: "deepseek-v3.2", DisplayName: "DeepSeek-V3.2-Exp", Description: "DeepSeek V3.2 experimental"},
		{ID: "deepseek-v3.1", DisplayName: "DeepSeek-V3.1-Terminus", Description: "DeepSeek V3.1 Terminus"},
		{ID: "deepseek-r1", DisplayName: "DeepSeek-R1", Description: "DeepSeek reasoning model R1"},
		{ID: "deepseek-v3", DisplayName: "DeepSeek-V3-671B", Description: "DeepSeek V3 671B"},
		{ID: "qwen3-32b", DisplayName: "Qwen3-32B", Description: "Qwen3 32B"},
		{ID: "qwen3-235b-a22b-thinking-2507", DisplayName: "Qwen3-235B-A22B-Thinking", Description: "Qwen3 235B A22B Thinking (2507)"},
		{ID: "qwen3-235b-a22b-instruct", DisplayName: "Qwen3-235B-A22B-Instruct", Description: "Qwen3 235B A22B Instruct"},
		{ID: "qwen3-235b", DisplayName: "Qwen3-235B-A22B", Description: "Qwen3 235B A22B"},
	}
	models := make([]*ModelInfo, 0, len(entries))
	for _, entry := range entries {
		models = append(models, &ModelInfo{
			ID:          entry.ID,
			Object:      "model",
			Created:     created,
			OwnedBy:     "iflow",
			Type:        "iflow",
			DisplayName: entry.DisplayName,
			Description: entry.Description,
		})
	}
	return models
}
