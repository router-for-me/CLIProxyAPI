package registry

import (
	"slices"
	"testing"
)

func TestMuseWonderModelsInMetaCatalog(t *testing.T) {
	tests := []struct {
		id            string
		contextLength int
		maxCompletion int
		input         []string
	}{
		{id: "muse-wonder-1.0", contextLength: 1048576, maxCompletion: 65536, input: []string{"text", "image", "video", "audio"}},
		{id: "muse-wonder-1.0-contributor", contextLength: 1048576, maxCompletion: 65536, input: []string{"text", "image", "video", "audio"}},
		{id: "muse-wonder-full-1.0", contextLength: 1007997, maxCompletion: 128000, input: []string{"text", "image"}},
		{id: "muse-wonder-full-1.0-contributor", contextLength: 1007997, maxCompletion: 128000, input: []string{"text", "image"}},
	}

	inCatalog := make(map[string]bool)
	for _, model := range GetMetaModels() {
		inCatalog[model.ID] = true
	}

	for _, test := range tests {
		t.Run(test.id, func(t *testing.T) {
			got := LookupStaticModelInfo(test.id)
			if got == nil {
				t.Fatalf("LookupStaticModelInfo(%q) = nil, want catalog entry", test.id)
			}
			if got.Type != "meta" {
				t.Fatalf("type = %q, want meta", got.Type)
			}
			if got.ContextLength != test.contextLength {
				t.Fatalf("context_length = %d, want %d", got.ContextLength, test.contextLength)
			}
			if got.MaxCompletionTokens != test.maxCompletion {
				t.Fatalf("max_completion_tokens = %d, want %d", got.MaxCompletionTokens, test.maxCompletion)
			}
			if got.Thinking == nil || !slices.Contains(got.Thinking.Levels, "max") {
				t.Fatalf("thinking = %+v, want levels including max", got.Thinking)
			}
			if !slices.Equal(got.SupportedInputModalities, test.input) {
				t.Fatalf("input modalities = %v, want %v", got.SupportedInputModalities, test.input)
			}
			if !slices.Equal(got.SupportedOutputModalities, []string{"text"}) {
				t.Fatalf("output modalities = %v, want [text]", got.SupportedOutputModalities)
			}
			if !inCatalog[test.id] {
				t.Fatalf("GetMetaModels() missing %q", test.id)
			}
		})
	}
}
