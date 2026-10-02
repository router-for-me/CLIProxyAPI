package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestClientCodexOptimizeMultiAgentV2(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want bool
	}{
		{"omitted", "config-version: 8\n", false},
		{"disabled", "client: {codex: {optimize-multi-agent-v2: false}}\n", false},
		{"enabled", "client: {codex: {optimize-multi-agent-v2: true, enable-apply-patch: true}}\n", true},
		{"null", "client: {codex: {optimize-multi-agent-v2: null}}\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if errValidate := ValidateV8Config([]byte(tc.raw)); errValidate != nil {
				t.Fatalf("validate: %v", errValidate)
			}
			cfg, errParse := ParseConfigBytes([]byte(tc.raw))
			if errParse != nil {
				t.Fatalf("parse: %v", errParse)
			}
			if cfg.Client.Codex.OptimizeMultiAgentV2 != tc.want {
				t.Fatalf("OptimizeMultiAgentV2 = %t, want %t", cfg.Client.Codex.OptimizeMultiAgentV2, tc.want)
			}
			var sdkCfg SDKConfig
			if errUnmarshal := yaml.Unmarshal([]byte(tc.raw), &sdkCfg); errUnmarshal != nil {
				t.Fatalf("decode SDK config: %v", errUnmarshal)
			}
			if sdkCfg.Client.Codex != cfg.Client.Codex {
				t.Fatal("SDK client configuration differs")
			}
			data, errMarshal := json.Marshal(cfg)
			if errMarshal != nil {
				t.Fatalf("encode JSON: %v", errMarshal)
			}
			var decoded Config
			if errUnmarshal := json.Unmarshal(data, &decoded); errUnmarshal != nil {
				t.Fatalf("decode JSON: %v", errUnmarshal)
			}
			if decoded.Client != cfg.Client || bytes.Count(data, []byte(`"optimize-multi-agent-v2"`)) != 1 {
				t.Fatal("JSON must contain only the canonical client setting")
			}
		})
	}
	for _, raw := range []string{
		"client: {codex: {optimize-multi-agent-v2: invalid}}",
		"client: {codex: {optimize-multi-agent-v2: 1}}",
		"client: false",
		"client: {codex: false}",
		"client: {codex: {optimize-multi-agent-v2: true, optimize-multi-agent-v2: false}}",
	} {
		if errValidate := ValidateV8Config([]byte(raw)); errValidate == nil {
			t.Fatalf("accepted invalid config %q", raw)
		}
	}
}

