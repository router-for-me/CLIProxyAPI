package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"gopkg.in/yaml.v3"
)

func TestOAuthSettingsConfigDecoding_V8(t *testing.T) {
	const rawYAML = `
config-version: 8
oauth:
  settings:
    codex:
      - name: "gpt-6-sol"
        max-context-length: 524288
      - name: "deepseek-v4-flash"
        max-context-length: 1048576
`
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(rawYAML), &cfg); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	cfg.SanitizeOAuthSettings()

	codexSettings, ok := cfg.OAuthSettings["codex"]
	if !ok {
		t.Fatalf("expected cfg.OAuthSettings[codex] to exist, got: %#v", cfg.OAuthSettings)
	}
	if len(codexSettings) != 2 {
		t.Fatalf("expected 2 codex settings, got %d", len(codexSettings))
	}
	if codexSettings[0].Name != "gpt-6-sol" || codexSettings[0].MaxContextLength != 524288 {
		t.Errorf("unexpected entry 0: %+v", codexSettings[0])
	}
	if codexSettings[1].Name != "deepseek-v4-flash" || codexSettings[1].MaxContextLength != 1048576 {
		t.Errorf("unexpected entry 1: %+v", codexSettings[1])
	}
}

func TestOAuthSettingsConfigDecoding_Legacy(t *testing.T) {
	const rawYAML = `
oauth-settings:
  codex:
    - name: "gpt-6-sol"
      max-context-length: 524288
`
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(rawYAML), &cfg); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	cfg.SanitizeOAuthSettings()

	codexSettings, ok := cfg.OAuthSettings["codex"]
	if !ok {
		t.Fatalf("expected cfg.OAuthSettings[codex] to exist, got: %#v", cfg.OAuthSettings)
	}
	if len(codexSettings) != 1 {
		t.Fatalf("expected 1 codex setting, got %d", len(codexSettings))
	}
	if codexSettings[0].Name != "gpt-6-sol" || codexSettings[0].MaxContextLength != 524288 {
		t.Errorf("unexpected entry 0: %+v", codexSettings[0])
	}
}

func TestSanitizeOAuthSettings(t *testing.T) {
	cfg := config.Config{
		OAuthSettings: map[string][]config.OAuthModelSetting{
			" CODEX ": {
				{Name: "  gpt-6-sol  ", MaxContextLength: 524288},
				{Name: "gpt-6-sol", MaxContextLength: 999999}, // duplicate name, same alias -> later entry wins!
				{Name: "   ", MaxContextLength: 12345},        // empty name
				{Name: "deepseek-v4-flash", MaxContextLength: 1048576},
			},
			"  ": {
				{Name: "ignored", MaxContextLength: 100},
			},
		},
	}

	cfg.SanitizeOAuthSettings()

	if _, ok := cfg.OAuthSettings["  "]; ok {
		t.Error("expected empty channel to be dropped")
	}

	codex, ok := cfg.OAuthSettings["codex"]
	if !ok {
		t.Fatalf("expected codex channel in sanitized settings: %#v", cfg.OAuthSettings)
	}
	if len(codex) != 2 {
		t.Fatalf("expected 2 codex entries, got %d", len(codex))
	}
	if codex[0].Name != "gpt-6-sol" || codex[0].MaxContextLength != 999999 {
		t.Errorf("unexpected entry 0: %+v, want MaxContextLength = 999999 (later entry wins)", codex[0])
	}
	if codex[1].Name != "deepseek-v4-flash" || codex[1].MaxContextLength != 1048576 {
		t.Errorf("unexpected entry 1: %+v", codex[1])
	}
}

func TestParseConfigBytes_DuplicateOAuthSettingsLaterWins(t *testing.T) {
	const rawYAML = `
config-version: 8
oauth:
  settings:
    codex:
      - name: "gpt-6-sol"
        max-context-length: 524288
      - name: "gpt-6-sol"
        max-context-length: 1048576
`
	cfg, err := config.ParseConfigBytes([]byte(rawYAML))
	if err != nil {
		t.Fatalf("ParseConfigBytes error = %v", err)
	}
	codexSettings, ok := cfg.OAuthSettings["codex"]
	if !ok || len(codexSettings) != 1 {
		t.Fatalf("expected 1 codex setting after sanitization, got: %#v", codexSettings)
	}
	if codexSettings[0].MaxContextLength != 1048576 {
		t.Errorf("MaxContextLength = %d, want 1048576 (later entry wins)", codexSettings[0].MaxContextLength)
	}
}

