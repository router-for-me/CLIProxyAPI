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
		name        string
		payload     string
		want        bool
		wantPresent bool
	}{
		{name: "default", payload: `{}`, want: true},
		{name: "explicit false", payload: `{"list-unprefixed-models":false}`, want: false, wantPresent: true},
		{name: "explicit true", payload: `{"list-unprefixed-models":true}`, want: true, wantPresent: true},
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
			if gotPresent := cfg.ListUnprefixedModels != nil; gotPresent != testCase.wantPresent {
				t.Fatalf("value presence = %t, want %t", gotPresent, testCase.wantPresent)
			}

			for _, value := range []any{cfg, &cfg} {
				encoded, errMarshal := json.Marshal(value)
				if errMarshal != nil {
					t.Fatalf("json.Marshal() error = %v", errMarshal)
				}
				var serialized SDKConfig
				if errUnmarshal := json.Unmarshal(encoded, &serialized); errUnmarshal != nil {
					t.Fatalf("json.Unmarshal(serialized) error = %v; data=%s", errUnmarshal, encoded)
				}
				if got := serialized.EffectiveListUnprefixedModels(); got != testCase.want {
					t.Fatalf("round-trip value = %t, want %t; data=%s", got, testCase.want, encoded)
				}
			}
		})
	}
}

func TestSDKConfigYAMLListUnprefixedModelsTracksPresence(t *testing.T) {
	testCases := []struct {
		name        string
		payload     string
		want        bool
		wantPresent bool
	}{
		{name: "absent", payload: "{}\n", want: true},
		{name: "explicit false", payload: "list-unprefixed-models: false\n", want: false, wantPresent: true},
		{name: "explicit true", payload: "list-unprefixed-models: true\n", want: true, wantPresent: true},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var cfg SDKConfig
			if errUnmarshal := yaml.Unmarshal([]byte(testCase.payload), &cfg); errUnmarshal != nil {
				t.Fatalf("yaml.Unmarshal() error = %v", errUnmarshal)
			}
			if got := cfg.EffectiveListUnprefixedModels(); got != testCase.want {
				t.Fatalf("effective value = %t, want %t", got, testCase.want)
			}
			if gotPresent := cfg.ListUnprefixedModels != nil; gotPresent != testCase.wantPresent {
				t.Fatalf("value presence = %t, want %t", gotPresent, testCase.wantPresent)
			}

			for _, value := range []any{cfg, &cfg} {
				encoded, errMarshal := yaml.Marshal(value)
				if errMarshal != nil {
					t.Fatalf("yaml.Marshal() error = %v", errMarshal)
				}
				var serialized SDKConfig
				if errUnmarshal := yaml.Unmarshal(encoded, &serialized); errUnmarshal != nil {
					t.Fatalf("yaml.Unmarshal(serialized) error = %v; data=%s", errUnmarshal, encoded)
				}
				if got := serialized.EffectiveListUnprefixedModels(); got != testCase.want {
					t.Fatalf("round-trip value = %t, want %t; data=%s", got, testCase.want, encoded)
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
	if cfg.ListUnprefixedModels != nil {
		t.Fatalf("list-unprefixed-models default pointer = %v, want nil", *cfg.ListUnprefixedModels)
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
	if cfg.ListUnprefixedModels == nil || *cfg.ListUnprefixedModels {
		t.Fatal("list-unprefixed-models is not an explicit false")
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
	if cfg.ListUnprefixedModels == nil || *cfg.ListUnprefixedModels {
		t.Fatal("list-unprefixed-models is not an explicit false")
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
		name        string
		cfg         *Config
		want        bool
		wantPresent bool
	}{
		{name: "zero value uses true default", cfg: &Config{}, want: true},
		{name: "programmatic false remains false", cfg: explicitListUnprefixedModelsConfig(false), want: false, wantPresent: true},
		{name: "parsed false remains false", cfg: parsedFalse, want: false, wantPresent: true},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.cfg.EffectiveListUnprefixedModels(); got != testCase.want {
				t.Fatalf("effective value = %t, want %t", got, testCase.want)
			}
			if gotPresent := testCase.cfg.ListUnprefixedModels != nil; gotPresent != testCase.wantPresent {
				t.Fatalf("value presence = %t, want %t", gotPresent, testCase.wantPresent)
			}

			for _, value := range []any{testCase.cfg, testCase.cfg.SDKConfig, &testCase.cfg.SDKConfig} {
				data, errMarshal := yaml.Marshal(value)
				if errMarshal != nil {
					t.Fatalf("yaml.Marshal() error = %v", errMarshal)
				}
				var persisted SDKConfig
				if errUnmarshal := yaml.Unmarshal(data, &persisted); errUnmarshal != nil {
					t.Fatalf("yaml.Unmarshal() error = %v; data=%s", errUnmarshal, data)
				}
				if got := persisted.EffectiveListUnprefixedModels(); got != testCase.want {
					t.Fatalf("round-trip value = %t, want %t; data=%s", got, testCase.want, data)
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

func TestConfigYAMLMarshalsAndUnmarshalsWithoutDuplicateSDKFields(t *testing.T) {
	cfg := &Config{
		SDKConfig: SDKConfig{
			ProxyURL:   "https://proxy.example.test",
			RequestLog: true,
		},
		Host:  "127.0.0.1",
		Port:  8317,
		Debug: true,
	}
	cfg.SetListUnprefixedModels(false)

	data, errMarshal := yaml.Marshal(cfg)
	if errMarshal != nil {
		t.Fatalf("yaml.Marshal() error = %v", errMarshal)
	}

	var decoded Config
	if errUnmarshal := yaml.Unmarshal(data, &decoded); errUnmarshal != nil {
		t.Fatalf("yaml.Unmarshal() error = %v; data=%s", errUnmarshal, data)
	}
	if decoded.ProxyURL != cfg.ProxyURL || !decoded.RequestLog {
		t.Fatalf("SDK fields did not round-trip: got proxy-url=%q request-log=%t", decoded.ProxyURL, decoded.RequestLog)
	}
	if decoded.Host != cfg.Host || decoded.Port != cfg.Port || !decoded.Debug {
		t.Fatalf("outer fields did not round-trip: got host=%q port=%d debug=%t", decoded.Host, decoded.Port, decoded.Debug)
	}
	if decoded.EffectiveListUnprefixedModels() {
		t.Fatal("effective list-unprefixed-models = true, want false")
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
