// Package config provides configuration management for the CLI Proxy API server.
// It handles loading and parsing YAML configuration files, and provides structured
// access to application settings including server port, authentication directory,
// debug settings, proxy configuration, and API keys.
package config

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config represents the application's configuration, loaded from a YAML file.
type Config struct {
	SDKConfig `yaml:",inline"`
	// Host is the network host/interface on which the API server will bind.
	// Default is empty ("") to bind all interfaces (IPv4 + IPv6). Use "127.0.0.1" or "localhost" for local-only access.
	Host string `yaml:"host" json:"-"`
	// Port is the network port on which the API server will listen.
	Port int `yaml:"port" json:"-"`

	// TLS config controls HTTPS server settings.
	TLS TLSConfig `yaml:"tls" json:"tls"`

	// Home config is runtime-only and is populated from -home-jwt.
	Home HomeConfig `yaml:"-" json:"-"`

	// CredentialConcurrency contains Home-authoritative credential lifecycle settings.
	CredentialConcurrency CredentialConcurrencyConfig `yaml:"credential-concurrency" json:"credential-concurrency"`

	// CredentialInFlight configures credential observation snapshots.
	CredentialInFlight CredentialInFlightConfig `yaml:"credential-in-flight" json:"credential-in-flight"`

	// RemoteManagement nests management-related options under 'remote-management'.
	RemoteManagement RemoteManagement `yaml:"remote-management" json:"-"`

	// Plugins configures dynamic plugin discovery and per-plugin settings.
	Plugins PluginsConfig `yaml:"plugins" json:"plugins"`

	// AuthDir is the directory where authentication token files are stored.
	AuthDir string `yaml:"auth-dir" json:"-"`

	// Debug enables or disables debug-level logging and other debug features.
	Debug bool `yaml:"debug" json:"debug"`

	// Pprof config controls the optional pprof HTTP debug server.
	Pprof PprofConfig `yaml:"pprof" json:"pprof"`

	// CommercialMode disables high-overhead request logging and HTTP middleware features to minimize per-request memory usage.
	CommercialMode bool `yaml:"commercial-mode" json:"commercial-mode"`

	// LoggingToFile controls whether application logs are written to rotating files or stdout.
	LoggingToFile bool `yaml:"logging-to-file" json:"logging-to-file"`

	// LogsMaxTotalSizeMB limits the total size (in MB) of log files under the logs directory.
	// When exceeded, the oldest log files are deleted until within the limit. Set to 0 to disable.
	LogsMaxTotalSizeMB int `yaml:"logs-max-total-size-mb" json:"logs-max-total-size-mb"`

	// ErrorLogsMaxFiles limits the number of error log files retained when request logging is disabled.
	// When exceeded, the oldest error log files are deleted. Default is 10. Set to 0 to disable cleanup.
	ErrorLogsMaxFiles int `yaml:"error-logs-max-files" json:"error-logs-max-files"`

	// UsageStatisticsEnabled toggles in-memory usage aggregation; when false, usage data is discarded.
	UsageStatisticsEnabled bool `yaml:"usage-statistics-enabled" json:"usage-statistics-enabled"`

	// RedisUsageQueueRetentionSeconds controls how long usage queue items are retained
	// in memory for Management API consumers.
	// Default: 60. Max: 3600.
	RedisUsageQueueRetentionSeconds int `yaml:"redis-usage-queue-retention-seconds" json:"redis-usage-queue-retention-seconds"`

	// DisableCooling disables auth/model cooldown scheduling when true unless a credential or provider overrides it.
	DisableCooling bool `yaml:"disable-cooling" json:"disable-cooling"`

	// SaveCooldownStatus persists runtime cooldown status next to auth files when true.
	SaveCooldownStatus bool `yaml:"save-cooldown-status" json:"save-cooldown-status"`

	// TransientErrorCooldownSeconds controls cooldowns for transient upstream errors.
	// 0 keeps the legacy default cooldown. Negative values disable these cooldowns.
	TransientErrorCooldownSeconds int `yaml:"transient-error-cooldown-seconds" json:"transient-error-cooldown-seconds"`

	// AuthAutoRefreshWorkers overrides the size of the core auth auto-refresh worker pool.
	// When <= 0, the default worker count is used.
	AuthAutoRefreshWorkers int `yaml:"auth-auto-refresh-workers" json:"auth-auto-refresh-workers"`

	// RequestRetry defines the number of additional credential retry rounds after
	// the first round has exhausted its eligible credentials.
	RequestRetry int `yaml:"request-retry" json:"request-retry"`
	// MaxRetryCredentials defines the maximum number of different credentials to
	// try in each credential retry round.
	// Set to 0 or a negative value to keep trying all available credentials (legacy behavior).
	MaxRetryCredentials int `yaml:"max-retry-credentials" json:"max-retry-credentials"`
	// MaxRetryInterval defines the maximum positive cooldown wait, in seconds,
	// allowed before starting another credential retry round. A non-positive value
	// forbids positive cooldown waits; it does not disable same-round credential
	// failover or immediate additional rounds allowed by RequestRetry.
	MaxRetryInterval int `yaml:"max-retry-interval" json:"max-retry-interval"`

	// QuotaExceeded defines the behavior when a quota is exceeded.
	QuotaExceeded QuotaExceeded `yaml:"quota-exceeded" json:"quota-exceeded"`

	// Routing controls credential selection behavior.
	Routing RoutingConfig `yaml:"routing" json:"routing"`

	// WebsocketAuth enables or disables authentication for the WebSocket API.
	WebsocketAuth bool `yaml:"ws-auth" json:"ws-auth"`

	// AntigravitySignatureCacheEnabled controls whether signature cache validation is enabled for thinking blocks.
	// When true (default), cached signatures are preferred and validated.
	// When false, client signatures are used directly after normalization (bypass mode).
	AntigravitySignatureCacheEnabled *bool `yaml:"antigravity-signature-cache-enabled,omitempty" json:"antigravity-signature-cache-enabled,omitempty"`

	AntigravitySignatureBypassStrict *bool `yaml:"antigravity-signature-bypass-strict,omitempty" json:"antigravity-signature-bypass-strict,omitempty"`

	// Antigravity configures provider-wide Antigravity request behavior.
	Antigravity AntigravityConfig `yaml:"antigravity" json:"antigravity"`

	// GeminiKey defines Gemini API key configurations with optional routing overrides.
	GeminiKey []GeminiKey `yaml:"gemini-api-key" json:"gemini-api-key"`

	// InteractionsKey defines native Google Interactions API key configurations.
	InteractionsKey []GeminiKey `yaml:"interactions-api-key" json:"interactions-api-key"`

	// Codex defines a list of Codex API key configurations as specified in the YAML configuration file.
	CodexKey []CodexKey `yaml:"codex-api-key" json:"codex-api-key"`

	// XAIKey defines xAI API key configurations using the same structure as Codex API keys.
	XAIKey []XAIKey `yaml:"xai-api-key" json:"xai-api-key"`

	// XAI configures provider-wide xAI request behavior.
	XAI XAIConfig `yaml:"xai" json:"xai"`

	// Codex configures provider-wide Codex request behavior.
	Codex CodexConfig `yaml:"codex" json:"codex"`

	// CodexHeaderDefaults configures fallback headers for Codex OAuth model requests.
	// These are used only when the client does not send its own headers.
	CodexHeaderDefaults CodexHeaderDefaults `yaml:"codex-header-defaults" json:"codex-header-defaults"`

	// ClaudeKey defines a list of Claude API key configurations as specified in the YAML configuration file.
	ClaudeKey []ClaudeKey `yaml:"claude-api-key" json:"claude-api-key"`

	// ClaudeHeaderDefaults configures default header values for Claude API requests.
	// These are used as fallbacks when the client does not send its own headers.
	ClaudeHeaderDefaults ClaudeHeaderDefaults `yaml:"claude-header-defaults" json:"claude-header-defaults"`

	// DisableClaudeCloakMode globally disables Claude request cloaking when true.
	// Cloaking disguises requests as the official Claude Code CLI and replaces the
	// system prompt. When true, every Claude credential defaults to no cloaking
	// ("never"); a specific credential can still re-enable or override it via its own
	// cloak settings (the per claude-api-key "cloak" block, or a "cloak_mode" value in
	// the auth/OAuth token file). Default false preserves the per-client "auto" behavior.
	DisableClaudeCloakMode bool `yaml:"disable-claude-cloak-mode" json:"disable-claude-cloak-mode"`

	// OpenAICompatibility defines OpenAI API compatibility configurations for external providers.
	OpenAICompatibility []OpenAICompatibility `yaml:"openai-compatibility" json:"openai-compatibility"`

	// VertexCompatAPIKey defines Vertex AI-compatible API key configurations for third-party providers.
	// Used for services that use Vertex AI-style paths but with simple API key authentication.
	VertexCompatAPIKey []VertexCompatKey `yaml:"vertex-api-key" json:"vertex-api-key"`

	// OAuthExcludedModels defines per-provider global model exclusions applied to OAuth/file-backed auth entries.
	OAuthExcludedModels map[string][]string `yaml:"oauth-excluded-models,omitempty" json:"oauth-excluded-models,omitempty"`

	// OAuthModelAlias defines global model name aliases for OAuth/file-backed auth channels.
	// These aliases affect both model listing and model routing for supported channels:
	// vertex, aistudio, antigravity, claude, codex, kimi, xai.
	//
	// NOTE: This does not apply to existing per-credential model alias features under:
	// gemini-api-key, interactions-api-key, codex-api-key, xai-api-key, claude-api-key, openai-compatibility, and vertex-api-key.
	OAuthModelAlias map[string][]OAuthModelAlias `yaml:"oauth-model-alias,omitempty" json:"oauth-model-alias,omitempty"`

	// OAuthRequestScopedErrors defines per-provider request-scoped error rules applied to OAuth/file-backed auth entries.
	// Supported channels include: vertex, aistudio, antigravity, claude, codex, kimi, xai, and OAuth plugin provider keys.
	//
	// NOTE: This applies only to OAuth credentials and does not affect per-credential request-scoped-errors under *-api-key.
	OAuthRequestScopedErrors map[string][]RequestScopedErrorRule `yaml:"oauth-request-scoped-errors,omitempty" json:"oauth-request-scoped-errors,omitempty"`

	// Payload defines default and override rules for provider payload parameters.
	Payload PayloadConfig `yaml:"payload" json:"payload"`
}

