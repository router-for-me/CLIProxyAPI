package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCodexCacheImportEnabled(t *testing.T) {
	cases := map[string]bool{
		"":     false,
		"true": true,
		"TRUE": true,
		"1":    true,
		"yes":  true,
		"no":   false,
		"off":  false,
	}
	for value, want := range cases {
		t.Setenv(codexCacheImportEnv, value)
		if got := codexCacheImportEnabled(); got != want {
			t.Errorf("enabled(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestImportCodexCacheSlugs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models_cache.json")
	payload, err := json.Marshal(map[string]any{
		"models": []map[string]any{
			{"slug": "gpt-daybreak-blue-latest", "display_name": "GPT Daybreak Blue", "visibility": "list", "supported_in_api": true},
			{"slug": "gpt-5.5"},
			{"slug": "hidden-slot", "visibility": "hide", "supported_in_api": true},
			{"slug": "noapi-slot", "supported_in_api": false},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	store := &modelStore{data: &staticModelsJSON{
		CodexPro: []*ModelInfo{{
			ID:                       "gpt-5.6-luna",
			Type:                     "openai",
			ContextLength:            372000,
			MaxCompletionTokens:      128000,
			Thinking:                 &ThinkingSupport{Levels: []string{"low", "high"}},
			SupportedInputModalities: []string{"text"},
		}},
	}}

	added := importCodexCacheSlugs(store, path)
	if len(added) != 1 || added[0] != "gpt-daybreak-blue-latest" {
		t.Fatalf("added = %v, want only gpt-daybreak-blue-latest", added)
	}

	var imported *ModelInfo
	for _, m := range store.data.CodexPro {
		if m.ID == "gpt-daybreak-blue-latest" {
			imported = m
		}
	}
	if imported == nil {
		t.Fatal("slug missing from codex-pro bucket")
	}
	if imported.ContextLength != 372000 || imported.Thinking == nil {
		t.Fatalf("template capabilities not cloned: ctx=%d thinking=%v", imported.ContextLength, imported.Thinking)
	}
	if len(store.data.CodexFree) == 0 || store.data.CodexFree[0].ID != "gpt-daybreak-blue-latest" {
		t.Fatal("slug missing from codex-free bucket")
	}

	// Idempotent: second run registers nothing new.
	if again := importCodexCacheSlugs(store, path); len(again) != 0 {
		t.Fatalf("second run added %v, want none", again)
	}
}

func TestImportCodexCacheSlugsMissingFile(t *testing.T) {
	store := &modelStore{data: &staticModelsJSON{}}
	if added := importCodexCacheSlugs(store, filepath.Join(t.TempDir(), "absent.json")); added != nil {
		t.Fatalf("expected nil for missing cache, got %v", added)
	}
}
