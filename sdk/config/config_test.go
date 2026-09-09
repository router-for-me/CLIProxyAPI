package config

import (
	"encoding/json"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPublicConfigJSONKeepsSDKFieldsFlattened(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		payload     string
		want        bool
		wantPresent bool
	}{
		{name: "absent", payload: `{}`, want: true},
		{name: "explicit false", payload: `{"list-unprefixed-models":false}`, want: false, wantPresent: true},
		{name: "explicit true", payload: `{"list-unprefixed-models":true}`, want: true, wantPresent: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var cfg Config
			if errUnmarshal := json.Unmarshal([]byte(testCase.payload), &cfg); errUnmarshal != nil {
				t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
			}
			if got := cfg.EffectiveListUnprefixedModels(); got != testCase.want {
				t.Fatalf("effective list-unprefixed-models = %t, want %t", got, testCase.want)
			}
			if gotPresent := cfg.ListUnprefixedModels != nil; gotPresent != testCase.wantPresent {
				t.Fatalf("value presence = %t, want %t", gotPresent, testCase.wantPresent)
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
			var roundTrip Config
			if errUnmarshal := json.Unmarshal(encoded, &roundTrip); errUnmarshal != nil {
				t.Fatalf("json.Unmarshal(round trip) error = %v; data=%s", errUnmarshal, encoded)
			}
			if got := roundTrip.EffectiveListUnprefixedModels(); got != testCase.want {
				t.Fatalf("round-trip list-unprefixed-models = %t, want %t: %s", got, testCase.want, encoded)
			}
		})
	}
}

func TestPublicConfigJSONMarshalsOuterFields(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		listValue bool
		explicit  bool
		wantList  bool
	}{
		{name: "default", wantList: true},
		{name: "explicit false", listValue: false, explicit: true, wantList: false},
		{name: "explicit true", listValue: true, explicit: true, wantList: true},
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

			var roundTrip Config
			if errDecode := json.Unmarshal(encoded, &roundTrip); errDecode != nil {
				t.Fatalf("json.Unmarshal(round trip) error = %v; data=%s", errDecode, encoded)
			}
			if got := roundTrip.EffectiveListUnprefixedModels(); got != testCase.wantList {
				t.Fatalf("round-trip list-unprefixed-models = %t, want %t: %s", got, testCase.wantList, encoded)
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
		name        string
		payload     string
		want        bool
		wantPresent bool
	}{
		{name: "absent", payload: "{}\n", want: true},
		{name: "explicit false", payload: "list-unprefixed-models: false\n", want: false, wantPresent: true},
		{name: "explicit true", payload: "list-unprefixed-models: true\n", want: true, wantPresent: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var cfg Config
			if errUnmarshal := yaml.Unmarshal([]byte(testCase.payload), &cfg); errUnmarshal != nil {
				t.Fatalf("yaml.Unmarshal() error = %v", errUnmarshal)
			}
			if got := cfg.EffectiveListUnprefixedModels(); got != testCase.want {
				t.Fatalf("effective list-unprefixed-models = %t, want %t", got, testCase.want)
			}
			if gotPresent := cfg.ListUnprefixedModels != nil; gotPresent != testCase.wantPresent {
				t.Fatalf("value presence = %t, want %t", gotPresent, testCase.wantPresent)
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

func TestPublicSDKConfigCanBeEmbeddedWithoutDroppingOuterFields(t *testing.T) {
	type wrapper struct {
		SDKConfig `yaml:",inline"`
		Name      string `yaml:"name" json:"name"`
	}

	cfg := wrapper{SDKConfig: SDKConfig{RequestLog: true}, Name: "example"}
	cfg.SetListUnprefixedModels(false)

	jsonData, errJSON := json.Marshal(cfg)
	if errJSON != nil {
		t.Fatalf("json.Marshal() error = %v", errJSON)
	}
	var jsonRoundTrip wrapper
	if errUnmarshal := json.Unmarshal(jsonData, &jsonRoundTrip); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v; data=%s", errUnmarshal, jsonData)
	}
	assertEmbeddedSDKConfig(t, jsonRoundTrip)

	yamlData, errYAML := yaml.Marshal(cfg)
	if errYAML != nil {
		t.Fatalf("yaml.Marshal() error = %v", errYAML)
	}
	var yamlRoundTrip wrapper
	if errUnmarshal := yaml.Unmarshal(yamlData, &yamlRoundTrip); errUnmarshal != nil {
		t.Fatalf("yaml.Unmarshal() error = %v; data=%s", errUnmarshal, yamlData)
	}
	assertEmbeddedSDKConfig(t, yamlRoundTrip)
}

func assertEmbeddedSDKConfig(t *testing.T, cfg struct {
	SDKConfig `yaml:",inline"`
	Name      string `yaml:"name" json:"name"`
}) {
	t.Helper()
	if cfg.Name != "example" || !cfg.RequestLog || cfg.EffectiveListUnprefixedModels() {
		t.Fatalf("embedded config did not round-trip: %#v", cfg)
	}
}
