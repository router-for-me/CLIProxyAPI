package config

import "testing"

func TestParseConfigBytesOpenAICompatibilityAPIBackend(t *testing.T) {
	t.Parallel()

	cfg, err := ParseConfigBytes([]byte(`openai-compatibility:
  - name: grok
    api-backend: responses
    base-url: https://example.com/v1
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if len(cfg.OpenAICompatibility) != 1 {
		t.Fatalf("openai-compatibility count = %d, want 1", len(cfg.OpenAICompatibility))
	}
	if got := cfg.OpenAICompatibility[0].APIBackend; got != "responses" {
		t.Fatalf("api-backend = %q, want responses", got)
	}
}
