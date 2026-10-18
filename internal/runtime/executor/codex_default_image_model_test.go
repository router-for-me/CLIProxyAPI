package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestImageRequestsWithoutModelDefaultToGPTImage25(t *testing.T) {
	const want = "gpt-image-2.5"

	if got := codexImageGenerationToolModel([]byte(`{"tools":[{"type":"image_generation"}]}`)); got != want {
		t.Errorf("image_generation tool default = %q, want %q", got, want)
	}
	if got := codexOpenAIImageToolModel("", ""); got != want {
		t.Errorf("images endpoint default = %q, want %q", got, want)
	}

	generation, errGeneration := codexPrepareOpenAIImageGenerationJSON([]byte(`{"prompt":"draw"}`), "")
	if errGeneration != nil {
		t.Fatalf("codexPrepareOpenAIImageGenerationJSON() error = %v", errGeneration)
	}
	if got := gjson.GetBytes(generation.Body, "tools.0.model").String(); got != want {
		t.Errorf("generation tool model = %q, want %q; body=%s", got, want, generation.Body)
	}

	edit, errEdit := codexPrepareOpenAIImageEditJSON([]byte(`{"prompt":"edit","images":[{"image_url":"data:image/png;base64,AA=="}]}`), "")
	if errEdit != nil {
		t.Fatalf("codexPrepareOpenAIImageEditJSON() error = %v", errEdit)
	}
	if got := gjson.GetBytes(edit.Body, "tools.0.model").String(); got != want {
		t.Errorf("edit tool model = %q, want %q; body=%s", got, want, edit.Body)
	}
}

func TestGPTImage2RemainsSelectableForImageRequests(t *testing.T) {
	const model = "gpt-image-2"

	if got := codexImageGenerationToolModel([]byte(`{"tools":[{"type":"image_generation","model":"gpt-image-2"}]}`)); got != model {
		t.Errorf("explicit image_generation tool model = %q, want %q", got, model)
	}
	if got := codexOpenAIImageToolModel(model, ""); got != model {
		t.Errorf("explicit request model = %q, want %q", got, model)
	}
	if got := codexOpenAIImageToolModel("", model); got != model {
		t.Errorf("explicit route model = %q, want %q", got, model)
	}
	if !codexIsDirectOpenAIImageModel(model) {
		t.Errorf("codexIsDirectOpenAIImageModel(%q) = false, want true", model)
	}
}
