package config

import (
	"encoding/json"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPublicConfigJSONKeepsSDKFieldsFlattened(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		payload      string
		want         bool
		wantExplicit bool
	}{
		{name: "absent", payload: `{}`, want: true},
		{name: "explicit false", payload: `{"list-unprefixed-models":false}`, want: false, wantExplicit: true},
		{name: "explicit true", payload: `{"list-unprefixed-models":true}`, want: true, wantExplicit: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var cfg Config
			if errUnmarshal := json.Unmarshal([]byte(testCase.payload), &cfg); errUnmarshal != nil {
				t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
			}
			if got := cfg.EffectiveListUnprefixedModels(); got != testCase.want {
				t.Fatalf("effective list-unprefixed-models = %t, want %t", got, testCase.want)
			}
			if cfg.ListUnprefixedModelsExplicit != testCase.wantExplicit {
				t.Fatalf("explicit marker = %t, want %t", cfg.ListUnprefixedModelsExplicit, testCase.wantExplicit)
			}
			if cfg.RequestLog || cfg.Debug {
				t.Fatalf("unexpected fields decoded from empty payload: %#v", cfg)
			}

			encoded, errMarshal := json.Marshal(cfg)
			if errMarshal != nil {
				t.Fatalf("json.Marshal() error = %v", errMarshal)
			}
			var fields map[string]json.RawMessage
			if errUnmarshal := json.Unmarshal(encoded, &fields); errUnmarshal != nil {
				t.Fatalf("json.Unmarshal(serialized) error = %v; data=%s", errUnmarshal, encoded)
			}
			if _, nested := fields["SDKConfig"]; nested {
				t.Fatalf("SDKConfig was nested instead of flattened: %s", encoded)
			}
			if _, explicit := fields["ListUnprefixedModelsExplicit"]; explicit {
				t.Fatalf("explicitness marker was serialized: %s", encoded)
			}
			var listUnprefixedModels bool
			if errUnmarshal := json.Unmarshal(fields["list-unprefixed-models"], &listUnprefixedModels); errUnmarshal != nil {
				t.Fatalf("json.Unmarshal(list-unprefixed-models) error = %v; data=%s", errUnmarshal, encoded)
			}
			if listUnprefixedModels != testCase.want {
				t.Fatalf("serialized list-unprefixed-models = %t, want %t: %s", listUnprefixedModels, testCase.want, encoded)
			}
		})
	}
}

func TestPublicConfigJSONMarshalsOuterFields(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		listValue    bool
		explicit     bool
		wantList     bool
		wantExplicit bool
	}{
		{name: "default", wantList: true},
		{name: "explicit false", listValue: false, explicit: true, wantList: false, wantExplicit: true},
		{name: "explicit true", listValue: true, explicit: true, wantList: true, wantExplicit: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := Config{
				SDKConfig: SDKConfig{RequestLog: true},
				Debug:     true,
				GeminiKey: []GeminiKey{{APIKey: "provider-key", Prefix: "team"}},
			}
			if testCase.explicit {
				cfg.SetListUnprefixedModels(testCase.listValue)
			}

			encoded, errMarshal := json.Marshal(cfg)
			if errMarshal != nil {
				t.Fatalf("json.Marshal() error = %v", errMarshal)
			}

			var fields map[string]json.RawMessage
			if errUnmarshal := json.Unmarshal(encoded, &fields); errUnmarshal != nil {
				t.Fatalf("json.Unmarshal(serialized) error = %v; data=%s", errUnmarshal, encoded)
			}
			if _, nested := fields["SDKConfig"]; nested {
				t.Fatalf("SDKConfig was nested instead of flattened: %s", encoded)
			}
			if _, explicit := fields["ListUnprefixedModelsExplicit"]; explicit {
				t.Fatalf("explicitness marker was serialized: %s", encoded)
			}

			var debug bool
			if errDecode := json.Unmarshal(fields["debug"], &debug); errDecode != nil {
				t.Fatalf("json.Unmarshal(debug) error = %v; data=%s", errDecode, encoded)
			}
			if !debug {
				t.Fatalf("serialized debug = false, want true: %s", encoded)
			}

			var requestLog bool
			if errDecode := json.Unmarshal(fields["request-log"], &requestLog); errDecode != nil {
				t.Fatalf("json.Unmarshal(request-log) error = %v; data=%s", errDecode, encoded)
			}
			if !requestLog {
				t.Fatalf("serialized request-log = false, want true: %s", encoded)
			}

			var providers []GeminiKey
			if errDecode := json.Unmarshal(fields["gemini-api-key"], &providers); errDecode != nil {
				t.Fatalf("json.Unmarshal(gemini-api-key) error = %v; data=%s", errDecode, encoded)
			}
			if len(providers) != 1 || providers[0].Prefix != "team" {
				t.Fatalf("serialized provider = %#v, want one provider with prefix team: %s", providers, encoded)
			}

			var listUnprefixedModels bool
			if errDecode := json.Unmarshal(fields["list-unprefixed-models"], &listUnprefixedModels); errDecode != nil {
				t.Fatalf("json.Unmarshal(list-unprefixed-models) error = %v; data=%s", errDecode, encoded)
			}
			if listUnprefixedModels != testCase.wantList {
				t.Fatalf("serialized list-unprefixed-models = %t, want %t: %s", listUnprefixedModels, testCase.wantList, encoded)
			}
			if cfg.ListUnprefixedModelsExplicit != testCase.wantExplicit {
				t.Fatalf("explicit marker = %t, want %t", cfg.ListUnprefixedModelsExplicit, testCase.wantExplicit)
			}
		})
	}
}

func TestPublicConfigJSONDecodesFlattenedFields(t *testing.T) {
	var cfg Config
	if errUnmarshal := json.Unmarshal([]byte(`{
		"request-log": true,
		"debug": true
	}`), &cfg); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	if !cfg.RequestLog || !cfg.Debug {
		t.Fatalf("flattened fields were not decoded: %#v", cfg)
	}
}

func TestPublicConfigYAMLListUnprefixedModelsTracksPresence(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		payload      string
		want         bool
		wantExplicit bool
	}{
		{name: "absent", payload: "{}\n", want: true},
		{name: "explicit false", payload: "list-unprefixed-models: false\n", want: false, wantExplicit: true},
		{name: "explicit true", payload: "list-unprefixed-models: true\n", want: true, wantExplicit: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var cfg Config
			if errUnmarshal := yaml.Unmarshal([]byte(testCase.payload), &cfg); errUnmarshal != nil {
				t.Fatalf("yaml.Unmarshal() error = %v", errUnmarshal)
			}
			if got := cfg.EffectiveListUnprefixedModels(); got != testCase.want {
				t.Fatalf("effective list-unprefixed-models = %t, want %t", got, testCase.want)
			}
			if cfg.ListUnprefixedModelsExplicit != testCase.wantExplicit {
				t.Fatalf("explicit marker = %t, want %t", cfg.ListUnprefixedModelsExplicit, testCase.wantExplicit)
			}
		})
	}
}

func TestPublicConfigYAMLDecodesFlattenedFields(t *testing.T) {
	var cfg Config
	if errUnmarshal := yaml.Unmarshal([]byte("list-unprefixed-models: false\nrequest-log: true\ndebug: true\n"), &cfg); errUnmarshal != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", errUnmarshal)
	}
	if cfg.EffectiveListUnprefixedModels() {
		t.Fatal("effective list-unprefixed-models = true, want false")
	}
	if !cfg.RequestLog || !cfg.Debug {
		t.Fatalf("flattened fields were not decoded: %#v", cfg)
	}
}
