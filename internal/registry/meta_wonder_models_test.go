package registry

import (
	"slices"
	"testing"
)

func TestMuseWonderModelsInMetaCatalog(t *testing.T) {
	ids := []string{"muse-wonder-1.0", "muse-wonder-1.0-contributor"}
	for _, id := range ids {
		got := LookupStaticModelInfo(id)
		if got == nil {
			t.Fatalf("LookupStaticModelInfo(%q) = nil, want catalog entry", id)
		}
		if got.Type != "meta" {
			t.Fatalf("%s type = %q, want meta", id, got.Type)
		}
		if got.ContextLength != 1048576 {
			t.Fatalf("%s context_length = %d, want 1048576", id, got.ContextLength)
		}
		if got.MaxCompletionTokens != 65536 {
			t.Fatalf("%s max_completion_tokens = %d, want 65536", id, got.MaxCompletionTokens)
		}
		if got.Thinking == nil || !slices.Contains(got.Thinking.Levels, "max") {
			t.Fatalf("%s thinking = %+v, want levels including max", id, got.Thinking)
		}
		for _, modality := range []string{"text", "image", "video", "audio"} {
			if !slices.Contains(got.SupportedInputModalities, modality) {
				t.Fatalf("%s input modalities = %v, want %q included", id, got.SupportedInputModalities, modality)
			}
		}
		if !slices.Equal(got.SupportedOutputModalities, []string{"text"}) {
			t.Fatalf("%s output modalities = %v, want [text]", id, got.SupportedOutputModalities)
		}
	}

	found := make(map[string]bool)
	for _, model := range GetMetaModels() {
		found[model.ID] = true
	}
	for _, id := range ids {
		if !found[id] {
			t.Fatalf("GetMetaModels() missing %q", id)
		}
	}
}

func TestMuseWonderFullModelsInMetaCatalog(t *testing.T) {
	ids := []string{"muse-wonder-full-1.0", "muse-wonder-full-1.0-contributor"}
	for _, id := range ids {
		got := LookupStaticModelInfo(id)
		if got == nil {
			t.Fatalf("LookupStaticModelInfo(%q) = nil, want catalog entry", id)
		}
		if got.Type != "meta" {
			t.Fatalf("%s type = %q, want meta", id, got.Type)
		}
		if got.ContextLength != 1007997 {
			t.Fatalf("%s context_length = %d, want 1007997", id, got.ContextLength)
		}
		if got.MaxCompletionTokens != 128000 {
			t.Fatalf("%s max_completion_tokens = %d, want 128000", id, got.MaxCompletionTokens)
		}
		if got.Thinking == nil || !slices.Contains(got.Thinking.Levels, "max") {
			t.Fatalf("%s thinking = %+v, want levels including max", id, got.Thinking)
		}
		if !slices.Equal(got.SupportedInputModalities, []string{"text", "image"}) {
			t.Fatalf("%s input modalities = %v, want [text image]", id, got.SupportedInputModalities)
		}
		if !slices.Equal(got.SupportedOutputModalities, []string{"text"}) {
			t.Fatalf("%s output modalities = %v, want [text]", id, got.SupportedOutputModalities)
		}
	}

	found := make(map[string]bool)
	for _, model := range GetMetaModels() {
		found[model.ID] = true
	}
	for _, id := range ids {
		if !found[id] {
			t.Fatalf("GetMetaModels() missing %q", id)
		}
	}
}
