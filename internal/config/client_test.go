package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestClientCodexEnableApplyPatch(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want bool
	}{
		{"omitted", "config-version: 8\n", false},
		{"disabled", "client: {codex: {enable-apply-patch: false}}\n", false},
		{"enabled", "client: {codex: {enable-apply-patch: true}}\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if errValidate := ValidateV8Config([]byte(tc.raw)); errValidate != nil {
				t.Fatalf("validate: %v", errValidate)
			}
			cfg, errParse := ParseConfigBytes([]byte(tc.raw))
			if errParse != nil {
				t.Fatalf("parse: %v", errParse)
			}
			if cfg.Client.Codex.EnableApplyPatch != tc.want {
				t.Fatalf("EnableApplyPatch = %t, want %t", cfg.Client.Codex.EnableApplyPatch, tc.want)
			}
			if cfg.OAuthOnlyFields["client.codex.enable-apply-patch"] {
				t.Fatal("client setting must not be OAuth-only")
			}
			var sdkCfg SDKConfig
			if errUnmarshal := yaml.Unmarshal([]byte(tc.raw), &sdkCfg); errUnmarshal != nil {
				t.Fatalf("decode SDK config: %v", errUnmarshal)
			}
			if sdkCfg.Client.Codex.EnableApplyPatch != tc.want {
				t.Fatalf("SDK EnableApplyPatch = %t, want %t", sdkCfg.Client.Codex.EnableApplyPatch, tc.want)
			}
			data, errMarshal := json.Marshal(cfg)
			if errMarshal != nil {
				t.Fatalf("encode JSON: %v", errMarshal)
			}
			var decoded Config
			if errUnmarshal := json.Unmarshal(data, &decoded); errUnmarshal != nil {
				t.Fatalf("decode JSON: %v", errUnmarshal)
			}
			if decoded.Client.Codex.EnableApplyPatch != tc.want {
				t.Fatal("JSON round trip changed client setting")
			}
			migrated, _, errNormalize := NormalizeConfigLayout([]byte(tc.raw), true)
			if errNormalize != nil {
				t.Fatalf("normalize: %v", errNormalize)
			}
			if errValidate := ValidateV8Config(migrated); errValidate != nil {
				t.Fatalf("validate normalized config: %v", errValidate)
			}
			if errUnmarshal := yaml.Unmarshal(migrated, &decoded); errUnmarshal != nil {
				t.Fatalf("decode normalized config: %v", errUnmarshal)
			}
			if decoded.Client.Codex.EnableApplyPatch != tc.want {
				t.Fatal("normalization changed client setting")
			}
		})
	}
	for _, raw := range []string{
		"client: {codex: {enable-apply-patch: invalid}}\n",
		"client: {codex: {unknown: true}}\n",
	} {
		if errValidate := ValidateV8Config([]byte(raw)); errValidate == nil {
			t.Fatalf("accepted invalid config %q", raw)
		}
	}
}

func TestClientCodexEnableApplyPatchSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(path, []byte("config-version: 8\n"), 0600); errWrite != nil {
		t.Fatalf("write: %v", errWrite)
	}
	cfg := &Config{}
	for _, enabled := range []bool{true, false} {
		cfg.Client.Codex.EnableApplyPatch = enabled
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
		loaded, errLoad := LoadConfig(path)
		if errLoad != nil {
			t.Fatalf("load: %v", errLoad)
		}
		if loaded.Client.Codex.EnableApplyPatch != enabled {
			t.Fatalf("saved EnableApplyPatch = %t, want %t", loaded.Client.Codex.EnableApplyPatch, enabled)
		}
	}
}
