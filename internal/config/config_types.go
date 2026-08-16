package config

import (
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	sdkpluginstore "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginstore"
	"gopkg.in/yaml.v3"
)

// RequestScopedErrorRule configures custom classification and handling for upstream errors.
type RequestScopedErrorRule struct {
	// Status matches the HTTP status code of the upstream response (e.g. 400).
	Status int `yaml:"status,omitempty" json:"status,omitempty"`
	// Match matches substrings in the upstream error body.
	Match []string `yaml:"match,omitempty" json:"match,omitempty"`
	// MatchRegexr matches regular expressions in the upstream error body.
	MatchRegexr []string `yaml:"match-regexr,omitempty" json:"match-regexr,omitempty"`
	// Action specifies the handling behavior: "stop", "stop-and-cooldown", "continue", "continue-and-cooldown".
	Action string `yaml:"action,omitempty" json:"action,omitempty"`
}

// PluginsConfig holds dynamic plugin system settings.
type PluginsConfig struct {
	// Enabled toggles dynamic plugin loading.
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Dir is the plugin discovery directory.
	Dir string `yaml:"dir" json:"dir"`
	// StoreSources appends third-party plugin store registries to the built-in official source.
	StoreSources []string `yaml:"store-sources,omitempty" json:"store-sources,omitempty"`
	// StoreAuth defines optional auth rules for plugin store registry, metadata, and artifact requests.
	StoreAuth []sdkpluginstore.AuthConfig `yaml:"store-auth,omitempty" json:"store-auth,omitempty"`
	// AuthRevision changes when Home-managed plugin credentials change.
	AuthRevision int64 `yaml:"auth-revision,omitempty" json:"auth-revision,omitempty"`
	// Configs stores per-plugin instance configuration by plugin ID.
	Configs map[string]PluginInstanceConfig `yaml:"configs" json:"configs"`
}

// PluginInstanceConfig stores host-owned plugin settings and the original plugin YAML subtree.
type PluginInstanceConfig struct {
	// Enabled toggles this plugin instance. Nil is normalized to false during YAML parsing.
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// Priority controls plugin startup and routing order.
	Priority int `yaml:"priority,omitempty" json:"priority,omitempty"`
	// Raw preserves the full original plugin configuration YAML subtree.
	Raw yaml.Node `yaml:"-" json:"-"`
}

// UnmarshalYAML extracts host-owned fields while preserving the full original YAML node.
func (c *PluginInstanceConfig) UnmarshalYAML(value *yaml.Node) error {
	if c == nil {
		return nil
	}

	c.Priority = 0
	defaultEnabled := false
	c.Enabled = &defaultEnabled

	if value == nil || value.Kind == 0 {
		c.Raw = *defaultPluginInstanceConfigNode()
		return nil
	}

	c.Raw = *deepCopyNode(value)
	if value.Kind != yaml.MappingNode {
		return nil
	}

	for i := 0; i+1 < len(value.Content); i += 2 {
		key := value.Content[i]
		node := value.Content[i+1]
		if key == nil {
			continue
		}
		switch key.Value {
		case "enabled":
			var enabled bool
			if errDecodeEnabled := node.Decode(&enabled); errDecodeEnabled != nil {
				return fmt.Errorf("parse plugin enabled: %w", errDecodeEnabled)
			}
			c.Enabled = &enabled
		case "priority":
			var priority int
			if errDecodePriority := node.Decode(&priority); errDecodePriority != nil {
				return fmt.Errorf("parse plugin priority: %w", errDecodePriority)
			}
			c.Priority = priority
		}
	}

	return nil
}

// MarshalYAML returns the preserved raw plugin YAML subtree for lossless config output.
func (c PluginInstanceConfig) MarshalYAML() (any, error) {
	if c.Raw.Kind == 0 {
		return defaultPluginInstanceConfigNode(), nil
	}
	return deepCopyNode(&c.Raw), nil
}

func defaultPluginInstanceConfigNode() *yaml.Node {
	return &yaml.Node{
		Kind:    yaml.MappingNode,
		Tag:     "!!map",
		Content: []*yaml.Node{},
	}
}

