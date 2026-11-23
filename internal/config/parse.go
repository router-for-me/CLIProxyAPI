package config

import (
	"fmt"
	"strings"

	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

// ParseConfigBytes parses a YAML configuration payload into Config and applies the same
// in-memory normalizations as LoadConfigOptional, without persisting any changes to disk.
func ParseConfigBytes(data []byte) (*Config, error) {
	parsed, err := validateStartupConfig(data, false)
	if err != nil {
		return nil, err
	}
	if errHash := parsed.hashManagementSecret(); errHash != nil {
		return nil, errHash
	}
	parsed.cfg.sanitizeStartupConfig()
	return parsed.cfg, nil
}

// ParsePreparedConfigBytes loads the exact snapshot returned by upload preparation
// and records its hash without reopening or rewriting the local mirror. A nil
// snapshot is the optional standby result returned by upload preparation.
func ParsePreparedConfigBytes(data []byte) (*Config, error) {
	if data == nil {
		return standbyConfig(), nil
	}
	cfg, errParse := ParseConfigBytes(data)
	if errParse != nil {
		return nil, errParse
	}
	cfg.SourceHash = ComputeSourceHash(data)
	return cfg, nil
}

type startupConfig struct {
	cfg  *Config
	docs []*yaml.Node
}

func standbyConfig() *Config {
	cfg := &Config{CredentialInFlight: DefaultCredentialInFlightConfig()}
	cfg.NormalizePluginsConfig()
	cfg.NormalizeClaudeConfig()
	return cfg
}

// Decode errors and malformed YAML weights keep optional deployments in standby.
// Runtime validation errors (including bcrypt's byte limit) still fail startup.
func validateStartupConfig(data []byte, optional bool) (*startupConfig, error) {
	docs, err := decodeConfigStream(data)
	if err == nil && len(docs) == 0 {
		err = fmt.Errorf("config payload is empty")
	}
	if err == nil {
		err = validateCredentialWeightsNode(docs[0].Content[0])
	}

	var cfg Config
	// Keep defaults aligned with LoadConfigOptional.
	cfg.Host = "" // Default empty: binds to all interfaces (IPv4 + IPv6)
	cfg.LoggingToFile = false
	cfg.LogsMaxTotalSizeMB = 0
	cfg.ErrorLogsMaxFiles = 10
	cfg.UsageStatisticsEnabled = false
	cfg.RedisUsageQueueRetentionSeconds = 60
	cfg.DisableCooling = false
	cfg.SaveCooldownStatus = false
	cfg.RestartFiveHourWindow = true
	cfg.TransientErrorCooldownSeconds = 0
	cfg.DisableImageGeneration = DisableImageGenerationOff
	cfg.WebsocketAuth = true
	cfg.Pprof.Enable = false
	cfg.Pprof.Addr = DefaultPprofAddr
	cfg.Discovery.Enabled = false
	cfg.Discovery.ServiceType = DefaultDiscoveryServiceType
	cfg.Discovery.Subtypes = []string{"_chat-completions", "_responses", "_messages", "_generate-content", "_interactions"}
	cfg.RemoteManagement.PanelGitHubRepository = DefaultPanelGitHubRepository
	cfg.CredentialInFlight = DefaultCredentialInFlightConfig()

	if err == nil {
		err = docs[0].Decode(&cfg)
	}
	if err != nil {
		if optional {
			return &startupConfig{cfg: standbyConfig()}, nil
		}
		return nil, fmt.Errorf("parse config payload: %w", err)
	}

	if errValidate := validateTrustedProxies(cfg.TrustedProxies); errValidate != nil {
		return nil, errValidate
	}

	cfg.CredentialConcurrency = cfg.CredentialConcurrency.WithDefaults()
	if errValidate := cfg.CredentialInFlight.Validate(); errValidate != nil {
		return nil, errValidate
	}
	if errValidate := cfg.ValidateCredentialWeights(); errValidate != nil {
		return nil, errValidate
	}
	if errValidate := cfg.Codex.LiveMediaRelay.Validate(); errValidate != nil {
		return nil, errValidate
	}
	if cfg.Discovery.ServiceType == "" {
		cfg.Discovery.ServiceType = DefaultDiscoveryServiceType
	}
	if len(cfg.Discovery.Subtypes) == 0 {
		cfg.Discovery.Subtypes = []string{"_chat-completions", "_responses", "_messages", "_generate-content", "_interactions"}
	}

	if secret := cfg.RemoteManagement.SecretKey; len(secret) > 72 && !looksLikeBcrypt(secret) {
		return nil, fmt.Errorf("hash remote management key: %w", bcrypt.ErrPasswordTooLong)
	}

	cfg.RemoteManagement.PanelGitHubRepository = strings.TrimSpace(cfg.RemoteManagement.PanelGitHubRepository)
	if cfg.RemoteManagement.PanelGitHubRepository == "" {
		cfg.RemoteManagement.PanelGitHubRepository = DefaultPanelGitHubRepository
	}

	cfg.Pprof.Addr = strings.TrimSpace(cfg.Pprof.Addr)
	if cfg.Pprof.Addr == "" {
		cfg.Pprof.Addr = DefaultPprofAddr
	}

	cfg.KVStore.Path = strings.TrimSpace(cfg.KVStore.Path)

	if cfg.LogsMaxTotalSizeMB < 0 {
		cfg.LogsMaxTotalSizeMB = 0
	}

	if cfg.ErrorLogsMaxFiles < 0 {
		cfg.ErrorLogsMaxFiles = 10
	}

	if cfg.RedisUsageQueueRetentionSeconds <= 0 {
		cfg.RedisUsageQueueRetentionSeconds = 60
	} else if cfg.RedisUsageQueueRetentionSeconds > 3600 {
		log.WithField("value", cfg.RedisUsageQueueRetentionSeconds).Warn("redis-usage-queue-retention-seconds too large; clamping to 3600")
		cfg.RedisUsageQueueRetentionSeconds = 3600
	}

	if cfg.MaxRetryCredentials < 0 {
		cfg.MaxRetryCredentials = 0
	}

	cfg.NormalizePluginsConfig()
	cfg.NormalizeClaudeConfig()
	if errResolvePluginsDir := cfg.ResolvePluginsDir(); errResolvePluginsDir != nil && cfg.Plugins.Enabled {
		return nil, errResolvePluginsDir
	}

	return &startupConfig{cfg: &cfg, docs: docs}, nil
}

func (cfg *Config) sanitizeStartupConfig() {
	// Apply the same sanitization pipeline.
	cfg.SanitizeGeminiKeys()
	cfg.SanitizeInteractionsKeys()
	cfg.SanitizeVertexCompatKeys()
	cfg.SanitizeCodexKeys()
	cfg.SanitizeXAIKeys()
	cfg.SanitizeMetaKeys()
	cfg.SanitizeCodexHeaderDefaults()
	cfg.SanitizeClaudeHeaderDefaults()
	cfg.SanitizeClaudeKeys()
	cfg.SanitizeAntigravity()
	cfg.SanitizeDevin()
	cfg.SanitizeOpenAICompatibility()
	cfg.OAuthExcludedModels = NormalizeOAuthExcludedModels(cfg.OAuthExcludedModels)
	cfg.SanitizeOAuthModelAlias()
	cfg.SanitizeOAuthSettings()
	cfg.SanitizeOAuthRequestScopedErrors()
	cfg.SanitizePayloadRules()

}

func (p *startupConfig) hashManagementSecret() error {
	secret := p.cfg.RemoteManagement.SecretKey
	if secret == "" || looksLikeBcrypt(secret) {
		return nil
	}
	hashed, errHash := hashSecret(secret)
	if errHash != nil {
		return fmt.Errorf("hash remote management key: %w", errHash)
	}
	p.cfg.RemoteManagement.SecretKey = hashed
	return nil
}
