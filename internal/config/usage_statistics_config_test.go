package config

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestUsageStatisticsConfigDefaults(t *testing.T) {
	yamlInput := `
usage-statistics:
  enabled: true
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(yamlInput), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !cfg.UsageStatistics.Enabled {
		t.Error("expected enabled=true")
	}
	if cfg.UsageStatistics.Table != "" {
		// Defaults are applied in ParseConfigBytes, not raw unmarshal.
		// This test verifies the raw YAML field mapping works.
		t.Logf("table=%q (raw unmarshal; defaults applied later)", cfg.UsageStatistics.Table)
	}
}

func TestUsageStatisticsConfigWithPrices(t *testing.T) {
	yamlInput := `
usage-statistics:
  enabled: true
  postgres-dsn: "postgres://user:pass@localhost:5432/db"
  postgres-schema: "myschema"
  table: "custom_usage"
  prices:
    - provider: "openai"
      model: "gpt-4.1-mini"
      input_cost_per_token: 0.0000004
      output_cost_per_token: 0.0000016
    - provider: "claude"
      model: "claude-sonnet-4"
      input_cost_per_token: 0.000003
      output_cost_per_token: 0.000015
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(yamlInput), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !cfg.UsageStatistics.Enabled {
		t.Error("expected enabled=true")
	}
	if cfg.UsageStatistics.PostgresDSN != "postgres://user:pass@localhost:5432/db" {
		t.Errorf("dsn = %q", cfg.UsageStatistics.PostgresDSN)
	}
	if cfg.UsageStatistics.Schema != "myschema" {
		t.Errorf("schema = %q", cfg.UsageStatistics.Schema)
	}
	if cfg.UsageStatistics.Table != "custom_usage" {
		t.Errorf("table = %q", cfg.UsageStatistics.Table)
	}
	if len(cfg.UsageStatistics.Prices) != 2 {
		t.Fatalf("prices count = %d, want 2", len(cfg.UsageStatistics.Prices))
	}
	p := cfg.UsageStatistics.Prices[0]
	if p.Provider != "openai" || p.Model != "gpt-4.1-mini" {
		t.Errorf("price[0] = %+v", p)
	}
	if p.InputCostPerToken != 0.0000004 {
		t.Errorf("input cost = %v", p.InputCostPerToken)
	}
}

func TestUsageStatisticsConfigDSNNotInJSON(t *testing.T) {
	cfg := Config{
		SDKConfig: SDKConfig{},
	}
	cfg.UsageStatistics.Enabled = true
	cfg.UsageStatistics.PostgresDSN = "postgres://secret:password@localhost/db"
	cfg.UsageStatistics.Table = "usage_records"

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	jsonStr := string(data)

	// The DSN must NOT appear in JSON output.
	if strings.Contains(jsonStr, "secret:password") {
		t.Error("DSN secret leaked into JSON output")
	}
	if strings.Contains(jsonStr, "postgres://secret") {
		t.Error("DSN leaked into JSON output")
	}
	// The enabled flag should be present.
	if !strings.Contains(jsonStr, `"enabled":true`) {
		t.Error("enabled flag not in JSON output")
	}
}

func TestUsageStatisticsConfigDisabledByDefault(t *testing.T) {
	yamlInput := `port: 8080`
	var cfg Config
	if err := yaml.Unmarshal([]byte(yamlInput), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.UsageStatistics.Enabled {
		t.Error("expected disabled by default")
	}
}

func TestParseConfigBytesUsageStatisticsTableDefault(t *testing.T) {
	yamlInput := `
port: 8080
usage-statistics:
  enabled: true
`
	parsed, err := ParseConfigBytes([]byte(yamlInput))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.UsageStatistics.Table != "usage_records" {
		t.Errorf("table = %q, want usage_records", parsed.UsageStatistics.Table)
	}
}

