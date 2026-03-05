// Package util provides utility functions used across the CLIProxyAPI application.
// These functions handle common tasks such as determining AI service providers
// from model names and managing HTTP proxies.
package util

import (
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	log "github.com/sirupsen/logrus"
)

// GetProviderName determines all AI service providers capable of serving a registered model.
// It first queries the global model registry to retrieve the providers backing the supplied model name.
// When the model has not been registered yet, it falls back to legacy string heuristics to infer
// potential providers.
//
// Supported providers include (but are not limited to):
//   - "gemini" for Google's Gemini family
//   - "codex" for OpenAI GPT-compatible providers
//   - "claude" for Anthropic models
//   - "qwen" for Alibaba's Qwen models
//   - "openai-compatibility" for external OpenAI-compatible providers
//
// Parameters:
//   - modelName: The name of the model to identify providers for.
//   - cfg: The application configuration containing OpenAI compatibility settings.
//
// Returns:
//   - []string: All provider identifiers capable of serving the model, ordered by preference.
func GetProviderName(modelName string) []string {
	if modelName == "" {
		return nil
	}

	providers := make([]string, 0, 4)
	seen := make(map[string]struct{})

	appendProvider := func(name string) {
		if name == "" {
			return
		}
		if _, exists := seen[name]; exists {
			return
		}
		seen[name] = struct{}{}
		providers = append(providers, name)
	}

	for _, provider := range registry.GetGlobalRegistry().GetModelProviders(modelName) {
		appendProvider(provider)
	}

	if len(providers) > 0 {
		return providers
	}

	return providers
}

// ResolveAutoModel resolves the "auto" model name to an actual available model.
// It uses an empty handler type to get any available model from the registry.
//
// Parameters:
//   - modelName: The model name to check (should be "auto")
//
// Returns:
//   - string: The resolved model name, or the original if not "auto" or resolution fails
func ResolveAutoModel(modelName string) string {
	if modelName != "auto" {
		return modelName
	}

	// Use empty string as handler type to get any available model
	firstModel, err := registry.GetGlobalRegistry().GetFirstAvailableModel("")
	if err != nil {
		log.Warnf("Failed to resolve 'auto' model: %v, falling back to original model name", err)
		return modelName
	}

	log.Infof("Resolved 'auto' model to: %s", firstModel)
	return firstModel
}

// IsOpenAICompatibilityAlias checks if the given model name is an alias
// configured for OpenAI compatibility routing.
//
// Parameters:
//   - modelName: The model name to check
//   - cfg: The application configuration containing OpenAI compatibility settings
//
// Returns:
//   - bool: True if the model name is an OpenAI compatibility alias, false otherwise
func IsOpenAICompatibilityAlias(modelName string, cfg *config.Config) bool {
	if cfg == nil {
		return false
	}

	for _, compat := range cfg.OpenAICompatibility {
		for _, model := range compat.Models {
			if model.Alias == modelName {
				return true
			}
		}
	}
	return false
}

// GetOpenAICompatibilityConfig returns the OpenAI compatibility configuration
// and model details for the given alias.
//
// Parameters:
//   - alias: The model alias to find configuration for
//   - cfg: The application configuration containing OpenAI compatibility settings
//
// Returns:
//   - *config.OpenAICompatibility: The matching compatibility configuration, or nil if not found
//   - *config.OpenAICompatibilityModel: The matching model configuration, or nil if not found
func GetOpenAICompatibilityConfig(alias string, cfg *config.Config) (*config.OpenAICompatibility, *config.OpenAICompatibilityModel) {
	if cfg == nil {
		return nil, nil
	}

	for _, compat := range cfg.OpenAICompatibility {
		for _, model := range compat.Models {
			if model.Alias == alias {
				return &compat, &model
			}
		}
	}
	return nil, nil
}

