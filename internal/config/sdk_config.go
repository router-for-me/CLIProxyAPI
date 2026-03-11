// Package config provides configuration management for the CLI Proxy API server.
// It handles loading and parsing YAML configuration files, and provides structured
// access to application settings including server port, authentication directory,
// debug settings, proxy configuration, and API keys.
package config

import "sync"

// SDKConfig represents the application's configuration, loaded from a YAML file.
type SDKConfig struct {
	// ProxyURL is the URL of an optional proxy server to use for outbound requests.
	ProxyURL string `yaml:"proxy-url" json:"proxy-url"`

	// APIKeyQuotas controls per-client API key token quota enforcement.
	APIKeyQuotas APIKeyQuotaConfig `yaml:"api-key-quotas,omitempty" json:"api-key-quotas,omitempty"`

	// ForceModelPrefix requires explicit model prefixes (e.g., "teamA/gemini-3-pro-preview")
	// to target prefixed credentials. When false, unprefixed model requests may use prefixed
	// credentials as well.
	ForceModelPrefix bool `yaml:"force-model-prefix" json:"force-model-prefix"`

	// RequestLog enables or disables detailed request logging functionality.
	RequestLog bool `yaml:"request-log" json:"request-log"`

	// APIKeys is a list of keys for authenticating clients to this proxy server.
	APIKeys []string `yaml:"api-keys" json:"api-keys"`

	// PassthroughHeaders controls whether upstream response headers are forwarded to downstream clients.
	// Default is false (disabled).
	PassthroughHeaders bool `yaml:"passthrough-headers" json:"passthrough-headers"`

	// Streaming configures server-side streaming behavior (keep-alives and safe bootstrap retries).
	Streaming StreamingConfig `yaml:"streaming" json:"streaming"`

	// NonStreamKeepAliveInterval controls how often blank lines are emitted for non-streaming responses.
	// <= 0 disables keep-alives. Value is in seconds.
	NonStreamKeepAliveInterval int `yaml:"nonstream-keepalive-interval,omitempty" json:"nonstream-keepalive-interval,omitempty"`
}

// StreamingConfig holds server streaming behavior configuration.
type APIKeyQuotaConfig struct {
	// Enabled toggles API key quota enforcement.
	Enabled bool `yaml:"enabled" json:"enabled"`

	// ExcludeModelPatterns defines wildcard model patterns that bypass quota checks.
	ExcludeModelPatterns []string `yaml:"exclude-model-patterns,omitempty" json:"exclude-model-patterns,omitempty"`

	// MonthlyTokenLimits defines monthly token limits by API key and model wildcard.
	MonthlyTokenLimits []APIKeyMonthlyModelTokenLimit `yaml:"monthly-token-limits,omitempty" json:"monthly-token-limits,omitempty"`

	mu sync.RWMutex `yaml:"-" json:"-"`
}

// APIKeyMonthlyModelTokenLimit binds an API key + model pattern pair to a monthly token cap.
type APIKeyMonthlyModelTokenLimit struct {
	// APIKey is an exact or wildcard API key pattern (supports '*').
	APIKey string `yaml:"api-key" json:"api-key"`

	// Model is an exact or wildcard model pattern (supports '*').
	Model string `yaml:"model" json:"model"`

	// Limit is the maximum total tokens allowed per API key in a UTC calendar month.
	Limit int64 `yaml:"limit" json:"limit"`
}

func (q *APIKeyQuotaConfig) Snapshot() APIKeyQuotaConfig {
	if q == nil {
		return APIKeyQuotaConfig{}
	}
	q.mu.RLock()
	defer q.mu.RUnlock()

	out := APIKeyQuotaConfig{Enabled: q.Enabled}
	if len(q.ExcludeModelPatterns) > 0 {
		out.ExcludeModelPatterns = append([]string(nil), q.ExcludeModelPatterns...)
	}
	if len(q.MonthlyTokenLimits) > 0 {
		out.MonthlyTokenLimits = append([]APIKeyMonthlyModelTokenLimit(nil), q.MonthlyTokenLimits...)
	}
	return out
}

func (q *APIKeyQuotaConfig) Update(fn func(*APIKeyQuotaConfig)) {
	if q == nil || fn == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	fn(q)
}

type StreamingConfig struct {
	// KeepAliveSeconds controls how often the server emits SSE heartbeats (": keep-alive\n\n").
	// <= 0 disables keep-alives. Default is 0.
	KeepAliveSeconds int `yaml:"keepalive-seconds,omitempty" json:"keepalive-seconds,omitempty"`

	// BootstrapRetries controls how many times the server may retry a streaming request before any bytes are sent,
	// to allow auth rotation / transient recovery.
	// <= 0 disables bootstrap retries. Default is 0.
	BootstrapRetries int `yaml:"bootstrap-retries,omitempty" json:"bootstrap-retries,omitempty"`
}
