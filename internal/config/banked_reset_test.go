package config

import (
	"gopkg.in/yaml.v3"
	"testing"
)

func TestBankedResetConfig(t *testing.T) {
	for _, tc := range []struct {
		yaml  string
		valid bool
	}{
		{`quota-exceeded: {banked-reset: {mode: auto, use-before: 2h, providers: [codex]}}`, true},
		{`quota-exceeded: {banked-reset: {mode: manual, use-before: 0s}}`, true},
		{`quota-exceeded: {banked-reset: {mode: on}}`, false},
		{`quota-exceeded: {banked-reset: {mode: auto, use-before: nope}}`, false},
		{`quota-exceeded: {banked-reset: {mode: auto, use-before: -1h}}`, false},
		{`quota-exceeded: {banked-reset: {mode: auto, providers: [typo]}}`, false},
	} {
		var cfg Config
		err := yaml.Unmarshal([]byte(tc.yaml), &cfg)
		if (err == nil) != tc.valid {
			t.Errorf("%s: %v", tc.yaml, err)
		}
	}
	var cfg Config
	if err := yaml.Unmarshal([]byte(`server: {port: 8317}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.QuotaExceeded.BankedReset.Mode != "" {
		t.Fatal("default not off")
	}
}

func TestBankedResetV8RoundTrip(t *testing.T) {
	var cfg Config
	if err := yaml.Unmarshal([]byte(`config-version: 8
quota-exceeded:
  banked-reset:
    mode: auto
    use-before: 2h
    providers: [codex]
`), &cfg); err != nil {
		t.Fatal(err)
	}
	data, err := yaml.Marshal(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	var loaded Config
	if err = yaml.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.QuotaExceeded.BankedReset.Mode != "auto" || len(loaded.QuotaExceeded.BankedReset.Providers) != 1 {
		t.Fatalf("lost policy: %+v", loaded.QuotaExceeded.BankedReset)
	}
}