func TestSanitizeOAuthSettings_PreservesLastOccurrenceOrderWithInterleavedRules(t *testing.T) {
	cfg := config.Config{
		OAuthSettings: map[string][]config.OAuthModelSetting{
			"codex": {
				{Name: "upstream", MaxContextLength: 524288},
				{Name: "public", MaxContextLength: 600000},
				{Name: "upstream", MaxContextLength: 1048576},
			},
		},
	}

	rawResolved := config.ResolveOAuthModelSetting(cfg.OAuthSettings["codex"], "public", "upstream", "")
	if rawResolved == nil || rawResolved.MaxContextLength != 1048576 {
		t.Fatalf("pre-sanitize resolved to %+v, want 1048576", rawResolved)
	}

	cfg.SanitizeOAuthSettings()

	codex := cfg.OAuthSettings["codex"]
	if len(codex) != 2 {
		t.Fatalf("expected 2 codex entries, got %d", len(codex))
	}
	if codex[0].Name != "public" || codex[0].MaxContextLength != 600000 {
		t.Errorf("entry 0: %+v, want public:600000", codex[0])
	}
	if codex[1].Name != "upstream" || codex[1].MaxContextLength != 1048576 {
		t.Errorf("entry 1: %+v, want upstream:1048576", codex[1])
	}

	sanitizedResolved := config.ResolveOAuthModelSetting(codex, "public", "upstream", "")
	if sanitizedResolved == nil || sanitizedResolved.MaxContextLength != 1048576 {
		t.Fatalf("post-sanitize resolved to %+v, want 1048576", sanitizedResolved)
	}
}

func TestSaveConfigPreserveComments_OAuthSettings(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	initialYAML := `config-version: 8
oauth:
  # Provider model settings
  settings:
    codex:
      - name: "gpt-6-sol"
        max-context-length: 524288
`
	if errWrite := os.WriteFile(configPath, []byte(initialYAML), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile error = %v", errWrite)
	}

	cfg, errLoad := config.LoadConfig(configPath)
	if errLoad != nil {
		t.Fatalf("LoadConfig error = %v", errLoad)
	}

	if len(cfg.OAuthSettings["codex"]) != 1 || cfg.OAuthSettings["codex"][0].MaxContextLength != 524288 {
		t.Fatalf("unexpected loaded settings: %#v", cfg.OAuthSettings)
	}

	// Update setting
	cfg.OAuthSettings["codex"][0].MaxContextLength = 1048576

	if errSave := config.SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments error = %v", errSave)
	}

	savedBytes, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("os.ReadFile error = %v", errRead)
	}
	savedText := string(savedBytes)

	if !strings.Contains(savedText, "1048576") {
		t.Errorf("saved config does not contain updated max-context-length: %s", savedText)
	}
	if !strings.Contains(savedText, "# Provider model settings") {
		t.Errorf("saved config lost comment: %s", savedText)
	}
}

func TestResolveOAuthModelSetting_Priority(t *testing.T) {
	settings := []config.OAuthModelSetting{
		{Name: "upstream", MaxContextLength: 524288},
		{Name: "upstream", Alias: "public", MaxContextLength: 1048576},
	}

	// Model with ID "upstream" (no alias) should match the general "upstream" rule
	sUpstream := config.ResolveOAuthModelSetting(settings, "upstream", "upstream", "")
	if sUpstream == nil || sUpstream.MaxContextLength != 524288 {
		t.Fatalf("upstream model resolved to %+v, want 524288", sUpstream)
	}

	// Model with ID "public" and MetadataModelID "upstream" should match the more specific alias rule
	sPublic := config.ResolveOAuthModelSetting(settings, "public", "upstream", "")
	if sPublic == nil || sPublic.MaxContextLength != 1048576 {
		t.Fatalf("public model resolved to %+v, want 1048576", sPublic)
	}

	// Unknown model should return nil
	if got := config.ResolveOAuthModelSetting(settings, "unknown", "", ""); got != nil {
		t.Fatalf("unknown model resolved to %+v, want nil", got)
	}
}
