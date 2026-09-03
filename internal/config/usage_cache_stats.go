package config

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultUsageCacheStatsMaxSessions       = 500
	defaultUsageCacheStatsPerSessionRequest = 200
	defaultUsageCacheStatsIdleTTL           = 24 * time.Hour
)

// UsageCacheStatsConfig configures the retained per-session prompt-cache
// statistics store.
//
// The store is in-memory and bounded: it keeps the last PerSessionRequests
// requests for at most MaxSessions Claude Code sessions, evicting the least
// recently seen session past that bound and dropping any session idle for
// longer than IdleTTL. A zero-value config keeps the feature disabled; every
// other field falls back to its documented default, so a config that only
// sets `enabled: true` is complete.
type UsageCacheStatsConfig struct {
	// Enabled turns the per-session cache statistics store on. Default false.
	Enabled bool `yaml:"enabled" json:"enabled"`

	// MaxSessions caps how many sessions are retained. Default 500.
	MaxSessions int `yaml:"max-sessions" json:"max-sessions"`

	// PerSessionRequests caps the per-session request ring buffer. Default 200.
	PerSessionRequests int `yaml:"per-session-requests" json:"per-session-requests"`

	// IdleTTL drops a session that has not been seen for this long. Default 24h.
	IdleTTL time.Duration `yaml:"idle-ttl" json:"idle-ttl"`

	enabledPresent            bool
	maxSessionsPresent        bool
	perSessionRequestsPresent bool
	idleTTLPresent            bool
}

// UnmarshalYAML preserves field presence so an explicitly configured zero is
// not silently replaced by the default.
func (c *UsageCacheStatsConfig) UnmarshalYAML(value *yaml.Node) error {
	type rawUsageCacheStatsConfig struct {
		Enabled            bool          `yaml:"enabled"`
		MaxSessions        int           `yaml:"max-sessions"`
		PerSessionRequests int           `yaml:"per-session-requests"`
		IdleTTL            time.Duration `yaml:"idle-ttl"`
	}

	var raw rawUsageCacheStatsConfig
	if errDecode := value.Decode(&raw); errDecode != nil {
		return errDecode
	}

	*c = UsageCacheStatsConfig{
		Enabled:                   raw.Enabled,
		MaxSessions:               raw.MaxSessions,
		PerSessionRequests:        raw.PerSessionRequests,
		IdleTTL:                   raw.IdleTTL,
		enabledPresent:            usageCacheStatsFieldPresent(value, "enabled"),
		maxSessionsPresent:        usageCacheStatsFieldPresent(value, "max-sessions"),
		perSessionRequestsPresent: usageCacheStatsFieldPresent(value, "per-session-requests"),
		idleTTLPresent:            usageCacheStatsFieldPresent(value, "idle-ttl"),
	}
	return nil
}

func usageCacheStatsFieldPresent(value *yaml.Node, field string) bool {
	if value == nil || value.Kind != yaml.MappingNode {
		return false
	}
	for index := 0; index+1 < len(value.Content); index += 2 {
		if value.Content[index].Value == field {
			return true
		}
	}
	return false
}

// WithDefaults fills in every field the operator left out.
func (c UsageCacheStatsConfig) WithDefaults() UsageCacheStatsConfig {
	if !c.maxSessionsPresent && c.MaxSessions == 0 {
		c.MaxSessions = defaultUsageCacheStatsMaxSessions
	}
	if !c.perSessionRequestsPresent && c.PerSessionRequests == 0 {
		c.PerSessionRequests = defaultUsageCacheStatsPerSessionRequest
	}
	if !c.idleTTLPresent && c.IdleTTL == 0 {
		c.IdleTTL = defaultUsageCacheStatsIdleTTL
	}
	return c
}

// Validate rejects a configuration that could not retain anything.
func (c UsageCacheStatsConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.MaxSessions <= 0 {
		return fmt.Errorf("usage-cache-stats.max-sessions must be positive")
	}
	if c.PerSessionRequests <= 0 {
		return fmt.Errorf("usage-cache-stats.per-session-requests must be positive")
	}
	if c.IdleTTL <= 0 {
		return fmt.Errorf("usage-cache-stats.idle-ttl must be positive")
	}
	return nil
}
