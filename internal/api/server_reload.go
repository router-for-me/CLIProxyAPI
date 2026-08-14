package api

import (
	"context"
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/access"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/managementasset"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

func (s *Server) applyAccessConfig(oldCfg, newCfg *config.Config) bool {
	if s == nil || s.accessManager == nil || newCfg == nil {
		return false
	}
	if _, err := access.ApplyAccessProviders(s.accessManager, oldCfg, newCfg); err != nil {
		return false
	}
	return true
}

// UpdateClients updates the server's client list and configuration.
// This method is called when the configuration or authentication tokens change.
//
// Parameters:
//   - clients: The new slice of AI service clients
//   - cfg: The new application configuration
func (s *Server) UpdateClients(cfg *config.Config) {
	s.UpdateClientsContext(context.Background(), cfg)
}

// UpdateClientsContext updates runtime clients while honoring cancellation between
// short configuration and filesystem operations.
func (s *Server) UpdateClientsContext(ctx context.Context, cfg *config.Config) bool {
	if s == nil || cfg == nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errContext := ctx.Err(); errContext != nil {
		return false
	}
	// Reconstruct old config from YAML snapshot to avoid reference sharing issues
	var oldCfg *config.Config
	if len(s.oldConfigYaml) > 0 {
		_ = yaml.Unmarshal(s.oldConfigYaml, &oldCfg)
	}

	// Update request logger enabled state if it has changed
	previousRequestLog := false
	if oldCfg != nil {
		previousRequestLog = oldCfg.RequestLog
	}
	if s.requestLogger != nil && (oldCfg == nil || previousRequestLog != cfg.RequestLog) {
		if s.loggerToggle != nil {
			s.loggerToggle(cfg.RequestLog)
		} else if toggler, ok := s.requestLogger.(interface{ SetEnabled(bool) }); ok {
			toggler.SetEnabled(cfg.RequestLog)
		}
	}

	if oldCfg == nil || oldCfg.Home.Enabled != cfg.Home.Enabled {
		if setter, ok := s.requestLogger.(interface{ SetHomeEnabled(bool) }); ok {
			setter.SetHomeEnabled(cfg.Home.Enabled)
		}
	}

	if oldCfg == nil || oldCfg.LoggingToFile != cfg.LoggingToFile || oldCfg.LogsMaxTotalSizeMB != cfg.LogsMaxTotalSizeMB {
		if err := logging.ConfigureLogOutput(cfg); err != nil {
			log.Errorf("failed to reconfigure log output: %v", err)
		}
		if errContext := ctx.Err(); errContext != nil {
			return false
		}
	}

	if oldCfg == nil || oldCfg.UsageStatisticsEnabled != cfg.UsageStatisticsEnabled {
		redisqueue.SetUsageStatisticsEnabled(cfg.UsageStatisticsEnabled)
	}

	if oldCfg == nil || oldCfg.RedisUsageQueueRetentionSeconds != cfg.RedisUsageQueueRetentionSeconds {
		redisqueue.SetRetentionSeconds(cfg.RedisUsageQueueRetentionSeconds)
	}

	if s.requestLogger != nil && (oldCfg == nil || oldCfg.ErrorLogsMaxFiles != cfg.ErrorLogsMaxFiles) {
		if setter, ok := s.requestLogger.(interface{ SetErrorLogsMaxFiles(int) }); ok {
			setter.SetErrorLogsMaxFiles(cfg.ErrorLogsMaxFiles)
		}
	}

	if oldCfg == nil || oldCfg.DisableCooling != cfg.DisableCooling {
		auth.SetQuotaCooldownDisabled(cfg.DisableCooling)
	}
	if oldCfg == nil || oldCfg.TransientErrorCooldownSeconds != cfg.TransientErrorCooldownSeconds {
		auth.SetTransientErrorCooldownSeconds(cfg.TransientErrorCooldownSeconds)
	}

	if oldCfg != nil && oldCfg.DisableImageGeneration != cfg.DisableImageGeneration {
		log.Infof("disable-image-generation updated: %v -> %v", oldCfg.DisableImageGeneration, cfg.DisableImageGeneration)
	}

	applySignatureCacheConfig(oldCfg, cfg)

	if s.handlers != nil && s.handlers.AuthManager != nil {
		s.handlers.AuthManager.SetRetryConfig(cfg.RequestRetry, time.Duration(cfg.MaxRetryInterval)*time.Second, cfg.MaxRetryCredentials)
	}

	// Update log level dynamically when debug flag changes
	if oldCfg == nil || oldCfg.Debug != cfg.Debug {
		util.SetLogLevel(cfg)
	}

	prevSecretEmpty := true
	if oldCfg != nil {
		prevSecretEmpty = oldCfg.RemoteManagement.SecretKey == ""
	}
	newSecretEmpty := cfg.RemoteManagement.SecretKey == ""
	if s.envManagementSecret {
		s.registerManagementRoutes()
		if s.managementRoutesEnabled.CompareAndSwap(false, true) {
			log.Info("management routes enabled via MANAGEMENT_PASSWORD")
		} else {
			s.managementRoutesEnabled.Store(true)
		}
	} else {
		switch {
		case prevSecretEmpty && !newSecretEmpty:
			s.registerManagementRoutes()
			if s.managementRoutesEnabled.CompareAndSwap(false, true) {
				log.Info("management routes enabled after secret key update")
			} else {
				s.managementRoutesEnabled.Store(true)
			}
		case !prevSecretEmpty && newSecretEmpty:
			if s.managementRoutesEnabled.CompareAndSwap(true, false) {
				log.Info("management routes disabled after secret key removal")
			} else {
				s.managementRoutesEnabled.Store(false)
			}
		default:
			s.managementRoutesEnabled.Store(!newSecretEmpty)
		}
	}
	redisqueue.SetEnabled(s.managementRoutesEnabled.Load() || (cfg != nil && cfg.Home.Enabled))

	exampleAPIKeySafeModeRequired := s.exampleAPIKeySafeModeRequired(cfg)
	if exampleAPIKeySafeModeRequired {
		s.exampleAPIKeySafeModeActive.Store(true)
	}
	accessConfigApplied := s.applyAccessConfig(oldCfg, cfg)
	if accessConfigApplied || exampleAPIKeySafeModeRequired {
		s.exampleAPIKeySafeModeActive.Store(exampleAPIKeySafeModeRequired)
	}
	s.cfg = cfg
	if s.codexLiveHandler != nil {
		if errUpdate := s.codexLiveHandler.UpdateConfig(cfg); errUpdate != nil {
			log.WithError(errUpdate).Error("failed to update Codex Live media relay configuration")
		}
	}
	s.wsAuthEnabled.Store(cfg.WebsocketAuth)
	if oldCfg != nil && s.wsAuthChanged != nil && oldCfg.WebsocketAuth != cfg.WebsocketAuth {
		s.wsAuthChanged(oldCfg.WebsocketAuth, cfg.WebsocketAuth)
	}
	managementasset.SetCurrentConfig(cfg)
	if errContext := ctx.Err(); errContext != nil {
		return false
	}
	// Save YAML snapshot for next comparison
	s.oldConfigYaml, _ = yaml.Marshal(cfg)

	s.handlers.UpdateClients(effectiveSDKConfig(cfg))
	s.handlers.SetPluginHost(s.pluginHost)
	if s.pluginHost != nil {
		s.pluginHost.SetModelExecutor(s.handlers)
		s.pluginHost.SetAuthManager(s.handlers.AuthManager)
	}

	if s.mgmt != nil {
		s.mgmt.SetConfig(cfg)
		s.mgmt.SetAuthManager(s.handlers.AuthManager)
		s.mgmt.SetPluginHost(s.pluginHost)
	}
	s.refreshPluginManagementRoutes()

	// Count client sources from configuration and auth store.
	authEntries := 0
	if cfg != nil && !cfg.Home.Enabled {
		tokenStore := sdkAuth.GetTokenStore()
		if dirSetter, ok := tokenStore.(interface{ SetBaseDir(string) }); ok {
			dirSetter.SetBaseDir(cfg.AuthDir)
		}
		authEntries = util.CountAuthFiles(ctx, tokenStore)
		if errContext := ctx.Err(); errContext != nil {
			return false
		}
	}
	geminiAPIKeyCount := len(cfg.GeminiKey)
	interactionsAPIKeyCount := len(cfg.InteractionsKey)
	claudeAPIKeyCount := len(cfg.ClaudeKey)
	codexAPIKeyCount := len(cfg.CodexKey)
	xaiAPIKeyCount := len(cfg.XAIKey)
	vertexAICompatCount := len(cfg.VertexCompatAPIKey)
	openAICompatCount := 0
	for i := range cfg.OpenAICompatibility {
		entry := cfg.OpenAICompatibility[i]
		if entry.Disabled {
			continue
		}
		openAICompatCount += len(entry.APIKeyEntries)
	}

	total := authEntries + geminiAPIKeyCount + interactionsAPIKeyCount + claudeAPIKeyCount + codexAPIKeyCount + xaiAPIKeyCount + vertexAICompatCount + openAICompatCount
	fmt.Printf("server clients and configuration updated: %d clients (%d auth entries + %d Gemini API keys + %d Interactions API keys + %d Claude API keys + %d Codex keys + %d xAI keys + %d Vertex-compat + %d OpenAI-compat)\n",
		total,
		authEntries,
		geminiAPIKeyCount,
		interactionsAPIKeyCount,
		claudeAPIKeyCount,
		codexAPIKeyCount,
		xaiAPIKeyCount,
		vertexAICompatCount,
		openAICompatCount,
	)
	return ctx.Err() == nil
}

func (s *Server) SetWebsocketAuthChangeHandler(fn func(bool, bool)) {
	if s == nil {
		return
	}
	s.wsAuthChanged = fn
}

func configuredSignatureCacheEnabled(cfg *config.Config) bool {
	if cfg != nil && cfg.AntigravitySignatureCacheEnabled != nil {
		return *cfg.AntigravitySignatureCacheEnabled
	}
	return true
}

func applySignatureCacheConfig(oldCfg, cfg *config.Config) {
	newVal := configuredSignatureCacheEnabled(cfg)
	newStrict := configuredSignatureBypassStrict(cfg)
	if oldCfg == nil {
		cache.SetSignatureCacheEnabled(newVal)
		cache.SetSignatureBypassStrictMode(newStrict)
		return
	}

	oldVal := configuredSignatureCacheEnabled(oldCfg)
	if oldVal != newVal {
		cache.SetSignatureCacheEnabled(newVal)
	}

	oldStrict := configuredSignatureBypassStrict(oldCfg)
	if oldStrict != newStrict {
		cache.SetSignatureBypassStrictMode(newStrict)
	}
}

func configuredSignatureBypassStrict(cfg *config.Config) bool {
	if cfg != nil && cfg.AntigravitySignatureBypassStrict != nil {
		return *cfg.AntigravitySignatureBypassStrict
	}
	return false
}