// ClaudeHeaderDefaults configures the measured Claude Code software baseline.
// Verified native requests preserve their entrypoint and software shape only when their
// Claude Code, package, and runtime versions exactly match this baseline; unmeasured
// versions use the configured values. Timeout remains a fallback. Stabilized profiles
// also pin OS and Arch and never learn newer software versions automatically.
type ClaudeHeaderDefaults struct {
	UserAgent              string `yaml:"user-agent" json:"user-agent"`
	PackageVersion         string `yaml:"package-version" json:"package-version"`
	RuntimeVersion         string `yaml:"runtime-version" json:"runtime-version"`
	OS                     string `yaml:"os" json:"os"`
	Arch                   string `yaml:"arch" json:"arch"`
	Timeout                string `yaml:"timeout" json:"timeout"`
	Timezone               string `yaml:"timezone" json:"timezone"`
	StabilizeDeviceProfile *bool  `yaml:"stabilize-device-profile,omitempty" json:"stabilize-device-profile,omitempty"`
}

// CodexHeaderDefaults configures fallback header values injected into Codex
// model requests for OAuth/file-backed auth when the client omits them.
// UserAgent applies to HTTP and websocket requests; BetaFeatures only applies to websockets.
type CodexHeaderDefaults struct {
	UserAgent    string `yaml:"user-agent" json:"user-agent"`
	BetaFeatures string `yaml:"beta-features" json:"beta-features"`
}

// XAIConfig configures provider-wide xAI request behavior.
type XAIConfig struct {
	// InjectXSearch injects xAI's native x_search tool when the request does not declare it.
	InjectXSearch bool `yaml:"inject-x-search" json:"inject-x-search"`
}

// AntigravityConfig configures provider-wide Antigravity request behavior.
type AntigravityConfig struct {
	// SensitiveWords is a list of words to obfuscate with zero-width characters in system instructions.
	SensitiveWords []string `yaml:"sensitive-words,omitempty" json:"sensitive-words,omitempty"`
}

// CodexConfig configures provider-wide Codex request behavior.
type CodexConfig struct {
	IdentityConfuse bool `yaml:"identity-confuse" json:"identity-confuse"`
	// DisableCodexCloaking disables forcing the official Codex identity headers on HTTP/SSE and WebSocket requests.
	DisableCodexCloaking bool `yaml:"disable-codex-cloaking" json:"disable-codex-cloaking"`
	// OptimizeMultiAgentV2 optimizes official Codex multi-agent requests.
	OptimizeMultiAgentV2 bool `yaml:"optimize-multi-agent-v2" json:"optimize-multi-agent-v2"`
	// LiveMediaRelay terminates and relays Codex Live WebRTC media in this process.
	LiveMediaRelay CodexLiveMediaRelayConfig `yaml:"live-media-relay" json:"live-media-relay"`
}

// CodexLiveMediaRelayConfig configures the in-process Codex Live WebRTC gateway.
type CodexLiveMediaRelayConfig struct {
	Enabled                 bool                 `yaml:"enabled" json:"enabled"`
	MaxSessions             int                  `yaml:"max-sessions" json:"max-sessions"`
	DisablePrivateRemoteIPs bool                 `yaml:"disable-private-remote-ips" json:"disable-private-remote-ips"`
	PublicIP                string               `yaml:"public-ip" json:"public-ip"`
	UDPPortMin              uint16               `yaml:"udp-port-min" json:"udp-port-min"`
	UDPPortMax              uint16               `yaml:"udp-port-max" json:"udp-port-max"`
	ICEServers              []CodexLiveICEServer `yaml:"ice-servers" json:"ice-servers"`
}

// CodexLiveICEServer configures a STUN or TURN server for the media relay.
type CodexLiveICEServer struct {
	URLs       []string `yaml:"urls" json:"urls"`
	Username   string   `yaml:"username" json:"-"`
	Credential string   `yaml:"credential" json:"-"`
}

// TLSConfig holds HTTPS server settings.
type TLSConfig struct {
	// Enable toggles HTTPS server mode.
	Enable bool `yaml:"enable" json:"enable"`
	// Cert is the path to the TLS certificate file.
	Cert string `yaml:"cert" json:"cert"`
	// Key is the path to the TLS private key file.
	Key string `yaml:"key" json:"key"`
}

// PprofConfig holds pprof HTTP server settings.
type PprofConfig struct {
	// Enable toggles the pprof HTTP debug server.
	Enable bool `yaml:"enable" json:"enable"`
	// Addr is the host:port address for the pprof HTTP server.
	Addr string `yaml:"addr" json:"addr"`
}

