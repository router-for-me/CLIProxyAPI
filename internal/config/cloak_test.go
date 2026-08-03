package config

import (
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
