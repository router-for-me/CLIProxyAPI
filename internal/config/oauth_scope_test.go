package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestKeyPayloadConfiguredView(t *testing.T) {
	cfg := &Config{}
	cfg.WebsocketAuth = true
	cfg.OAuthOnlyFields = map[string]bool{"ws-auth": true}
	cfg.Payload.Filter = make([]PayloadFilterRule, 1, 4)
	cfg.Payload.Filter[0] = PayloadFilterRule{Models: []PayloadModelRule{{Name: "*", Headers: map[string]string{"X-Filter": "yes"}}}, Params: []string{"global"}}
	cfg.Payload.Filter[:cap(cfg.Payload.Filter)][1] = PayloadFilterRule{Params: []string{"spare-capacity-sentinel"}}
	key := &ClaudeKey{KeyPayloadPolicy: KeyPayloadPolicy{Payload: &KeyPayloadConfig{Filter: []PayloadFilterRule{{Models: []PayloadModelRule{{Name: "claude-*", Match: []map[string]any{{"mode": "test"}}}}, Params: []string{"system.0"}}}}}}
	before := cfg.CloneForRuntime()
	keyBefore := key.PayloadFilters()[0].Models[0].Match[0]["mode"]
	first, second := cfg.ForConfiguredKey(key), cfg.ForConfiguredKey(key)
	keyless := cfg.WithKeyPayloadFilters(key)
	if !keyless.WebsocketAuth || !reflect.DeepEqual(keyless.OAuthOnlyFields, cfg.OAuthOnlyFields) || !reflect.DeepEqual(keyless.Payload.Filter, first.Payload.Filter) {
		t.Fatal("keyless policy changed OAuth scope or filter composition")
	}
	if first == cfg || first == second || first.WebsocketAuth || len(first.Payload.Filter) != len(cfg.Payload.Filter)+len(key.PayloadFilters()) {
		t.Fatal("selected view lost scoping, isolation or filter composition")
	}
	if !reflect.DeepEqual(first.Payload.Filter[:len(cfg.Payload.Filter)], cfg.Payload.Filter) || !reflect.DeepEqual(first.Payload.Filter[len(cfg.Payload.Filter):], key.PayloadFilters()) {
		t.Fatal("global filters must precede local filters")
	}
	first.Payload.Filter[0] = PayloadFilterRule{Params: []string{"request-local"}}
	if !reflect.DeepEqual(second.Payload.Filter[0], cfg.Payload.Filter[0]) || !reflect.DeepEqual(cfg, before) || cfg.Payload.Filter[:cap(cfg.Payload.Filter)][1].Params[0] != "spare-capacity-sentinel" || key.PayloadFilters()[0].Models[0].Match[0]["mode"] != keyBefore {
		t.Fatal("view composition changed shared slices or nested rule data")
	}
	for _, source := range []PayloadFilterSource{nil, &ClaudeKey{}, &CodexKey{KeyPayloadPolicy: KeyPayloadPolicy{Payload: &KeyPayloadConfig{}}}} {
		if !reflect.DeepEqual(cfg.ForConfiguredKey(source), cfg.ForAPIKey()) {
			t.Fatal("absent/empty policy changed static-key scoping")
		}
	}
	var absent *Config
	if absent.ForConfiguredKey(key) != nil {
		t.Fatal("nil configuration gained a view")
	}
}

func TestV8OAuthScopeSurvivesSnapshotsAndSaves(t *testing.T) {
	raw := []byte(`codex: {stream-bootstrap-buffering: true}
oauth:
  providers:
    codex: {disable-codex-cloaking: true, model-level-cooling: true}
    aistudio: {ws-auth: true}
    claude:
      disable-claude-cloak-mode: true
      header-defaults: {user-agent: oauth-agent}
    xai: {inject-x-search: true}
api-keys:
  codex:
    - name: api
      base-url: https://example.invalid
      keys: [{api-key: test-key, disable-codex-cloaking: true}]
`)
	cfg, err := ParseConfigBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := ParseConfigBytes(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]*Config{"parsed": cfg, "cloned": cfg.CloneForRuntime(), "snapshot": decoded} {
		t.Run(name, func(t *testing.T) {
			api := value.ForAPIKey()
			if api.WebsocketAuth {
				t.Fatal("API-key view inherited OAuth-only relay settings")
			}
			if !api.Codex.DisableCodexCloaking || !api.Codex.ModelLevelCooling || !api.DisableClaudeCloakMode || api.ClaudeHeaderDefaults.UserAgent != "oauth-agent" || !api.XAI.InjectXSearch {
				t.Fatal("API-key view lost shared upstream settings")
			}
			if !api.Codex.StreamBootstrapBuffering || api.CodexKey[0].DisableCodexCloaking == nil || !*api.CodexKey[0].DisableCodexCloaking {
				t.Fatal("API-key view lost a legacy setting or explicit key override")
			}
			if !value.Codex.DisableCodexCloaking || !value.XAI.InjectXSearch || value.ClaudeHeaderDefaults.UserAgent != "oauth-agent" {
				t.Fatal("API-key view mutated the shared OAuth configuration")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cfg.Debug = true
	if err = SaveConfigPreserveComments(path, cfg); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.ForAPIKey().Codex.DisableCodexCloaking || !reloaded.Codex.DisableCodexCloaking || reloaded.ForAPIKey().WebsocketAuth {
		t.Fatal("save/reload lost OAuth scope")
	}
}

func TestV8ScopeKeepsLegacyGlobals(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("codex: {disable-codex-cloaking: true}\ndisable-claude-cloak-mode: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ForAPIKey() != cfg || !cfg.ForAPIKey().Codex.DisableCodexCloaking || !cfg.ForAPIKey().DisableClaudeCloakMode {
		t.Fatal("legacy-only globals must keep their API-key fallback behavior")
	}
}

func TestV8MigrationUpdatesScopeAfterSaving(t *testing.T) {
	for _, migrate := range []bool{false, true} {
		name := "legacy"
		if migrate {
			name = "v8"
		}
		t.Run(name, func(t *testing.T) {
			raw := []byte("codex: {disable-codex-cloaking: true, response-steering: true}\nxai: {inject-x-search: true}\n")
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := ParseConfigBytes(raw)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Home.Enabled = true
			cfg.CodexResponseSteering = true
			before := cfg.CloneForRuntime()
			if err = SaveConfigPreserveComments(path, cfg, migrate); err != nil {
				t.Fatal(err)
			}
			disk, err := LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cfg.OAuthOnlyFields, disk.OAuthOnlyFields) {
				t.Fatal("saved config and runtime have different OAuth scopes")
			}
			if !cfg.ForAPIKey().Codex.DisableCodexCloaking || !cfg.ForAPIKey().XAI.InjectXSearch {
				t.Fatal("migration lost shared API-key settings")
			}
			after := cfg.CloneForRuntime()
			after.OAuthOnlyFields = nil
			if !reflect.DeepEqual(before, after) {
				t.Fatal("save changed values or discarded runtime-only state")
			}
		})
	}
}
