package config

import (
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestNormalizeClientKeys(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil", nil, nil},
		{"empty-and-blank", []string{"", "   "}, nil},
		{"trim", []string{"  key-1 "}, []string{"key-1"}},
		{"dedupe-preserve-order", []string{"b", "a", "b"}, []string{"b", "a"}},
		{"case-preserved", []string{"KeyA", "keya"}, []string{"KeyA", "keya"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeClientKeys(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("NormalizeClientKeys(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestOpenAICompatibilityAllowedKeysParse(t *testing.T) {
	const data = `
openai-compatibility:
  - name: "linkapi"
    base-url: "https://api.linkapi.ai/v1"
    allowed-keys:
      - "  team-a "
      - "team-a"
      - "team-b"
    models:
      - name: "gpt-5.5"
        alias: "gpt-5.5"
  - name: "public-provider"
    base-url: "https://example.com/v1"
    models:
      - name: "m1"
        alias: "m1"
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(data), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	cfg.SanitizeOpenAICompatibility()
	if len(cfg.OpenAICompatibility) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(cfg.OpenAICompatibility))
	}
	got := cfg.OpenAICompatibility[0].AllowedKeys
	if want := []string{"team-a", "team-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("private provider allowed-keys = %v, want %v (trimmed + deduped)", got, want)
	}
	if cfg.OpenAICompatibility[1].AllowedKeys != nil {
		t.Fatalf("public provider should have no allowed-keys, got %v", cfg.OpenAICompatibility[1].AllowedKeys)
	}
}
