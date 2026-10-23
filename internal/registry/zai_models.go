package registry

var staticZAIModels = []*ModelInfo{
	{ID: "glm-5.3", Type: "openai-compatibility", OwnedBy: "zai", DisplayName: "GLM-5.3", Thinking: &ThinkingSupport{Levels: []string{"low", "high", "max"}}},
	{ID: "glm-5.3-flash", Type: "openai-compatibility", OwnedBy: "zai", DisplayName: "GLM-5.3 Flash", Thinking: &ThinkingSupport{Levels: []string{"low", "high", "max"}}},
	{ID: "glm-5.2", Type: "openai-compatibility", OwnedBy: "zai", DisplayName: "GLM-5.2", Thinking: &ThinkingSupport{Levels: []string{"low", "high", "max"}}},
	{ID: "glm-5.1", Type: "openai-compatibility", OwnedBy: "zai", DisplayName: "GLM-5.1", Thinking: &ThinkingSupport{Levels: []string{"low", "high", "max"}}},
	{ID: "glm-5", Type: "openai-compatibility", OwnedBy: "zai", DisplayName: "GLM-5", Thinking: &ThinkingSupport{Levels: []string{"low", "high", "max"}}},
	{ID: "glm-4.7", Type: "openai-compatibility", OwnedBy: "zai", DisplayName: "GLM-4.7", Thinking: &ThinkingSupport{Levels: []string{"low", "high", "max"}}},
	{ID: "glm-4.6", Type: "openai-compatibility", OwnedBy: "zai", DisplayName: "GLM-4.6", Thinking: &ThinkingSupport{Levels: []string{"low", "high", "max"}}},
	{ID: "glm-4.5", Type: "openai-compatibility", OwnedBy: "zai", DisplayName: "GLM-4.5", Thinking: &ThinkingSupport{Levels: []string{"low", "high", "max"}}},
}

// GetZAIModels returns the built-in Z.ai Coding Plan model catalog.
func GetZAIModels() []*ModelInfo { return cloneModelInfos(staticZAIModels) }