// RemoteManagement holds management API configuration under 'remote-management'.
type RemoteManagement struct {
	// AllowRemote toggles remote (non-localhost) access to management API.
	AllowRemote bool `yaml:"allow-remote"`
	// SecretKey is the management key (plaintext or bcrypt hashed). YAML key intentionally 'secret-key'.
	SecretKey string `yaml:"secret-key"`
	// DisableControlPanel skips serving and syncing the bundled management UI when true.
	DisableControlPanel bool `yaml:"disable-control-panel"`
	// DisableAutoUpdatePanel disables automatic periodic background updates of the management panel asset from GitHub.
	// When false (the default), the background updater remains enabled; when true, the panel is only downloaded on first access if missing.
	DisableAutoUpdatePanel bool `yaml:"disable-auto-update-panel"`
	// PanelGitHubRepository overrides the GitHub repository used to fetch the management panel asset.
	// Accepts either a repository URL (https://github.com/org/repo) or an API releases endpoint.
	PanelGitHubRepository string `yaml:"panel-github-repository"`
}

// QuotaExceeded defines the behavior when API quota limits are exceeded.
// It provides configuration options for automatic failover mechanisms.
type QuotaExceeded struct {
	// SwitchProject indicates whether to automatically switch to another project when a quota is exceeded.
	SwitchProject bool `yaml:"switch-project" json:"switch-project"`

	// SwitchPreviewModel indicates whether to automatically switch to a preview model when a quota is exceeded.
	SwitchPreviewModel bool `yaml:"switch-preview-model" json:"switch-preview-model"`

	// AntigravityCredits enables credits-based last-resort fallback for Claude models.
	// When all free-tier auths are exhausted (429/503), the conductor retries with
	// an auth that has available Google One AI credits.
	AntigravityCredits bool `yaml:"antigravity-credits" json:"antigravity-credits"`
}

// RoutingConfig configures how credentials are selected for requests.
type RoutingConfig struct {
	// Strategy selects the credential selection strategy.
	// Supported values: "round-robin" (default), "weighted-round-robin", "fill-first".
	Strategy string `yaml:"strategy,omitempty" json:"strategy,omitempty"`

	// SessionAffinity enables universal session-sticky routing for all clients.
	// Explicit Claude Code, Codex, OpenCode, and pi session headers are preferred,
	// followed by prompt_cache_key, Responses conversation IDs, legacy body IDs,
	// execution or derived session identity, and the existing message-content hash fallback.
	// Automatic failover is always enabled when bound auth becomes unavailable.
	SessionAffinity bool `yaml:"session-affinity,omitempty" json:"session-affinity,omitempty"`

	// SessionAffinityTTL specifies how long session-to-auth bindings are retained.
	// Default: 1h. Accepts duration strings like "30m", "1h", "2h30m".
	SessionAffinityTTL string `yaml:"session-affinity-ttl,omitempty" json:"session-affinity-ttl,omitempty"`
}

// OAuthModelAlias defines a model ID alias for a specific channel.
// It maps the upstream model name (Name) to the client-visible alias (Alias).
// When Fork is true, the alias is added as an additional model in listings while
// keeping the original model ID available.
type OAuthModelAlias struct {
	Name  string `yaml:"name" json:"name"`
	Alias string `yaml:"alias" json:"alias"`
	Fork  bool   `yaml:"fork,omitempty" json:"fork,omitempty"`

	// DisplayName is the optional human-readable name shown in model catalogs.
	DisplayName string `yaml:"display-name,omitempty" json:"display-name,omitempty"`

	ForceMapping bool `yaml:"force-mapping,omitempty" json:"force-mapping,omitempty"`
}

// PayloadConfig defines default and override parameter rules applied to provider payloads.
type PayloadConfig struct {
	// Default defines rules that only set parameters when they are missing in the payload.
	Default []PayloadRule `yaml:"default" json:"default"`
	// DefaultRaw defines rules that set raw JSON values only when they are missing.
	DefaultRaw []PayloadRule `yaml:"default-raw" json:"default-raw"`
	// Override defines rules that always set parameters, overwriting any existing values.
	Override []PayloadRule `yaml:"override" json:"override"`
	// OverrideRaw defines rules that always set raw JSON values, overwriting any existing values.
	OverrideRaw []PayloadRule `yaml:"override-raw" json:"override-raw"`
	// Filter defines rules that remove parameters from the payload by JSON path.
	Filter []PayloadFilterRule `yaml:"filter" json:"filter"`
}

// PayloadFilterRule describes a rule to remove specific JSON paths from matching model payloads.
type PayloadFilterRule struct {
	// Models lists model entries with name pattern and protocol constraint.
	Models []PayloadModelRule `yaml:"models" json:"models"`
	// Params lists JSON paths (gjson/sjson syntax) to remove from the payload.
	Params []string `yaml:"params" json:"params"`
}

