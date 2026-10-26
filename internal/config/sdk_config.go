// Package config provides configuration management for the CLI Proxy API server.
// It handles loading and parsing YAML configuration files, and provides structured
// access to application settings including server port, authentication directory,
// debug settings, proxy configuration, and API keys.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// SDKConfig represents the application's configuration, loaded from a YAML file.
type SDKConfig struct {
	// MaxRequestBodyBytes limits the size (in bytes) of incoming HTTP request bodies.
	// Values <= 0 disable the limit (default).
	MaxRequestBodyBytes int64 `yaml:"max-request-body-bytes" json:"max-request-body-bytes"`

	// ResponseCompression controls negotiated compression of downstream HTTP responses.
	// It is configured under server.response-compression. Config.ServerResponseCompression
	// mirrors it for JSON and programmatic access; read the effective value via
	// Config.EffectiveResponseCompression.
	ResponseCompression ResponseCompressionConfig `yaml:"response-compression" json:"-"`

	// Client configures client-facing compatibility behavior.
	Client ClientConfig `yaml:"client" json:"client"`

	// OAuthOnlyFields records v8 provider settings that must wait for credential
	// selection and must not affect API-key credentials. Config YAML snapshots
	// preserve the corresponding v8 paths instead of serializing this metadata.
	OAuthOnlyFields map[string]bool `yaml:"-" json:"-"`

	// CodexResponseSteering mirrors the provider-wide runtime setting for API handlers.
	CodexResponseSteering bool `yaml:"-" json:"-"`

	// ProxyURL is the URL of an optional proxy server to use for outbound requests.
	ProxyURL string `yaml:"proxy-url" json:"proxy-url"`

	// TLSHandshakeTimeoutSeconds bounds upstream TLS handshakes made by the
	// shared uTLS client, in seconds. It applies to both the Claude Code
	// Anthropic transport and the Chrome ChatGPT/Codex transport; the
	// standard fallback transport is unaffected. It starts after TCP or
	// proxy connection establishment returns and does not bound dialing or
	// proxy CONNECT. Those uTLS paths use custom TLS dialing, for which Go
	// ignores http.Transport.TLSHandshakeTimeout, so a peer that accepts the
	// connection but never answers the ClientHello would otherwise block the
	// request indefinitely and request-retry could never engage. The bound
	// applies only during the TLS handshake and is removed once it succeeds,
	// so inference and long-running streams are unaffected. The default 0
	// disables the bound (previous behavior); negative values are treated as
	// 0, and values beyond the representable duration range saturate.
	TLSHandshakeTimeoutSeconds int `yaml:"tls-handshake-timeout-seconds" json:"tls-handshake-timeout-seconds"`

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

	// CodexOrphanDelegationCompatibility mirrors the provider-wide runtime setting for API handlers.
	CodexOrphanDelegationCompatibility bool `yaml:"-" json:"-"`

	// ClaudeCode configures Claude Code compatibility behavior.
	ClaudeCode ClaudeCodeConfig `yaml:"claude-code" json:"claude-code"`

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

// TLSHandshakeTimeout returns the configured upstream TLS handshake bound
// for requests using the shared uTLS client. A non-positive configured
// value disables the bound and yields 0.
func (c *SDKConfig) TLSHandshakeTimeout() time.Duration {
	if c == nil || c.TLSHandshakeTimeoutSeconds <= 0 {
		return 0
	}
	const maxTLSHandshakeTimeoutSeconds = math.MaxInt64 / int64(time.Second)
	if int64(c.TLSHandshakeTimeoutSeconds) > maxTLSHandshakeTimeoutSeconds {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(c.TLSHandshakeTimeoutSeconds) * time.Second
}

// ClientConfig configures client-facing compatibility behavior.
type ClientConfig struct {
	Codex CodexClientConfig `yaml:"codex" json:"codex"`
}

// CodexClientConfig configures Codex client compatibility and the model catalog.
type CodexClientConfig struct {
	// OptimizeMultiAgentV2 optimizes official Codex multi-agent requests across providers.
	// Default false leaves the client's multi-agent behavior unchanged.
	OptimizeMultiAgentV2 bool `yaml:"optimize-multi-agent-v2" json:"optimize-multi-agent-v2"`

	// EnableApplyPatch advertises freeform apply_patch only for supported models.
	// Default false clears the capability regardless of template metadata.
	EnableApplyPatch bool `yaml:"enable-apply-patch" json:"enable-apply-patch"`

	// WebSearchResultCache configures caching of upstream web search results for
	// Codex Responses clients that drop search results from replayed history.
	WebSearchResultCache CodexWebSearchResultCacheConfig `yaml:"web-search-result-cache" json:"web-search-result-cache"`
}

const (
	// DefaultCodexWebSearchResultCacheTTL is the default lifetime of cached web search results.
	DefaultCodexWebSearchResultCacheTTL = 24 * time.Hour

	// DefaultCodexWebSearchResultCacheLimit is the default maximum number of cached web search result entries.
	DefaultCodexWebSearchResultCacheLimit = 1024

	maxCodexWebSearchResultCacheTTLSeconds = int64(math.MaxInt64 / time.Second)
)

// CodexWebSearchResultCacheConfig configures the session-scoped web search result cache.
type CodexWebSearchResultCacheConfig struct {
	// Enabled controls whether web search results are cached and restored for Codex Responses requests.
	// Default is false (disabled).
	Enabled bool `yaml:"enabled" json:"enabled"`

	// TTL specifies how long cached web search results remain valid (e.g. 1800, "30m", "24h").
	// Pure numbers are interpreted as seconds. Defaults to "24h" when unset.
	TTL string `yaml:"ttl" json:"ttl"`

	// Limit specifies the maximum number of entries retained in the web search result cache.
	// Defaults to 1024 when unset or zero.
	Limit int `yaml:"limit" json:"limit"`
}

// Validate checks that the configured TTL and entry limit are valid.
func (c CodexWebSearchResultCacheConfig) Validate() error {
	if _, err := parseCodexWebSearchResultCacheTTL(c.TTL); err != nil {
		return err
	}
	if c.Limit < 0 {
		return fmt.Errorf("client.codex.web-search-result-cache.limit must not be negative")
	}
	return nil
}

// EffectiveTTL returns the parsed cache TTL, or DefaultCodexWebSearchResultCacheTTL when unset or zero.
func (c CodexWebSearchResultCacheConfig) EffectiveTTL() time.Duration {
	d, err := parseCodexWebSearchResultCacheTTL(c.TTL)
	if err != nil || d <= 0 {
		return DefaultCodexWebSearchResultCacheTTL
	}
	return d
}

// EffectiveLimit returns the configured entry limit, or DefaultCodexWebSearchResultCacheLimit when unset or non-positive.
func (c CodexWebSearchResultCacheConfig) EffectiveLimit() int {
	if c.Limit > 0 {
		return c.Limit
	}
	return DefaultCodexWebSearchResultCacheLimit
}

func (c *CodexWebSearchResultCacheConfig) UnmarshalYAML(value *yaml.Node) error {
	if c == nil {
		return nil
	}
	if value == nil || value.Kind == 0 || value.Tag == "!!null" {
		*c = CodexWebSearchResultCacheConfig{}
		return nil
	}
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("client.codex.web-search-result-cache must be a mapping")
	}
	var next CodexWebSearchResultCacheConfig
	seen := make(map[string]bool, len(value.Content)/2)
	for i := 0; i+1 < len(value.Content); i += 2 {
		keyNode := value.Content[i]
		valNode := value.Content[i+1]
		if keyNode == nil || valNode == nil {
			continue
		}
		if seen[keyNode.Value] {
			return fmt.Errorf("client.codex.web-search-result-cache: duplicate field %q", keyNode.Value)
		}
		seen[keyNode.Value] = true
		switch keyNode.Value {
		case "enabled":
			if valNode.Tag == "!!null" {
				next.Enabled = false
				continue
			}
			if valNode.Kind != yaml.ScalarNode || valNode.Tag != "!!bool" {
				return fmt.Errorf("client.codex.web-search-result-cache.enabled must be a boolean")
			}
			var enabled bool
			if errDecode := valNode.Decode(&enabled); errDecode != nil {
				return fmt.Errorf("client.codex.web-search-result-cache.enabled: %w", errDecode)
			}
			next.Enabled = enabled
		case "ttl":
			if valNode.Tag == "!!null" {
				next.TTL = ""
				continue
			}
			if valNode.Kind != yaml.ScalarNode || (valNode.Tag != "!!str" && valNode.Tag != "!!int") {
				return fmt.Errorf("client.codex.web-search-result-cache.ttl must be a duration string or integer seconds")
			}
			next.TTL = strings.TrimSpace(valNode.Value)
		case "limit":
			if valNode.Tag == "!!null" {
				next.Limit = 0
				continue
			}
			if valNode.Kind != yaml.ScalarNode || valNode.Tag != "!!int" {
				return fmt.Errorf("client.codex.web-search-result-cache.limit must be an integer")
			}
			var limit int
			if errDecode := valNode.Decode(&limit); errDecode != nil {
				return fmt.Errorf("client.codex.web-search-result-cache.limit: %w", errDecode)
			}
			next.Limit = limit
		default:
			return fmt.Errorf("client.codex.web-search-result-cache: unknown field %q", keyNode.Value)
		}
	}
	if errValidate := next.Validate(); errValidate != nil {
		return errValidate
	}
	*c = next
	return nil
}

func (c *CodexWebSearchResultCacheConfig) UnmarshalJSON(data []byte) error {
	if c == nil {
		return nil
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*c = CodexWebSearchResultCacheConfig{}
		return nil
	}
	var raw struct {
		Enabled bool            `json:"enabled"`
		TTL     json.RawMessage `json:"ttl"`
		Limit   int             `json:"limit"`
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if errDecode := decoder.Decode(&raw); errDecode != nil {
		return errDecode
	}
	ttl, errTTL := parseCodexWebSearchResultCacheTTLJSON(raw.TTL)
	if errTTL != nil {
		return errTTL
	}
	next := CodexWebSearchResultCacheConfig{
		Enabled: raw.Enabled,
		TTL:     ttl,
		Limit:   raw.Limit,
	}
	if errValidate := next.Validate(); errValidate != nil {
		return errValidate
	}
	*c = next
	return nil
}

func parseCodexWebSearchResultCacheTTLJSON(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}
	if trimmed[0] == '"' {
		var s string
		if errUnmarshal := json.Unmarshal(trimmed, &s); errUnmarshal != nil {
			return "", fmt.Errorf("client.codex.web-search-result-cache.ttl: %w", errUnmarshal)
		}
		return strings.TrimSpace(s), nil
	}
	var n int64
	if errUnmarshal := json.Unmarshal(trimmed, &n); errUnmarshal != nil {
		return "", fmt.Errorf("client.codex.web-search-result-cache.ttl must be a duration string or integer seconds")
	}
	return strconv.FormatInt(n, 10), nil
}

func parseCodexWebSearchResultCacheTTL(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "0" {
		return DefaultCodexWebSearchResultCacheTTL, nil
	}
	if secs, errParseInt := strconv.ParseInt(raw, 10, 64); errParseInt == nil {
		if secs < 0 {
			return 0, fmt.Errorf("client.codex.web-search-result-cache.ttl must not be negative")
		}
		if secs > maxCodexWebSearchResultCacheTTLSeconds {
			return 0, fmt.Errorf("client.codex.web-search-result-cache.ttl overflows duration")
		}
		return time.Duration(secs) * time.Second, nil
	}
	d, errParseDuration := time.ParseDuration(raw)
	if errParseDuration != nil {
		return 0, fmt.Errorf("client.codex.web-search-result-cache.ttl %q is invalid: %w", raw, errParseDuration)
	}
	if d < 0 {
		return 0, fmt.Errorf("client.codex.web-search-result-cache.ttl must not be negative")
	}
	if d == 0 {
		return DefaultCodexWebSearchResultCacheTTL, nil
	}
	return d, nil
}

// ClaudeCodeConfig configures Claude Code compatibility behavior.
type ClaudeCodeConfig struct {
	// DisableCloakingModelList disables model ID cloaking in Anthropic model list responses.
	DisableCloakingModelList bool `yaml:"disable-cloaking-model-list" json:"disable-cloaking-model-list"`
}

// StreamingConfig holds server streaming behavior configuration.
type StreamingConfig struct {
	// KeepAliveSeconds controls how often the server emits SSE heartbeats (": keep-alive\n\n")
	// or WebSocket Ping control frames.
	// <= 0 disables keep-alives. Default is 0.
	KeepAliveSeconds int `yaml:"keepalive-seconds,omitempty" json:"keepalive-seconds,omitempty"`

	// BootstrapRetries controls how many times the server may retry a streaming request before any bytes are sent,
	// to allow auth rotation / transient recovery.
	// <= 0 disables bootstrap retries. Default is 0.
	BootstrapRetries int `yaml:"bootstrap-retries,omitempty" json:"bootstrap-retries,omitempty"`

	// SendKeepAliveBeforeFT controls whether SSE heartbeats are sent before the first token arrives
	// when KeepAliveSeconds > 0. Default is false.
	SendKeepAliveBeforeFT bool `yaml:"send-keepalive-before-ft,omitempty" json:"send-keepalive-before-ft,omitempty"`
}
