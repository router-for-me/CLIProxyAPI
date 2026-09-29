package common

import "strings"

var (
	// OpenAIResponseOnlyModels is a list of models that are only available for OpenAI responses.
	OpenAIResponseOnlyModels = []string{
		"o3-pro",
		"o3-deep-research",
		"o4-mini-deep-research",
	}
	ImageGenerationModels = []string{
		"dall-e-3",
		"dall-e-2",
		"prefix:dall-e", // Deprecated upstream models; retained for compatible routes.
		"gpt-image-",
		"qwen-image",
		"z-image",
		"wan2.7-image-pro",
		"wan2.7-image",
		"wan2.6-image",
		"wan2.6-t2i",
		"wan2.5-t2i-preview",
		"wan2.2-t2i-flash",
		"wan2.2-t2i-plus",
		"wanx2.1-t2i-turbo",
		"wanx2.1-t2i-plus",
		"wanx2.0-t2i-turbo",
		"prefix:imagen-",
		"flux-",
		"flux.1-",
	}
	OpenAITextModels = []string{
		"gpt-",
		"o1",
		"o3",
		"o4",
		"chatgpt",
	}
)

func IsOpenAIResponseOnlyModel(modelName string) bool {
	for _, m := range OpenAIResponseOnlyModels {
		if strings.Contains(modelName, m) {
			return true
		}
	}
	return false
}

func IsImageGenerationModel(modelName string) bool {
	modelName = strings.ToLower(modelName)
	for _, m := range ImageGenerationModels {
		if prefix, ok := strings.CutPrefix(m, "prefix:"); ok {
			if strings.HasPrefix(modelName, prefix) {
				return true
			}
			continue
		}
		if strings.Contains(modelName, m) {
			return true
		}
	}
	return false
}

func IsOpenAITextModel(modelName string) bool {
	modelName = strings.ToLower(modelName)
	for _, m := range OpenAITextModels {
		if strings.Contains(modelName, m) {
			return true
		}
	}
	return false
}
