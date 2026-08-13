package config

import "testing"

func TestParseConfigBytesOpenAICompatibilityPassthroughHeaders(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`openai-compatibility:
  - name: enabled
    base-url: https://enabled.example/v1
    passthrough-headers: true
    models:
      - name: model
        alias: public
  - name: default
    base-url: https://default.example/v1
    models:
      - name: model
        alias: public-default
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if len(cfg.OpenAICompatibility) != 2 {
		t.Fatalf("providers = %d, want 2", len(cfg.OpenAICompatibility))
	}
	if !cfg.OpenAICompatibility[0].PassthroughHeaders {
		t.Fatal("enabled provider passthrough-headers = false, want true")
	}
	if cfg.OpenAICompatibility[1].PassthroughHeaders {
		t.Fatal("default provider passthrough-headers = true, want false")
	}
}
