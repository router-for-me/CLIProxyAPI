package registry

// CommandCodeModelDef defines metadata for Command Code models.
type CommandCodeModelDef struct {
	ID            string
	Name          string
	ContextWindow int
	MaxTokens     int
}

// BuiltinCommandCodeModels lists the known supported open-weight models available under Command Code.
// Canonical model IDs are normalized to lowercase to match official `cmdc --list-models`.
var BuiltinCommandCodeModels = []CommandCodeModelDef{
	// DeepSeek
	{
		ID:            "deepseek/deepseek-v4-pro",
		Name:          "DeepSeek V4 Pro (Command Code)",
		ContextWindow: 1_000_000,
		MaxTokens:     131_072,
	},
	{
		ID:            "deepseek/deepseek-v4-flash",
		Name:          "DeepSeek V4 Flash (Command Code)",
		ContextWindow: 1_000_000,
		MaxTokens:     131_072,
	},
	// Moonshot Kimi
	{
		ID:            "moonshotai/kimi-k3",
		Name:          "Kimi K3 (Command Code)",
		ContextWindow: 1_000_000,
		MaxTokens:     65_536,
	},
	{
		ID:            "moonshotai/kimi-k2.7-code",
		Name:          "Kimi K2.7 Code (Command Code)",
		ContextWindow: 256_000,
		MaxTokens:     65_536,
	},
	{
		ID:            "moonshotai/kimi-k2.7-code-highspeed",
		Name:          "Kimi K2.7 Code HighSpeed (Command Code)",
		ContextWindow: 262_000,
		MaxTokens:     65_536,
	},
	{
		ID:            "moonshotai/kimi-k2.6",
		Name:          "Kimi K2.6 (Command Code)",
		ContextWindow: 256_000,
		MaxTokens:     65_536,
	},
	{
		ID:            "moonshotai/kimi-k2.5",
		Name:          "Kimi K2.5 (Command Code)",
		ContextWindow: 256_000,
		MaxTokens:     65_536,
	},
	// Zhipu GLM
	{
		ID:            "zai-org/glm-5.3",
		Name:          "GLM 5.3 (Command Code)",
		ContextWindow: 1_000_000,
		MaxTokens:     131_072,
	},
	{
		ID:            "zai-org/glm-5.2",
		Name:          "GLM 5.2 (Command Code)",
		ContextWindow: 1_000_000,
		MaxTokens:     131_072,
	},
	{
		ID:            "zai-org/glm-5.2-fast",
		Name:          "GLM 5.2 Fast (Command Code)",
		ContextWindow: 1_000_000,
		MaxTokens:     65_536,
	},
	{
		ID:            "zai-org/glm-5.1",
		Name:          "GLM 5.1 (Command Code)",
		ContextWindow: 200_000,
		MaxTokens:     32_768,
	},
	{
		ID:            "zai-org/glm-5",
		Name:          "GLM 5 (Command Code)",
		ContextWindow: 200_000,
		MaxTokens:     32_768,
	},
	// MiniMax
	{
		ID:            "minimaxai/minimax-m3",
		Name:          "MiniMax M3 (Command Code)",
		ContextWindow: 1_000_000,
		MaxTokens:     131_072,
	},
	{
		ID:            "minimaxai/minimax-m2.7",
		Name:          "MiniMax M2.7 (Command Code)",
		ContextWindow: 200_000,
		MaxTokens:     65_536,
	},
	{
		ID:            "minimaxai/minimax-m2.5",
		Name:          "MiniMax M2.5 (Command Code)",
		ContextWindow: 200_000,
		MaxTokens:     65_536,
	},
	// Xiaomi MiMo
	{
		ID:            "xiaomi/mimo-v2.5-pro",
		Name:          "MiMo V2.5 Pro (Command Code)",
		ContextWindow: 1_000_000,
		MaxTokens:     131_072,
	},
	{
		ID:            "xiaomi/mimo-v2.5",
		Name:          "MiMo V2.5 (Command Code)",
		ContextWindow: 200_000,
		MaxTokens:     65_536,
	},
	// Qwen
	{
		ID:            "qwen/qwen3.8-max",
		Name:          "Qwen 3.8 Max (Command Code)",
		ContextWindow: 1_000_000,
		MaxTokens:     131_072,
	},
	{
		ID:            "qwen/qwen3.7-max",
		Name:          "Qwen 3.7 Max (Command Code)",
		ContextWindow: 1_000_000,
		MaxTokens:     131_072,
	},
	{
		ID:            "qwen/qwen3.7-plus",
		Name:          "Qwen 3.7 Plus (Command Code)",
		ContextWindow: 1_000_000,
		MaxTokens:     65_536,
	},
	{
		ID:            "qwen/qwen3.7-flash",
		Name:          "Qwen 3.7 Flash (Command Code)",
		ContextWindow: 1_000_000,
		MaxTokens:     65_536,
	},
	{
		ID:            "qwen/qwen3.6-max-preview",
		Name:          "Qwen 3.6 Max Preview (Command Code)",
		ContextWindow: 1_000_000,
		MaxTokens:     131_072,
	},
	{
		ID:            "qwen/qwen3.6-plus",
		Name:          "Qwen 3.6 Plus (Command Code)",
		ContextWindow: 1_000_000,
		MaxTokens:     65_536,
	},
	// StepFun
	{
		ID:            "stepfun/step-3.7-flash",
		Name:          "Step 3.7 Flash (Command Code)",
		ContextWindow: 1_000_000,
		MaxTokens:     65_536,
	},
	{
		ID:            "stepfun/step-3.5-flash",
		Name:          "Step 3.5 Flash (Command Code)",
		ContextWindow: 256_000,
		MaxTokens:     65_536,
	},
	// Tencent
	{
		ID:            "tencent/hy3-paid",
		Name:          "Hunyuan 3 Paid (Command Code)",
		ContextWindow: 256_000,
		MaxTokens:     65_536,
	},
	// Nvidia
	{
		ID:            "nvidia/nemotron-3-ultra-550b-a55b",
		Name:          "Nemotron 3 Ultra 550B (Command Code)",
		ContextWindow: 1_000_000,
		MaxTokens:     131_072,
	},
	// Thinking Machines
	{
		ID:            "thinkingmachines/inkling",
		Name:          "Inkling (Command Code)",
		ContextWindow: 200_000,
		MaxTokens:     32_768,
	},
	{
		ID:            "thinkingmachines/inkling-small",
		Name:          "Inkling Small (Command Code)",
		ContextWindow: 128_000,
		MaxTokens:     16_384,
	},
	// Poolside
	{
		ID:            "poolside/laguna-s-2.1-free",
		Name:          "Laguna S 2.1 Free (Command Code)",
		ContextWindow: 1_000_000,
		MaxTokens:     131_072,
	},
}

// GetCommandCodeModels returns standard Command Code model definitions.
func GetCommandCodeModels() []*ModelInfo {
	infos := make([]*ModelInfo, 0, len(BuiltinCommandCodeModels))
	for _, m := range BuiltinCommandCodeModels {
		infos = append(infos, &ModelInfo{
			ID:                  m.ID,
			Object:              "model",
			OwnedBy:             "commandcode",
			Type:                "commandcode",
			DisplayName:         m.Name,
			ContextLength:       m.ContextWindow,
			MaxCompletionTokens: m.MaxTokens,
			SupportedInputModalities: []string{
				"text",
				"image",
			},
			SupportedOutputModalities: []string{
				"text",
			},
		})
	}
	return cloneModelInfos(infos)
}
