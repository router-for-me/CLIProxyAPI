package config

import "strings"

const (
	DefaultUsageStatisticsRetentionDays = 90
	MaxUsageStatisticsRetentionDays     = 3650
	DefaultUsagePricingCurrency         = "USD"
	DefaultUsagePricingVersion          = "default"
)

// UsagePricingConfig defines the currency, revision, and ordered price rules
// used to estimate request costs. Rules use first-match semantics.
type UsagePricingConfig struct {
	Currency string             `yaml:"currency,omitempty" json:"currency,omitempty"`
	Version  string             `yaml:"version,omitempty" json:"version,omitempty"`
	Rules    []UsagePricingRule `yaml:"rules,omitempty" json:"rules,omitempty"`
}

// UsagePricingRule prices token categories per one million tokens. Provider,
// model, and service tier accept glob patterns; "*" matches every value.
type UsagePricingRule struct {
	Provider             string `yaml:"provider" json:"provider"`
	Model                string `yaml:"model" json:"model"`
	ServiceTier          string `yaml:"service-tier,omitempty" json:"service-tier,omitempty"`
	InputPerMillion      string `yaml:"input-per-million,omitempty" json:"input-per-million,omitempty"`
	OutputPerMillion     string `yaml:"output-per-million,omitempty" json:"output-per-million,omitempty"`
	CacheReadPerMillion  string `yaml:"cache-read-per-million,omitempty" json:"cache-read-per-million,omitempty"`
	CacheWritePerMillion string `yaml:"cache-write-per-million,omitempty" json:"cache-write-per-million,omitempty"`
}

// NormalizeUsageConfig applies bounded usage-retention defaults and trims the
// optional pricing configuration. It deliberately does not inject model prices.
func (cfg *Config) NormalizeUsageConfig() {
	if cfg == nil {
		return
	}
	if cfg.UsageStatisticsRetentionDays <= 0 {
		cfg.UsageStatisticsRetentionDays = DefaultUsageStatisticsRetentionDays
	} else if cfg.UsageStatisticsRetentionDays > MaxUsageStatisticsRetentionDays {
		cfg.UsageStatisticsRetentionDays = MaxUsageStatisticsRetentionDays
	}

	cfg.UsagePricing.Currency = strings.ToUpper(strings.TrimSpace(cfg.UsagePricing.Currency))
	if cfg.UsagePricing.Currency == "" {
		cfg.UsagePricing.Currency = DefaultUsagePricingCurrency
	}
	cfg.UsagePricing.Version = strings.TrimSpace(cfg.UsagePricing.Version)
	if cfg.UsagePricing.Version == "" {
		cfg.UsagePricing.Version = DefaultUsagePricingVersion
	}
	for i := range cfg.UsagePricing.Rules {
		rule := &cfg.UsagePricing.Rules[i]
		rule.Provider = normalizeUsagePricingPattern(rule.Provider)
		rule.Model = normalizeUsagePricingPattern(rule.Model)
		rule.ServiceTier = normalizeUsagePricingPattern(rule.ServiceTier)
		rule.InputPerMillion = strings.TrimSpace(rule.InputPerMillion)
		rule.OutputPerMillion = strings.TrimSpace(rule.OutputPerMillion)
		rule.CacheReadPerMillion = strings.TrimSpace(rule.CacheReadPerMillion)
		rule.CacheWritePerMillion = strings.TrimSpace(rule.CacheWritePerMillion)
	}
}

func normalizeUsagePricingPattern(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "*"
	}
	return value
}
