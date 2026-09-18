package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCloakConfigRelaxedSystemPromptPreservesPresence(t *testing.T) {
	tests := []struct {
		name      string
		yamlValue string
		want      *bool
	}{
		{name: "omitted", yamlValue: "mode: auto\n", want: nil},
		{name: "enabled", yamlValue: "relaxed-system-prompt: true\n", want: boolPointer(true)},
		{name: "disabled", yamlValue: "relaxed-system-prompt: false\n", want: boolPointer(false)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var cloak CloakConfig
			if err := yaml.Unmarshal([]byte(test.yamlValue), &cloak); err != nil {
				t.Fatalf("yaml.Unmarshal() error = %v", err)
			}
			if test.want == nil {
				if cloak.RelaxedSystemPrompt != nil {
					t.Fatalf("RelaxedSystemPrompt = %v, want nil", *cloak.RelaxedSystemPrompt)
				}
				return
			}
			if cloak.RelaxedSystemPrompt == nil || *cloak.RelaxedSystemPrompt != *test.want {
				t.Fatalf("RelaxedSystemPrompt = %v, want %v", cloak.RelaxedSystemPrompt, *test.want)
			}
		})
	}
}

func boolPointer(value bool) *bool {
	return &value
}

func TestSaveConfigPreserveCommentsRelaxedSystemPromptPresence(t *testing.T) {
	for _, initial := range []struct {
		name string
		yaml string
	}{
		{name: "new credential", yaml: "{}\n"},
		{name: "new cloak", yaml: "claude-api-key:\n  - api-key: test-key\n"},
		{name: "new field", yaml: "claude-api-key:\n  - api-key: test-key\n    cloak:\n      mode: auto\n"},
		{name: "existing field", yaml: "claude-api-key:\n  - api-key: test-key\n    cloak:\n      relaxed-system-prompt: true\n"},
	} {
		for _, setting := range []struct {
			name  string
			value *bool
		}{
			{name: "omitted"},
			{name: "disabled", value: boolPointer(false)},
			{name: "enabled", value: boolPointer(true)},
		} {
			t.Run(initial.name+"/"+setting.name, func(t *testing.T) {
				configPath := filepath.Join(t.TempDir(), "config.yaml")
				if errWrite := os.WriteFile(configPath, []byte(initial.yaml), 0o600); errWrite != nil {
					t.Fatal(errWrite)
				}
				cfg := &Config{ClaudeKey: []ClaudeKey{{
					APIKey: "test-key", Cloak: &CloakConfig{RelaxedSystemPrompt: setting.value},
				}}}
				if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
					t.Fatal(errSave)
				}
				saved, errRead := os.ReadFile(configPath)
				if errRead != nil {
					t.Fatal(errRead)
				}
				var document map[string]any
				if errYAML := yaml.Unmarshal(saved, &document); errYAML != nil {
					t.Fatal(errYAML)
				}
				entry := document["claude-api-key"].([]any)[0].(map[string]any)
				for key := range entry {
					if key != "api-key" && key != "cloak" {
						t.Fatalf("save added default credential field %q: %s", key, saved)
					}
				}
				if setting.value == nil && (initial.name == "new credential" || initial.name == "new cloak") {
					if _, exists := entry["cloak"]; exists {
						t.Fatalf("save added an empty cloak block: %s", saved)
					}
				}
				loaded, errLoad := LoadConfigOptional(configPath, false)
				if errLoad != nil {
					t.Fatal(errLoad)
				}
				var got *bool
				if loaded.ClaudeKey[0].Cloak != nil {
					got = loaded.ClaudeKey[0].Cloak.RelaxedSystemPrompt
				}
				if (got == nil) != (setting.value == nil) {
					t.Fatalf("saved relaxed-system-prompt presence = %t, want %t", got != nil, setting.value != nil)
				}
				if got != nil && *got != *setting.value {
					t.Fatalf("saved relaxed-system-prompt = %t, want %t", *got, *setting.value)
				}
			})
		}
	}
}
