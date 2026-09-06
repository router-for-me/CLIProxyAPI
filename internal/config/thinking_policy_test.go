package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseConfigBytesSanitizesThinkingEffortMapping(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
thinking:
  effort-mapping:
    - from: " MAX "
      to: " Ultra "
      source-protocol: " Claude "
      target-protocol: " Responses "
      target-provider: " Codex "
      models: [" gpt-* ", "", "gpt-*", " o3-* "]
    - from: " low "
      to: " HIGH "
      models: ["claude-*"]
    - from: ultra
      to: max
    - from: ""
      to: high
    - from: medium
      to: " MEDIUM "
    - from: high
      to: ""
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}

	want := []ThinkingEffortMappingRule{
		{
			From:           "max",
			To:             "ultra",
			SourceProtocol: "claude",
			TargetProtocol: "responses",
			TargetProvider: "codex",
			Models:         []string{"gpt-*", "o3-*"},
		},
		{
			From:   "low",
			To:     "high",
			Models: []string{"claude-*"},
		},
	}
	if !reflect.DeepEqual(cfg.Thinking.EffortMapping, want) {
		t.Fatalf("thinking effort mapping = %#v, want %#v", cfg.Thinking.EffortMapping, want)
	}
}

func TestLoadConfigOptionalSanitizesThinkingEffortMapping(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`
thinking:
  effort-mapping:
    - from: " XHIGH "
      to: " Ultra "
      source-protocol: " Claude "
      models: [" gpt-* ", "gpt-*", " "]
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfigOptional(path, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}
	want := []ThinkingEffortMappingRule{{
		From:           "xhigh",
		To:             "ultra",
		SourceProtocol: "claude",
		Models:         []string{"gpt-*"},
	}}
	if !reflect.DeepEqual(cfg.Thinking.EffortMapping, want) {
		t.Fatalf("thinking effort mapping = %#v, want %#v", cfg.Thinking.EffortMapping, want)
	}
}

func TestParseConfigBytesRejectsMalformedThinkingPolicyShapes(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{name: "thinking is sequence", yaml: "thinking: []\n"},
		{name: "effort mapping is mapping", yaml: "thinking:\n  effort-mapping:\n    from: max\n    to: ultra\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseConfigBytes([]byte(tt.yaml)); err == nil {
				t.Fatal("ParseConfigBytes() error = nil, want malformed shape error")
			}
		})
	}
}

func TestParseConfigBytesWithoutThinkingPolicyLeavesRulesEmpty(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("port: 8317\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if len(cfg.Thinking.EffortMapping) != 0 {
		t.Fatalf("thinking effort mapping = %#v, want no implicit rules", cfg.Thinking.EffortMapping)
	}
}

func TestParseConfigBytesIgnoresUnknownYAMLKeys(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
unknown-top-level: retained-nowhere
thinking:
  unknown-policy-key: ignored
  effort-mapping:
    - from: max
      to: ultra
      unknown-rule-key: ignored
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	want := []ThinkingEffortMappingRule{{From: "max", To: "ultra"}}
	if !reflect.DeepEqual(cfg.Thinking.EffortMapping, want) {
		t.Fatalf("thinking effort mapping = %#v, want %#v", cfg.Thinking.EffortMapping, want)
	}
}