// PayloadRule describes a single rule targeting a list of models with parameter updates.
type PayloadRule struct {
	// Models lists model entries with name pattern and protocol constraint.
	Models []PayloadModelRule `yaml:"models" json:"models"`
	// Params maps JSON paths (gjson/sjson syntax) to values written into the payload.
	// For *-raw rules, values are treated as raw JSON fragments (strings are used as-is).
	Params map[string]any `yaml:"params" json:"params"`
}

// PayloadModelRule ties a model name pattern to a specific translator protocol.
type PayloadModelRule struct {
	// Name is the model name or wildcard pattern (e.g., "gpt-*", "*-5", "gemini-*-pro").
	Name string `yaml:"name" json:"name"`
	// Protocol restricts the rule to a specific translator format (e.g., "gemini", "responses").
	Protocol string `yaml:"protocol" json:"protocol"`
	// Headers restricts the rule to requests whose headers match all configured wildcard patterns.
	Headers map[string]string `yaml:"headers" json:"headers"`
	// FromProtocol restricts the rule to a specific source protocol (e.g., "gemini", "responses").
	FromProtocol string `yaml:"from-protocol" json:"from-protocol"`
	// Match requires payload JSON paths to equal the configured values.
	Match []map[string]any `yaml:"match" json:"match"`
	// NotMatch requires payload JSON paths to not equal the configured values.
	NotMatch []map[string]any `yaml:"not-match" json:"not-match"`
	// Exist requires payload JSON paths to exist and not be null.
	Exist []string `yaml:"exist" json:"exist"`
	// NotExist requires payload JSON paths to be missing or null.
	NotExist []string `yaml:"not-exist" json:"not-exist"`
}

// CloakConfig configures request cloaking for non-Claude-Code clients.
// Cloaking disguises API requests to appear as originating from the official Claude Code CLI.
type CloakConfig struct {
	// Mode controls cloaking behavior: "auto" (default), "always", or "never".
	// - "auto": cloak unless strong request signals identify a verified native entrypoint
	// - "always": cloak every unconfirmed client; confirmed native Claude Code remains passthrough
	// - "never": never apply cloaking
	Mode string `yaml:"mode,omitempty" json:"mode,omitempty"`

	// StrictMode controls how caller system prompts are handled when cloaking.
	// - false (default): legacy-model whitelist uses a user reminder; all other models use a mid-conversation system message
	// - true: strip caller system prompts and keep only the Claude Code billing and identity blocks
	StrictMode bool `yaml:"strict-mode,omitempty" json:"strict-mode,omitempty"`

	// SensitiveWords is a list of words to obfuscate with zero-width characters.
	// This can help bypass certain content filters.
	SensitiveWords []string `yaml:"sensitive-words,omitempty" json:"sensitive-words,omitempty"`

	// CacheUserID controls whether Claude user_id values are cached per API key.
	// When false, a fresh random user_id is generated for every request.
	CacheUserID *bool `yaml:"cache-user-id,omitempty" json:"cache-user-id,omitempty"`
}

