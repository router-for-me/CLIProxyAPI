package config

import (
	"gopkg.in/yaml.v3"
	"testing"
)

func TestAccountingOutboxConfiguration(t *testing.T) {
	var cfg Config
	if err := yaml.Unmarshal([]byte("observability:\n  accounting-outbox:\n    enabled: true\n    data-path: /data/accounting\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.AccountingOutbox.Enabled || cfg.AccountingOutbox.DataPath != "/data/accounting" {
		t.Fatalf("%+v", cfg.AccountingOutbox)
	}
	defaults := cfg.AccountingOutbox.Defaults()
	if defaults.QueueCapacity != 512 || defaults.MaxDiskMB != 256 || defaults.MaxEvents != 100000 || defaults.RetentionHours != 168 || defaults.ShutdownSeconds != 5 {
		t.Fatalf("%+v", defaults)
	}
	for _, input := range []string{"enabled: true", "queue-capacity: -1", "max-disk-mb: 7", "max-events: -1", "retention-hours: -1", "shutdown-seconds: 31"} {
		cfg = Config{}
		if err := yaml.Unmarshal([]byte("observability:\n  accounting-outbox:\n    "+input+"\n"), &cfg); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
}

func TestAccountingPricingConfiguration(t *testing.T) {
	var cfg Config
	raw := `observability:
  accounting-outbox:
    pricing:
      litellm-catalog-path: /prices/catalog.json
      overrides:
        - provider: openai
          model: m
          currency: USD
          tier: priority
          min-context: 101
          max-context: 200
          input: "0.000001"
          cache-read: "0"
      aliases:
        - provider: codex
          model: reported
          target-provider: openai
          target-model: m
`
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	pricing := cfg.AccountingOutbox.Pricing
	if pricing.LiteLLMCatalogPath != "/prices/catalog.json" || len(pricing.Overrides) != 1 || len(pricing.Aliases) != 1 {
		t.Fatalf("%+v", pricing)
	}
	rate := pricing.Overrides[0]
	if rate.MinContext != 101 || rate.MaxContext != 200 || rate.Input == nil || *rate.Input != "0.000001" || rate.CacheRead == nil || *rate.CacheRead != "0" || rate.Output != nil {
		t.Fatalf("%+v", rate)
	}
}

func TestLiteLLMExporterConfiguration(t *testing.T) {
	cfg := AccountingOutboxConfig{Enabled: true, DataPath: "/data/accounting", LiteLLM: LiteLLMExporterConfig{Enabled: true, URL: "https://accounting.example", AdminKeyEnv: "LITELLM_ACCOUNTING_ADMIN_KEY"}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, url := range []string{"https://user:credential@accounting.example", "https://accounting.example?token=credential", "https://accounting.example#credential", "file:///credential"} {
		cfg.LiteLLM.URL = url
		if err := cfg.Validate(); err == nil {
			t.Fatal("unsafe URL accepted")
		}
	}
}
