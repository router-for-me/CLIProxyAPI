package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSDKConfigJSONListUnprefixedModelsTracksPresence(t *testing.T) {
	testCases := []struct {
		name         string
		payload      string
		want         bool
		wantExplicit bool
	}{
		{name: "default", payload: `{}`, want: true},
		{name: "explicit false", payload: `{"list-unprefixed-models":false}`, want: false, wantExplicit: true},
		{name: "explicit true", payload: `{"list-unprefixed-models":true}`, want: true, wantExplicit: true},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var cfg SDKConfig
			if errUnmarshal := json.Unmarshal([]byte(testCase.payload), &cfg); errUnmarshal != nil {
				t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
			}
			if got := cfg.EffectiveListUnprefixedModels(); got != testCase.want {
				t.Fatalf("effective value = %t, want %t", got, testCase.want)
			}
			if cfg.ListUnprefixedModelsExplicit != testCase.wantExplicit {
				t.Fatalf("explicit marker = %t, want %t", cfg.ListUnprefixedModelsExplicit, testCase.wantExplicit)
			}

			for _, value := range []any{cfg, &cfg} {
				encoded, errMarshal := json.Marshal(value)
				if errMarshal != nil {
					t.Fatalf("json.Marshal() error = %v", errMarshal)
				}
				var serialized struct {
					ListUnprefixedModels bool `json:"list-unprefixed-models"`
				}
				if errUnmarshal := json.Unmarshal(encoded, &serialized); errUnmarshal != nil {
					t.Fatalf("json.Unmarshal(serialized) error = %v; data=%s", errUnmarshal, encoded)
				}
				if serialized.ListUnprefixedModels != testCase.want {
					t.Fatalf("serialized value = %t, want %t; data=%s", serialized.ListUnprefixedModels, testCase.want, encoded)
				}
			}
		})
	}
}

func TestParseConfigBytesListUnprefixedModelsDefaultsToTrue(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte("port: 8317\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if !cfg.ListUnprefixedModels {
		t.Fatal("list-unprefixed-models default = false, want true")
	}
	if !cfg.EffectiveListUnprefixedModels() {
		t.Fatal("effective list-unprefixed-models default = false, want true")
	}
}

func TestParseConfigBytesListUnprefixedModelsCanBeDisabled(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte("list-unprefixed-models: false\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if cfg.ListUnprefixedModels {
		t.Fatal("list-unprefixed-models = true, want false")
	}
	if cfg.EffectiveListUnprefixedModels() {
		t.Fatal("effective list-unprefixed-models = true, want false")
	}
}

func TestLoadConfigListUnprefixedModelsCanBeDisabled(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(configPath, []byte("list-unprefixed-models: false\n"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}

	cfg, errLoad := LoadConfig(configPath)
	if errLoad != nil {
		t.Fatalf("LoadConfig() error = %v", errLoad)
	}
	if cfg.ListUnprefixedModels {
		t.Fatal("list-unprefixed-models = true, want false")
	}
	if cfg.EffectiveListUnprefixedModels() {
		t.Fatal("effective list-unprefixed-models = true, want false")
	}
}

func TestListUnprefixedModelsSerializationPreservesEffectiveBehavior(t *testing.T) {
	parsedFalse, errParse := ParseConfigBytes([]byte("list-unprefixed-models: false\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}

	testCases := []struct {
		name         string
		cfg          *Config
		want         bool
		wantExplicit bool
	}{
		{name: "zero value uses true default", cfg: &Config{}, want: true},
		{name: "programmatic false remains false", cfg: explicitListUnprefixedModelsConfig(false), want: false, wantExplicit: true},
		{name: "parsed false remains false", cfg: parsedFalse, want: false, wantExplicit: true},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.cfg.EffectiveListUnprefixedModels(); got != testCase.want {
				t.Fatalf("effective value = %t, want %t", got, testCase.want)
			}
			if testCase.cfg.ListUnprefixedModelsExplicit != testCase.wantExplicit {
				t.Fatalf("explicit marker = %t, want %t", testCase.cfg.ListUnprefixedModelsExplicit, testCase.wantExplicit)
			}

			for _, value := range []any{testCase.cfg, &testCase.cfg.SDKConfig} {
				data, errMarshal := yaml.Marshal(value)
				if errMarshal != nil {
					t.Fatalf("yaml.Marshal() error = %v", errMarshal)
				}
				var persisted struct {
					ListUnprefixedModels bool `yaml:"list-unprefixed-models"`
				}
				if errUnmarshal := yaml.Unmarshal(data, &persisted); errUnmarshal != nil {
					t.Fatalf("yaml.Unmarshal() error = %v; data=%s", errUnmarshal, data)
				}
				if persisted.ListUnprefixedModels != testCase.want {
					t.Fatalf("serialized value = %t, want %t; data=%s", persisted.ListUnprefixedModels, testCase.want, data)
				}
			}

			configPath := filepath.Join(t.TempDir(), "config.yaml")
			if errWrite := os.WriteFile(configPath, []byte("# keep this comment\nlist-unprefixed-models: false\n"), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			if errSave := SaveConfigPreserveComments(configPath, testCase.cfg); errSave != nil {
				t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
			}
			saved, errRead := os.ReadFile(configPath)
			if errRead != nil {
				t.Fatal(errRead)
			}
			wantLine := "list-unprefixed-models: " + boolString(testCase.want)
			if !strings.Contains(string(saved), wantLine) {
				t.Fatalf("saved config does not contain %q: %s", wantLine, saved)
			}
			if !strings.Contains(string(saved), "# keep this comment") {
				t.Fatalf("saved config lost the existing comment: %s", saved)
			}
		})
	}
}

func TestSaveConfigPreserveCommentsKeepsExplicitFalseWhenKeyIsNew(t *testing.T) {
	parsedFalse, errParse := ParseConfigBytes([]byte("list-unprefixed-models: false\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}

	for _, testCase := range []struct {
		name string
		cfg  *Config
	}{
		{name: "programmatic false", cfg: explicitListUnprefixedModelsConfig(false)},
		{name: "parsed false", cfg: parsedFalse},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			if errWrite := os.WriteFile(configPath, []byte("debug: true\n"), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			if errSave := SaveConfigPreserveComments(configPath, testCase.cfg); errSave != nil {
				t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
			}
			saved, errRead := os.ReadFile(configPath)
			if errRead != nil {
				t.Fatal(errRead)
			}
			if !strings.Contains(string(saved), "list-unprefixed-models: false") {
				t.Fatalf("saved config lost explicit false:\n%s", saved)
			}
		})
	}
}

func explicitListUnprefixedModelsConfig(enabled bool) *Config {
	cfg := &Config{}
	cfg.SetListUnprefixedModels(enabled)
	return cfg
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
