package registry

import (
	"strings"
	"sync"
)

type openCodeModelsStore struct {
	mu     sync.RWMutex
	models []*ModelInfo
}

var openCodeCatalogStore = &openCodeModelsStore{}

var staticOpenCodeModels = []*ModelInfo{
	// --- Free Tier Models (1M Context) ---
	{
		ID:                  "step-5-preview-free",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "stepfun",
		Type:                "opencode",
		DisplayName:         "Step 5 Preview (Free)",
		ContextLength:       1048576,
		MaxCompletionTokens: 64000,
		InputTokenLimit:     1048576,
		OutputTokenLimit:    64000,
		Thinking: &ThinkingSupport{
			Levels: []string{"low", "medium", "high"},
		},
	},
	{
		ID:                  "longcat-2.5-preview-free",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "meituan",
		Type:                "opencode",
		DisplayName:         "LongCat 2.5 Preview (Free)",
		ContextLength:       1048576,
		MaxCompletionTokens: 64000,
		InputTokenLimit:     1048576,
		OutputTokenLimit:    64000,
		Thinking: &ThinkingSupport{
			Levels: []string{"low", "medium", "high"},
		},
	},
	{
		ID:                  "mimo-v2.6-flash-free",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "xiaomi",
		Type:                "opencode",
		DisplayName:         "MiMo v2.6 Flash (Free)",
		ContextLength:       1048576,
		MaxCompletionTokens: 64000,
		InputTokenLimit:     1048576,
		OutputTokenLimit:    64000,
	},
	{
		ID:                  "ling-3.1-flash-free",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "antgroup",
		Type:                "opencode",
		DisplayName:         "Ling 3.1 Flash (Free)",
		ContextLength:       1048576,
		MaxCompletionTokens: 64000,
		InputTokenLimit:     1048576,
		OutputTokenLimit:    64000,
	},
	{
		ID:                  "ling-3.0-flash-fin-free",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "antgroup",
		Type:                "opencode",
		DisplayName:         "Ling 3.0 Flash Fin (Free)",
		ContextLength:       1048576,
		MaxCompletionTokens: 64000,
		InputTokenLimit:     1048576,
		OutputTokenLimit:    64000,
	},
	{
		ID:                  "deepseek-v4-flash-free",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "deepseek",
		Type:                "opencode",
		DisplayName:         "DeepSeek V4 Flash (Free)",
		ContextLength:       1048576,
		MaxCompletionTokens: 64000,
		InputTokenLimit:     1048576,
		OutputTokenLimit:    64000,
		Thinking: &ThinkingSupport{
			Levels: []string{"low", "medium", "high", "max"},
		},
	},
	{
		ID:                  "nemotron-3-ultra-free",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "nvidia",
		Type:                "opencode",
		DisplayName:         "Nemotron 3 Ultra (Free)",
		ContextLength:       1048576,
		MaxCompletionTokens: 64000,
		InputTokenLimit:     1048576,
		OutputTokenLimit:    64000,
	},
	{
		ID:                  "nemotron-3.5-lightning-free",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "nvidia",
		Type:                "opencode",
		DisplayName:         "Nemotron 3.5 Lightning (Free)",
		ContextLength:       1048576,
		MaxCompletionTokens: 64000,
		InputTokenLimit:     1048576,
		OutputTokenLimit:    64000,
	},
	{
		ID:                  "space-bunny-free",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "opencode",
		Type:                "opencode",
		DisplayName:         "Space Bunny (Free)",
		ContextLength:       1048576,
		MaxCompletionTokens: 64000,
		InputTokenLimit:     1048576,
		OutputTokenLimit:    64000,
	},
	{
		ID:                  "exo-free",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "opencode",
		Type:                "opencode",
		DisplayName:         "Exo (Free)",
		ContextLength:       1048576,
		MaxCompletionTokens: 64000,
		InputTokenLimit:     1048576,
		OutputTokenLimit:    64000,
	},
	{
		ID:                  "jev-1.13-free",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "opencode",
		Type:                "opencode",
		DisplayName:         "Jev 1.13 (Free)",
		ContextLength:       1048576,
		MaxCompletionTokens: 64000,
		InputTokenLimit:     1048576,
		OutputTokenLimit:    64000,
	},
	{
		ID:                  "muse-spark-1.3-contributor-free",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "meta",
		Type:                "opencode",
		DisplayName:         "Muse Spark 1.3 Contributor (Free)",
		ContextLength:       1048576,
		MaxCompletionTokens: 64000,
		InputTokenLimit:     1048576,
		OutputTokenLimit:    64000,
	},
	{
		ID:                  "muse-spark-1.2-contributor-free",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "meta",
		Type:                "opencode",
		DisplayName:         "Muse Spark 1.2 Contributor (Free)",
		ContextLength:       1048576,
		MaxCompletionTokens: 64000,
		InputTokenLimit:     1048576,
		OutputTokenLimit:    64000,
	},

	// --- Zen Standard / Flagship Models ---
	{
		ID:                  "claude-fable-5-1",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "anthropic",
		Type:                "opencode",
		DisplayName:         "Claude Fable 5.1",
		ContextLength:       1000000,
		MaxCompletionTokens: 64000,
		InputTokenLimit:     1000000,
		OutputTokenLimit:    64000,
		Thinking: &ThinkingSupport{
			Levels: []string{"low", "medium", "high", "xhigh", "max"},
		},
	},
	{
		ID:                  "claude-opus-5-5",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "anthropic",
		Type:                "opencode",
		DisplayName:         "Claude Opus 5.5",
		ContextLength:       1000000,
		MaxCompletionTokens: 64000,
		InputTokenLimit:     1000000,
		OutputTokenLimit:    64000,
		Thinking: &ThinkingSupport{
			Levels: []string{"low", "medium", "high", "xhigh", "max"},
		},
	},
	{
		ID:                  "claude-sonnet-5-5",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "anthropic",
		Type:                "opencode",
		DisplayName:         "Claude Sonnet 5.5",
		ContextLength:       1000000,
		MaxCompletionTokens: 64000,
		InputTokenLimit:     1000000,
		OutputTokenLimit:    64000,
		Thinking: &ThinkingSupport{
			Levels: []string{"low", "medium", "high", "xhigh", "max"},
		},
	},
	{
		ID:                  "claude-haiku-5-5",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "anthropic",
		Type:                "opencode",
		DisplayName:         "Claude Haiku 5.5",
		ContextLength:       500000,
		MaxCompletionTokens: 64000,
		InputTokenLimit:     500000,
		OutputTokenLimit:    64000,
	},
	{
		ID:                  "gpt-6-astra",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "openai",
		Type:                "opencode",
		DisplayName:         "GPT-6 Astra",
		ContextLength:       1000000,
		MaxCompletionTokens: 64000,
		InputTokenLimit:     1000000,
		OutputTokenLimit:    64000,
		Thinking: &ThinkingSupport{
			Levels: []string{"low", "medium", "high", "xhigh", "max"},
		},
	},
	{
		ID:                  "gpt-6.1-sol",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "openai",
		Type:                "opencode",
		DisplayName:         "GPT-6.1 Sol",
		ContextLength:       1000000,
		MaxCompletionTokens: 128000,
		InputTokenLimit:     1000000,
		OutputTokenLimit:    128000,
		Thinking: &ThinkingSupport{
			Levels: []string{"none", "low", "medium", "high", "xhigh", "max"},
		},
	},
	{
		ID:                  "gpt-5.6-sol",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "openai",
		Type:                "opencode",
		DisplayName:         "GPT-5.6 Sol",
		ContextLength:       1000000,
		MaxCompletionTokens: 128000,
		InputTokenLimit:     1000000,
		OutputTokenLimit:    128000,
		Thinking: &ThinkingSupport{
			Levels: []string{"none", "low", "medium", "high", "xhigh", "max"},
		},
	},
	{
		ID:                  "gpt-5.5",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "openai",
		Type:                "opencode",
		DisplayName:         "GPT-5.5",
		ContextLength:       1000000,
		MaxCompletionTokens: 128000,
		InputTokenLimit:     1000000,
		OutputTokenLimit:    128000,
	},
	{
		ID:                  "gemini-3.8-flash",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "google",
		Type:                "opencode",
		DisplayName:         "Gemini 3.8 Flash",
		ContextLength:       1048576,
		MaxCompletionTokens: 65536,
		InputTokenLimit:     1048576,
		OutputTokenLimit:    65536,
		Thinking: &ThinkingSupport{
			Levels: []string{"low", "medium", "high"},
		},
	},
	{
		ID:                  "gemini-3.7-flash",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "google",
		Type:                "opencode",
		DisplayName:         "Gemini 3.7 Flash",
		ContextLength:       1048576,
		MaxCompletionTokens: 65536,
		InputTokenLimit:     1048576,
		OutputTokenLimit:    65536,
	},
	{
		ID:                  "grok-4.7",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "xai",
		Type:                "opencode",
		DisplayName:         "Grok 4.7",
		ContextLength:       1048576,
		MaxCompletionTokens: 131072,
		InputTokenLimit:     1048576,
		OutputTokenLimit:    131072,
		Thinking: &ThinkingSupport{
			Levels: []string{"low", "medium", "high", "xhigh"},
		},
	},
	{
		ID:                  "grok-4.6",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "xai",
		Type:                "opencode",
		DisplayName:         "Grok 4.6",
		ContextLength:       500000,
		MaxCompletionTokens: 131072,
		InputTokenLimit:     500000,
		OutputTokenLimit:    131072,
		Thinking: &ThinkingSupport{
			Levels: []string{"low", "medium", "high", "xhigh"},
		},
	},
	{
		ID:                  "deepseek-v4.1-flash",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "deepseek",
		Type:                "opencode",
		DisplayName:         "DeepSeek V4.1 Flash",
		ContextLength:       1048576,
		MaxCompletionTokens: 64000,
		InputTokenLimit:     1048576,
		OutputTokenLimit:    64000,
		Thinking: &ThinkingSupport{
			Levels: []string{"high", "max"},
		},
	},
	{
		ID:                  "deepseek-v4-pro",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "deepseek",
		Type:                "opencode",
		DisplayName:         "DeepSeek V4 Pro",
		ContextLength:       1048576,
		MaxCompletionTokens: 64000,
		InputTokenLimit:     1048576,
		OutputTokenLimit:    64000,
		Thinking: &ThinkingSupport{
			Levels: []string{"high", "max"},
		},
	},
	{
		ID:                  "deepseek-v4-flash",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "deepseek",
		Type:                "opencode",
		DisplayName:         "DeepSeek V4 Flash",
		ContextLength:       1048576,
		MaxCompletionTokens: 64000,
		InputTokenLimit:     1048576,
		OutputTokenLimit:    64000,
		Thinking: &ThinkingSupport{
			Levels: []string{"high", "max"},
		},
	},
	{
		ID:                  "glm-5.3-flash",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "zhipu",
		Type:                "opencode",
		DisplayName:         "GLM-5.3 Flash",
		ContextLength:       1000000,
		MaxCompletionTokens: 128000,
		InputTokenLimit:     1000000,
		OutputTokenLimit:    128000,
		Thinking: &ThinkingSupport{
			Levels: []string{"low", "high", "max"},
		},
	},
	{
		ID:                  "glm-5.3",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "zhipu",
		Type:                "opencode",
		DisplayName:         "GLM-5.3",
		ContextLength:       1048576,
		MaxCompletionTokens: 128000,
		InputTokenLimit:     1048576,
		OutputTokenLimit:    128000,
		Thinking: &ThinkingSupport{
			Levels: []string{"low", "high", "max"},
		},
	},
	{
		ID:                  "minimax-m3",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "minimax",
		Type:                "opencode",
		DisplayName:         "MiniMax M3",
		ContextLength:       1048576,
		MaxCompletionTokens: 64000,
		InputTokenLimit:     1048576,
		OutputTokenLimit:    64000,
	},
	{
		ID:                  "kimi-k3",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "moonshotai",
		Type:                "opencode",
		DisplayName:         "Kimi K3",
		ContextLength:       1048576,
		MaxCompletionTokens: 64000,
		InputTokenLimit:     1048576,
		OutputTokenLimit:    64000,
		Thinking: &ThinkingSupport{
			Levels: []string{"low", "medium", "high"},
		},
	},
	{
		ID:                  "qwen3.8-max",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "alibaba",
		Type:                "opencode",
		DisplayName:         "Qwen 3.8 Max",
		ContextLength:       1048576,
		MaxCompletionTokens: 64000,
		InputTokenLimit:     1048576,
		OutputTokenLimit:    64000,
	},
	{
		ID:                  "qwen3.8-flash",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "alibaba",
		Type:                "opencode",
		DisplayName:         "Qwen 3.8 Flash",
		ContextLength:       1048576,
		MaxCompletionTokens: 64000,
		InputTokenLimit:     1048576,
		OutputTokenLimit:    64000,
	},
	{
		ID:                  "qwen3.6-plus",
		Object:              "model",
		Created:             1791611341,
		OwnedBy:             "alibaba",
		Type:                "opencode",
		DisplayName:         "Qwen 3.6 Plus",
		ContextLength:       1048576,
		MaxCompletionTokens: 64000,
		InputTokenLimit:     1048576,
		OutputTokenLimit:    64000,
	},
}

// GetOpenCodeModels returns the complete OpenCode model catalog.
func GetOpenCodeModels() []*ModelInfo {
	openCodeCatalogStore.mu.RLock()
	models := openCodeCatalogStore.models
	openCodeCatalogStore.mu.RUnlock()

	if len(models) > 0 {
		return cloneModelInfos(models)
	}
	return cloneModelInfos(staticOpenCodeModels)
}

// LookupOpenCodeModel finds an OpenCode model by ID.
func LookupOpenCodeModel(modelID string) *ModelInfo {
	clean := strings.ToLower(strings.TrimSpace(modelID))
	clean = strings.TrimPrefix(clean, "opencode/")
	if clean == "" {
		return nil
	}

	for _, m := range GetOpenCodeModels() {
		if m == nil {
			continue
		}
		mClean := strings.ToLower(strings.TrimPrefix(m.ID, "opencode/"))
		if mClean == clean || strings.ToLower(m.ID) == clean {
			return cloneModelInfo(m)
		}
	}
	return nil
}

// IsOpenCodeModel checks if a model ID belongs to the OpenCode catalog.
func IsOpenCodeModel(modelID string) bool {
	return LookupOpenCodeModel(modelID) != nil
}
