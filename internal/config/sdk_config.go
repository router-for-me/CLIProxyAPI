// Package config provides configuration management for the CLI Proxy API server.
// It handles loading and parsing YAML configuration files, and provides structured
// access to application settings including server port, authentication directory,
// debug settings, proxy configuration, and API keys.
package config

import (
	"path"
	"strings"
	"sync/atomic"
)

// APIKeyDefaultPolicyAllowAll permits any model when a key has no explicit AllowedModels list.
const APIKeyDefaultPolicyAllowAll = "allow-all"

// APIKeyDefaultPolicyDenyAll forbids every model when a key has no explicit AllowedModels list.
const APIKeyDefaultPolicyDenyAll = "deny-all"

// APIKeyPolicy describes per-client-key access controls. AllowedModels supports
// path.Match-style globs (e.g. "claude-3-*", "gpt-4o*"). An empty AllowedModels
// list defers to APIKeyDefaultPolicy on the parent SDKConfig.
type APIKeyPolicy struct {
	// Key is the bearer/api key value this policy applies to.
	Key string `yaml:"key" json:"key"`

	// AllowedModels lists glob patterns of model identifiers this key may target.
	// JSON output uses camelCase to match what the dashboard sends on PUT.
	// PutAPIKeys still accepts allowed-models / allowed_models / allowedModels on
	// input for backward compatibility with older dashboard builds.
	AllowedModels []string `yaml:"allowed-models,omitempty" json:"allowedModels,omitempty"`
}

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
	DisableImageGeneration DisableImageGenerationMode `yaml:"disable-image-generation" json:"disable-image-generation"`

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

	// APIKeyPolicies stores per-key access policies (allowed model globs).
	// Keys present here must also exist in APIKeys to be accepted by the auth
	// provider; entries without a matching APIKeys row are ignored.
	APIKeyPolicies []APIKeyPolicy `yaml:"api-key-policies,omitempty" json:"api-key-policies,omitempty"`

	// policyIndex is a lazily-built, per-request-cheap lookup from api-key
	// value to its APIKeyPolicy. It is populated on first read after any
	// mutation to APIKeyPolicies. Mutators must call InvalidatePolicyIndex
	// so subsequent reads rebuild it. The pointer-based cache avoids locking
	// on the read path in the ModelACLMiddleware hot loop. The field is
	// unexported and carries no serialization tags — encoding/json and
	// yaml.v3 both ignore unexported fields, so it will not leak into
	// persisted config files.
	policyIndex atomic.Pointer[map[string]*APIKeyPolicy]

	// APIKeyDefaultPolicy controls behavior for keys with no entry in
	// APIKeyPolicies, or whose entry has an empty AllowedModels list. Valid
	// values are "allow-all" (default, backward compatible) and "deny-all".
	APIKeyDefaultPolicy string `yaml:"api-key-default-policy,omitempty" json:"api-key-default-policy,omitempty"`

	// PassthroughHeaders controls whether upstream response headers are forwarded to downstream clients.
	// Default is false (disabled).
	PassthroughHeaders bool `yaml:"passthrough-headers" json:"passthrough-headers"`

	// Streaming configures server-side streaming behavior (keep-alives and safe bootstrap retries).
	Streaming StreamingConfig `yaml:"streaming" json:"streaming"`

	// NonStreamKeepAliveInterval controls how often blank lines are emitted for non-streaming responses.
	// <= 0 disables keep-alives. Value is in seconds.
	NonStreamKeepAliveInterval int `yaml:"nonstream-keepalive-interval,omitempty" json:"nonstream-keepalive-interval,omitempty"`
}

// IsModelAllowedForKey reports whether the given client api key may target the
// given model. Matching uses path.Match glob semantics. An unknown key, or a
// key whose AllowedModels list is empty, falls back to APIKeyDefaultPolicy:
// "deny-all" rejects, anything else (including the default empty value) allows.
//
// The model argument is matched after stripping any provider prefix of the form
// "<prefix>/<model>" so that policies stay portable across prefixed credentials.
func (c *SDKConfig) IsModelAllowedForKey(key, model string) bool {
	if c == nil {
		return true
	}
	trimmedKey := strings.TrimSpace(key)
	if trimmedKey == "" {
		return c.defaultAllows()
	}

	// Strip a single leading "<prefix>/" segment if present so glob authors do
	// not have to anticipate every prefixed credential.
	candidate := model
	if idx := strings.Index(candidate, "/"); idx >= 0 && idx < len(candidate)-1 {
		candidate = candidate[idx+1:]
	}

	policy, ok := c.lookupPolicy(trimmedKey)
	if !ok {
		return c.defaultAllows()
	}
	patterns := policy.AllowedModels
	if len(patterns) == 0 {
		return c.defaultAllows()
	}
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if pattern == candidate {
			return true
		}
		if matched, err := path.Match(pattern, candidate); err == nil && matched {
			return true
		}
		// Also try the unstripped form for keys whose policies were
		// authored against the prefixed model name verbatim.
		if matched, err := path.Match(pattern, model); err == nil && matched {
			return true
		}
	}
	return false
}

// lookupPolicy returns the APIKeyPolicy for the given key using an O(1) map
// cache. The cache is lazily rebuilt after any mutation that calls
// InvalidatePolicyIndex. Callers must not retain the returned pointer across
// a mutation of APIKeyPolicies — the backing entry may have been replaced.
func (c *SDKConfig) lookupPolicy(key string) (*APIKeyPolicy, bool) {
	if c == nil {
		return nil, false
	}
	mp := c.policyIndex.Load()
	if mp == nil {
		built := make(map[string]*APIKeyPolicy, len(c.APIKeyPolicies))
		for i := range c.APIKeyPolicies {
			built[c.APIKeyPolicies[i].Key] = &c.APIKeyPolicies[i]
		}
		// Publish only if nobody beat us to it. If another goroutine won the
		// race, adopt their map — both are semantically equivalent snapshots
		// of the same APIKeyPolicies slice at this moment.
		if c.policyIndex.CompareAndSwap(nil, &built) {
			mp = &built
		} else {
			mp = c.policyIndex.Load()
		}
	}
	if mp == nil {
		return nil, false
	}
	policy, ok := (*mp)[key]
	return policy, ok
}

// InvalidatePolicyIndex clears the cached lookup map. Call this after any
// mutation of APIKeyPolicies (assignment, in-place edit, append, delete) so
// subsequent reads rebuild the index. Safe to call on a nil receiver.
func (c *SDKConfig) InvalidatePolicyIndex() {
	if c == nil {
		return
	}
	c.policyIndex.Store(nil)
}

// SetAPIKeyPolicies atomically replaces the policy slice and invalidates the
// cached lookup map in one step. Prefer this over direct slice assignment to
// keep read-side lookups consistent.
func (c *SDKConfig) SetAPIKeyPolicies(policies []APIKeyPolicy) {
	if c == nil {
		return
	}
	if len(policies) == 0 {
		c.APIKeyPolicies = nil
	} else {
		c.APIKeyPolicies = append([]APIKeyPolicy(nil), policies...)
	}
	c.InvalidatePolicyIndex()
}

func (c *SDKConfig) defaultAllows() bool {
	if c == nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(c.APIKeyDefaultPolicy), APIKeyDefaultPolicyDenyAll) == false
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