// InArray checks if a string exists in a slice of strings.
// It iterates through the slice and returns true if the target string is found,
// otherwise it returns false.
//
// Parameters:
//   - hystack: The slice of strings to search in
//   - needle: The string to search for
//
// Returns:
//   - bool: True if the string is found, false otherwise
func InArray(hystack []string, needle string) bool {
	for _, item := range hystack {
		if needle == item {
			return true
		}
	}
	return false
}

// HideAPIKey obscures an API key for logging purposes, showing only the first and last few characters.
//
// Parameters:
//   - apiKey: The API key to hide.
//
// Returns:
//   - string: The obscured API key.
func HideAPIKey(apiKey string) string {
	if len(apiKey) > 8 {
		return apiKey[:4] + "..." + apiKey[len(apiKey)-4:]
	} else if len(apiKey) > 4 {
		return apiKey[:2] + "..." + apiKey[len(apiKey)-2:]
	} else if len(apiKey) > 2 {
		return apiKey[:1] + "..." + apiKey[len(apiKey)-1:]
	}
	return apiKey
}

// maskAuthorizationHeader masks the Authorization header value while preserving the auth type prefix.
// Common formats: "Bearer <token>", "Basic <credentials>", "ApiKey <key>", etc.
// It preserves the prefix (e.g., "Bearer ") and only masks the token/credential part.
//
// Parameters:
//   - value: The Authorization header value
//
// Returns:
//   - string: The masked Authorization value with prefix preserved
func MaskAuthorizationHeader(value string) string {
	parts := strings.SplitN(strings.TrimSpace(value), " ", 2)
	if len(parts) < 2 {
		return HideAPIKey(value)
	}
	return parts[0] + " " + HideAPIKey(parts[1])
}

// MaskSensitiveHeaderValue masks sensitive header values while preserving expected formats.
//
// Behavior by header key (case-insensitive):
//   - "Authorization": Preserve the auth type prefix (e.g., "Bearer ") and mask only the credential part.
//   - Headers containing "api-key": Mask the entire value using HideAPIKey.
//   - Others: Return the original value unchanged.
//
// Parameters:
//   - key:   The HTTP header name to inspect (case-insensitive matching).
//   - value: The header value to mask when sensitive.
//
// Returns:
//   - string: The masked value according to the header type; unchanged if not sensitive.
func MaskSensitiveHeaderValue(key, value string) string {
	lowerKey := strings.ToLower(strings.TrimSpace(key))
	switch {
	case strings.Contains(lowerKey, "authorization"):
		return MaskAuthorizationHeader(value)
	case strings.Contains(lowerKey, "api-key"),
		strings.Contains(lowerKey, "apikey"),
		strings.Contains(lowerKey, "token"),
		strings.Contains(lowerKey, "secret"):
		return HideAPIKey(value)
	default:
		return value
	}
}

// MaskSensitiveQuery masks sensitive query parameters, e.g. auth_token, within the raw query string.
func MaskSensitiveQuery(raw string) string {
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, "&")
	changed := false
	for i, part := range parts {
		if part == "" {
			continue
		}
		keyPart := part
		valuePart := ""
		if idx := strings.Index(part, "="); idx >= 0 {
			keyPart = part[:idx]
			valuePart = part[idx+1:]
		}
		decodedKey, err := url.QueryUnescape(keyPart)
		if err != nil {
			decodedKey = keyPart
		}
		if !shouldMaskQueryParam(decodedKey) {
			continue
		}
		decodedValue, err := url.QueryUnescape(valuePart)
		if err != nil {
			decodedValue = valuePart
		}
		masked := HideAPIKey(strings.TrimSpace(decodedValue))
		parts[i] = keyPart + "=" + url.QueryEscape(masked)
		changed = true
	}
	if !changed {
		return raw
	}
	return strings.Join(parts, "&")
}

func shouldMaskQueryParam(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return false
	}
	key = strings.TrimSuffix(key, "[]")
	if key == "key" || strings.Contains(key, "api-key") || strings.Contains(key, "apikey") || strings.Contains(key, "api_key") {
		return true
	}
	if strings.Contains(key, "token") || strings.Contains(key, "secret") {
		return true
	}
	return false
}
