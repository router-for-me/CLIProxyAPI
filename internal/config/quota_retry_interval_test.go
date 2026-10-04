package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestQuotaRetryInterval_V8RoundTrip(t *testing.T) {
	var cfg Config
	if err := yaml.Unmarshal([]byte("routing:\n  cooldown:\n    quota-retry-interval-seconds: 300\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.QuotaRetryIntervalSeconds != 300 {
		t.Fatalf("interval=%d, want 300", cfg.QuotaRetryIntervalSeconds)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("routing:\n  cooldown:\n    quota-retry-interval-seconds: 300\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SaveConfigPreserveComments(path, &cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.QuotaRetryIntervalSeconds != 300 {
		t.Fatal("interval lost during v8 round trip")
	}
	var doc map[string]any
	if err = yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if _, flat := doc["quota-retry-interval-seconds"]; flat {
		t.Fatal("new config serialized outside v8 routing.cooldown")
	}
}
