package api

import (
	"context"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/errormessages"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/policy"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type serverOptionConfig struct {
	extraMiddleware       []gin.HandlerFunc
	engineConfigurator    func(*gin.Engine)
	routerConfigurator    func(*gin.Engine, *handlers.BaseAPIHandler, *config.Config)
	requestLoggerFactory  func(*config.Config, string) logging.RequestLogger
	localPassword         string
	keepAliveEnabled      bool
	keepAliveTimeout      time.Duration
	keepAliveOnTimeout    func()
	postAuthHook          auth.PostAuthHook
	postAuthPersistHook   auth.PostAuthHook
	pluginHost            *pluginhost.Host
	configReloadHook      func(context.Context, *config.Config)
	exampleAPIKeySafeMode bool

	// policyService enforces per-API-key limits (RPM, budget, model access)
	// after authentication. nil when the PGSTORE_DSN backend is inactive so
	// the policy middleware is a fast pass-through.
	policyService policy.PolicyService
	// pgStores holds the optional PostgreSQL-backed stores used by the
	// management routes for API Keys, Usage Stats, and Model Catalog. nil
	// when the PG backend is inactive.
	pgStores *PgStoreHandles
}

// PgStoreHandles bundles the optional PG-backed stores and adapters that the
// management handlers and middleware consume. All fields may be nil when the
// PGSTORE_DSN backend is inactive; callers should nil-check before use.
type PgStoreHandles struct {
	APIKeys        *store.APIKeyStore
	Usage          *store.UsageStore
	Models         *store.ModelsStore
	Users          *store.UserStore
	PGSync         *registry.PGSync
	Policy         policy.PolicyService
	ErrorMessages  errormessages.Store
	PricingSources store.PricingSourceStore
	// PricingSourcesDir is the local directory backing uploaded pricing
	// catalog files for source_type=file pricing sources. Empty when PG
	// is not configured.
	PricingSourcesDir string
	// ManagementTokens is the PG-backed store for management API tokens
	// that gate access to the /v0/management REST surface (per-token policy
	// + audit log). nil when PG is not configured — the /api-tokens routes
	// return 503 in that case.
	ManagementTokens *store.ManagementTokenStore
	// UpstreamProviders is the PG-backed store for the upstream_providers
	// table (the normalized source of truth for both API-key providers and
	// OAuth/file-backed auths). nil when PG is not configured — the
	// /upstream-providers routes return 503 in that case.
	UpstreamProviders store.UpstreamProviderStore
	// SyncLog is the PG-backed store for the upstream_sync_log table, which
	// records every upstream OAuth/auth token refresh outcome (success +
	// failure) for the Analysis → Upstream Providers page. nil when PG is not
	// configured — the /upstream-sync-log routes return 503 in that case.
	SyncLog *store.SyncLogStore
	// ModelGroups is the PG-backed store for reusable Model Group templates
	// (allowed-models grant lists + per-model upstream routing). nil when PG
	// is not configured — the /model-groups routes return 503 in that case.
	ModelGroups *store.ModelGroupStore
	// ModelHealth is the PG-backed store for model health-check snapshots +
	// history + operator settings (Analysis → Model Health page + the public
	// /v0/model-health/uptime endpoint). nil when PG is not configured — the
	// /model-health routes return 503 and the public uptime endpoint degrades
	// to an empty response.
	ModelHealth *store.ModelHealthStore
	// Alerts is the PG-backed store for the alerts feed + settings (Analysis →
	// Alerts). nil when PG is not configured — the /alerts routes return 503
	// and the detection sweep is a no-op.
	Alerts *store.AlertStore
	// Backup is the PG-backed store that powers the /export and /import routes
	// (dump/restore of the PG tables). nil when PG is not configured — those
	// routes return 503.
	Backup *store.PostgresStore
}

// ServerOption customises HTTP server construction.
type ServerOption func(*serverOptionConfig)

func defaultRequestLoggerFactory(cfg *config.Config, configPath string) logging.RequestLogger {
	configDir := filepath.Dir(configPath)
	logsDir := logging.ResolveLogDirectory(cfg)
	logger := logging.NewFileRequestLogger(cfg.RequestLog, logsDir, configDir, cfg.ErrorLogsMaxFiles)
	logger.SetHomeEnabled(cfg != nil && cfg.Home.Enabled)
	return logger
}

func effectiveSDKConfig(cfg *config.Config) *config.SDKConfig {
	if cfg == nil {
		return nil
	}
	sdkCfg := cfg.SDKConfig
	sdkCfg.CodexOptimizeMultiAgentV2 = cfg.Codex.OptimizeMultiAgentV2
	if cfg.CommercialMode {
		sdkCfg.RequestLog = false
	}
	return &sdkCfg
}

// WithMiddleware appends additional Gin middleware during server construction.
func WithMiddleware(mw ...gin.HandlerFunc) ServerOption {
	return func(cfg *serverOptionConfig) {
		cfg.extraMiddleware = append(cfg.extraMiddleware, mw...)
	}
}

// WithEngineConfigurator allows callers to mutate the Gin engine prior to middleware setup.
func WithEngineConfigurator(fn func(*gin.Engine)) ServerOption {
	return func(cfg *serverOptionConfig) {
		cfg.engineConfigurator = fn
	}
}

// WithRouterConfigurator appends a callback after default routes are registered.
func WithRouterConfigurator(fn func(*gin.Engine, *handlers.BaseAPIHandler, *config.Config)) ServerOption {
	return func(cfg *serverOptionConfig) {
		cfg.routerConfigurator = fn
	}
}

// WithLocalManagementPassword stores a runtime-only management password accepted for localhost requests.
func WithLocalManagementPassword(password string) ServerOption {
	return func(cfg *serverOptionConfig) {
		cfg.localPassword = password
	}
}

// WithKeepAliveEndpoint enables a keep-alive endpoint with the provided timeout and callback.
func WithKeepAliveEndpoint(timeout time.Duration, onTimeout func()) ServerOption {
	return func(cfg *serverOptionConfig) {
		if timeout <= 0 || onTimeout == nil {
			return
		}
		cfg.keepAliveEnabled = true
		cfg.keepAliveTimeout = timeout
		cfg.keepAliveOnTimeout = onTimeout
	}
}

// WithRequestLoggerFactory customises request logger creation.
func WithRequestLoggerFactory(factory func(*config.Config, string) logging.RequestLogger) ServerOption {
	return func(cfg *serverOptionConfig) {
		cfg.requestLoggerFactory = factory
	}
}

// WithPostAuthHook registers a hook to be called after auth record creation.
func WithPostAuthHook(hook auth.PostAuthHook) ServerOption {
	return func(cfg *serverOptionConfig) {
		cfg.postAuthHook = hook
	}
}

// WithPostAuthPersistHook registers a hook to be called after auth persistence.
func WithPostAuthPersistHook(hook auth.PostAuthHook) ServerOption {
	return func(cfg *serverOptionConfig) {
		cfg.postAuthPersistHook = hook
	}
}

// WithPluginHost registers dynamic plugin HTTP adapters with the server.
func WithPluginHost(host *pluginhost.Host) ServerOption {
	return func(cfg *serverOptionConfig) {
		cfg.pluginHost = host
	}
}

// WithConfigReloadHook registers a callback used after management saves config changes.
func WithConfigReloadHook(hook func(context.Context, *config.Config)) ServerOption {
	return func(cfg *serverOptionConfig) {
		cfg.configReloadHook = hook
	}
}

// WithExampleAPIKeySafeMode blocks proxy API endpoints while template API keys remain configured.
func WithExampleAPIKeySafeMode() ServerOption {
	return func(cfg *serverOptionConfig) {
		cfg.exampleAPIKeySafeMode = true
	}
}

// WithPolicyService enables per-API-key policy enforcement (RPM, hourly
// rate, model access, budget caps) and exposes the PG-backed stores to the
// management API handlers. Pass nil handles to disable; the option is
// idempotent and safe to call multiple times.
func WithPolicyService(svc policy.PolicyService, handles *PgStoreHandles) ServerOption {
	return func(cfg *serverOptionConfig) {
		cfg.policyService = svc
		cfg.pgStores = handles
	}
}
