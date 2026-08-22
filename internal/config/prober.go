package config

import "time"

const (
	defaultCredentialProberInterval       = 60 * time.Second
	defaultCredentialProberMaxConcurrency = 4
	defaultCredentialProberRatePerMinute  = 60
	defaultCredentialProberPath           = "/models"
)

// CredentialProberConfig controls optional active health probing for registered credentials.
// When enabled, the conductor periodically issues a lightweight HTTP probe per credential
// and feeds failures into the existing cooldown/suspension machinery.
// Disabled by default.
type CredentialProberConfig struct {
	// Enabled turns active credential health probing on.
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Interval is the period between probe sweeps. Default 60s.
	Interval time.Duration `yaml:"interval" json:"interval"`
	// MaxConcurrency limits the number of in-flight probes. Default 4.
	MaxConcurrency int `yaml:"max-concurrency" json:"max-concurrency"`
	// RateLimitPerMinute caps the number of probe requests across all credentials per minute. Default 60.
	RateLimitPerMinute int `yaml:"rate-limit-per-minute" json:"rate-limit-per-minute"`
	// DefaultProbePath is the HTTP path resolved against the credential base_url for the probe. Default /models.
	DefaultProbePath string `yaml:"default-probe-path" json:"default-probe-path"`
	// BackoffBase is the minimum cooldown applied by a probe failure. Default 30s.
	BackoffBase time.Duration `yaml:"backoff-base" json:"backoff-base"`
	// BackoffMax caps the cooldown applied by a probe failure. Default 5m.
	BackoffMax time.Duration `yaml:"backoff-max" json:"backoff-max"`
}

// DefaultCredentialProberConfig returns the prober default configuration.
func DefaultCredentialProberConfig() CredentialProberConfig {
	return CredentialProberConfig{
		Enabled:            false,
		Interval:           defaultCredentialProberInterval,
		MaxConcurrency:     defaultCredentialProberMaxConcurrency,
		RateLimitPerMinute: defaultCredentialProberRatePerMinute,
		DefaultProbePath:   defaultCredentialProberPath,
	}
}
