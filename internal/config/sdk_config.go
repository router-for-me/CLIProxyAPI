// Package config provides configuration management for the CLI Proxy API server.
// It handles loading and parsing YAML configuration files, and provides structured
// access to application settings including server port, authentication directory,
// debug settings, proxy configuration, and API keys.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

const DefaultAPIKeyRequestsPerSecond = 5

// APIKeyEntry defines a client-facing API key plus its local request rate limit.
// The YAML/JSON decoder accepts either:
//   - a plain string: "sk-..."
//   - an object: { api-key: "sk-...", requests-per-second: 5 }
type APIKeyEntry struct {
	APIKey            string `yaml:"api-key" json:"api-key"`
	RequestsPerSecond int    `yaml:"requests-per-second,omitempty" json:"requests-per-second"`
}

func (e *APIKeyEntry) normalize() {
	if e == nil {
		return
	}
	e.APIKey = strings.TrimSpace(e.APIKey)
	if e.RequestsPerSecond <= 0 {
		e.RequestsPerSecond = DefaultAPIKeyRequestsPerSecond
	}
}

// UnmarshalYAML supports both legacy string items and structured objects.
func (e *APIKeyEntry) UnmarshalYAML(value *yaml.Node) error {
	if e == nil {
		return nil
	}
	switch value.Kind {
	case yaml.ScalarNode:
		var apiKey string
		if err := value.Decode(&apiKey); err != nil {
			return err
		}
		e.APIKey = apiKey
		e.RequestsPerSecond = DefaultAPIKeyRequestsPerSecond
	case yaml.MappingNode:
		type rawEntry APIKeyEntry
		var decoded rawEntry
		if err := value.Decode(&decoded); err != nil {
			return err
		}
		*e = APIKeyEntry(decoded)
	default:
		return fmt.Errorf("api key entry must be a string or object")
	}
	e.normalize()
	return nil
}

// UnmarshalJSON supports both legacy string items and structured objects.
func (e *APIKeyEntry) UnmarshalJSON(data []byte) error {
	if e == nil {
		return nil
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*e = APIKeyEntry{}
		return nil
	}
	if trimmed[0] == '"' {
		var apiKey string
		if err := json.Unmarshal(trimmed, &apiKey); err != nil {
			return err
		}
		e.APIKey = apiKey
		e.RequestsPerSecond = DefaultAPIKeyRequestsPerSecond
		e.normalize()
		return nil
	}
	type rawEntry APIKeyEntry
	var decoded rawEntry
	if err := json.Unmarshal(trimmed, &decoded); err != nil {
		return err
	}
	*e = APIKeyEntry(decoded)
	e.normalize()
	return nil
}

// NormalizeAPIKeyEntries trims, defaults, and deduplicates client API keys.
func NormalizeAPIKeyEntries(entries []APIKeyEntry) []APIKeyEntry {
	if len(entries) == 0 {
		return nil
	}
	normalized := make([]APIKeyEntry, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		entry.normalize()
		if entry.APIKey == "" {
			continue
		}
		if _, exists := seen[entry.APIKey]; exists {
			continue
		}
		seen[entry.APIKey] = struct{}{}
		normalized = append(normalized, entry)
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

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
	APIKeys []APIKeyEntry `yaml:"api-keys" json:"api-keys"`

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
