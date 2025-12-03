package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestLegacyConfigMigration(t *testing.T) {
	t.Run("onlyLegacyFields", func(t *testing.T) {
		path := writeConfig(t, `
port: 8080
generative-language-api-key:
  - "legacy-gemini-1"
openai-compatibility:
  - name: "legacy-provider"
    base-url: "https://example.com"
    api-keys:
      - "legacy-openai-1"
amp-upstream-url: "https://amp.example.com"
amp-upstream-api-key: "amp-legacy-key"
amp-restrict-management-to-localhost: false
amp-model-mappings:
  - from: "old-model"
    to: "new-model"
`)
		cfg, err := config.LoadConfig(path)
		if err != nil {
			t.Fatalf("load legacy config: %v", err)
		}
		if got := len(cfg.GeminiKey); got != 1 || cfg.GeminiKey[0].APIKey != "legacy-gemini-1" {
			t.Fatalf("gemini migration mismatch: %+v", cfg.GeminiKey)
		}
		if got := len(cfg.OpenAICompatibility); got != 1 {
			t.Fatalf("expected 1 openai-compat provider, got %d", got)
		}
		if entries := cfg.OpenAICompatibility[0].APIKeyEntries; len(entries) != 1 || entries[0].APIKey != "legacy-openai-1" {
			t.Fatalf("openai-compat migration mismatch: %+v", entries)
		}
		if cfg.AmpCode.UpstreamURL != "https://amp.example.com" || cfg.AmpCode.UpstreamAPIKey != "amp-legacy-key" {
			t.Fatalf("amp migration failed: %+v", cfg.AmpCode)
		}
		if cfg.AmpCode.RestrictManagementToLocalhost {
			t.Fatalf("expected amp restriction to be false after migration")
		}
		if got := len(cfg.AmpCode.ModelMappings); got != 1 || cfg.AmpCode.ModelMappings[0].From != "old-model" {
			t.Fatalf("amp mappings migration mismatch: %+v", cfg.AmpCode.ModelMappings)
		}
		updated := readFile(t, path)
		if strings.Contains(updated, "generative-language-api-key") {
			t.Fatalf("legacy gemini key still present:\n%s", updated)
		}
		if strings.Contains(updated, "amp-upstream-url") || strings.Contains(updated, "amp-restrict-management-to-localhost") {
			t.Fatalf("legacy amp keys still present:\n%s", updated)
		}
		if strings.Contains(updated, "\n    api-keys:") {
			t.Fatalf("legacy openai compat keys still present:\n%s", updated)
		}
	})

	t.Run("mixedLegacyAndNewFields", func(t *testing.T) {
		path := writeConfig(t, `
gemini-api-key:
  - api-key: "new-gemini"
generative-language-api-key:
  - "new-gemini"
  - "legacy-gemini-only"
openai-compatibility:
  - name: "mixed-provider"
    base-url: "https://mixed.example.com"
    api-key-entries:
      - api-key: "new-entry"
    api-keys:
      - "legacy-entry"
      - "new-entry"
`)
		cfg, err := config.LoadConfig(path)
		if err != nil {
			t.Fatalf("load mixed config: %v", err)
		}
		if got := len(cfg.GeminiKey); got != 2 {
			t.Fatalf("expected 2 gemini entries, got %d: %+v", got, cfg.GeminiKey)
		}
		seen := make(map[string]struct{}, len(cfg.GeminiKey))
		for _, entry := range cfg.GeminiKey {
			if _, exists := seen[entry.APIKey]; exists {
				t.Fatalf("duplicate gemini key %q after migration", entry.APIKey)
			}
			seen[entry.APIKey] = struct{}{}
		}
		provider := cfg.OpenAICompatibility[0]
		if got := len(provider.APIKeyEntries); got != 2 {
			t.Fatalf("expected 2 openai entries, got %d: %+v", got, provider.APIKeyEntries)
		}
		entrySeen := make(map[string]struct{}, len(provider.APIKeyEntries))
		for _, entry := range provider.APIKeyEntries {
			if _, ok := entrySeen[entry.APIKey]; ok {
				t.Fatalf("duplicate openai key %q after migration", entry.APIKey)
			}
			entrySeen[entry.APIKey] = struct{}{}
		}
	})

	t.Run("onlyNewFields", func(t *testing.T) {
		path := writeConfig(t, `
gemini-api-key:
  - api-key: "new-only"
openai-compatibility:
  - name: "new-only-provider"
    base-url: "https://new-only.example.com"
    api-key-entries:
      - api-key: "new-only-entry"
ampcode:
  upstream-url: "https://amp.new"
  upstream-api-key: "new-amp-key"
  restrict-management-to-localhost: true
  model-mappings:
    - from: "a"
      to: "b"
`)
		cfg, err := config.LoadConfig(path)
		if err != nil {
			t.Fatalf("load new config: %v", err)
		}
		if len(cfg.GeminiKey) != 1 || cfg.GeminiKey[0].APIKey != "new-only" {
			t.Fatalf("unexpected gemini entries: %+v", cfg.GeminiKey)
		}
		if len(cfg.OpenAICompatibility) != 1 || len(cfg.OpenAICompatibility[0].APIKeyEntries) != 1 {
			t.Fatalf("unexpected openai compat entries: %+v", cfg.OpenAICompatibility)
		}
		if cfg.AmpCode.UpstreamURL != "https://amp.new" || cfg.AmpCode.UpstreamAPIKey != "new-amp-key" {
			t.Fatalf("unexpected amp config: %+v", cfg.AmpCode)
		}
	})

	t.Run("duplicateNamesDifferentBase", func(t *testing.T) {
		path := writeConfig(t, `
openai-compatibility:
  - name: "dup-provider"
    base-url: "https://provider-a"
    api-keys:
      - "key-a"
  - name: "dup-provider"
    base-url: "https://provider-b"
    api-keys:
      - "key-b"
`)
		cfg, err := config.LoadConfig(path)
		if err != nil {
			t.Fatalf("load duplicate config: %v", err)
		}
		if len(cfg.OpenAICompatibility) != 2 {
			t.Fatalf("expected 2 providers, got %d", len(cfg.OpenAICompatibility))
		}
		for _, entry := range cfg.OpenAICompatibility {
			if len(entry.APIKeyEntries) != 1 {
				t.Fatalf("expected 1 key entry per provider: %+v", entry)
			}
			switch entry.BaseURL {
			case "https://provider-a":
				if entry.APIKeyEntries[0].APIKey != "key-a" {
					t.Fatalf("provider-a key mismatch: %+v", entry.APIKeyEntries)
				}
			case "https://provider-b":
				if entry.APIKeyEntries[0].APIKey != "key-b" {
					t.Fatalf("provider-b key mismatch: %+v", entry.APIKeyEntries)
				}
			default:
				t.Fatalf("unexpected provider base url: %s", entry.BaseURL)
			}
		}
	})
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read temp config: %v", err)
	}
	return string(data)
}
