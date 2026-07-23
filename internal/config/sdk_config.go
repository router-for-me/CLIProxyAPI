// Package config provides configuration management for the CLI Proxy API server.
// It handles loading and parsing YAML configuration files, and provides structured
// access to application settings including server port, authentication directory,
// debug settings, proxy configuration, and API keys.
package config

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

	// CodexIntegration configures the optional Codex desktop/CLI integration.
	// It is disabled by default and runs inside the existing proxy process.
	CodexIntegration CodexIntegrationConfig `yaml:"codex-integration" json:"codex-integration"`
}

const (
	DefaultCodexCatalogFile    = "cliproxyapi-catalog.json"
	DefaultCodexMultiAgentMode = "v1"
)

// CodexIntegrationConfig controls Codex catalog generation and local configuration management.
type CodexIntegrationConfig struct {
	Enabled        bool                    `yaml:"enabled" json:"enabled"`
	CodexHome      string                  `yaml:"codex-home,omitempty" json:"codex-home,omitempty"`
	LoopbackAccess bool                    `yaml:"loopback-access" json:"loopback-access"`
	AutoSync       bool                    `yaml:"auto-sync" json:"auto-sync"`
	CatalogFile    string                  `yaml:"catalog-file" json:"catalog-file"`
	MultiAgentMode string                  `yaml:"multi-agent-mode" json:"multi-agent-mode"`
	Models         []CodexIntegrationModel `yaml:"models" json:"models"`
}

// CodexIntegrationModel maps a stable Codex-visible slug to one upstream provider model.
type CodexIntegrationModel struct {
	Slug                  string   `yaml:"slug" json:"slug"`
	Provider              string   `yaml:"provider" json:"provider"`
	UpstreamModel         string   `yaml:"upstream-model" json:"upstream-model"`
	DisplayName           string   `yaml:"display-name,omitempty" json:"display-name,omitempty"`
	Visible               bool     `yaml:"visible" json:"visible"`
	Featured              bool     `yaml:"featured" json:"featured"`
	Priority              int      `yaml:"priority,omitempty" json:"priority,omitempty"`
	InputModalities       []string `yaml:"input-modalities,omitempty" json:"input-modalities,omitempty"`
	SupportsTools         bool     `yaml:"supports-tools" json:"supports-tools"`
	SupportsParallelTools bool     `yaml:"supports-parallel-tools" json:"supports-parallel-tools"`
	SupportsWebSearch     bool     `yaml:"supports-web-search" json:"supports-web-search"`
}

// DefaultCodexIntegrationConfig returns the stable, opt-in Codex integration policy.
func DefaultCodexIntegrationConfig() CodexIntegrationConfig {
	return CodexIntegrationConfig{
		Enabled:        false,
		LoopbackAccess: true,
		AutoSync:       true,
		CatalogFile:    DefaultCodexCatalogFile,
		MultiAgentMode: DefaultCodexMultiAgentMode,
		Models: []CodexIntegrationModel{
			{
				Slug: "xai/grok-4.5", Provider: "xai", UpstreamModel: "grok-4.5",
				DisplayName: "Grok 4.5", Visible: true, Featured: true, Priority: 2,
				InputModalities: []string{"text", "image"}, SupportsTools: true, SupportsParallelTools: true, SupportsWebSearch: true,
			},
			{
				Slug: "antigravity/gemini-3.6-flash", Provider: "antigravity", UpstreamModel: "gemini-3.6-flash-high",
				DisplayName: "Gemini 3.6 Flash", Visible: true, Featured: true, Priority: 3,
				InputModalities: []string{"text", "image"}, SupportsTools: true, SupportsParallelTools: true,
			},
			{
				Slug: "antigravity/gemini-3.1-pro", Provider: "antigravity", UpstreamModel: "gemini-pro-agent",
				DisplayName: "Gemini 3.1 Pro", Visible: true, Featured: true, Priority: 4,
				InputModalities: []string{"text", "image"}, SupportsTools: true, SupportsParallelTools: true,
			},
			{
				Slug: "antigravity/claude-opus-4-6-thinking", Provider: "antigravity", UpstreamModel: "claude-opus-4-6-thinking",
				DisplayName: "Claude Opus 4.6 Thinking", Visible: true, Featured: true, Priority: 5,
				InputModalities: []string{"text", "image"}, SupportsTools: true, SupportsParallelTools: true,
			},
		},
	}
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
