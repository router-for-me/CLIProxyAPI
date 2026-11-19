package api

import (
	"testing"
)

// TestFormatHomeGeminiModelKeepsNativeGenerationMethods guards the Home catalog path:
// formatHomeGeminiModel used to hard-code generateContent, so with home.enabled the
// native-only entries were advertised as chat models.
func TestFormatHomeGeminiModelKeepsNativeGenerationMethods(t *testing.T) {
	for _, tt := range []struct {
		id   string
		want []string
	}{
		{id: "gemini-embedding-001", want: []string{"embedContent", "batchEmbedContents"}},
		{id: "gemini-embedding-2", want: []string{"embedContent", "batchEmbedContents"}},
		{id: "gemini-decision-1", want: []string{"decisions"}},
		{id: "models/gemini-decision-1", want: []string{"decisions"}},
		{id: "gemini-2.5-pro", want: []string{"generateContent"}},
		{id: "unknown-model", want: []string{"generateContent"}},
	} {
		model := formatHomeGeminiModel(homeModelEntry{id: tt.id})
		got, ok := model["supportedGenerationMethods"].([]string)
		if !ok {
			t.Fatalf("%s: missing supportedGenerationMethods", tt.id)
		}
		if len(got) != len(tt.want) {
			t.Fatalf("%s: methods = %v, want %v", tt.id, got, tt.want)
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Fatalf("%s: methods = %v, want %v", tt.id, got, tt.want)
			}
		}
	}
}

// TestFormatHomeGeminiModelPrefersHomeMethods keeps a Home-provided contract working once
// the Home catalog starts carrying the field, including for aliases.
func TestFormatHomeGeminiModelPrefersHomeMethods(t *testing.T) {
	model := formatHomeGeminiModel(homeModelEntry{
		id:                "my-embed-alias",
		generationMethods: []string{"embedContent", "batchEmbedContents"},
	})
	got := model["supportedGenerationMethods"].([]string)
	if len(got) != 2 || got[0] != "embedContent" {
		t.Fatalf("methods = %v, want the Home-provided native methods", got)
	}

	chatOnly := formatHomeGeminiModel(homeModelEntry{
		id:                "my-embed-alias",
		generationMethods: []string{"generateContent"},
	})
	if methods := chatOnly["supportedGenerationMethods"].([]string); len(methods) != 1 || methods[0] != "generateContent" {
		t.Fatalf("chat-only Home methods = %v, want generateContent", methods)
	}
}

func TestDecodeHomeModelsKeepsSupportedGenerationMethods(t *testing.T) {
	raw := []byte(`{"gemini":[{"name":"models/my-embed-alias","supportedGenerationMethods":["embedContent","batchEmbedContents"]}]}`)
	entries, err := decodeHomeModels(raw)
	if err != nil {
		t.Fatalf("decodeHomeModels() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if len(entries[0].generationMethods) != 2 {
		t.Fatalf("supportedGenerationMethods = %v, want it retained", entries[0].generationMethods)
	}
}

// TestFormatHomeGeminiModelResolvesAliasEntries covers Home aliases: the alias ID is not in
// the static catalog, but the canonical name recorded on the entry is.
func TestFormatHomeGeminiModelResolvesAliasEntries(t *testing.T) {
	for _, entry := range []homeModelEntry{
		{id: "my-embed", modelName: "models/gemini-embedding-001"},
		{id: "my-embed", metadataModelID: "gemini-embedding-001"},
	} {
		model := formatHomeGeminiModel(entry)
		methods := model["supportedGenerationMethods"].([]string)
		if len(methods) != 2 || methods[0] != "embedContent" {
			t.Fatalf("alias entry %+v methods = %v, want the native embedding methods", entry, methods)
		}
	}
}