// UnmarshalYAML preserves the flattened SDKConfig YAML shape while decoding
// every other Config field through its normal YAML handling.
func (c *Config) UnmarshalYAML(value *yaml.Node) error {
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
	if errSDK := c.SDKConfig.unmarshalYAMLWithFields(value, fields); errSDK != nil {
		return errSDK
	}

	configValue := reflect.ValueOf(c).Elem()
	configType := configValue.Type()
	sdkConfigType := reflect.TypeOf(SDKConfig{})
	for index := 0; index < configValue.NumField(); index++ {
		fieldType := configType.Field(index)
		if fieldType.Anonymous && fieldType.Type == sdkConfigType {
			continue
		}
		if fieldType.PkgPath != "" {
			continue
		}

		fieldName := yamlFieldName(fieldType.Tag.Get("yaml"), fieldType.Name)
		if fieldName == "-" {
			continue
		}
		fieldNode, ok := fields[fieldName]
		if !ok {
			continue
		}
		if errDecode := fieldNode.Decode(configValue.Field(index).Addr().Interface()); errDecode != nil {
			return fmt.Errorf("decode config field %q: %w", fieldName, errDecode)
		}
	}
	return nil
}

// UnmarshalJSON preserves the flattened SDKConfig JSON shape. SDKConfig has a
// custom JSON unmarshaler for presence tracking, so an ordinary Config alias
// would promote that method and skip the rest of Config's fields.
func (c *Config) UnmarshalJSON(data []byte) error {
	if c == nil {
		return nil
	}

	fields, errFields := decodeJSONFields(data)
	if errFields != nil {
		return errFields
	}
	if errSDK := c.SDKConfig.unmarshalJSONWithFields(data, fields); errSDK != nil {
		return errSDK
	}

	configValue := reflect.ValueOf(c).Elem()
	configType := configValue.Type()
	sdkConfigType := reflect.TypeOf(SDKConfig{})
	for index := 0; index < configValue.NumField(); index++ {
		fieldType := configType.Field(index)
		if fieldType.Anonymous && fieldType.Type == sdkConfigType {
			continue
		}
		if fieldType.PkgPath != "" {
			continue
		}

		fieldName := jsonFieldName(fieldType.Tag.Get("json"), fieldType.Name)
		if fieldName == "-" {
			continue
		}
		fieldData, ok := lookupJSONField(fields, fieldName)
		if !ok {
			continue
		}
		if errDecode := json.Unmarshal(fieldData, configValue.Field(index).Addr().Interface()); errDecode != nil {
			return fmt.Errorf("decode config field %q: %w", fieldName, errDecode)
		}
	}
	return nil
}

