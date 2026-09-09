package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

// ParseConfigBytesAndPersistMigrations parses a config snapshot and persists
// the same load-time management-secret migration as LoadConfig. The returned
// bytes are the generation represented by the returned config.
func ParseConfigBytesAndPersistMigrations(configFile string, data []byte) (*Config, []byte, error) {
	var raw struct {
		RemoteManagement struct {
			SecretKey string `yaml:"secret-key"`
		} `yaml:"remote-management"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, nil, fmt.Errorf("parse config payload: %w", err)
	}
	cfg, err := ParseConfigBytes(data)
	if err != nil {
		return nil, nil, err
	}
	if errValidate := cfg.Codex.LiveMediaRelay.Validate(); errValidate != nil {
		return nil, nil, errValidate
	}
	persisted := data
	if raw.RemoteManagement.SecretKey != "" && !looksLikeBcrypt(raw.RemoteManagement.SecretKey) {
		if migrated, errMigrate := updateNestedScalarBytes(data, []string{"remote-management", "secret-key"}, cfg.RemoteManagement.SecretKey); errMigrate == nil && persistMigrationIfUnchanged(configFile, data, migrated) {
			persisted = migrated
		}
	}
	return cfg, persisted, nil
}

type migrationFile interface {
	io.Reader
	io.Writer
	io.Seeker
	Close() error
	Truncate(size int64) error
}

var openMigrationFile = func(configFile string) (migrationFile, error) {
	return os.OpenFile(configFile, os.O_RDWR, 0)
}

func persistMigrationIfUnchanged(configFile string, original, migrated []byte) bool {
	f, err := openMigrationFile(configFile)
	if err != nil {
		return false
	}
	defer f.Close()
	current, err := io.ReadAll(f)
	if err != nil || !bytes.Equal(current, original) {
		return false
	}
	if _, err = f.Seek(0, 0); err != nil {
		return false
	}
	written, err := io.Copy(f, bytes.NewReader(migrated))
	if err == nil && written == int64(len(migrated)) && f.Truncate(int64(len(migrated))) == nil {
		return true
	}
	if !restoreMigrationOriginal(f, original) {
		log.WithField("path", configFile).Error("failed to restore config after management-secret migration write failure")
	}
	return false
}

func restoreMigrationOriginal(f migrationFile, original []byte) bool {
	if _, err := f.Seek(0, 0); err != nil {
		return false
	}
	written, err := io.Copy(f, bytes.NewReader(original))
	if err != nil || written != int64(len(original)) {
		return false
	}
	return f.Truncate(int64(len(original))) == nil
}

// ParseConfigBytes parses a YAML configuration payload into Config and applies the same
// in-memory normalizations as LoadConfigOptional, without persisting any changes to disk.
func ParseConfigBytes(data []byte) (*Config, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("config payload is empty")
	}

	if errValidate := validateCredentialWeightYAML(data); errValidate != nil {
		return nil, errValidate
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
	cfg.TransientErrorCooldownSeconds = 0
	cfg.DisableImageGeneration = DisableImageGenerationOff
	cfg.WebsocketAuth = true
	cfg.Pprof.Enable = false
	cfg.Pprof.Addr = DefaultPprofAddr
	cfg.RemoteManagement.PanelGitHubRepository = DefaultPanelGitHubRepository
	cfg.CredentialInFlight = DefaultCredentialInFlightConfig()

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config payload: %w", err)
	}

	cfg.CredentialConcurrency = cfg.CredentialConcurrency.WithDefaults()
	if errValidate := cfg.CredentialInFlight.Validate(); errValidate != nil {
		return nil, errValidate
	}
	if errValidate := cfg.ValidateCredentialWeights(); errValidate != nil {
		return nil, errValidate
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

	cfg.NormalizePluginsConfig()
	if errResolvePluginsDir := cfg.ResolvePluginsDir(); errResolvePluginsDir != nil && cfg.Plugins.Enabled {
		return nil, errResolvePluginsDir
	}

	// Apply the same sanitization pipeline.
	cfg.SanitizeGeminiKeys()
	cfg.SanitizeInteractionsKeys()
	cfg.SanitizeVertexCompatKeys()
	cfg.SanitizeCodexKeys()
	cfg.SanitizeXAIKeys()
	cfg.SanitizeCodexHeaderDefaults()
	cfg.SanitizeClaudeHeaderDefaults()
	cfg.SanitizeClaudeKeys()
	cfg.SanitizeOpenAICompatibility()
	cfg.OAuthExcludedModels = NormalizeOAuthExcludedModels(cfg.OAuthExcludedModels)
	cfg.SanitizeOAuthModelAlias()
	cfg.SanitizeOAuthRequestScopedErrors()
	cfg.SanitizePayloadRules()

	return &cfg, nil
}
