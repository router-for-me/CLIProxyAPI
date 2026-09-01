package config

import "testing"

func TestParseConfigBytesOpenAICompatibilityRemoteCompactionV2(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`openai-compatibility:
  - name: compact-provider
    base-url: https://compact.example.com/v1
    remote-compaction-v2: true
  - name: plain-provider
    base-url: https://plain.example.com/v1
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if len(cfg.OpenAICompatibility) != 2 {
		t.Fatalf("OpenAICompatibility length = %d, want 2", len(cfg.OpenAICompatibility))
	}
	if !cfg.OpenAICompatibility[0].RemoteCompactionV2 {
		t.Fatal("remote-compaction-v2=true was not parsed")
	}
	if cfg.OpenAICompatibility[1].RemoteCompactionV2 {
		t.Fatal("remote-compaction-v2 unexpectedly enabled by default")
	}
}
