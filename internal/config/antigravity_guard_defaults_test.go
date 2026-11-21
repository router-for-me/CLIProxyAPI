package config

import (
	"os"
	"path/filepath"
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
	if rules, ok := cfg.OAuthRequestScopedErrors[antigravityGuardChannel]; !ok || len(rules) != 0 {
		t.Fatalf("explicit empty antigravity rules = %#v (present %v), want kept and empty", rules, ok)
	}
	if len(cfg.OAuthRequestScopedErrors["gemini"]) != 1 {
		t.Fatalf("other channels changed: %#v", cfg.OAuthRequestScopedErrors)
	}
}

// A config save must neither drop the synthesized defaults nor revoke the
// empty-list opt-out.
func TestAntigravityGuardDefaultsSurviveConfigSave(t *testing.T) {
	cases := map[string]struct {
		yaml     string
		wantRule bool
	}{
		"defaults": {yaml: `config-version: 8
port: 8317
`, wantRule: true},
		"opt-out": {yaml: `config-version: 8
port: 8317
oauth:
  request-scoped-errors:
    antigravity: []
`, wantRule: false},
	}
	for name, tc := range cases {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(tc.yaml), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatalf("%s: LoadConfig: %v", name, err)
		}
		if err = SaveConfigPreserveComments(path, cfg); err != nil {
			t.Fatalf("%s: SaveConfigPreserveComments: %v", name, err)
		}
		saved, _ := os.ReadFile(path)
		reloaded, err := LoadConfig(path)
		if err != nil {
			t.Fatalf("%s: reload: %v", name, err)
		}
		if plugin := reloaded.Plugins.Configs[AntigravityGuardPluginID]; !reloaded.Plugins.Enabled || plugin.Enabled == nil || !*plugin.Enabled {
			t.Fatalf("%s: bundled plugin disabled after save:\n%s", name, saved)
		}
		if got := len(reloaded.OAuthRequestScopedErrors[antigravityGuardChannel]) == 1; got != tc.wantRule {
			t.Fatalf("%s: stop rule present = %v, want %v after save:\n%s", name, got, tc.wantRule, saved)
		}
	}
}

func TestAntigravityGuardPatternSeparatesContentFilterFromQuota(t *testing.T) {
	contentFilter := "{\n  \"error\": {\n    \"code\": 429,\n    \"message\": \"Resource has been exhausted (e.g. check quota).\",\n    \"status\": \"RESOURCE_EXHAUSTED\"\n  }\n}\n"
	quota := `{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED"}]}}`
	if !IsAntigravityContentFilter429(contentFilter) {
		t.Fatal("pattern does not match the signal-less 429")
	}
	if IsAntigravityContentFilter429(quota) {
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
