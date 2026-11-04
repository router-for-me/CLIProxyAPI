package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

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
