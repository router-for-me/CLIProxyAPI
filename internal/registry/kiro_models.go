package registry

func GetKiroModels() []*ModelInfo {
	return cloneModelInfos(kiroStaticModels)
}

var kiroStaticModels = []*ModelInfo{
	{
		ID:                  "kiro-claude-sonnet-4-6",
		Object:              "model",
		Type:                "kiro",
		OwnedBy:             "amazon",
		DisplayName:         "Claude Sonnet 4.6 (Kiro)",
		ContextLength:       200000,
		MaxCompletionTokens: 64000,
		InputTokenLimit:     200000,
		OutputTokenLimit:    64000,
	},
	{
		ID:                  "kiro-claude-opus-4-6",
		Object:              "model",
		Type:                "kiro",
		OwnedBy:             "amazon",
		DisplayName:         "Claude Opus 4.6 (Kiro)",
		ContextLength:       200000,
		MaxCompletionTokens: 64000,
		InputTokenLimit:     200000,
		OutputTokenLimit:    64000,
	},
	{
		ID:                  "kiro-auto",
		Object:              "model",
		Type:                "kiro",
		OwnedBy:             "amazon",
		DisplayName:         "Kiro Auto",
		ContextLength:       200000,
		MaxCompletionTokens: 64000,
		InputTokenLimit:     200000,
		OutputTokenLimit:    64000,
	},
}
