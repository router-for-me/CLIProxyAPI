package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestClaudeCommentaryConfigV8(t *testing.T) {
	for _, test := range []struct {
		name string
		yaml string
		want bool
	}{
		{"default", "config-version: 8\n", false},
		{"enabled", "config-version: 8\nclient:\n  claude:\n    show-codex-commentary: true\n", true},
		{"disabled", "config-version: 8\nclient:\n  claude:\n    show-codex-commentary: false\n", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if errValidate := ValidateV8Config([]byte(test.yaml)); errValidate != nil {
				t.Fatalf("ValidateV8Config: %v", errValidate)
			}
			var cfg Config
			if err := yaml.Unmarshal([]byte(test.yaml), &cfg); err != nil {
				t.Fatal(err)
			}
			if cfg.Client.Claude.ShowCodexCommentary != test.want {
				t.Fatalf("show-codex-commentary = %v, want %v", cfg.Client.Claude.ShowCodexCommentary, test.want)
			}
			encoded, err := yaml.Marshal(&cfg)
			if err != nil {
				t.Fatal(err)
			}
			var roundTrip Config
			if err := yaml.Unmarshal(encoded, &roundTrip); err != nil {
				t.Fatal(err)
			}
			if roundTrip.Client.Claude.ShowCodexCommentary != test.want {
				t.Fatal("commentary setting lost in config round-trip")
			}
		})
	}
}
