// Package ignite starts each account's five-hour quota window right after the
// previous one resets, by sending one minimal request pinned to the account.
// A five-hour window only starts with the first request after a reset, so an
// idle account otherwise leaves quota time unused.
package ignite

import (
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ProviderConfig controls ignition for one provider.
type ProviderConfig struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Model overrides automatic model selection.
	Model string `yaml:"model" json:"model,omitempty"`
}

// Config is the ignition block of plugins.configs.prox.
type Config struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
	// StartHour and EndHour bound the daily ignition window in Timezone.
	StartHour       int    `yaml:"start_hour" json:"start_hour"`
	EndHour         int    `yaml:"end_hour" json:"end_hour"`
	EndGraceMinutes int    `yaml:"end_grace_minutes" json:"end_grace_minutes"`
	Timezone        string `yaml:"timezone" json:"timezone,omitempty"`
	// GraceSeconds is the wait after a reset before igniting.
	GraceSeconds int `yaml:"grace_seconds" json:"grace_seconds"`
	// Transient failure handling: retry after FailureRetrySeconds, multiplied
	// by FailureBackoffMultiplier per retry; after MaxTransientFailures in a
	// row the account pauses until the next day.
	FailureRetrySeconds      int     `yaml:"failure_retry_seconds" json:"failure_retry_seconds"`
	FailureBackoffMultiplier float64 `yaml:"failure_backoff_multiplier" json:"failure_backoff_multiplier"`
	MaxTransientFailures     int     `yaml:"max_transient_failures" json:"max_transient_failures"`
	// ConfirmSeconds is how long to wait for a poll to show the new window.
	ConfirmSeconds int                       `yaml:"confirm_seconds" json:"confirm_seconds"`
	Providers      map[string]ProviderConfig `yaml:"providers" json:"providers"`

	loc *time.Location
}

// Default returns ignition defaults. Ignition is opt-in.
func Default() Config {
	return Config{
		StartHour:                7,
		EndHour:                  22,
		EndGraceMinutes:          30,
		GraceSeconds:             3,
		FailureRetrySeconds:      300,
		FailureBackoffMultiplier: 3,
		MaxTransientFailures:     3,
		ConfirmSeconds:           240,
		Providers: map[string]ProviderConfig{
			"claude": {Enabled: true},
			"codex":  {Enabled: true},
		},
	}
}

// Decode reads the ignition block from the prox settings YAML.
func Decode(raw []byte) (Config, error) {
	var wrapper struct {
		Ignition yaml.Node `yaml:"ignition"`
	}
	cfg := Default()
	if len(strings.TrimSpace(string(raw))) != 0 {
		if err := yaml.Unmarshal(raw, &wrapper); err != nil {
			return Config{}, fmt.Errorf("ignition: invalid YAML")
		}
		if wrapper.Ignition.Kind != 0 {
			providers := cfg.Providers
			cfg.Providers = nil
			if err := wrapper.Ignition.Decode(&cfg); err != nil {
				return Config{}, fmt.Errorf("ignition: %w", err)
			}
			// Providers not mentioned keep their defaults.
			for k, v := range providers {
				if _, ok := cfg.Providers[k]; !ok {
					if cfg.Providers == nil {
						cfg.Providers = map[string]ProviderConfig{}
					}
					cfg.Providers[k] = v
				}
			}
		}
	}
	return cfg, cfg.validate()
}

func (c *Config) validate() error {
	if c.StartHour < 0 || c.StartHour > 23 || c.EndHour < 0 || c.EndHour > 23 || c.EndHour < c.StartHour {
		return fmt.Errorf("ignition: start_hour and end_hour must be 0-23 with start_hour <= end_hour")
	}
	if c.EndGraceMinutes < 0 || c.EndGraceMinutes > 59 {
		return fmt.Errorf("ignition: end_grace_minutes must be 0-59")
	}
	if c.GraceSeconds < 0 || c.FailureRetrySeconds < 30 || c.FailureBackoffMultiplier < 1 || c.MaxTransientFailures < 1 || c.ConfirmSeconds < 30 {
		return fmt.Errorf("ignition: grace_seconds >= 0, failure_retry_seconds >= 30, failure_backoff_multiplier >= 1, max_transient_failures >= 1, confirm_seconds >= 30")
	}
	c.loc = time.Local
	if tz := strings.TrimSpace(c.Timezone); tz != "" {
		loc, err := time.LoadLocation(tz)
		if err != nil {
			return fmt.Errorf("ignition: unknown timezone %q", tz)
		}
		c.loc = loc
	}
	return nil
}

func (c Config) location() *time.Location {
	if c.loc == nil {
		return time.Local
	}
	return c.loc
}

func (c Config) provider(name string) (ProviderConfig, bool) {
	p, ok := c.Providers[name]
	return p, ok && p.Enabled
}

// Allowed reports whether t falls inside the daily ignition window.
func (c Config) Allowed(t time.Time) bool {
	t = t.In(c.location())
	start := time.Date(t.Year(), t.Month(), t.Day(), c.StartHour, 0, 0, 0, t.Location())
	end := time.Date(t.Year(), t.Month(), t.Day(), c.EndHour, c.EndGraceMinutes, 0, 0, t.Location())
	return !t.Before(start) && !t.After(end)
}

// NextStart returns the next daily window start strictly after t.
func (c Config) NextStart(t time.Time) time.Time {
	l := t.In(c.location())
	start := time.Date(l.Year(), l.Month(), l.Day(), c.StartHour, 0, 0, 0, l.Location())
	if !start.After(t) {
		start = time.Date(l.Year(), l.Month(), l.Day()+1, c.StartHour, 0, 0, 0, l.Location())
	}
	return start
}
