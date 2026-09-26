package registry

// CommandCodeModelDef defines metadata for Command Code models.
type CommandCodeModelDef struct {
	ID            string
	Name          string
	ContextWindow int
	MaxTokens     int
	// OfficialID is the gateway-side spelling when it differs from the
	// lowercase canonical ID (camel-case ids like "Qwen/Qwen3.8-Flash").
	// Empty means the lowercase ID already matches the official spelling.
	OfficialID string
}

// BuiltinCommandCodeModels lists the known supported open-weight models available under Command Code.
// Canonical model IDs are normalized to lowercase to match official `cmdc --list-models`.
// OfficialID carries the gateway-side spelling for camel-case ids: the
// /alpha/generate upstream matches ids strictly against the official catalog
// spelling, so lowercase-only builtin entries can never route those models.
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
		OfficialID:    "moonshotai/Kimi-K3",
		Name:          "Kimi K3 (Command Code)",
		ContextWindow: 1_000_000,
		MaxTokens:     65_536,
	},
	{
		ID:            "moonshotai/kimi-k2.7-code",
		OfficialID:    "moonshotai/Kimi-K2.7-Code",
		Name:          "Kimi K2.7 Code (Command Code)",
		ContextWindow: 256_000,
		MaxTokens:     65_536,
	},
	{
		ID:            "moonshotai/kimi-k2.7-code-highspeed",
		OfficialID:    "moonshotai/Kimi-K2.7-Code-Highspeed",
		Name:          "Kimi K2.7 Code HighSpeed (Command Code)",
		ContextWindow: 262_000,
		MaxTokens:     65_536,
	},
	{
		ID:            "moonshotai/kimi-k2.6",
		OfficialID:    "moonshotai/Kimi-K2.6",
		Name:          "Kimi K2.6 (Command Code)",
		ContextWindow: 256_000,
		MaxTokens:     65_536,
	},
	{
		ID:            "moonshotai/kimi-k2.5",
		OfficialID:    "moonshotai/Kimi-K2.5",
		Name:          "Kimi K2.5 (Command Code)",
		ContextWindow: 256_000,
		MaxTokens:     65_536,
	},
	// Zhipu GLM
	{
		ID:            "zai-org/glm-5.3",
		OfficialID:    "zai-org/GLM-5.3",
		Name:          "GLM 5.3 (Command Code)",
		ContextWindow: 1_000_000,
		MaxTokens:     131_072,
	},
	{
		ID:            "zai-org/glm-5.2",
		OfficialID:    "zai-org/GLM-5.2",
		Name:          "GLM 5.2 (Command Code)",
		ContextWindow: 1_000_000,
		MaxTokens:     131_072,
	},
	{
		ID:            "zai-org/glm-5.2-fast",
		OfficialID:    "zai-org/GLM-5.2-Fast",
		Name:          "GLM 5.2 Fast (Command Code)",
		ContextWindow: 1_000_000,
		MaxTokens:     65_536,
	},
	{
		ID:            "zai-org/glm-5.1",
		OfficialID:    "zai-org/GLM-5.1",
		Name:          "GLM 5.1 (Command Code)",
		ContextWindow: 200_000,
		MaxTokens:     32_768,
	},
	{
		ID:            "zai-org/glm-5",
		OfficialID:    "zai-org/GLM-5",
		Name:          "GLM 5 (Command Code)",
		ContextWindow: 200_000,
		MaxTokens:     32_768,
	},
	// MiniMax
	{
		ID:            "minimaxai/minimax-m3",
		OfficialID:    "MiniMaxAI/MiniMax-M3",
		Name:          "MiniMax M3 (Command Code)",
		ContextWindow: 1_000_000,
		MaxTokens:     131_072,
	},
	{
		ID:            "minimaxai/minimax-m2.7",
		OfficialID:    "MiniMaxAI/MiniMax-M2.7",
		Name:          "MiniMax M2.7 (Command Code)",
		ContextWindow: 200_000,
		MaxTokens:     65_536,
	},
	{
		ID:            "minimaxai/minimax-m2.5",
		OfficialID:    "MiniMaxAI/MiniMax-M2.5",
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
		OfficialID:    "Qwen/Qwen3.8-Max",
		Name:          "Qwen 3.8 Max (Command Code)",
		ContextWindow: 1_000_000,
		MaxTokens:     131_072,
	},
	{
		ID:            "qwen/qwen3.7-max",
		OfficialID:    "Qwen/Qwen3.7-Max",
		Name:          "Qwen 3.7 Max (Command Code)",
		ContextWindow: 1_000_000,
		MaxTokens:     131_072,
	},
	{
		ID:            "qwen/qwen3.7-plus",
		OfficialID:    "Qwen/Qwen3.7-Plus",
		Name:          "Qwen 3.7 Plus (Command Code)",
		ContextWindow: 1_000_000,
		MaxTokens:     65_536,
	},
	{
		ID:            "qwen/qwen3.7-flash",
		OfficialID:    "Qwen/Qwen3.7-Flash",
		Name:          "Qwen 3.7 Flash (Command Code)",
		ContextWindow: 1_000_000,
		MaxTokens:     65_536,
	},
	{
		ID:            "qwen/qwen3.6-max-preview",
		OfficialID:    "Qwen/Qwen3.6-Max-Preview",
		Name:          "Qwen 3.6 Max Preview (Command Code)",
		ContextWindow: 1_000_000,
		MaxTokens:     131_072,
	},
	{
		ID:            "qwen/qwen3.6-plus",
		OfficialID:    "Qwen/Qwen3.6-Plus",
		Name:          "Qwen 3.6 Plus (Command Code)",
		ContextWindow: 1_000_000,
		MaxTokens:     65_536,
	},
	// StepFun
	{
		ID:            "stepfun/step-3.7-flash",
		OfficialID:    "stepfun/Step-3.7-Flash",
		Name:          "Step 3.7 Flash (Command Code)",
		ContextWindow: 1_000_000,
		MaxTokens:     65_536,
	},
	{
		ID:            "stepfun/step-3.5-flash",
		OfficialID:    "stepfun/Step-3.5-Flash",
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

// GetCommandCodeModels returns Command Code model definitions.
//
// It prefers the dynamic catalog discovered from the installed cmdc CLI
// (last-known-good). When no CLI catalog has been loaded yet, it falls back to
// the built-in static catalog so startup is never empty. The static catalog is
// a bootstrap/fallback only — it is never an allowlist, because the CLI catalog
// can contain newer models than the built-in list.
func GetCommandCodeModels() []*ModelInfo {
	if dynamic, ok := getCommandCodeCatalog(); ok {
		return dynamic
	}
	return cloneModelInfos(staticCommandCodeModels())
}

// staticCommandCodeModels builds the built-in bootstrap catalog.
func staticCommandCodeModels() []*ModelInfo {
	infos := make([]*ModelInfo, 0, len(BuiltinCommandCodeModels))
	for _, m := range BuiltinCommandCodeModels {
		registerCommandCodeOfficialSpelling(m.OfficialID)
		registerCommandCodeOfficialSpelling(m.ID)
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
	return infos
}
