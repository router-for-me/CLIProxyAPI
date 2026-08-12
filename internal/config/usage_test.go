package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseConfigBytesUsageDefaultsAndBounds(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte("usage-statistics-enabled: true\nusage-statistics-retention-days: 99999\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if cfg.UsageStatisticsRetentionDays != MaxUsageStatisticsRetentionDays {
		t.Fatalf("retention = %d, want %d", cfg.UsageStatisticsRetentionDays, MaxUsageStatisticsRetentionDays)
	}
	if cfg.UsagePricing.Currency != DefaultUsagePricingCurrency || cfg.UsagePricing.Version != DefaultUsagePricingVersion {
		t.Fatalf("pricing defaults = %#v", cfg.UsagePricing)
	}

	cfg, errParse = ParseConfigBytes([]byte("usage-statistics-retention-days: 0\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if cfg.UsageStatisticsRetentionDays != DefaultUsageStatisticsRetentionDays {
		t.Fatalf("retention = %d, want default %d", cfg.UsageStatisticsRetentionDays, DefaultUsageStatisticsRetentionDays)
	}
}

func TestNormalizeUsageConfigRules(t *testing.T) {
	cfg := &Config{UsagePricing: UsagePricingConfig{
		Currency: " cny ",
		Version:  " rates-1 ",
		Rules: []UsagePricingRule{{
			Provider: " ", Model: " gpt-* ", ServiceTier: " ", InputPerMillion: " 1.25 ",
		}},
	}}
	cfg.NormalizeUsageConfig()
	if cfg.UsagePricing.Currency != "CNY" || cfg.UsagePricing.Version != "rates-1" {
		t.Fatalf("normalized pricing = %#v", cfg.UsagePricing)
	}
	rule := cfg.UsagePricing.Rules[0]
	if rule.Provider != "*" || rule.Model != "gpt-*" || rule.ServiceTier != "*" || rule.InputPerMillion != "1.25" {
		t.Fatalf("normalized rule = %#v", rule)
	}
}

func TestSaveConfigPreserveCommentsPrunesUsageDefaults(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(configPath, []byte("debug: true\n"), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}
	cfg := &Config{
		Debug:                        true,
		UsageStatisticsRetentionDays: DefaultUsageStatisticsRetentionDays,
		UsagePricing: UsagePricingConfig{
			Currency: DefaultUsagePricingCurrency,
			Version:  DefaultUsagePricingVersion,
		},
	}
	if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
	}
	data, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	text := string(data)
	for _, unexpected := range []string{"usage-statistics-retention-days:", "usage-pricing:"} {
		if strings.Contains(text, unexpected) {
			t.Fatalf("saved config contains default %q:\n%s", unexpected, text)
		}
	}
}
