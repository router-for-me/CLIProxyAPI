package config

import "strings"

// VertexCompatKey represents the configuration for Vertex AI-compatible API keys.
// This supports third-party services that use Vertex AI-style endpoint paths
// (/publishers/google/models/{model}:streamGenerateContent) but authenticate
// with simple API keys instead of Google Cloud service account credentials.
//
// Example services: zenmux.ai and similar Vertex-compatible providers.
type VertexCompatKey struct {
	// APIKey is the authentication key for accessing the Vertex-compatible API.
	// Maps to the x-goog-api-key header.
	APIKey string `yaml:"api-key" json:"api-key"`

	// BaseURL is the base URL for the Vertex-compatible API endpoint.
	// The executor will append "/v1/publishers/google/models/{model}:action" to this.
	// Example: "https://zenmux.ai/api" becomes "https://zenmux.ai/api/v1/publishers/google/models/..."
	BaseURL string `yaml:"base-url,omitempty" json:"base-url,omitempty"`

	// ProxyURL optionally overrides the global proxy for this API key.
	ProxyURL string `yaml:"proxy-url,omitempty" json:"proxy-url,omitempty"`

	// Headers optionally adds extra HTTP headers for requests sent with this key.
	// Commonly used for cookies, user-agent, and other authentication headers.
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
}

// SanitizeVertexCompatKeys deduplicates and normalizes Vertex-compatible API key credentials.
func (cfg *Config) SanitizeVertexCompatKeys() {
	if cfg == nil {
		return
	}

	seen := make(map[string]struct{}, len(cfg.VertexCompatAPIKey))
	out := cfg.VertexCompatAPIKey[:0]
	for i := range cfg.VertexCompatAPIKey {
		entry := cfg.VertexCompatAPIKey[i]
		entry.APIKey = strings.TrimSpace(entry.APIKey)
		if entry.APIKey == "" {
			continue
		}
		entry.BaseURL = strings.TrimSpace(entry.BaseURL)
		if entry.BaseURL == "" {
			// BaseURL is required for vertex-compat keys
			continue
		}
		entry.ProxyURL = strings.TrimSpace(entry.ProxyURL)
		entry.Headers = NormalizeHeaders(entry.Headers)

		// Use API key + base URL as uniqueness key
		uniqueKey := entry.APIKey + "|" + entry.BaseURL
		if _, exists := seen[uniqueKey]; exists {
			continue
		}
		seen[uniqueKey] = struct{}{}
		out = append(out, entry)
	}
	cfg.VertexCompatAPIKey = out
}
