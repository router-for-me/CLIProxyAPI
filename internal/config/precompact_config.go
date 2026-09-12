package config

import (
	"fmt"
	"strings"
	"time"
)

const (
	DefaultPreCompactAuxModel         = "claude-haiku-4-5-20251001"
	DefaultPreCompactThreshold        = 0.85
	DefaultPreCompactKeepRecentTurns  = 6
	DefaultPreCompactKeepRecentTokens = 40000
	DefaultPreCompactCacheTTL         = "2h"
	DefaultPreCompactCacheMaxSess     = 2000
)

// PreCompactConfig controls server-side pre-compaction: when a request does not
// fit the selected upstream model's context window, older turns are summarized
// with an auxiliary model before the request is forwarded.
type PreCompactConfig struct {
	// Enabled turns the feature on. Default false.
	Enabled bool `yaml:"enabled" json:"enabled"`
	// AuxModel is the model used to write the summary.
	AuxModel string `yaml:"aux-model,omitempty" json:"aux-model,omitempty"`
	// Threshold is the fraction of the context window that triggers compaction (0 < t <= 1).
	Threshold float64 `yaml:"threshold,omitempty" json:"threshold,omitempty"`
	// KeepRecentTurns is the number of trailing user turns kept byte-identical.
	KeepRecentTurns int `yaml:"keep-recent-turns,omitempty" json:"keep-recent-turns,omitempty"`
	// KeepRecentTokens stops keeping further turns once the kept tail exceeds
	// this many tokens. At least one full turn is always kept.
	KeepRecentTokens int `yaml:"keep-recent-tokens,omitempty" json:"keep-recent-tokens,omitempty"`
	// CacheTTL is how long a per-session summary stays reusable (duration string).
	CacheTTL string `yaml:"cache-ttl,omitempty" json:"cache-ttl,omitempty"`
	// CacheMaxSessions bounds the number of cached sessions.
	CacheMaxSessions int `yaml:"cache-max-sessions,omitempty" json:"cache-max-sessions,omitempty"`
}

// WithDefaults fills absent values with defaults.
func (c PreCompactConfig) WithDefaults() PreCompactConfig {
	if strings.TrimSpace(c.AuxModel) == "" {
		c.AuxModel = DefaultPreCompactAuxModel
	}
	c.AuxModel = strings.TrimSpace(c.AuxModel)
	if c.Threshold <= 0 {
		c.Threshold = DefaultPreCompactThreshold
	}
	if c.KeepRecentTurns <= 0 {
		c.KeepRecentTurns = DefaultPreCompactKeepRecentTurns
	}
	if c.KeepRecentTokens <= 0 {
		c.KeepRecentTokens = DefaultPreCompactKeepRecentTokens
	}
	if strings.TrimSpace(c.CacheTTL) == "" {
		c.CacheTTL = DefaultPreCompactCacheTTL
	}
	if c.CacheMaxSessions <= 0 {
		c.CacheMaxSessions = DefaultPreCompactCacheMaxSess
	}
	return c
}

// Validate reports configuration errors.
func (c PreCompactConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.Threshold <= 0 || c.Threshold > 1 {
		return fmt.Errorf("pre-compact.threshold must be in (0, 1], got %v", c.Threshold)
	}
	if c.KeepRecentTurns < 1 {
		return fmt.Errorf("pre-compact.keep-recent-turns must be >= 1, got %d", c.KeepRecentTurns)
	}
	if c.KeepRecentTokens < 1 {
		return fmt.Errorf("pre-compact.keep-recent-tokens must be >= 1, got %d", c.KeepRecentTokens)
	}
	if _, err := time.ParseDuration(c.CacheTTL); err != nil {
		return fmt.Errorf("pre-compact.cache-ttl: %w", err)
	}
	return nil
}

// CacheTTLDuration returns the parsed TTL or the default on error.
func (c PreCompactConfig) CacheTTLDuration() time.Duration {
	d, err := time.ParseDuration(c.CacheTTL)
	if err != nil || d <= 0 {
		d, _ = time.ParseDuration(DefaultPreCompactCacheTTL)
	}
	return d
}
