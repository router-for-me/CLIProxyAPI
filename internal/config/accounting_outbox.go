package config

import (
	"fmt"
	"net/url"
	"strings"
)

type PriceRate struct {
	Provider      string  `yaml:"provider" json:"provider"`
	Model         string  `yaml:"model" json:"model"`
	Tier          string  `yaml:"tier" json:"tier"`
	MinContext    int64   `yaml:"min-context" json:"min_context"`
	MaxContext    int64   `yaml:"max-context" json:"max_context"`
	Currency      string  `yaml:"currency" json:"currency"`
	Input         *string `yaml:"input" json:"input,omitempty"`
	Output        *string `yaml:"output" json:"output,omitempty"`
	CacheRead     *string `yaml:"cache-read" json:"cache_read,omitempty"`
	CacheCreation *string `yaml:"cache-creation" json:"cache_creation,omitempty"`
	Reasoning     *string `yaml:"reasoning" json:"reasoning,omitempty"`
}

type PriceAlias struct {
	Provider       string `yaml:"provider" json:"provider"`
	Model          string `yaml:"model" json:"model"`
	TargetProvider string `yaml:"target-provider" json:"target_provider"`
	TargetModel    string `yaml:"target-model" json:"target_model"`
}

type PricingConfig struct {
	LiteLLMCatalogPath string       `yaml:"litellm-catalog-path" json:"litellm-catalog-path"`
	Overrides          []PriceRate  `yaml:"overrides" json:"overrides"`
	Aliases            []PriceAlias `yaml:"aliases" json:"aliases"`
}

type LiteLLMExporterConfig struct {
	ReceiverProfile string                     `yaml:"receiver-profile" json:"receiver-profile"`
	Enabled         bool                       `yaml:"enabled" json:"enabled"`
	URL             string                     `yaml:"url" json:"url"`
	Version         string                     `yaml:"version" json:"version"`
	AdminKeyEnv     string                     `yaml:"admin-key-env" json:"admin-key-env"`
	Clients         map[string]LiteLLMIdentity `yaml:"clients" json:"clients"`
}

type LiteLLMIdentity struct {
	KeyHash string `yaml:"key-hash" json:"key-hash"`
	UserID  string `yaml:"user-id" json:"user-id"`
	TeamID  string `yaml:"team-id" json:"team-id"`
}

type AccountingOutboxConfig struct {
	LiteLLM         LiteLLMExporterConfig `yaml:"litellm" json:"litellm"`
	Pricing         PricingConfig         `yaml:"pricing" json:"pricing"`
	Enabled         bool                  `yaml:"enabled" json:"enabled"`
	DataPath        string                `yaml:"data-path" json:"data-path"`
	QueueCapacity   int                   `yaml:"queue-capacity" json:"queue-capacity"`
	MaxDiskMB       int                   `yaml:"max-disk-mb" json:"max-disk-mb"`
	MaxEvents       int                   `yaml:"max-events" json:"max-events"`
	RetentionHours  int                   `yaml:"retention-hours" json:"retention-hours"`
	ShutdownSeconds int                   `yaml:"shutdown-seconds" json:"shutdown-seconds"`
}

func (c AccountingOutboxConfig) Defaults() AccountingOutboxConfig {
	if c.QueueCapacity == 0 {
		c.QueueCapacity = 512
	}
	if c.MaxDiskMB == 0 {
		c.MaxDiskMB = 256
	}
	if c.MaxEvents == 0 {
		c.MaxEvents = 100000
	}
	if c.RetentionHours == 0 {
		c.RetentionHours = 168
	}
	if c.ShutdownSeconds == 0 {
		c.ShutdownSeconds = 5
	}
	return c
}

func (c AccountingOutboxConfig) Validate() error {
	c = c.Defaults()
	if c.LiteLLM.Enabled {
		u, err := url.Parse(c.LiteLLM.URL)
		if !c.Enabled || err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimSpace(c.LiteLLM.AdminKeyEnv) == "" {
			return fmt.Errorf("LiteLLM exporter requires an enabled outbox, HTTP(S) URL without credentials or query, and admin-key-env")
		}
	}
	if c.Enabled && strings.TrimSpace(c.DataPath) == "" {
		return fmt.Errorf("observability.accounting-outbox.data-path is required when enabled")
	}
	if c.QueueCapacity < 1 || c.QueueCapacity > 4096 || c.MaxDiskMB < 8 || c.MaxDiskMB > 4096 || c.MaxEvents < 1 || c.MaxEvents > 1000000 || c.RetentionHours < 1 || c.RetentionHours > 8760 || c.ShutdownSeconds < 1 || c.ShutdownSeconds > 30 {
		return fmt.Errorf("invalid observability.accounting-outbox limits")
	}
	return nil
}