// ClaudeKey represents the configuration for a Claude API key,
// including the API key itself and an optional base URL for the API endpoint.
type ClaudeKey struct {
	// APIKey is the authentication key for accessing Claude API services.
	APIKey string `yaml:"api-key" json:"api-key"`

	// Priority controls selection preference when multiple credentials match.
	// Higher values are preferred; defaults to 0.
	Priority int `yaml:"priority,omitempty" json:"priority,omitempty"`

	// Weight controls proportional selection under weighted-round-robin.
	// An omitted value defaults to 1; non-positive values exclude this credential; maximum 1,000,000.
	Weight *int `yaml:"weight,omitempty" json:"weight,omitempty"`

	// Prefix optionally namespaces models for this credential (e.g., "teamA/claude-sonnet-4").
	Prefix string `yaml:"prefix,omitempty" json:"prefix,omitempty"`

	// BaseURL is the base URL for the Claude API endpoint.
	// If empty, the default Claude API URL will be used.
	BaseURL string `yaml:"base-url" json:"base-url"`

	// ProxyURL overrides the global proxy setting for this API key if provided.
	ProxyURL string `yaml:"proxy-url" json:"proxy-url"`

	// Models defines upstream model names and aliases for request routing.
	Models []ClaudeModel `yaml:"models" json:"models"`

	// Headers optionally adds extra HTTP headers for requests sent with this key.
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`

	// ExcludedModels lists model IDs that should be excluded for this provider.
	ExcludedModels []string `yaml:"excluded-models,omitempty" json:"excluded-models,omitempty"`

	// RebuildMidSystemMessage moves Claude messages with role "system" into the top-level system field.
	RebuildMidSystemMessage bool `yaml:"rebuild-mid-system-message,omitempty" json:"rebuild-mid-system-message,omitempty"`

	// DisableCooling disables auth/model cooldown scheduling for this credential when true.
	DisableCooling bool `yaml:"disable-cooling,omitempty" json:"disable-cooling,omitempty"`

	// RequestRetry optionally overrides the global request-retry for this credential.
	// Nil or a negative value means "use the global request-retry". 0 disables retries.
	RequestRetry *int `yaml:"request-retry,omitempty" json:"request-retry,omitempty"`

	// RequestScopedErrors configures custom classification rules for upstream errors.
	RequestScopedErrors []RequestScopedErrorRule `yaml:"request-scoped-errors,omitempty" json:"request-scoped-errors,omitempty"`

	// Cloak configures request cloaking for non-Claude-Code clients.
	Cloak *CloakConfig `yaml:"cloak,omitempty" json:"cloak,omitempty"`

	// ExperimentalCCHSigning is retained for configuration compatibility.
	// CCH signing is automatic for Claude OAuth and supported direct upstreams.
	ExperimentalCCHSigning bool `yaml:"experimental-cch-signing,omitempty" json:"experimental-cch-signing,omitempty"`
}

func (k ClaudeKey) GetAPIKey() string { return k.APIKey }

func (k ClaudeKey) GetBaseURL() string { return k.BaseURL }

func (k ClaudeKey) GetPrefix() string { return k.Prefix }

func (k ClaudeKey) GetProxyURL() string { return k.ProxyURL }

// ClaudeModel describes a mapping between an alias and the actual upstream model name.
type ClaudeModel struct {
	// Name is the upstream model identifier used when issuing requests.
	Name string `yaml:"name" json:"name"`

	// Alias is the client-facing model name that maps to Name.
	Alias string `yaml:"alias" json:"alias"`

	// DisplayName is the optional human-readable name shown in model catalogs.
	DisplayName string `yaml:"display-name,omitempty" json:"display-name,omitempty"`

	// MaxContextLength overrides the context window advertised to Codex clients.
	MaxContextLength int `yaml:"max-context-length,omitempty" json:"max-context-length,omitempty"`

	// ForceMapping rewrites upstream response model fields back to Alias.
	ForceMapping bool `yaml:"force-mapping,omitempty" json:"force-mapping,omitempty"`

	// IsCompat preserves thinking blocks with empty signatures for compatible upstreams
	// and enables provider-aware signed-thinking replay for Claude-compatible API-key models.
	// Default false keeps the normal signature validation behavior.
	IsCompat bool `yaml:"is-compat,omitempty" json:"is-compat,omitempty"`

	// Thinking configures the thinking/reasoning capability for this model.
	Thinking *registry.ThinkingSupport `yaml:"thinking,omitempty" json:"thinking,omitempty"`
}

func (m ClaudeModel) GetName() string { return m.Name }

func (m ClaudeModel) GetAlias() string { return m.Alias }

func (m ClaudeModel) GetDisplayName() string   { return m.DisplayName }
func (m ClaudeModel) GetMaxContextLength() int { return m.MaxContextLength }
func (m ClaudeModel) GetForceMapping() bool    { return m.ForceMapping }
func (m ClaudeModel) GetIsCompat() bool        { return m.IsCompat }

func (m ClaudeModel) GetThinking() *registry.ThinkingSupport { return m.Thinking }

// CodexKey represents the configuration for a Codex API key,
// including the API key itself and an optional base URL for the API endpoint.
type CodexKey struct {
	// APIKey is the authentication key for accessing Codex API services.
	APIKey string `yaml:"api-key" json:"api-key"`

	// Priority controls selection preference when multiple credentials match.
	// Higher values are preferred; defaults to 0.
	Priority int `yaml:"priority,omitempty" json:"priority,omitempty"`

	// Weight controls proportional selection under weighted-round-robin.
	// An omitted value defaults to 1; non-positive values exclude this credential; maximum 1,000,000.
	Weight *int `yaml:"weight,omitempty" json:"weight,omitempty"`

	// Prefix optionally namespaces models for this credential (e.g., "teamA/gpt-5-codex").
	Prefix string `yaml:"prefix,omitempty" json:"prefix,omitempty"`

	// BaseURL is the base URL for the Codex API endpoint.
	// If empty, the default Codex API URL will be used.
	BaseURL string `yaml:"base-url" json:"base-url"`

	// Websockets enables the Responses API websocket transport for this credential.
	Websockets bool `yaml:"websockets,omitempty" json:"websockets,omitempty"`

	// AlphaSearch allows this Codex API key to serve the Alpha Search endpoint.
	AlphaSearch bool `yaml:"alpha-search,omitempty" json:"alpha-search,omitempty"`

	// ProxyURL overrides the global proxy setting for this API key if provided.
	ProxyURL string `yaml:"proxy-url" json:"proxy-url"`

	// Models defines upstream model names and aliases for request routing.
	Models []CodexModel `yaml:"models" json:"models"`

	// Headers optionally adds extra HTTP headers for requests sent with this key.
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`

	// ExcludedModels lists model IDs that should be excluded for this provider.
	ExcludedModels []string `yaml:"excluded-models,omitempty" json:"excluded-models,omitempty"`

	// DisableCooling disables auth/model cooldown scheduling for this credential when true.
	DisableCooling bool `yaml:"disable-cooling,omitempty" json:"disable-cooling,omitempty"`

	// RequestRetry optionally overrides the global request-retry for this credential.
	// Nil or a negative value means "use the global request-retry". 0 disables retries.
	RequestRetry *int `yaml:"request-retry,omitempty" json:"request-retry,omitempty"`

	// RequestScopedErrors configures custom classification rules for upstream errors.
	RequestScopedErrors []RequestScopedErrorRule `yaml:"request-scoped-errors,omitempty" json:"request-scoped-errors,omitempty"`
}

