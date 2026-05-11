package cliproxy

import (
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// getCodeArtsModels returns the hardcoded list of CodeArts models.
func getCodeArtsModels() []*ModelInfo {
	now := time.Now().Unix()
	return []*ModelInfo{
		{
			ID:          "Glm-5-internal",
			Object:      "model",
			Created:     now,
			OwnedBy:     "huaweicloud",
			Type:        "codearts",
			DisplayName: "GLM-5 Internal",
			Thinking:    &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
		{
			ID:          "GLM-5.1",
			Object:      "model",
			Created:     now,
			OwnedBy:     "huaweicloud",
			Type:        "codearts",
			DisplayName: "GLM-5.1",
			Thinking:    &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
		{
			ID:          "deepseek-v3.2",
			Object:      "model",
			Created:     now,
			OwnedBy:     "huaweicloud",
			Type:        "codearts",
			DisplayName: "DeepSeek V3.2",
			Thinking:    &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
		{
			ID:          "Glm-4.7-internal",
			Object:      "model",
			Created:     now,
			OwnedBy:     "huaweicloud",
			Type:        "codearts",
			DisplayName: "GLM-4.7 Internal",
			Thinking:    &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
		{
			ID:          "GLM-4.7-SFT-Harmony",
			Object:      "model",
			Created:     now,
			OwnedBy:     "huaweicloud",
			Type:        "codearts",
			DisplayName: "GLM-4.7 SFT Harmony",
			Thinking:    &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}},
		},
	}
}