func TestClientCodexOptimizeMultiAgentV2Migration(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want bool
	}{
		{"flat", "codex: {optimize-multi-agent-v2: true}\n", true},
		{"providers", "providers: {codex: {optimize-multi-agent-v2: true}}\n", true},
		{"oauth", "oauth: {providers: {codex: {optimize-multi-agent-v2: true}}}\n", true},
		{"old false", "providers: {codex: {optimize-multi-agent-v2: false}}\n", false},
		{"client false wins", "client: {codex: {optimize-multi-agent-v2: false}}\nproviders: {codex: {optimize-multi-agent-v2: true}}\noauth: {providers: {codex: {optimize-multi-agent-v2: true}}}\ncodex: {optimize-multi-agent-v2: true}\n", false},
		{"client true wins", "client: {codex: {optimize-multi-agent-v2: true}}\noauth: {providers: {codex: {optimize-multi-agent-v2: false}}}\n", true},
		{"client null wins", "client: {codex: {optimize-multi-agent-v2: null}}\nproviders: {codex: {optimize-multi-agent-v2: true}}\n", false},
		{"oauth wins old conflicts", "oauth: {providers: {codex: {optimize-multi-agent-v2: false}}}\nproviders: {codex: {optimize-multi-agent-v2: true}}\ncodex: {optimize-multi-agent-v2: true}\n", false},
		{"providers wins flat", "providers: {codex: {optimize-multi-agent-v2: false}}\ncodex: {optimize-multi-agent-v2: true}\n", false},
		{"aliases", "client: &client {codex: {optimize-multi-agent-v2: false}}\nproviders: {<<: *client}\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(tc.raw)
			cfg, errParse := ParseConfigBytes(raw)
			if errParse != nil {
				t.Fatalf("parse: %v", errParse)
			}
			if cfg.Client.Codex.OptimizeMultiAgentV2 != tc.want {
				t.Fatalf("OptimizeMultiAgentV2 = %t, want %t", cfg.Client.Codex.OptimizeMultiAgentV2, tc.want)
			}
			for _, path := range v8ClientPaths {
				if cfg.OAuthOnlyFields[path.old] {
					t.Fatalf("historical client field marked OAuth-only: %s", path.old)
				}
			}
			cleaned, _, errClean := NormalizeConfigLayout(raw, false)
			if errClean != nil {
				t.Fatalf("clean conflicts: %v", errClean)
			}
			var original, normalized yaml.Node
			if errOriginal := yaml.Unmarshal(raw, &original); errOriginal != nil {
				t.Fatalf("decode original layout: %v", errOriginal)
			}
			if errCleaned := yaml.Unmarshal(cleaned, &normalized); errCleaned != nil {
				t.Fatalf("decode cleaned layout: %v", errCleaned)
			}
			if yamlPath(original.Content[0], "client.codex.optimize-multi-agent-v2") != nil {
				for _, path := range v8ClientPaths {
					if yamlPath(normalized.Content[0], path.old) != nil {
						t.Fatalf("conflicting historical path was retained: %s", path.old)
					}
				}
			}
			// Force an actual scoped copy while ensuring the client field survives.
			cfg.Codex.ResponseSteering = true
			cfg.OAuthOnlyFields = map[string]bool{"codex.response-steering": true}
			api := cfg.ForAPIKey()
			if api.Client != cfg.Client || api.Codex.ResponseSteering || !cfg.Codex.ResponseSteering {
				t.Fatal("API-key scope changed client configuration or shared state")
			}
			snapshot, errMarshal := yaml.Marshal(cfg)
			if errMarshal != nil {
				t.Fatalf("snapshot: %v", errMarshal)
			}
			var restored Config
			if errUnmarshal := yaml.Unmarshal(snapshot, &restored); errUnmarshal != nil {
				t.Fatalf("restore snapshot: %v", errUnmarshal)
			}
			if restored.ForAPIKey().Client.Codex.OptimizeMultiAgentV2 != tc.want {
				t.Fatal("snapshot round trip changed client setting")
			}
			migrated, _, errNormalize := NormalizeConfigLayout(raw, true)
			if errNormalize != nil {
				t.Fatalf("normalize: %v", errNormalize)
			}
			if errValidate := ValidateV8Config(migrated); errValidate != nil {
				t.Fatalf("validate migrated config: %v; data=%s", errValidate, migrated)
			}
			if errUnmarshal := yaml.Unmarshal(migrated, &restored); errUnmarshal != nil {
				t.Fatalf("decode migrated config: %v", errUnmarshal)
			}
			if restored.Client.Codex.OptimizeMultiAgentV2 != tc.want {
				t.Fatal("migration changed effective value")
			}
			again, changed, errNormalizeAgain := NormalizeConfigLayout(migrated, false)
			if errNormalizeAgain != nil || changed || !bytes.Equal(again, migrated) {
				t.Fatalf("normalized config not stable: changed=%t, err=%v", changed, errNormalizeAgain)
			}
		})
	}
	for _, path := range v8ClientPaths {
		var root yaml.Node
		root.Kind, root.Tag = yaml.MappingNode, "!!map"
		setYAMLPath(&root, path.old, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"})
		data, errMarshal := yaml.Marshal(&root)
		if errMarshal != nil {
			t.Fatalf("encode old path: %v", errMarshal)
		}
		if errValidate := ValidateV8Config(data); errValidate == nil {
			t.Fatalf("v8 write accepted historical path %s", path.old)
		}
		unchanged, changed, errNormalize := NormalizeConfigLayout(data, false)
		if errNormalize != nil || changed || !bytes.Equal(unchanged, data) {
			t.Fatalf("legacy-only normalization rewrote %s: %v", path.old, errNormalize)
		}
	}
}

func TestClientCodexOptimizeMultiAgentV2Save(t *testing.T) {
	for _, old := range v8ClientPaths {
		t.Run(old.old, func(t *testing.T) {
			var root yaml.Node
			root.Kind, root.Tag = yaml.MappingNode, "!!map"
			setYAMLPath(&root, old.old, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true", LineComment: "keep optimize comment"})
			parts := strings.Split(old.old, ".")
			parent := yamlPath(&root, strings.Join(parts[:len(parts)-1], "."))
			parent.Content[findMapKeyIndex(parent, parts[len(parts)-1])].HeadComment = "keep optimize heading"
			setYAMLPath(&root, "client.codex.enable-apply-patch", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"})
			raw, errMarshal := yaml.Marshal(&root)
			if errMarshal != nil {
				t.Fatalf("marshal: %v", errMarshal)
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if errWrite := os.WriteFile(path, raw, 0600); errWrite != nil {
				t.Fatalf("write: %v", errWrite)
			}
			cfg, errLoad := LoadConfig(path)
			if errLoad != nil {
				t.Fatalf("load: %v", errLoad)
			}
			for _, enabled := range []bool{true, false, true} {
				cfg.Client.Codex.OptimizeMultiAgentV2 = enabled
				if errSave := SaveConfigPreserveComments(path, cfg, true); errSave != nil {
					t.Fatalf("save: %v", errSave)
				}
				data, errRead := os.ReadFile(path)
				if errRead != nil {
					t.Fatalf("read: %v", errRead)
				}
				if errValidate := ValidateV8Config(data); errValidate != nil {
					t.Fatalf("validate saved config: %v", errValidate)
				}
				loaded, errReload := LoadConfig(path)
				if errReload != nil {
					t.Fatalf("reload: %v", errReload)
				}
				if loaded.Client.Codex.OptimizeMultiAgentV2 != enabled || !loaded.Client.Codex.EnableApplyPatch {
					t.Fatal("save changed client settings")
				}
				if !bytes.Contains(data, []byte("keep optimize comment")) || !bytes.Contains(data, []byte("keep optimize heading")) {
					t.Fatalf("save lost historical field comment (enabled=%t): raw=%s; saved=%s", enabled, raw, data)
				}
			}
		})
	}
}
