package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOpenAICompatibilityFallbackCompactionYAML(t *testing.T) {
	var cfg Config
	err := yaml.Unmarshal([]byte(`openai-compatibility:
  - name: deepseek
    base-url: https://api.deepseek.com
    fallback-compaction: true
    models:
      - name: deepseek-chat
        alias: deepseek-chat
`), &cfg)
	if err != nil {
		t.Fatalf("LoadConfigData: %v", err)
	}
	if len(cfg.OpenAICompatibility) != 1 || !cfg.OpenAICompatibility[0].FallbackCompaction {
		t.Fatalf("fallback-compaction not loaded: %+v", cfg.OpenAICompatibility)
	}
}