// MarshalJSON writes the effective SDK catalog setting while keeping SDKConfig
// fields flattened into the full configuration object.
func (c Config) MarshalJSON() ([]byte, error) {
	c.SDKConfig.ListUnprefixedModels = c.EffectiveListUnprefixedModels()

	type sdkConfigJSON SDKConfig
	sdkData, errSDK := json.Marshal(sdkConfigJSON(c.SDKConfig))
	if errSDK != nil {
		return nil, errSDK
	}

	fields := make(map[string]json.RawMessage)
	if errDecode := json.Unmarshal(sdkData, &fields); errDecode != nil {
		return nil, errDecode
	}

	configValue := reflect.ValueOf(c)
	configType := configValue.Type()
	sdkConfigType := reflect.TypeOf(SDKConfig{})
	for index := 0; index < configValue.NumField(); index++ {
		fieldType := configType.Field(index)
		if fieldType.Anonymous && fieldType.Type == sdkConfigType {
			continue
		}
		if fieldType.PkgPath != "" {
			continue
		}

		fieldName, options, _ := strings.Cut(fieldType.Tag.Get("json"), ",")
		if fieldName == "" {
			fieldName = fieldType.Name
		}
		if fieldName == "-" {
			continue
		}

		fieldValue := configValue.Field(index)
		if hasJSONOption(options, "omitempty") && isEmptyJSONValue(fieldValue) {
			continue
		}
		fieldData, errField := json.Marshal(fieldValue.Interface())
		if errField != nil {
			return nil, fmt.Errorf("marshal config field %q: %w", fieldName, errField)
		}
		fields[fieldName] = fieldData
	}

	return json.Marshal(fields)
}