func (k CodexKey) GetAPIKey() string { return k.APIKey }

func (k CodexKey) GetBaseURL() string { return k.BaseURL }

func (k CodexKey) GetPrefix() string { return k.Prefix }

func (k CodexKey) GetProxyURL() string { return k.ProxyURL }

// CodexModel describes a mapping between an alias and the actual upstream model name.
type CodexModel struct {
	// Name is the upstream model identifier used when issuing requests.
	Name string `yaml:"name" json:"name"`

	// Alias is the client-facing model name that maps to Name.
	Alias string `yaml:"alias" json:"alias"`

	// DisplayName is the optional human-readable name shown in model catalogs.
	DisplayName string `yaml:"display-name,omitempty" json:"display-name,omitempty"`

	// MaxContextLength overrides the context window advertised to Codex clients.
	MaxContextLength int `yaml:"max-context-length,omitempty" json:"max-context-length,omitempty"`

	// ForceMapping rewrites upstream response model fields back to Alias.
	ForceMapping bool `yaml:"force-mapping,omitempty" json:"force-mapping,omitempty"`

	// IsCompat converts Codex MultiAgentV2 agent_message items into portable
	// Responses message/user input when codex.optimize-multi-agent-v2 is also true.
	// Use this for third-party Responses-compatible endpoints that do not accept
	// native agent_message items or empty-signature thinking blocks. Default false
	// keeps the native behavior unchanged.
	IsCompat bool `yaml:"is-compat,omitempty" json:"is-compat,omitempty"`

	// Thinking configures the thinking/reasoning capability for this model.
	Thinking *registry.ThinkingSupport `yaml:"thinking,omitempty" json:"thinking,omitempty"`
}

func (m CodexModel) GetName() string { return m.Name }

func (m CodexModel) GetAlias() string { return m.Alias }

func (m CodexModel) GetDisplayName() string   { return m.DisplayName }
func (m CodexModel) GetMaxContextLength() int { return m.MaxContextLength }
func (m CodexModel) GetForceMapping() bool    { return m.ForceMapping }
func (m CodexModel) GetIsCompat() bool        { return m.IsCompat }

func (m CodexModel) GetThinking() *registry.ThinkingSupport { return m.Thinking }

// XAIKey uses the Codex API key structure for native xAI execution.
type XAIKey = CodexKey

// XAIModel uses the Codex model mapping structure for xAI models.
type XAIModel = CodexModel

