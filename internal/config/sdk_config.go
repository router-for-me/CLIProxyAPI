// Package config provides configuration management for the CLI Proxy API server.
// It handles loading and parsing YAML configuration files, and provides structured
// access to application settings including server port, authentication directory,
// debug settings, proxy configuration, and API keys.
package config

import (
	"fmt"
	"strings"
)

// SDKConfig represents the application's configuration, loaded from a YAML file.
type SDKConfig struct {
	// ProxyURL is the URL of an optional proxy server to use for outbound requests.
	ProxyURL string `yaml:"proxy-url" json:"proxy-url"`

	// DisableImageGeneration controls whether the built-in image_generation tool is injected/allowed.
	//
	// Supported values:
	//   - false (default): image_generation is enabled everywhere (normal behavior).
	//   - true: image_generation is disabled everywhere. The server stops injecting it, removes it from request payloads,
	//     and returns 404 for /v1/images/generations and /v1/images/edits.
	//   - "chat": disable image_generation injection for all non-images endpoints (e.g. /v1/responses, /v1/chat/completions),
	//     while keeping /v1/images/generations and /v1/images/edits enabled and preserving image_generation there.
	//   - "passthrough": do not modify the tool list on non-images endpoints — keep image_generation if the client
	//     sent it and do not inject it otherwise; on /v1/images/generations and /v1/images/edits behave like "chat".
	DisableImageGeneration DisableImageGenerationMode `yaml:"disable-image-generation" json:"disable-image-generation"`

	// GPTImage2BaseModel sets the base (mainline) model used by the legacy hosted
	// image_generation tool path when a Codex image request is not proxied directly
	// through the Image API.
	//
	// The value must start with "gpt-" (case-insensitive). If empty or invalid, the
	// default base model ("gpt-5.4-mini") is used.
	GPTImage2BaseModel string `yaml:"gpt-image-2-base-model,omitempty" json:"gpt-image-2-base-model,omitempty"`

	// VideoResultAuthCacheTTL controls how long video IDs stay pinned to the credential
	// that created them. Accepts duration strings like "30m" or "3h".
	// Empty or invalid values use the default 3h.
	VideoResultAuthCacheTTL string `yaml:"video-result-auth-cache-ttl,omitempty" json:"video-result-auth-cache-ttl,omitempty"`

	// ForceModelPrefix requires explicit model prefixes (e.g., "teamA/gemini-3-pro-preview")
	// to target prefixed credentials. When false, unprefixed model requests may use prefixed
	// credentials as well.
	ForceModelPrefix bool `yaml:"force-model-prefix" json:"force-model-prefix"`

	// RequestLog enables or disables detailed request logging functionality.
	RequestLog bool `yaml:"request-log" json:"request-log"`

	// CodexOptimizeMultiAgentV2 mirrors the provider-wide runtime setting for API handlers.
	CodexOptimizeMultiAgentV2 bool `yaml:"-" json:"-"`

	// ClaudeCode configures Claude Code compatibility behavior.
	ClaudeCode ClaudeCodeConfig `yaml:"claude-code" json:"claude-code"`

	// APIKeys is a list of keys for authenticating clients to this proxy server.
	APIKeys []string `yaml:"api-keys" json:"api-keys"`

	// CodexBuckets maps bucket names to the client API keys assigned to them.
	// A mapped key only uses codex credentials whose auth file carries the same
	// top-level "bucket" value; unmapped keys only use unbucketed credentials.
	CodexBuckets map[string]CodexBucket `yaml:"codex-buckets" json:"codex-buckets"`

	// PassthroughHeaders controls whether upstream response headers are forwarded to downstream clients.
	// Default is false (disabled).
	PassthroughHeaders bool `yaml:"passthrough-headers" json:"passthrough-headers"`

	// Streaming configures server-side streaming behavior (keep-alives and safe bootstrap retries).
	Streaming StreamingConfig `yaml:"streaming" json:"streaming"`

	// NonStreamKeepAliveInterval controls how often blank lines are emitted for non-streaming responses.
	// <= 0 disables keep-alives. Value is in seconds.
	NonStreamKeepAliveInterval int `yaml:"nonstream-keepalive-interval,omitempty" json:"nonstream-keepalive-interval,omitempty"`
}

// ClaudeCodeConfig configures Claude Code compatibility behavior.
type ClaudeCodeConfig struct {
	// DisableCloakingModelList disables model ID cloaking in Anthropic model list responses.
	DisableCloakingModelList bool `yaml:"disable-cloaking-model-list" json:"disable-cloaking-model-list"`
}

// CodexBucket groups client API keys allowed to use codex credentials tagged
// with the bucket's name.
type CodexBucket struct {
	// APIKeys lists the client API keys mapped to this bucket.
	APIKeys []string `yaml:"api-keys" json:"api-keys"`
}

// CodexBucketForAPIKey returns the bucket name the client API key is mapped
// to, or the empty string when the key is unmapped. Configured keys are
// trimmed before comparison (matching ValidateCodexBuckets); apiKey is
// compared as-is since it comes directly from the caller's request.
func (c *SDKConfig) CodexBucketForAPIKey(apiKey string) string {
	if c == nil || apiKey == "" {
		return ""
	}
	for name, bucket := range c.CodexBuckets {
		for _, key := range bucket.APIKeys {
			if strings.TrimSpace(key) == apiKey {
				return name
			}
		}
	}
	return ""
}

// CodexBucketForContextValue resolves the codex bucket for a raw context
// value (typically a gin "userApiKey" context entry) by formatting it and
// delegating to CodexBucketForAPIKey. It exists so every call site that
// reads the client API key out of request context (handlers, codex-only
// side channels) shares one lookup implementation instead of re-deriving
// it. Returns "" when v is nil or the key is unmapped.
func (c *SDKConfig) CodexBucketForContextValue(v any) string {
	if c == nil || v == nil {
		return ""
	}
	return c.CodexBucketForAPIKey(fmt.Sprint(v))
}

// ValidateCodexBuckets rejects configurations that map one client API key
// into more than one bucket.
func (c *SDKConfig) ValidateCodexBuckets() error {
	if c == nil {
		return nil
	}
	seen := make(map[string]string)
	for name, bucket := range c.CodexBuckets {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("codex-buckets: bucket name must not be empty or whitespace")
		}
		for _, key := range bucket.APIKeys {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			if prev, ok := seen[key]; ok && prev != name {
				return fmt.Errorf("codex-buckets: an api key is mapped to both bucket %q and bucket %q", prev, name)
			}
			seen[key] = name
		}
	}
	return nil
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
