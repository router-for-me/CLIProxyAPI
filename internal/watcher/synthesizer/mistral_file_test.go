package synthesizer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func synthesizeMistralAuthFile(t *testing.T, authData map[string]any) map[string]string {
	t.Helper()
	tempDir := t.TempDir()
	data, errMarshal := json.Marshal(authData)
	if errMarshal != nil {
		t.Fatalf("marshal auth file: %v", errMarshal)
	}
	if errWrite := os.WriteFile(filepath.Join(tempDir, "mistral-default.json"), data, 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	auths, err := NewFileSynthesizer().Synthesize(&SynthesisContext{
		Config:      &config.Config{},
		AuthDir:     tempDir,
		Now:         time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		IDGenerator: NewStableIDGenerator(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("expected 1 auth, got %d", len(auths))
	}
	if got, want := auths[0].Provider, auths[0].Attributes["provider_key"]; got != want {
		t.Fatalf("provider = %q, want the resolved provider key %q", got, want)
	}
	return auths[0].Attributes
}

func assertMistralAttribute(t *testing.T, attributes map[string]string, key, want string) {
	t.Helper()
	if got := attributes[key]; got != want {
		t.Errorf("%s = %q, want %q", key, got, want)
	}
}

// A Mistral auth file must surface the attributes the OpenAI-compatibility
// executor resolves credentials from, otherwise requests have no base URL or key.
func TestFileSynthesizer_MistralAuthPopulatesCompatAttributes(t *testing.T) {
	attributes := synthesizeMistralAuthFile(t, map[string]any{
		"type":        "mistral",
		"api_key":     "  sk-mistral-test  ",
		"base_url":    "https://proxy.example.com/v1",
		"compat_name": "mistral-eu",
	})

	assertMistralAttribute(t, attributes, "api_key", "sk-mistral-test")
	assertMistralAttribute(t, attributes, "base_url", "https://proxy.example.com/v1")
	assertMistralAttribute(t, attributes, "compat_name", "mistral-eu")
	// The provider key must match what a config-declared entry of the same name
	// resolves to, otherwise the auth registers under a provider that has no
	// executor and every request fails with auth_not_found.
	assertMistralAttribute(t, attributes, "provider_key", "openai-compatible-mistral-eu")
}

// Files written by an older importer, or hand-edited ones, may carry only the
// key; the routing attributes must still default to the Mistral endpoint.
func TestFileSynthesizer_MistralAuthAppliesDefaults(t *testing.T) {
	attributes := synthesizeMistralAuthFile(t, map[string]any{
		"type":    "mistral",
		"api_key": "sk-mistral-test",
	})

	assertMistralAttribute(t, attributes, "api_key", "sk-mistral-test")
	assertMistralAttribute(t, attributes, "base_url", "https://api.mistral.ai/v1")
	assertMistralAttribute(t, attributes, "compat_name", "mistral")
	assertMistralAttribute(t, attributes, "provider_key", "openai-compatible-mistral")
}

// An empty api_key must not be recorded: an empty attribute would present the
// credential as usable and fail upstream with a 401 on every request.
func TestFileSynthesizer_MistralAuthSkipsEmptyAPIKey(t *testing.T) {
	attributes := synthesizeMistralAuthFile(t, map[string]any{
		"type":    "mistral",
		"api_key": "   ",
	})

	if _, ok := attributes["api_key"]; ok {
		t.Errorf("api_key should be absent, got %q", attributes["api_key"])
	}
}