// GeminiKey represents the configuration for a Gemini API key,
// including optional overrides for upstream base URL, proxy routing, and headers.
type GeminiKey struct {
	// APIKey is the authentication key for accessing Gemini API services.
	APIKey string `yaml:"api-key" json:"api-key"`

	// Priority controls selection preference when multiple credentials match.
	// Higher values are preferred; defaults to 0.
	Priority int `yaml:"priority,omitempty" json:"priority,omitempty"`

	// Weight controls proportional selection under weighted-round-robin.
	// An omitted value defaults to 1; non-positive values exclude this credential; maximum 1,000,000.
	Weight *int `yaml:"weight,omitempty" json:"weight,omitempty"`

	// Prefix optionally namespaces models for this credential (e.g., "teamA/gemini-3-pro-preview").
	Prefix string `yaml:"prefix,omitempty" json:"prefix,omitempty"`

	// BaseURL optionally overrides the Gemini API endpoint.
	BaseURL string `yaml:"base-url,omitempty" json:"base-url,omitempty"`

	// ProxyURL optionally overrides the global proxy for this API key.
	ProxyURL string `yaml:"proxy-url,omitempty" json:"proxy-url,omitempty"`

	// Models defines upstream model names and aliases for request routing.
	Models []GeminiModel `yaml:"models,omitempty" json:"models,omitempty"`

	// Headers optionally adds extra HTTP headers for requests sent with this key.
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`

	// ExcludedModels lists model IDs that should be excluded for this provider.
	ExcludedModels []string `yaml:"excluded-models,omitempty" json:"excluded-models,omitempty"`

	// DisableCooling disables auth/model cooldown scheduling for this credential when true.
	DisableCooling bool `yaml:"disable-cooling,omitempty" json:"disable-cooling,omitempty"`

	// RequestRetry optionally overrides the global request-retry for this credential.
	// Nil or a negative value means "use the global request-retry". 0 disables retries.
	RequestRetry *int `yaml:"request-retry,omitempty" json:"request-retry,omitempty"`

	// RequestScopedErrors configures custom classification rules for upstream errors.
	RequestScopedErrors []RequestScopedErrorRule `yaml:"request-scoped-errors,omitempty" json:"request-scoped-errors,omitempty"`
}

func (k GeminiKey) GetAPIKey() string { return k.APIKey }

func (k GeminiKey) GetBaseURL() string { return k.BaseURL }

func (k GeminiKey) GetPrefix() string { return k.Prefix }

func (k GeminiKey) GetProxyURL() string { return k.ProxyURL }

// GeminiModel describes a mapping between an alias and the actual upstream model name.
type GeminiModel struct {
	// Name is the upstream model identifier used when issuing requests.
	Name string `yaml:"name" json:"name"`

	// Alias is the client-facing model name that maps to Name.
	Alias string `yaml:"alias" json:"alias"`

	// DisplayName is the optional human-readable name shown in model catalogs.
	DisplayName string `yaml:"display-name,omitempty" json:"display-name,omitempty"`

	// MaxContextLength overrides the context window advertised to Codex clients.
	MaxContextLength int `yaml:"max-context-length,omitempty" json:"max-context-length,omitempty"`

	// ForceMapping rewrites upstream response model fields back to Alias.
	ForceMapping bool `yaml:"force-mapping,omitempty" json:"force-mapping,omitempty"`

	// IsCompat preserves thinking blocks with empty signatures for compatible upstreams.
	// Default false keeps the normal signature validation behavior.
	IsCompat bool `yaml:"is-compat,omitempty" json:"is-compat,omitempty"`

	// Thinking configures the thinking/reasoning capability for this model.
	Thinking *registry.ThinkingSupport `yaml:"thinking,omitempty" json:"thinking,omitempty"`
}

func (m GeminiModel) GetName() string { return m.Name }

func (m GeminiModel) GetAlias() string { return m.Alias }

func (m GeminiModel) GetDisplayName() string   { return m.DisplayName }
func (m GeminiModel) GetMaxContextLength() int { return m.MaxContextLength }
func (m GeminiModel) GetForceMapping() bool    { return m.ForceMapping }
func (m GeminiModel) GetIsCompat() bool        { return m.IsCompat }

func (m GeminiModel) GetThinking() *registry.ThinkingSupport { return m.Thinking }

// OpenAICompatibility represents the configuration for OpenAI API compatibility
// with external providers, allowing model aliases to be routed through OpenAI API format.
type OpenAICompatibility struct {
	// Name is the identifier for this OpenAI compatibility configuration.
	Name string `yaml:"name" json:"name"`

	// Priority controls selection preference when multiple providers or credentials match.
	// Higher values are preferred; defaults to 0.
	Priority int `yaml:"priority,omitempty" json:"priority,omitempty"`

	// Disabled prevents this provider from being used for routing.
	Disabled bool `yaml:"disabled,omitempty" json:"disabled,omitempty"`

	// Prefix optionally namespaces model aliases for this provider (e.g., "teamA/kimi-k2").
	Prefix string `yaml:"prefix,omitempty" json:"prefix,omitempty"`

	// BaseURL is the base URL for the external OpenAI-compatible API endpoint.
	BaseURL string `yaml:"base-url" json:"base-url"`

	// APIKeyEntries defines API keys with optional per-key proxy configuration.
	APIKeyEntries []OpenAICompatibilityAPIKey `yaml:"api-key-entries,omitempty" json:"api-key-entries,omitempty"`

	// Models defines the model configurations including aliases for routing.
	Models []OpenAICompatibilityModel `yaml:"models" json:"models"`

	// Headers optionally adds extra HTTP headers for requests sent to this provider.
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`

	// SupportPromptCacheKey enables derived prompt_cache_key injection for supported requests.
	SupportPromptCacheKey bool `yaml:"support-prompt-cache-key,omitempty" json:"support-prompt-cache-key,omitempty"`

	// DisableCooling disables auth/model cooldown scheduling for this provider when true.
	DisableCooling bool `yaml:"disable-cooling,omitempty" json:"disable-cooling,omitempty"`

	// RequestRetry optionally overrides the global request-retry for this provider.
	// Nil or a negative value means "use the global request-retry". 0 disables retries.
	RequestRetry *int `yaml:"request-retry,omitempty" json:"request-retry,omitempty"`

	// RequestScopedErrors configures custom classification rules for upstream errors.
	RequestScopedErrors []RequestScopedErrorRule `yaml:"request-scoped-errors,omitempty" json:"request-scoped-errors,omitempty"`
}

