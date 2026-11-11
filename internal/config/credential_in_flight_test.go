package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestLoadConfigOptionalMissingFallbackAppliesCredentialInFlightDefaults(t *testing.T) {
	cfg, errLoad := LoadConfigOptional(filepath.Join(t.TempDir(), "missing.yaml"), true)
	if errLoad != nil {
		t.Fatalf("LoadConfigOptional() error = %v", errLoad)
	}
	assertOptionalConfigFallback(t, cfg)
}

func TestLoadConfigOptionalEmptyFallbackAppliesCredentialInFlightDefaults(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(configPath, nil, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errLoad := LoadConfigOptional(configPath, true)
	if errLoad != nil {
		t.Fatalf("LoadConfigOptional() error = %v", errLoad)
	}
	assertOptionalConfigFallback(t, cfg)
}

func TestLoadConfigOptionalWhitespaceFallbackAppliesCredentialInFlightDefaults(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(configPath, []byte(" \t\n\r "), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errLoad := LoadConfigOptional(configPath, true)
	if errLoad != nil {
		t.Fatalf("LoadConfigOptional() error = %v", errLoad)
	}
	assertOptionalConfigFallback(t, cfg)
}

func TestLoadConfigOptionalInvalidFallbackAppliesCredentialInFlightDefaults(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(configPath, []byte(":"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errLoad := LoadConfigOptional(configPath, true)
	if errLoad != nil {
		t.Fatalf("LoadConfigOptional() error = %v", errLoad)
	}
	assertOptionalConfigFallback(t, cfg)
}

func assertOptionalConfigFallback(t *testing.T, cfg *Config) {
	t.Helper()
	if cfg.CredentialInFlight != DefaultCredentialInFlightConfig() {
		t.Fatalf("CredentialInFlight = %#v, want %#v", cfg.CredentialInFlight, DefaultCredentialInFlightConfig())
	}
	if errValidate := cfg.CredentialInFlight.Validate(); errValidate != nil {
		t.Fatalf("CredentialInFlight.Validate() error = %v", errValidate)
	}
	if cfg.ErrorLogsMaxFiles != 0 || cfg.WebsocketAuth || cfg.CredentialConcurrency != (CredentialConcurrencyConfig{}) {
		t.Fatalf("fallback config changed existing empty-config defaults: %#v", cfg)
	}
}

func TestCredentialInFlightConfigContractFixture(t *testing.T) {
	raw, errRead := os.ReadFile(filepath.Join("..", "home", "testdata", "credential_in_flight_contract.json"))
	if errRead != nil {
		t.Fatal(errRead)
	}
	fixture, errDecode := decodeCredentialInFlightConfigFixture(raw)
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if fixture.Config != DefaultCredentialInFlightConfig() {
		t.Fatalf("default config = %#v, want %#v", DefaultCredentialInFlightConfig(), fixture.Config)
	}
	if errValidate := fixture.Config.Validate(); errValidate != nil {
		t.Fatalf("Validate() error = %v", errValidate)
	}
	assertCredentialInFlightConfigFields(t)
	assertRequiredJSONKeys(t, raw, []string{"config", "part", "overflow"})
	assertRequiredJSONKeys(t, fixture.ConfigJSON, []string{"snapshot-interval", "stale-after", "max-part-bytes", "max-part-count", "max-revision-bytes", "max-aggregate-groups", "max-details", "max-string-bytes", "staging-retention"})
}

func TestCredentialInFlightConfigFixtureRejectsInvalidJSON(t *testing.T) {
	raw, errRead := os.ReadFile(filepath.Join("..", "home", "testdata", "credential_in_flight_contract.json"))
	if errRead != nil {
		t.Fatal(errRead)
	}
	for _, test := range []struct {
		name string
		raw  []byte
	}{
		{name: "unknown config field", raw: bytes.Replace(raw, []byte(`"snapshot-interval": "2s"`), []byte(`"snapshot-interval": "2s", "secret": "secret"`), 1)},
		{name: "trailing JSON", raw: append(append([]byte{}, raw...), []byte(` {"config": {}}`)...)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, errDecode := decodeCredentialInFlightConfigFixture(test.raw); errDecode == nil {
				t.Fatal("decodeCredentialInFlightConfigFixture() error = nil")
			}
		})
	}
}

func TestCredentialInFlightConfigDurationBounds(t *testing.T) {
	for _, test := range []struct {
		name  string
		stale string
		every string
		valid bool
	}{
		{name: "exact three intervals", every: "1s", stale: "3s", valid: true},
		{name: "below three intervals", every: "1s", stale: "2999999999ns", valid: false},
		{name: "near duration maximum", every: time.Duration(math.MaxInt64 / 2).String(), stale: time.Duration(math.MaxInt64).String(), valid: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := DefaultCredentialInFlightConfig()
			cfg.SnapshotInterval = test.every
			cfg.StaleAfter = test.stale
			errValidate := cfg.Validate()
			if (errValidate == nil) != test.valid {
				t.Fatalf("Validate() error = %v, want valid = %t", errValidate, test.valid)
			}
		})
	}
}

func TestCredentialInFlightConfigRejectsUnsafeBounds(t *testing.T) {
	cfg := DefaultCredentialInFlightConfig()
	cfg.StaleAfter = "5s"
	if errValidate := cfg.Validate(); errValidate == nil {
		t.Fatal("Validate() error = nil, want stale-after error")
	}
	cfg = DefaultCredentialInFlightConfig()
	cfg.MaxRevisionBytes = 16*1024*1024 + 1
	if errValidate := cfg.Validate(); errValidate == nil {
		t.Fatal("Validate() error = nil, want hard revision bound error")
	}
	cfg = DefaultCredentialInFlightConfig()
	cfg.MaxPartBytes = math.MaxInt
	if errValidate := cfg.Validate(); errValidate == nil {
		t.Fatal("Validate() error = nil, want overflow-safe part bound error")
	}
}

type credentialInFlightConfigFixture struct {
	Config     CredentialInFlightConfig `json:"config"`
	ConfigJSON json.RawMessage          `json:"-"`
}

func decodeCredentialInFlightConfigFixture(raw []byte) (credentialInFlightConfigFixture, error) {
	var fixture credentialInFlightConfigFixture
	var document struct {
		Config   json.RawMessage `json:"config"`
		Part     json.RawMessage `json:"part"`
		Overflow json.RawMessage `json:"overflow"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if errDecode := decoder.Decode(&document); errDecode != nil {
		return fixture, errDecode
	}
	if errDecode := decoder.Decode(&struct{}{}); errDecode == nil {
		return fixture, errors.New("unexpected trailing JSON")
	} else if errDecode != io.EOF {
		return fixture, errDecode
	}
	decoder = json.NewDecoder(bytes.NewReader(document.Config))
	decoder.DisallowUnknownFields()
	if errDecode := decoder.Decode(&fixture.Config); errDecode != nil {
		return fixture, errDecode
	}
	if errDecode := decoder.Decode(&struct{}{}); errDecode == nil {
		return fixture, errors.New("unexpected trailing config JSON")
	} else if errDecode != io.EOF {
		return fixture, errDecode
	}
	fixture.ConfigJSON = document.Config
	return fixture, nil
}

func assertCredentialInFlightConfigFields(t *testing.T) {
	t.Helper()
	assertOrderedJSONFields(t, reflect.TypeOf(CredentialInFlightConfig{}), []jsonField{
		{name: "SnapshotInterval", tag: "snapshot-interval"},
		{name: "StaleAfter", tag: "stale-after"},
		{name: "MaxPartBytes", tag: "max-part-bytes"},
		{name: "MaxPartCount", tag: "max-part-count"},
		{name: "MaxRevisionBytes", tag: "max-revision-bytes"},
		{name: "MaxAggregateGroups", tag: "max-aggregate-groups"},
		{name: "MaxDetails", tag: "max-details"},
		{name: "MaxStringBytes", tag: "max-string-bytes"},
		{name: "StagingRetention", tag: "staging-retention"},
	})
}

type jsonField struct {
	name string
	tag  string
}

func assertOrderedJSONFields(t *testing.T, structType reflect.Type, want []jsonField) {
	t.Helper()
	if structType.NumField() != len(want) {
		t.Fatalf("%s field count = %d, want %d", structType.Name(), structType.NumField(), len(want))
	}
	for index, expected := range want {
		field := structType.Field(index)
		if field.Name != expected.name || field.Tag.Get("json") != expected.tag {
			t.Fatalf("%s field %d = (%q, %q), want (%q, %q)", structType.Name(), index, field.Name, field.Tag.Get("json"), expected.name, expected.tag)
		}
	}
}

func assertRequiredJSONKeys(t *testing.T, raw json.RawMessage, required []string) {
	t.Helper()
	var fields map[string]json.RawMessage
	if errDecode := json.Unmarshal(raw, &fields); errDecode != nil {
		t.Fatalf("json.Unmarshal() error = %v", errDecode)
	}
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			t.Fatalf("required JSON key %q is missing", key)
		}
	}
}
