package diff

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestDiffOpenAICompatibility(t *testing.T) {
	oldList := []config.OpenAICompatibility{
		{
			Name: "provider-a",
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{
				{APIKey: "key-a"},
			},
			Models: []config.OpenAICompatibilityModel{
				{Name: "m1"},
			},
		},
	}
	newList := []config.OpenAICompatibility{
		{
			Name: "provider-a",
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{
				{APIKey: "key-a"},
				{APIKey: "key-b"},
			},
			Models: []config.OpenAICompatibilityModel{
				{Name: "m1"},
				{Name: "m2"},
			},
			Headers: map[string]string{"X-Test": "1"},
		},
		{
			Name:          "provider-b",
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "key-b"}},
		},
	}

	changes := DiffOpenAICompatibility(oldList, newList)
	expectContains(t, changes, "provider added: provider-b (api-keys=1, models=0)")
	expectContains(t, changes, "provider updated: provider-a (api-keys 1 -> 2, models 1 -> 2, headers updated)")
}

func TestDiffOpenAICompatibility_RemovedAndUnchanged(t *testing.T) {
	oldList := []config.OpenAICompatibility{
		{
			Name:          "provider-a",
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "key-a"}},
			Models:        []config.OpenAICompatibilityModel{{Name: "m1"}},
		},
	}
	newList := []config.OpenAICompatibility{
		{
			Name:          "provider-a",
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "key-a"}},
			Models:        []config.OpenAICompatibilityModel{{Name: "m1"}},
		},
	}
	if changes := DiffOpenAICompatibility(oldList, newList); len(changes) != 0 {
		t.Fatalf("expected no changes, got %v", changes)
	}

	newList = nil
	changes := DiffOpenAICompatibility(oldList, newList)
	expectContains(t, changes, "provider removed: provider-a (api-keys=1, models=1)")
}

func TestOpenAICompatKeyFallbacks(t *testing.T) {
	entry := config.OpenAICompatibility{
		BaseURL: "http://base",
		Models:  []config.OpenAICompatibilityModel{{Alias: "alias-only"}},
	}
	key, label := openAICompatKey(entry, 0)
	if key != "base:http://base" || label != "http://base" {
		t.Fatalf("expected base key, got %s/%s", key, label)
	}

	entry.BaseURL = ""
	key, label = openAICompatKey(entry, 1)
	if key != "alias:alias-only" || label != "alias-only" {
		t.Fatalf("expected alias fallback, got %s/%s", key, label)
	}

	entry.Models = nil
	key, label = openAICompatKey(entry, 2)
	if key != "index:2" || label != "entry-3" {
		t.Fatalf("expected index fallback, got %s/%s", key, label)
	}
}

func TestOpenAICompatKey_UsesName(t *testing.T) {
	entry := config.OpenAICompatibility{Name: "My-Provider"}
	key, label := openAICompatKey(entry, 0)
	if key != "name:My-Provider" || label != "My-Provider" {
		t.Fatalf("expected name key, got %s/%s", key, label)
	}
}

func TestOpenAICompatKey_SignatureFallbackWhenOnlyAPIKeys(t *testing.T) {
	entry := config.OpenAICompatibility{
		APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "k1"}, {APIKey: "k2"}},
	}
	key, label := openAICompatKey(entry, 0)
	if !strings.HasPrefix(key, "sig:") || !strings.HasPrefix(label, "compat-") {
		t.Fatalf("expected signature key, got %s/%s", key, label)
	}
}

func TestOpenAICompatSignature_EmptyReturnsEmpty(t *testing.T) {
	if got := openAICompatSignature(config.OpenAICompatibility{}); got != "" {
		t.Fatalf("expected empty signature, got %q", got)
	}
}

func TestOpenAICompatSignature_StableAndNormalized(t *testing.T) {
	a := config.OpenAICompatibility{
		Name:    "  Provider  ",
		BaseURL: "http://base",
		Models: []config.OpenAICompatibilityModel{
			{Name: "m1"},
			{Name: "  "},
			{Alias: "A1"},
		},
		Headers: map[string]string{
			"X-Test": "1",
			"  ":     "ignored",
		},
		APIKeyEntries: []config.OpenAICompatibilityAPIKey{
			{APIKey: "k1"},
			{APIKey: " "},
		},
	}
	b := config.OpenAICompatibility{
		Name:    "provider",
		BaseURL: "http://base",
		Models: []config.OpenAICompatibilityModel{
			{Alias: "a1"},
			{Name: "m1"},
		},
		Headers: map[string]string{
			"x-test": "2",
		},
		APIKeyEntries: []config.OpenAICompatibilityAPIKey{
			{APIKey: "k2"},
		},
	}

	sigA := openAICompatSignature(a)
	sigB := openAICompatSignature(b)
	if sigA == "" || sigB == "" {
		t.Fatalf("expected non-empty signatures, got %q / %q", sigA, sigB)
	}
	if sigA != sigB {
		t.Fatalf("expected normalized signatures to match, got %s / %s", sigA, sigB)
	}

	c := b
	c.Models = append(c.Models, config.OpenAICompatibilityModel{Name: "m2"})
	if sigC := openAICompatSignature(c); sigC == sigB {
		t.Fatalf("expected signature to change when models change, got %s", sigC)
	}
}

func TestCountOpenAIModelsSkipsBlanks(t *testing.T) {
	models := []config.OpenAICompatibilityModel{
		{Name: "m1"},
		{Name: ""},
		{Alias: ""},
		{Name: " "},
		{Alias: "a1"},
	}
	if got := countOpenAIModels(models); got != 2 {
		t.Fatalf("expected 2 counted models, got %d", got)
	}
}

func TestOpenAICompatKeyUsesModelNameWhenAliasEmpty(t *testing.T) {
	entry := config.OpenAICompatibility{
		Models: []config.OpenAICompatibilityModel{{Name: "model-name"}},
	}
	key, label := openAICompatKey(entry, 5)
	if key != "alias:model-name" || label != "model-name" {
		t.Fatalf("expected model-name fallback, got %s/%s", key, label)
	}
}
