package config

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const (
	DefaultAPIKeyUsageRetentionDays = 400
	DefaultAPIKeyUsageTimezone      = "UTC"
)

// APIKeyLimit defines a request and token ceiling for one reset period.
// A zero value means unlimited.
type APIKeyLimit struct {
	Requests int64 `yaml:"requests,omitempty" json:"requests,omitempty"`
	Tokens   int64 `yaml:"tokens,omitempty" json:"tokens,omitempty"`
}

// APIKeyProfile describes one downstream user or application key.
type APIKeyProfile struct {
	ID            string      `yaml:"id" json:"id"`
	Name          string      `yaml:"name" json:"name"`
	APIKey        string      `yaml:"api-key" json:"api-key"`
	Disabled      bool        `yaml:"disabled,omitempty" json:"disabled,omitempty"`
	AllowedModels []string    `yaml:"allowed-models,omitempty" json:"allowed-models,omitempty"`
	Weekly        APIKeyLimit `yaml:"weekly,omitempty" json:"weekly,omitempty"`
	Monthly       APIKeyLimit `yaml:"monthly,omitempty" json:"monthly,omitempty"`
}

// APIKeyUsageConfig controls the embedded usage database.
type APIKeyUsageConfig struct {
	Enabled       bool   `yaml:"enabled" json:"enabled"`
	DatabasePath  string `yaml:"database-path,omitempty" json:"database-path,omitempty"`
	RetentionDays int    `yaml:"retention-days,omitempty" json:"retention-days,omitempty"`
	Timezone      string `yaml:"timezone,omitempty" json:"timezone,omitempty"`
}

// NormalizeAPIKeyProfiles trims profile fields, removes unusable duplicates,
// and clamps negative limits to their unlimited value.
func (cfg *Config) NormalizeAPIKeyProfiles() {
	if cfg == nil {
		return
	}

	seenIDs := make(map[string]struct{}, len(cfg.APIKeyProfiles))
	seenKeys := make(map[string]struct{}, len(cfg.APIKeyProfiles))
	profiles := make([]APIKeyProfile, 0, len(cfg.APIKeyProfiles))
	for i := range cfg.APIKeyProfiles {
		profile := cfg.APIKeyProfiles[i]
		profile.APIKey = strings.TrimSpace(profile.APIKey)
		if profile.APIKey == "" {
			continue
		}
		profile.ID = strings.TrimSpace(profile.ID)
		if profile.ID == "" {
			profile.ID = "key-" + apiKeyProfileFingerprint(profile.APIKey)
		}
		profile.Name = strings.TrimSpace(profile.Name)
		if profile.Name == "" {
			profile.Name = profile.ID
		}
		if _, exists := seenIDs[strings.ToLower(profile.ID)]; exists {
			continue
		}
		if _, exists := seenKeys[profile.APIKey]; exists {
			continue
		}
		seenIDs[strings.ToLower(profile.ID)] = struct{}{}
		seenKeys[profile.APIKey] = struct{}{}
		profile.AllowedModels = normalizeAllowedModels(profile.AllowedModels)
		profile.Weekly = normalizeAPIKeyLimit(profile.Weekly)
		profile.Monthly = normalizeAPIKeyLimit(profile.Monthly)
		profiles = append(profiles, profile)
	}
	cfg.APIKeyProfiles = profiles

	cfg.APIKeyUsage.DatabasePath = strings.TrimSpace(cfg.APIKeyUsage.DatabasePath)
	cfg.APIKeyUsage.Timezone = strings.TrimSpace(cfg.APIKeyUsage.Timezone)
	if cfg.APIKeyUsage.Timezone == "" {
		cfg.APIKeyUsage.Timezone = DefaultAPIKeyUsageTimezone
	}
	if cfg.APIKeyUsage.RetentionDays <= 0 {
		cfg.APIKeyUsage.RetentionDays = DefaultAPIKeyUsageRetentionDays
	}
}

func normalizeAllowedModels(models []string) []string {
	if len(models) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(models))
	out := make([]string, 0, len(models))
	for _, value := range models {
		model := strings.TrimSpace(value)
		if model == "" {
			continue
		}
		key := strings.ToLower(model)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, model)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeAPIKeyLimit(limit APIKeyLimit) APIKeyLimit {
	if limit.Requests < 0 {
		limit.Requests = 0
	}
	if limit.Tokens < 0 {
		limit.Tokens = 0
	}
	return limit
}

func apiKeyProfileFingerprint(apiKey string) string {
	sum := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(sum[:6])
}
