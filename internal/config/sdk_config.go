// Package config provides configuration management for the CLI Proxy API server.
// It handles loading and parsing YAML configuration files, and provides structured
// access to application settings including server port, authentication directory,
// debug settings, proxy configuration, and API keys.
package config

// SDKConfig represents the application's configuration, loaded from a YAML file.
type SDKConfig struct {
	// ProxyURL is the URL of an optional proxy server to use for outbound requests.
	ProxyURL string `yaml:"proxy-url" json:"proxy-url"`

	// EnableGeminiCLIEndpoint controls whether Gemini CLI internal endpoints (/v1internal:*) are enabled.
	// Default is false for safety; when false, /v1internal:* requests are rejected.
	EnableGeminiCLIEndpoint bool `yaml:"enable-gemini-cli-endpoint" json:"enable-gemini-cli-endpoint"`

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

	// OAuthRefresh controls background refresh behavior for OAuth / auth-file credentials.
	OAuthRefresh OAuthRefreshConfig `yaml:"oauth-refresh" json:"oauth-refresh"`

	// Streaming configures server-side streaming behavior (keep-alives and safe bootstrap retries).
	Streaming StreamingConfig `yaml:"streaming" json:"streaming"`

	// NonStreamKeepAliveInterval controls how often blank lines are emitted for non-streaming responses.
	// <= 0 disables keep-alives. Value is in seconds.
	NonStreamKeepAliveInterval int `yaml:"nonstream-keepalive-interval,omitempty" json:"nonstream-keepalive-interval,omitempty"`

	// SessionAffinity configures optional sticky session to auth routing.
	SessionAffinity SessionAffinityConfig `yaml:"session-affinity,omitempty" json:"session-affinity,omitempty"`
}

// OAuthRefreshConfig controls background refresh scheduling for OAuth/file-backed auths.
type OAuthRefreshConfig struct {
	// OnStartup controls whether the auto-refresh loop immediately checks credentials when the service starts.
	// Nil preserves the legacy default (enabled).
	OnStartup *bool `yaml:"on-startup,omitempty" json:"on-startup,omitempty"`

	// BatchSize limits how many due credentials may be refreshed in one scheduler pass.
	// <= 0 preserves the legacy behavior (no per-pass cap).
	BatchSize int `yaml:"batch-size,omitempty" json:"batch-size,omitempty"`
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

// SessionAffinityConfig controls session-to-auth sticky routing behavior.
type SessionAffinityConfig struct {
	// Provider selects the session affinity backend.
	// Supported values: "", "memory" (default), "redis".
	Provider string `yaml:"provider,omitempty" json:"provider,omitempty"`

	// Header is the preferred downstream header carrying the session affinity key.
	// Defaults to "X-Session-Affinity" when empty.
	Header string `yaml:"header,omitempty" json:"header,omitempty"`

	// TTLSeconds controls binding expiration for backends that support TTL.
	// <= 0 defaults to 86400 seconds.
	TTLSeconds int `yaml:"ttl-seconds,omitempty" json:"ttl-seconds,omitempty"`

	// Redis configures the Redis-backed affinity store.
	Redis SessionAffinityRedisConfig `yaml:"redis,omitempty" json:"redis,omitempty"`
}

// SessionAffinityRedisConfig holds Redis connection parameters.
type SessionAffinityRedisConfig struct {
	// Addr is the Redis server address in host:port form.
	Addr string `yaml:"addr,omitempty" json:"addr,omitempty"`

	// Password is the optional Redis password.
	Password string `yaml:"password,omitempty" json:"password,omitempty"`

	// DB is the Redis logical database index.
	DB int `yaml:"db,omitempty" json:"db,omitempty"`

	// KeyPrefix namespaces affinity bindings in Redis.
	KeyPrefix string `yaml:"key-prefix,omitempty" json:"key-prefix,omitempty"`
}
