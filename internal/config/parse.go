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
	if len(data) == 0 {
		return nil, fmt.Errorf("config payload is empty")
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
	cfg.DisableImageGeneration = DisableImageGenerationOff
	cfg.Pprof.Enable = false
	cfg.Pprof.Addr = DefaultPprofAddr
	cfg.AmpCode.RestrictManagementToLocalhost = false // Default to false: API key auth is sufficient
	cfg.RemoteManagement.PanelGitHubRepository = DefaultPanelGitHubRepository

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config payload: %w", err)
	}

	// Hash remote management key if plaintext is detected (nested), but do NOT persist.
	if cfg.RemoteManagement.SecretKey != "" && !looksLikeBcrypt(cfg.RemoteManagement.SecretKey) {
		hashed, errHash := bcrypt.GenerateFromPassword([]byte(cfg.RemoteManagement.SecretKey), bcrypt.DefaultCost)
		if errHash != nil {
			return nil, fmt.Errorf("hash remote management key: %w", errHash)
		}
		cfg.RemoteManagement.SecretKey = string(hashed)
	}

	cfg.RemoteManagement.PanelGitHubRepository = strings.TrimSpace(cfg.RemoteManagement.PanelGitHubRepository)
	if cfg.RemoteManagement.PanelGitHubRepository == "" {
		cfg.RemoteManagement.PanelGitHubRepository = DefaultPanelGitHubRepository
	}

	cfg.Pprof.Addr = strings.TrimSpace(cfg.Pprof.Addr)
	if cfg.Pprof.Addr == "" {
		cfg.Pprof.Addr = DefaultPprofAddr
	}

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

	// Apply the same sanitization pipeline.
	cfg.SanitizeGeminiKeys()
	cfg.SanitizeVertexCompatKeys()
	cfg.SanitizeCodexKeys()
	cfg.SanitizeCodexHeaderDefaults()
	cfg.SanitizeClaudeHeaderDefaults()
	cfg.SanitizeClaudeKeys()
	cfg.SanitizeOpenAICompatibility()
	cfg.OAuthExcludedModels = NormalizeOAuthExcludedModels(cfg.OAuthExcludedModels)
	cfg.SanitizeOAuthModelAlias()
	cfg.SanitizePayloadRules()

	return &cfg, nil
}
