// Package config provides configuration management for the CLI Proxy API server.
// It handles loading and parsing YAML configuration files, and provides structured
// access to application settings including server port, authentication directory,
// debug settings, proxy configuration, and API keys.
package config

import (
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// APIKeyEntry represents a client API key with optional model restrictions.
type APIKeyEntry struct {
	Key           string   `yaml:"key" json:"key"`
	AllowedModels []string `yaml:"allowed-models,omitempty" json:"allowed-models,omitempty"`
}

// UnmarshalYAML supports both legacy string and object formats.
func (e *APIKeyEntry) UnmarshalYAML(value *yaml.Node) error {
	if e == nil {
		return fmt.Errorf("nil APIKeyEntry")
	}

	// Legacy format: "api-key-string"
	if value.Kind == yaml.ScalarNode {
		var key string
		if err := value.Decode(&key); err != nil {
			return err
		}
		e.Key = strings.TrimSpace(key)
		e.AllowedModels = nil
		return nil
	}

	// New format: { key: "...", allowed-models: [...] }
	var raw struct {
		Key           string   `yaml:"key"`
		AllowedModels []string `yaml:"allowed-models"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	*e = APIKeyEntry{Key: strings.TrimSpace(raw.Key), AllowedModels: normalizeStringList(raw.AllowedModels)}
	return nil
}

// MarshalYAML writes object format for consistency in management workflows.
func (e APIKeyEntry) MarshalYAML() (interface{}, error) {
	return struct {
		Key           string   `yaml:"key"`
		AllowedModels []string `yaml:"allowed-models,omitempty"`
	}{
		Key:           strings.TrimSpace(e.Key),
		AllowedModels: normalizeStringList(e.AllowedModels),
	}, nil
}

// UnmarshalJSON supports both legacy string and object formats.
func (e *APIKeyEntry) UnmarshalJSON(data []byte) error {
	if e == nil {
		return fmt.Errorf("nil APIKeyEntry")
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*e = APIKeyEntry{}
		return nil
	}

	if len(trimmed) > 0 && trimmed[0] == '"' {
		var key string
		if err := json.Unmarshal(data, &key); err != nil {
			return err
		}
		e.Key = strings.TrimSpace(key)
		e.AllowedModels = nil
		return nil
	}

	var raw struct {
		Key           string   `json:"key"`
		AllowedModels []string `json:"allowed-models"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*e = APIKeyEntry{Key: strings.TrimSpace(raw.Key), AllowedModels: normalizeStringList(raw.AllowedModels)}
	return nil
}

// MarshalJSON writes legacy string format when unrestricted to preserve compatibility.
func (e APIKeyEntry) MarshalJSON() ([]byte, error) {
	if len(e.AllowedModels) == 0 {
		return json.Marshal(strings.TrimSpace(e.Key))
	}
	return json.Marshal(struct {
		Key           string   `json:"key"`
		AllowedModels []string `json:"allowed-models,omitempty"`
	}{
		Key:           strings.TrimSpace(e.Key),
		AllowedModels: normalizeStringList(e.AllowedModels),
	})
}

// IsModelAllowed checks whether a model is allowed by this key's whitelist.
// Empty AllowedModels means unrestricted.
// Supports exact model match and provider wildcard syntax: "provider:*".
func (e APIKeyEntry) IsModelAllowed(model string, providerNames []string) bool {
	allowed := normalizeStringList(e.AllowedModels)
	if len(allowed) == 0 {
		return true
	}

	trimmedModel := strings.TrimSpace(model)
	providerSet := make(map[string]struct{}, len(providerNames))
	for _, provider := range providerNames {
		provider = strings.ToLower(strings.TrimSpace(provider))
		if provider == "" {
			continue
		}
		providerSet[provider] = struct{}{}
	}

	for _, item := range allowed {
		if item == trimmedModel {
			return true
		}
		if strings.HasSuffix(item, ":*") {
			provider := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(item, ":*")))
			if provider == "" {
				continue
			}
			if _, ok := providerSet[provider]; ok {
				return true
			}
		}
	}
	return false
}

func normalizeStringList(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	result := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		result = append(result, trimmed)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

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
	APIKeys []APIKeyEntry `yaml:"api-keys" json:"api-keys"`

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
type StreamingConfig struct {
	// KeepAliveSeconds controls how often the server emits SSE heartbeats (": keep-alive\n\n").
	// <= 0 disables keep-alives. Default is 0.
	KeepAliveSeconds int `yaml:"keepalive-seconds,omitempty" json:"keepalive-seconds,omitempty"`

	// BootstrapRetries controls how many times the server may retry a streaming request before any bytes are sent,
	// to allow auth rotation / transient recovery.
	// <= 0 disables bootstrap retries. Default is 0.
	BootstrapRetries int `yaml:"bootstrap-retries,omitempty" json:"bootstrap-retries,omitempty"`
}