func hasJSONOption(options, want string) bool {
	for _, option := range strings.Split(options, ",") {
		if option == want {
			return true
		}
	}
	return false
}

func isEmptyJSONValue(value reflect.Value) bool {
	switch value.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return value.Len() == 0
	case reflect.Bool:
		return !value.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return value.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return value.Float() == 0
	case reflect.Interface, reflect.Pointer:
		return value.IsNil()
	default:
		return false
	}
}

// MarshalYAML writes the effective SDK catalog setting before flattening the
// embedded SDK configuration into the root mapping.
func (c Config) MarshalYAML() (any, error) {
	c.SDKConfig.ListUnprefixedModels = c.EffectiveListUnprefixedModels()

	type sdkConfigYAML SDKConfig
	type configYAML Config
	sdkMapping, errSDK := marshalYAMLMapping(sdkConfigYAML(c.SDKConfig))
	if errSDK != nil {
		return nil, errSDK
	}
	configMapping, errConfig := marshalYAMLMapping(configYAML(c))
	if errConfig != nil {
		return nil, errConfig
	}
	removeYAMLMappingKeys(configMapping, sdkMapping)

	mapping := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	mapping.Content = append(mapping.Content, sdkMapping.Content...)
	mapping.Content = append(mapping.Content, configMapping.Content...)
	return mapping, nil
}

func removeYAMLMappingKeys(mapping, excluded *yaml.Node) {
	if mapping == nil || excluded == nil || mapping.Kind != yaml.MappingNode || excluded.Kind != yaml.MappingNode {
		return
	}

	excludedKeys := make(map[string]struct{}, len(excluded.Content)/2)
	for index := 0; index+1 < len(excluded.Content); index += 2 {
		key := excluded.Content[index]
		if key != nil {
			excludedKeys[key.Value] = struct{}{}
		}
	}

	filtered := make([]*yaml.Node, 0, len(mapping.Content))
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		key := mapping.Content[index]
		if key != nil {
			if _, duplicate := excludedKeys[key.Value]; duplicate {
				continue
			}
		}
		filtered = append(filtered, mapping.Content[index], mapping.Content[index+1])
	}
	mapping.Content = filtered
}

func marshalYAMLMapping(value any) (*yaml.Node, error) {
	data, errMarshal := yaml.Marshal(value)
	if errMarshal != nil {
		return nil, errMarshal
	}

	var document yaml.Node
	if errDecode := yaml.Unmarshal(data, &document); errDecode != nil {
		return nil, errDecode
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) == 0 || document.Content[0] == nil {
		return nil, fmt.Errorf("invalid generated yaml structure")
	}
	if document.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected generated root mapping node")
	}
	return document.Content[0], nil
}
