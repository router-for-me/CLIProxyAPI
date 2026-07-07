package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestBuildOpenAICompatibilityConfigModels_InputModalities(t *testing.T) {
	compat := &config.OpenAICompatibility{
		Name: "mimo",
		Models: []config.OpenAICompatibilityModel{
			{
				Name:            "upstream-vision",
				Alias:           "mimo-v2.5-pro",
				InputModalities: []string{"TEXT", "image", "image"},
			},
			{
				Name:  "upstream-image",
				Alias: "compat-image",
				Image: true,
			},
		},
	}

	models := buildOpenAICompatibilityConfigModels(compat)
	if len(models) != 2 {
		t.Fatalf("model count = %d, want 2", len(models))
	}

	var vision *ModelInfo
	var imageModel *ModelInfo
	for _, model := range models {
		if model == nil {
			continue
		}
		switch model.ID {
		case "mimo-v2.5-pro":
			vision = model
		case "compat-image":
			imageModel = model
		}
	}
	if vision == nil {
		t.Fatal("expected vision model")
	}
	if got := joinModalities(vision.SupportedInputModalities); got != "text,image" {
		t.Fatalf("SupportedInputModalities = %q, want text,image", got)
	}
	if imageModel == nil {
		t.Fatal("expected image model")
	}
	if imageModel.Type != registry.OpenAIImageModelType {
		t.Fatalf("image model type = %q, want %q", imageModel.Type, registry.OpenAIImageModelType)
	}
	if len(imageModel.SupportedInputModalities) != 0 {
		t.Fatalf("image model input modalities = %+v, want none", imageModel.SupportedInputModalities)
	}
}

func joinModalities(modalities []string) string {
	if len(modalities) == 0 {
		return ""
	}
	out := modalities[0]
	for i := 1; i < len(modalities); i++ {
		out += "," + modalities[i]
	}
	return out
}
