package config

import (
	"regexp"
	"testing"
)

func TestAntigravityGuardDefaultsApplyWithoutConfig(t *testing.T) {
	cfg := mustParseGuardConfig(t, "config-version: 8\n")
	if !cfg.Plugins.Enabled {
		t.Fatal("plugins.enabled = false, want default true")
	}
	plugin, ok := cfg.Plugins.Configs[AntigravityGuardPluginID]
	if !ok || plugin.Enabled == nil || !*plugin.Enabled {
		t.Fatalf("bundled plugin config = %#v, want enabled", plugin)
	}
	rules := cfg.OAuthRequestScopedErrors[antigravityGuardChannel]
	if len(rules) != 1 || rules[0].Status != 429 || rules[0].Action != "stop" {
		t.Fatalf("antigravity rules = %#v, want default stop rule", rules)
	}
}

func TestAntigravityGuardDefaultsYieldToExplicitConfig(t *testing.T) {
	cfg := mustParseGuardConfig(t, `config-version: 8
plugins:
  enabled: false
  configs:
    antigravity-coding-filter:
      enabled: false
oauth:
  request-scoped-errors:
    antigravity: []
    Gemini:
      - status: 400
        match: ["x"]
        action: "stop"
`)
	if cfg.Plugins.Enabled {
		t.Fatal("explicit plugins.enabled: false was overridden")
	}
	if plugin := cfg.Plugins.Configs[AntigravityGuardPluginID]; plugin.Enabled == nil || *plugin.Enabled {
		t.Fatalf("explicit plugin enabled: false was overridden: %#v", plugin)
	}
	if rules, ok := cfg.OAuthRequestScopedErrors[antigravityGuardChannel]; ok {
		t.Fatalf("explicit empty antigravity rules were replaced: %#v", rules)
	}
	if len(cfg.OAuthRequestScopedErrors["gemini"]) != 1 {
		t.Fatalf("other channels changed: %#v", cfg.OAuthRequestScopedErrors)
	}
}

func TestAntigravityGuardPatternSeparatesContentFilterFromQuota(t *testing.T) {
	pattern := regexp.MustCompile(antigravityContentFilter429Pattern)
	contentFilter := "{\n  \"error\": {\n    \"code\": 429,\n    \"message\": \"Resource has been exhausted (e.g. check quota).\",\n    \"status\": \"RESOURCE_EXHAUSTED\"\n  }\n}\n"
	quota := `{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED"}]}}`
	if !pattern.MatchString(contentFilter) {
		t.Fatal("pattern does not match the signal-less 429")
	}
	if pattern.MatchString(quota) {
		t.Fatal("pattern matches a quota 429 with details")
	}
}

func mustParseGuardConfig(t *testing.T, yaml string) *Config {
	t.Helper()
	cfg, err := ParseConfigBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("ParseConfigBytes: %v", err)
	}
	return cfg
}
