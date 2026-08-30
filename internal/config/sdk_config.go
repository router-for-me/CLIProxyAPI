// Package config provides configuration management for the CLI Proxy API server.
// It handles loading and parsing YAML configuration files, and provides structured
// access to application settings including server port, authentication directory,
// debug settings, proxy configuration, and API keys.
package config

import (
	"encoding/json"
	"strings"

	"gopkg.in/yaml.v3"
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

	// ListUnprefixedModels controls whether unprefixed model aliases are exposed
	// in the model catalog when a credential has a prefix. When false, only the
	// prefixed form (e.g., "nim/<model>") is listed, while unprefixed requests
	// still route as before. The default is true. Use SetListUnprefixedModels to
	// explicitly disable this behavior in a programmatic configuration.
	ListUnprefixedModels bool `yaml:"list-unprefixed-models" json:"list-unprefixed-models"`

	// ListUnprefixedModelsExplicit distinguishes an intentional programmatic
	// value from the zero-value default. It is managed by SetListUnprefixedModels
	// and is not serialized.
	ListUnprefixedModelsExplicit bool `yaml:"-" json:"-"`

	// RequestLog enables or disables detailed request logging functionality.
	RequestLog bool `yaml:"request-log" json:"request-log"`

	// CodexOptimizeMultiAgentV2 mirrors the provider-wide runtime setting for API handlers.
	CodexOptimizeMultiAgentV2 bool `yaml:"-" json:"-"`

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

const (
	listUnprefixedModelsJSONKey = "list-unprefixed-models"
	listUnprefixedModelsYAMLKey = "list-unprefixed-models"
)

// UnmarshalYAML preserves the presence of list-unprefixed-models so an explicit
// YAML false is not confused with the zero-value default. This also handles the
// SDKConfig field when it is inlined into Config.
func (c *SDKConfig) UnmarshalYAML(value *yaml.Node) error {
	if c == nil {
		return nil
	}
	if value == nil {
		return nil
	}

	fields, errFields := decodeYAMLFields(value)
	if errFields != nil {
		return errFields
	}
	return c.unmarshalYAMLWithFields(value, fields)
}

func (c *SDKConfig) unmarshalYAMLWithFields(value *yaml.Node, fields map[string]yaml.Node) error {
	type sdkConfigYAML SDKConfig
	decoded := sdkConfigYAML(*c)
	if errDecode := value.Decode(&decoded); errDecode != nil {
		return errDecode
	}

	*c = SDKConfig(decoded)
	if yamlFieldPresent(fields, listUnprefixedModelsYAMLKey) {
		c.ListUnprefixedModelsExplicit = true
	}
	return nil
}

func decodeYAMLFields(value *yaml.Node) (map[string]yaml.Node, error) {
	var fields map[string]yaml.Node
	if errDecode := value.Decode(&fields); errDecode != nil {
		return nil, errDecode
	}
	return fields, nil
}

func yamlFieldPresent(fields map[string]yaml.Node, name string) bool {
	_, ok := fields[name]
	return ok
}

func yamlFieldName(tag, fallback string) string {
	name, _, _ := strings.Cut(tag, ",")
	if name == "" {
		return strings.ToLower(fallback)
	}
	return name
}

// UnmarshalJSON preserves the presence of list-unprefixed-models so an explicit
// JSON false is not confused with the zero-value default.
func (c *SDKConfig) UnmarshalJSON(data []byte) error {
	if c == nil {
		return nil
	}

	fields, errFields := decodeJSONFields(data)
	if errFields != nil {
		return errFields
	}
	return c.unmarshalJSONWithFields(data, fields)
}

func (c *SDKConfig) unmarshalJSONWithFields(data []byte, fields map[string]json.RawMessage) error {
	type sdkConfigJSON SDKConfig
	decoded := sdkConfigJSON(*c)
	if errDecode := json.Unmarshal(data, &decoded); errDecode != nil {
		return errDecode
	}

	*c = SDKConfig(decoded)
	if jsonFieldPresent(fields, listUnprefixedModelsJSONKey) {
		c.ListUnprefixedModelsExplicit = true
	}
	return nil
}

// MarshalJSON writes the effective list-unprefixed-models behavior while keeping
// the explicitness marker out of the serialized SDK configuration. A value
// receiver also covers direct SDKConfig values passed to json.Marshal.
func (c SDKConfig) MarshalJSON() ([]byte, error) {
	c.ListUnprefixedModels = (&c).EffectiveListUnprefixedModels()
	type sdkConfigJSON SDKConfig
	return json.Marshal(sdkConfigJSON(c))
}

func decodeJSONFields(data []byte) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if errDecode := json.Unmarshal(data, &fields); errDecode != nil {
		return nil, errDecode
	}
	return fields, nil
}

func jsonFieldPresent(fields map[string]json.RawMessage, name string) bool {
	_, ok := lookupJSONField(fields, name)
	return ok
}

func lookupJSONField(fields map[string]json.RawMessage, name string) (json.RawMessage, bool) {
	if value, ok := fields[name]; ok {
		return value, true
	}
	for key, value := range fields {
		if strings.EqualFold(key, name) {
			return value, true
		}
	}
	return nil, false
}

func jsonFieldName(tag, fallback string) string {
	name, _, _ := strings.Cut(tag, ",")
	if name == "" {
		return fallback
	}
	return name
}

// MarshalYAML writes the effective list-unprefixed-models behavior. The
// explicitness marker is intentionally not serialized, so a zero-value config
// must serialize its documented true default instead of raw false. A pointer
// receiver keeps the method out of Config's inline field flattening.
func (c *SDKConfig) MarshalYAML() (any, error) {
	if c == nil {
		return nil, nil
	}
	copyConfig := *c
	copyConfig.ListUnprefixedModels = c.EffectiveListUnprefixedModels()
	type sdkConfigYAML SDKConfig
	return sdkConfigYAML(copyConfig), nil
}

// SetListUnprefixedModels explicitly sets whether unprefixed model aliases are
// exposed in model catalogs. It lets programmatic configurations distinguish an
// explicit false from the zero value, which uses the documented true default.
func (c *SDKConfig) SetListUnprefixedModels(enabled bool) {
	if c == nil {
		return
	}
	c.ListUnprefixedModels = enabled
	c.ListUnprefixedModelsExplicit = true
}

// EffectiveListUnprefixedModels returns the configured catalog behavior. A
// programmatic zero-value configuration keeps the documented default true.
func (c *SDKConfig) EffectiveListUnprefixedModels() bool {
	if c == nil {
		return true
	}
	if c.ListUnprefixedModelsExplicit {
		return c.ListUnprefixedModels
	}
	return true
}

// ClaudeCodeConfig configures Claude Code compatibility behavior.
type ClaudeCodeConfig struct {
	// DisableCloakingModelList disables model ID cloaking in Anthropic model list responses.
	DisableCloakingModelList bool `yaml:"disable-cloaking-model-list" json:"disable-cloaking-model-list"`
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