// OpenAICompatibilityAPIKey represents an API key configuration with optional proxy setting.
type OpenAICompatibilityAPIKey struct {
	// APIKey is the authentication key for accessing the external API services.
	APIKey string `yaml:"api-key" json:"api-key"`

	// Weight controls proportional selection under weighted-round-robin.
	// An omitted value defaults to 1; non-positive values exclude this credential; maximum 1,000,000.
	Weight *int `yaml:"weight,omitempty" json:"weight,omitempty"`

	// ProxyURL overrides the global proxy setting for this API key if provided.
	ProxyURL string `yaml:"proxy-url,omitempty" json:"proxy-url,omitempty"`
}

// OpenAICompatibilityModel represents a model configuration for OpenAI compatibility,
// including the actual model name and its alias for API routing.
type OpenAICompatibilityModel struct {
	// Name is the actual model name used by the external provider.
	Name string `yaml:"name" json:"name"`

	// Alias is the model name alias that clients will use to reference this model.
	Alias string `yaml:"alias" json:"alias"`

	// DisplayName is the optional human-readable name shown in model catalogs.
	DisplayName string `yaml:"display-name,omitempty" json:"display-name,omitempty"`

	// MaxContextLength overrides the context window advertised to Codex clients.
	MaxContextLength int `yaml:"max-context-length,omitempty" json:"max-context-length,omitempty"`

	// ForceMapping rewrites upstream response model fields back to Alias.
	ForceMapping bool `yaml:"force-mapping,omitempty" json:"force-mapping,omitempty"`

	// Image marks this model as callable through /v1/images/generations and /v1/images/edits.
	Image bool `yaml:"image,omitempty" json:"image,omitempty"`

	// InputModalities declares chat/responses input capabilities (e.g. text, image) for Codex and other clients.
	// This is separate from Image, which only enables /v1/images/* endpoints.
	InputModalities []string `yaml:"input-modalities,omitempty" json:"input-modalities,omitempty"`

	// OutputModalities declares supported output modalities when known (e.g. text, image).
	OutputModalities []string `yaml:"output-modalities,omitempty" json:"output-modalities,omitempty"`

	// IsCompat preserves Claude thinking blocks for compatible upstreams.
	// Default false keeps the normal signature validation behavior.
	IsCompat bool `yaml:"is-compat,omitempty" json:"is-compat,omitempty"`

	// Thinking configures the thinking/reasoning capability for this model.
	// If nil, the model defaults to level-based reasoning with levels ["low", "medium", "high"].
	Thinking *registry.ThinkingSupport `yaml:"thinking,omitempty" json:"thinking,omitempty"`
}

func (m OpenAICompatibilityModel) GetName() string { return m.Name }

func (m OpenAICompatibilityModel) GetAlias() string { return m.Alias }

func (m OpenAICompatibilityModel) GetDisplayName() string   { return m.DisplayName }
func (m OpenAICompatibilityModel) GetMaxContextLength() int { return m.MaxContextLength }
func (m OpenAICompatibilityModel) GetForceMapping() bool    { return m.ForceMapping }
func (m OpenAICompatibilityModel) GetIsCompat() bool        { return m.IsCompat }

func (m OpenAICompatibilityModel) GetThinking() *registry.ThinkingSupport { return m.Thinking }
