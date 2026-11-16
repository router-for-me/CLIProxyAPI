package config

import (
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParseConfigBytes_ServerKVStoreV8AndLegacy(t *testing.T) {
	v8YAML := []byte("config-version: 8\nserver:\n  kv-store:\n    disabled: true\n    path: ./data/kv.db\n")
	if errValidate := ValidateV8Config(v8YAML); errValidate != nil {
		t.Fatalf("ValidateV8Config() error = %v", errValidate)
	}
	cfg, errParse := ParseConfigBytes(v8YAML)
	if errParse != nil {
		t.Fatalf("ParseConfigBytes(v8) error = %v", errParse)
	}
	if !cfg.KVStore.Disabled || cfg.KVStore.Path != "./data/kv.db" {
		t.Fatalf("cfg.KVStore = %+v, want {Disabled:true Path:./data/kv.db}", cfg.KVStore)
	}

	cloned := cfg.CloneForRuntime()
	if !cloned.KVStore.Disabled || cloned.KVStore.Path != "./data/kv.db" {
		t.Fatalf("cloned.KVStore = %+v, want {Disabled:true Path:./data/kv.db}", cloned.KVStore)
	}

	legacyYAML := []byte("kv-store:\n  disabled: true\n  path: /var/lib/cpa/kv.db\n")
	migrated, changed, errNorm := NormalizeConfigLayout(legacyYAML, true)
	if errNorm != nil || !changed {
		t.Fatalf("NormalizeConfigLayout() changed=%v err=%v", changed, errNorm)
	}
	var tree map[string]any
	if errUnmarshal := yaml.Unmarshal(migrated, &tree); errUnmarshal != nil {
		t.Fatalf("yaml.Unmarshal(migrated) error = %v", errUnmarshal)
	}
	serverMap, okServer := tree["server"].(map[string]any)
	if !okServer {
		t.Fatalf("migrated server section missing: %#v", tree)
	}
	kvMap, okKV := serverMap["kv-store"].(map[string]any)
	if !okKV || kvMap["disabled"] != true || kvMap["path"] != "/var/lib/cpa/kv.db" {
		t.Fatalf("migrated server.kv-store = %#v", serverMap["kv-store"])
	}
}

func TestParseConfigBytes_ServerKVStoreDefaultsAndEffectivePath(t *testing.T) {
	wantDefaultAuthPath := filepath.Join(DefaultAuthDir, DefaultKVStorePath)

	// Omitted kv-store section defaults to Disabled=false and Path="" (resolving to <auth-dir>/kv.db).
	omittedCfg, errOmitted := ParseConfigBytes([]byte("config-version: 8\nserver:\n  port: 8317\n"))
	if errOmitted != nil {
		t.Fatalf("ParseConfigBytes(omitted) error = %v", errOmitted)
	}
	if omittedCfg.KVStore.Disabled || omittedCfg.KVStore.Path != "" {
		t.Fatalf("omittedCfg.KVStore = %+v, want {Disabled:false Path:\"\"}", omittedCfg.KVStore)
	}
	if got := omittedCfg.KVStore.EffectivePath(); got != DefaultKVStorePath {
		t.Fatalf("omittedCfg.KVStore.EffectivePath() = %q, want %q", got, DefaultKVStorePath)
	}
	if got := omittedCfg.EffectiveKVStorePath(); got != wantDefaultAuthPath {
		t.Fatalf("omittedCfg.EffectiveKVStorePath() = %q, want %q", got, wantDefaultAuthPath)
	}

	// Custom auth-dir with omitted kv-store.path resolves to <auth-dir>/kv.db.
	customAuthCfg, errCustomAuth := ParseConfigBytes([]byte("config-version: 8\noauth:\n  auth-dir: /tmp/custom-auth\n"))
	if errCustomAuth != nil {
		t.Fatalf("ParseConfigBytes(custom auth-dir) error = %v", errCustomAuth)
	}
	wantCustomAuthPath := filepath.Join("/tmp/custom-auth", DefaultKVStorePath)
	if got := customAuthCfg.EffectiveKVStorePath(); got != wantCustomAuthPath {
		t.Fatalf("customAuthCfg.EffectiveKVStorePath() = %q, want %q", got, wantCustomAuthPath)
	}

	// Explicit kv-store.path overrides auth-dir.
	explicitPathCfg, errExplicitPath := ParseConfigBytes([]byte("config-version: 8\noauth:\n  auth-dir: /tmp/custom-auth\nserver:\n  kv-store:\n    path: /var/data/custom.db\n"))
	if errExplicitPath != nil {
		t.Fatalf("ParseConfigBytes(explicit path) error = %v", errExplicitPath)
	}
	if explicitPathCfg.KVStore.Disabled || explicitPathCfg.KVStore.Path != "/var/data/custom.db" {
		t.Fatalf("explicitPathCfg.KVStore = %+v, want {Disabled:false Path:/var/data/custom.db}", explicitPathCfg.KVStore)
	}
	if got := explicitPathCfg.EffectiveKVStorePath(); got != "/var/data/custom.db" {
		t.Fatalf("explicitPathCfg.EffectiveKVStorePath() = %q, want /var/data/custom.db", got)
	}

	// Whitespace-only path is trimmed to "" (not overwritten with "kv.db") in ParseConfigBytes and LoadConfigOptional.
	blankYAML := "config-version: 8\noauth:\n  auth-dir: /tmp/blank-auth\nserver:\n  kv-store:\n    disabled: false\n    path: \"   \"\n"
	blankCfg, errBlank := ParseConfigBytes([]byte(blankYAML))
	if errBlank != nil {
		t.Fatalf("ParseConfigBytes(blank path) error = %v", errBlank)
	}
	if blankCfg.KVStore.Disabled || blankCfg.KVStore.Path != "" {
		t.Fatalf("blankCfg.KVStore = %+v, want {Disabled:false Path:\"\"}", blankCfg.KVStore)
	}
	wantBlankAuthPath := filepath.Join("/tmp/blank-auth", DefaultKVStorePath)
	if got := blankCfg.EffectiveKVStorePath(); got != wantBlankAuthPath {
		t.Fatalf("blankCfg.EffectiveKVStorePath() = %q, want %q", got, wantBlankAuthPath)
	}

	configFile := writeUploadTestConfig(t, blankYAML)
	loadedCfg, errLoad := LoadConfigOptional(configFile, false)
	if errLoad != nil {
		t.Fatalf("LoadConfigOptional(blank path) error = %v", errLoad)
	}
	if loadedCfg.KVStore.Disabled || loadedCfg.KVStore.Path != "" {
		t.Fatalf("loadedCfg.KVStore = %+v, want {Disabled:false Path:\"\"}", loadedCfg.KVStore)
	}
	if got := loadedCfg.EffectiveKVStorePath(); got != wantBlankAuthPath {
		t.Fatalf("loadedCfg.EffectiveKVStorePath() = %q, want %q", got, wantBlankAuthPath)
	}

	// Nil *Config EffectiveKVStorePath fallback.
	var nilCfg *Config
	if got := nilCfg.EffectiveKVStorePath(); got != wantDefaultAuthPath {
		t.Fatalf("nilCfg.EffectiveKVStorePath() = %q, want %q", got, wantDefaultAuthPath)
	}

	// EffectivePath and EffectivePathForAuthDir helpers.
	if got := (KVStoreConfig{Path: "   "}).EffectivePath(); got != DefaultKVStorePath {
		t.Fatalf("EffectivePath(blank) = %q, want %q", got, DefaultKVStorePath)
	}
	if got := (KVStoreConfig{Path: " ./custom/kv.db "}).EffectivePath(); got != "./custom/kv.db" {
		t.Fatalf("EffectivePath(custom) = %q, want %q", got, "./custom/kv.db")
	}
	if got := (KVStoreConfig{Path: " ./custom/kv.db "}).EffectivePathForAuthDir("/ignored"); got != "./custom/kv.db" {
		t.Fatalf("EffectivePathForAuthDir(custom) = %q, want %q", got, "./custom/kv.db")
	}
}
