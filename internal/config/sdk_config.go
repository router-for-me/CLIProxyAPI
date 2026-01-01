// Package config provides configuration management for the CLI Proxy API server.
// It handles loading and parsing YAML configuration files, and provides structured
// access to application settings including server port, authentication directory,
// debug settings, proxy configuration, and API keys.
package config

// SDKConfig represents the application's configuration, loaded from a YAML file.
type SDKConfig struct {
	// ProxyURL is the URL of an optional proxy server to use for outbound requests.
	ProxyURL string `yaml:"proxy-url" json:"proxy-url"`

	// ForceModelPrefix requires explicit model prefixes (e.g., "teamA/gemini-3-pro-preview")
	// to target prefixed credentials. When false, unprefixed model requests may use prefixed
	// credentials as well.
	ForceModelPrefix bool `yaml:"force-model-prefix" json:"force-model-prefix"`

	// RequestLog enables or disables detailed request logging functionality.
	RequestLog bool `yaml:"request-log" json:"request-log"`

	// APIKeys is a list of keys for authenticating clients to this proxy server.
	APIKeys []string `yaml:"api-keys" json:"api-keys"`

	// Access holds request authentication provider configuration.
	Access AccessConfig `yaml:"auth,omitempty" json:"auth,omitempty"`

	// Streaming configures server-side streaming behavior (keep-alives and safe bootstrap retries).
	Streaming StreamingConfig `yaml:"streaming" json:"streaming"`

	// Session configures conversation session continuity and history management.
	Session SessionConfig `yaml:"session" json:"session"`
}

// StreamingConfig holds server streaming behavior configuration.
type StreamingConfig struct {
	// KeepAliveSeconds controls how often the server emits SSE heartbeats (": keep-alive\n\n").
	// <= 0 disables keep-alives. Default is 0.
	KeepAliveSeconds int `yaml:"keepalive-seconds,omitempty" json:"keepalive-seconds,omitempty"`

	// BootstrapRetries controls how many times the server may retry a streaming request before any bytes are sent,
	// to allow auth rotation / transient recovery.
	// <= 0 disables bootstrap retries. Default is 0.
	BootstrapRetries int `yaml:"bootstrap-retries,omitempty" json:"bootstrap-retries,omitempty"`
}

// SessionConfig holds conversation session continuity configuration.
type SessionConfig struct {
	// Enabled toggles session management functionality.
	Enabled bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`

	// StorageDir is the directory where session files are stored.
	// Defaults to ~/.cli-proxy-api/sessions if not specified.
	StorageDir string `yaml:"storage-dir,omitempty" json:"storage-dir,omitempty"`

	// DefaultTTLHours is the default session expiration time in hours.
	// Defaults to 24 hours if not specified or <= 0.
	DefaultTTLHours int `yaml:"default-ttl-hours,omitempty" json:"default-ttl-hours,omitempty"`

	// CleanupIntervalHours is how often to run expired session cleanup in hours.
	// Defaults to 1 hour if not specified. Set to 0 to disable automatic cleanup.
	CleanupIntervalHours int `yaml:"cleanup-interval-hours,omitempty" json:"cleanup-interval-hours,omitempty"`
}

// AccessConfig groups request authentication providers.
type AccessConfig struct {
	// Providers lists configured authentication providers.
	Providers []AccessProvider `yaml:"providers,omitempty" json:"providers,omitempty"`
}

// AccessProvider describes a request authentication provider entry.
type AccessProvider struct {
	// Name is the instance identifier for the provider.
	Name string `yaml:"name" json:"name"`

	// Type selects the provider implementation registered via the SDK.
	Type string `yaml:"type" json:"type"`

	// SDK optionally names a third-party SDK module providing this provider.
	SDK string `yaml:"sdk,omitempty" json:"sdk,omitempty"`

	// APIKeys lists inline keys for providers that require them.
	APIKeys []string `yaml:"api-keys,omitempty" json:"api-keys,omitempty"`

	// Config passes provider-specific options to the implementation.
	Config map[string]any `yaml:"config,omitempty" json:"config,omitempty"`
}

const (
	// AccessProviderTypeConfigAPIKey is the built-in provider validating inline API keys.
	AccessProviderTypeConfigAPIKey = "config-api-key"

	// DefaultAccessProviderName is applied when no provider name is supplied.
	DefaultAccessProviderName = "config-inline"
)

// ConfigAPIKeyProvider returns the first inline API key provider if present.
func (c *SDKConfig) ConfigAPIKeyProvider() *AccessProvider {
	if c == nil {
		return nil
	}
	for i := range c.Access.Providers {
		if c.Access.Providers[i].Type == AccessProviderTypeConfigAPIKey {
			if c.Access.Providers[i].Name == "" {
				c.Access.Providers[i].Name = DefaultAccessProviderName
			}
			return &c.Access.Providers[i]
		}
	}
	return nil
}

// MakeInlineAPIKeyProvider constructs an inline API key provider configuration.
// It returns nil when no keys are supplied.
func MakeInlineAPIKeyProvider(keys []string) *AccessProvider {
	if len(keys) == 0 {
		return nil
	}
	provider := &AccessProvider{
		Name:    DefaultAccessProviderName,
		Type:    AccessProviderTypeConfigAPIKey,
		APIKeys: append([]string(nil), keys...),
	}
	return provider
}
