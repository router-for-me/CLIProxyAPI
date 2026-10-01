// Package api provides the HTTP API server implementation for the CLI Proxy API.
// It includes the main server struct, routing setup, middleware for CORS and authentication,
// and integration with various AI API handlers (OpenAI, Claude, Gemini).
// The server supports hot-reloading of clients and configuration.
package api

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	managementHandlers "github.com/router-for-me/CLIProxyAPI/v7/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/api/middleware"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/autorouter/jevclient"
	codexlive "github.com/router-for-me/CLIProxyAPI/v7/internal/client/codex/live"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/managementasset"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/policy"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/http2"
	"gopkg.in/yaml.v3"
)

// Server represents the main API server.
// It encapsulates the Gin engine, HTTP server, handlers, and configuration.
type Server struct {
	// engine is the Gin web framework engine instance.
	engine *gin.Engine

	// server is the underlying HTTP server.
	server *http.Server

	// muxBaseListener is the shared TCP listener used to serve both HTTP and Redis protocol traffic.
	muxBaseListener net.Listener

	// muxHTTPListener receives HTTP connections selected by the multiplexer.
	muxHTTPListener *muxListener

	// handlers contains the API handlers for processing requests.
	handlers         *handlers.BaseAPIHandler
	codexLiveHandler *codexlive.Handler

	// cfg holds the current server configuration.
	cfg *config.Config

	// oldConfigYaml stores a YAML snapshot of the previous configuration for change detection.
	// This prevents issues when the config object is modified in place by Management API.
	oldConfigYaml []byte

	// accessManager handles request authentication providers.
	accessManager *sdkaccess.Manager

	// requestLogger is the request logger instance for dynamic configuration updates.
	requestLogger logging.RequestLogger
	loggerToggle  func(bool)

	// configFilePath is the absolute path to the YAML config file for persistence.
	configFilePath string

	// currentPath is the absolute path to the current working directory.
	currentPath string

	// wsRoutes tracks registered websocket upgrade paths.
	wsRouteMu     sync.Mutex
	wsRoutes      map[string]struct{}
	wsAuthChanged func(bool, bool)
	wsAuthEnabled atomic.Bool

	// management handler
	mgmt *managementHandlers.Handler

	// pluginHost owns dynamic plugin Management API route dispatch.
	pluginHost *pluginhost.Host

	// managementRoutesRegistered tracks whether the management routes have been attached to the engine.
	managementRoutesRegistered atomic.Bool
	// managementRoutesEnabled controls whether management endpoints serve real handlers.
	managementRoutesEnabled atomic.Bool

	// envManagementSecret indicates whether MANAGEMENT_PASSWORD is configured.
	envManagementSecret bool

	localPassword string

	keepAliveEnabled   bool
	keepAliveTimeout   time.Duration
	keepAliveOnTimeout func()
	keepAliveHeartbeat chan struct{}
	keepAliveStop      chan struct{}

	exampleAPIKeySafeModeEnabled bool
	exampleAPIKeySafeModeActive  atomic.Bool

	// policyService, when non-nil, enables per-API-key policy enforcement.
	policyService policy.PolicyService
	// pgStores, when non-nil, exposes the PG-backed stores to the management
	// API handlers so /api-keys-pg, /usage-stats, and /models-catalog work.
	pgStores *PgStoreHandles

	// autoDisableSweeper is the background auto-re-enable sweeper for
	// auto-disabled upstream-provider API-key entries whose cooldown elapsed.
	// Constructed and started at boot (server.go) alongside the auto-disable
	// sink; its ticks are a no-op when PG is not configured. Stopped during
	// shutdown (Stop) so the goroutine cannot outlive the server.
	autoDisableSweeper *managementHandlers.AutoDisableSweeper
}

// newGinEngine returns a Gin engine configured to route on the raw
// (percent-encoded) request path. Go's net/http decodes %2F into URL.Path, so
// without this a path parameter can never carry a value containing "/" (for
// example a model id like "openai/gpt-4o"): the encoded value is split across
// path segments and the :id route no longer matches. Routing on RawPath keeps
// the encoded segment intact, and the default UnescapePathValues=true makes
// c.Param("id") return the original decoded value.
func newGinEngine() *gin.Engine {
	engine := gin.New()
	engine.UseRawPath = true
	return engine
}

// NewServer creates and initializes a new API server instance.
// It sets up the Gin engine, middleware, routes, and handlers.
//
// Parameters:
//   - cfg: The server configuration
//   - authManager: core runtime auth manager
//   - accessManager: request authentication manager
//
// Returns:
//   - *Server: A new server instance
func NewServer(cfg *config.Config, authManager *auth.Manager, accessManager *sdkaccess.Manager, configFilePath string, opts ...ServerOption) *Server {
	optionState := &serverOptionConfig{
		requestLoggerFactory: defaultRequestLoggerFactory,
	}
	for i := range opts {
		opts[i](optionState)
	}
	// Set gin mode
	if !cfg.Debug {
		gin.SetMode(gin.ReleaseMode)
	}

	// Create gin engine
	engine := newGinEngine()
	if optionState.engineConfigurator != nil {
		optionState.engineConfigurator(engine)
	}

	// Add middleware
	engine.Use(logging.GinLogrusLogger())
	engine.Use(logging.GinLogrusRecovery())
	engine.Use(logging.CPATraceIDMiddleware())
	for _, mw := range optionState.extraMiddleware {
		engine.Use(mw)
	}

	// Add request logging middleware (positioned after recovery, before auth)
	// Resolve logs directory relative to the configuration file directory.
	var requestLogger logging.RequestLogger
	var toggle func(bool)
	if !cfg.CommercialMode {
		if optionState.requestLoggerFactory != nil {
			requestLogger = optionState.requestLoggerFactory(cfg, configFilePath)
		}
		if requestLogger != nil {
			engine.Use(middleware.RequestLoggingMiddleware(requestLogger))
			if setter, ok := requestLogger.(interface{ SetEnabled(bool) }); ok {
				toggle = setter.SetEnabled
			}
		}
	}

	engine.Use(corsMiddleware())
	wd, err := os.Getwd()
	if err != nil {
		wd = configFilePath
	}

	envAdminPassword, envAdminPasswordSet := os.LookupEnv("MANAGEMENT_PASSWORD")
	envAdminPassword = strings.TrimSpace(envAdminPassword)
	envManagementSecret := envAdminPasswordSet && envAdminPassword != ""

	// Create server instance
	s := &Server{
		engine:              engine,
		handlers:            handlers.NewBaseAPIHandlers(effectiveSDKConfig(cfg), authManager),
		cfg:                 cfg,
		accessManager:       accessManager,
		requestLogger:       requestLogger,
		loggerToggle:        toggle,
		configFilePath:      configFilePath,
		currentPath:         wd,
		envManagementSecret: envManagementSecret,
		wsRoutes:            make(map[string]struct{}),
		pluginHost:          optionState.pluginHost,

		policyService: optionState.policyService,
		pgStores:      optionState.pgStores,

		exampleAPIKeySafeModeEnabled: optionState.exampleAPIKeySafeMode,
	}
	s.wsAuthEnabled.Store(cfg.WebsocketAuth)
	s.exampleAPIKeySafeModeActive.Store(s.exampleAPIKeySafeModeRequired(cfg))
	s.handlers.SetPluginHost(optionState.pluginHost)
	if optionState.pluginHost != nil {
		optionState.pluginHost.SetModelExecutor(s.handlers)
		optionState.pluginHost.SetAuthManager(authManager)
	}
	// Save initial YAML snapshot
	s.oldConfigYaml, _ = yaml.Marshal(cfg)
	s.applyAccessConfig(nil, cfg)
	if authManager != nil {
		authManager.SetRetryConfig(cfg.RequestRetry, time.Duration(cfg.MaxRetryInterval)*time.Second, cfg.MaxRetryCredentials)
	}
	managementasset.SetCurrentConfig(cfg)
	auth.SetQuotaCooldownDisabled(cfg.DisableCooling)
	auth.SetTransientErrorCooldownSeconds(cfg.TransientErrorCooldownSeconds)
	auth.SetCooldownWaitConfig(cfg.Routing.CooldownWait.MaxWaitMS, cfg.Routing.CooldownWait.MaxAttempts, cfg.Routing.CooldownWait.Reclassify403)
	applySignatureCacheConfig(nil, cfg)
	// Initialize management handler
	s.mgmt = managementHandlers.NewHandler(cfg, configFilePath, authManager)
	s.mgmt.SetPluginHost(optionState.pluginHost)
	s.mgmt.SetConfigReloadHook(optionState.configReloadHook)
	if optionState.reloadCoordinator != nil {
		s.mgmt.SetReloadCoordinator(optionState.reloadCoordinator)
	}
	if optionState.localPassword != "" {
		s.mgmt.SetLocalPassword(optionState.localPassword)
	}
	logDir := logging.ResolveLogDirectory(cfg)
	s.mgmt.SetLogDirectory(logDir)
	if optionState.postAuthHook != nil {
		s.mgmt.SetPostAuthHook(optionState.postAuthHook)
	}
	if optionState.postAuthPersistHook != nil {
		s.mgmt.SetPostAuthPersistHook(optionState.postAuthPersistHook)
	}
	s.localPassword = optionState.localPassword

	// Wire the optional PG-backed stores and policy service into the
	// management handler so the /api-keys-pg, /usage-stats, and
	// /models-catalog routes resolve correctly. All parameters may be nil
	// when the PG backend is inactive.
	if handles := optionState.pgStores; handles != nil {
		s.mgmt.SetPostgresStores(handles.APIKeys, handles.Usage, handles.Models, handles.PGSync, handles.Policy)
		s.mgmt.SetPGControl(handles.PG)
		s.mgmt.SetErrorMessagesStore(handles.ErrorMessages)
		s.mgmt.SetPricingSourcesStore(handles.PricingSources, handles.PricingSourcesDir)
		s.mgmt.SetUserStore(handles.Users)
		s.mgmt.SetManagementTokenStore(handles.ManagementTokens)
		s.mgmt.SetUpstreamProvidersStore(handles.UpstreamProviders)
		s.mgmt.SetProxyPoolStore(handles.ProxyPools)
		s.mgmt.SetModelGroupStore(handles.ModelGroups)
		s.mgmt.SetAutoRouterStore(handles.AutoRouters)
		s.mgmt.SetAutoRouterProfileStore(handles.AutoRouterProfiles)
		// Wire the Manage-LiteLLM stores (dedicated litellm_* tables). No-op
		// when PG is not configured — the /litellm routes return 503.
		s.mgmt.SetLiteLLMStores(handles.LiteLLMUsers, handles.LiteLLMKeys)
		// Wire the Manage-LiteLLM external-sync settings store and start the
		// background auto-sync sweep. The sweep is nil-safe and skips syncs when
		// disabled/unconfigured; started here (not in the setter) so it only
		// runs after the auth manager is attached.
		s.mgmt.SetLiteLLMSyncStore(handles.LiteLLMSync)
		s.mgmt.StartLiteLLMSyncSweep()
		s.mgmt.SetLiteLLMOnTheFlyStore(handles.LiteLLMOnTheFly)
		// Re-register every enabled Auto Router's model id in the in-memory
		// registry + models catalog so auto routers survive a restart (the
		// registry is rebuilt at boot and only sessions recreate it otherwise).
		s.mgmt.RehydrateAutoRouters(context.Background())
		s.mgmt.SetSyncLogStore(handles.SyncLog)
		// Wire the PG-backed model health store (Analysis → Model Health page
		// + the public /v0/model-health/uptime endpoint). The retention sweep
		// for the history table is launched inside the setter; the probe sweep
		// is started separately below so it only runs after the auth manager is
		// attached (it needs live auths to probe against). nil-safe.
		s.mgmt.SetModelHealthStore(handles.ModelHealth)
		s.mgmt.StartModelHealthSweep()
		// Wire the PG-backed alerts feed + settings (Analysis → Alerts). The
		// detection sweep is started here so it only runs after the auth manager
		// is attached (the provider-cooldown detector polls live cooldowns).
		s.mgmt.SetAlertsStore(handles.Alerts)
		// Wire the PG-backed Jev AI classifier settings (master toggle +
		// sealed API key + pinned model). The gate itself is attached
		// separately below, since it needs a client built from the stored key.
		s.mgmt.SetJevStore(handles.Jev)
		s.mgmt.SetUsageFlusher(handles.Flusher)
		s.mgmt.StartAlertSweep()
		// Wire the backup store that powers the /export and /import routes
		// (dump/restore of the PG tables). No-op when PG is not configured.
		s.mgmt.SetBackupStore(handles.Backup)
		// Attach the refresh-outcome sink so every OAuth/auth token refresh is
		// persisted to upstream_sync_log. No-op when PG is not configured
		// (SyncLogSink returns nil, SetRefreshSink detaches). Re-attached on
		// config reload via server_reload.go (SetAuthManager is re-called).
		if s.handlers.AuthManager != nil {
			s.handlers.AuthManager.SetRefreshSink(s.mgmt.SyncLogSink())
		}
		// Attach the auto-disable sink so the conductor's classified upstream
		// error-code matches persist auto_disabled=true + re-render. No-op when
		// PG is not configured (AutoDisableSink returns nil, SetAutoDisableSink
		// detaches). Re-attached on config reload via server_reload.go.
		if s.handlers.AuthManager != nil {
			s.handlers.AuthManager.SetAutoDisableSink(s.mgmt.AutoDisableSink())
		}
		// Start the auto-re-enable sweeper: it periodically clears the runtime
		// auto-disable flags on entries whose provider cooldown elapsed so they
		// drop back into routing. Constructed here (like AutoDisableSink) so it
		// tracks the same store; its ticks are a harmless no-op when PG is
		// absent. It is stopped on server shutdown (see Stop).
		s.autoDisableSweeper = s.mgmt.ReenableSweeper()
		s.autoDisableSweeper.Start()
		// Surface persisted official_provider values in auth-selection errors.
		s.handlers.SetModelsCatalogStore(store.NewModelsCatalogResolver(handles.Models))
		// Wire the per-model-id global routing override (pinned providers +
		// strategy + priorities) from the persisted model catalog so requests
		// route by Global Model the same way a Models Group route does. Shares
		// the same ModelsStore-backed resolver instance; nil when PG is off.
		s.handlers.SetGlobalModelRouter(store.NewModelsCatalogResolver(handles.Models))
		// Wire the Auto Router resolver so requests whose model id matches an
		// Auto Router are scored and forwarded to a tier-appropriate upstream
		// model. Non-nil only when PG is configured and routers exist.
		s.handlers.SetAutoRouterResolver(store.NewAutoRoutersResolver(handles.AutoRouters, handles.AutoRouterProfiles))
		// Wire the global Jev AI classifier switches so the hybrid gate can
		// run. The provider only reports the toggle + whether a key is set; the
		// gate itself is attached below once the key is unsealed, which keeps
		// the plaintext credential confined to this wiring step.
		s.handlers.SetJevSettingsProvider(handles.Jev)
		if key, errKey := jevAPIKey(handles.Jev); errKey != nil {
			log.WithError(errKey).Warn("api: Jev classifier disabled; could not unseal the API key")
			handlers.SetJevGate(nil)
		} else {
			// The client is always built, even with no key, so that saving one
			// from the dashboard can be adopted live (SetJevConfigRotator below).
			// The gate still short-circuits before any call while the provider
			// reports apiKeySet=false.
			client := jevclient.New(jevBaseURL(handles.Jev), key, nil)
			handlers.SetJevGate(client)
			s.mgmt.SetJevConfigRotator(client)
		}
		// Surface official_provider on Usage Stats / Errors rows so the
		// dashboard can show "Provider Official" (e.g. "anthropic") instead of
		// the raw internal provider key (e.g. "claude").
		s.mgmt.SetModelsCatalogResolver(store.NewModelsCatalogResolver(handles.Models))
	}
	// Wire the full-backup-to-S3 subsystem (runner + interval + retention)
	// into the management handler so the /v0/management/backup routes can
	// list, create, and restore snapshots. The runner also satisfies the
	// restore-from-S3 interface via RestoreFromS3. runner is nil when
	// BACKUP_S3_ENDPOINT is not set — the routes return 503. This wiring is
	// independent of the PG backend.
	s.mgmt.SetBackupS3(optionState.backupSubsystem, optionState.backupSubsystem, optionState.backupInterval, optionState.backupRetention)
	// Wire the in-memory events ring recorder so the /v0/management/events
	// and /events/stats endpoints can serve the live buffer. The same ring
	// is installed as events.SetGlobal in cmd/server so emission sites
	// (round-2 Task 12 — router decisions, breaker trips, cooldown waits)
	// and the management handlers share a single buffer. nil disables both
	// endpoints (gate flips off; routes return 503).
	s.mgmt.SetEventsRing(optionState.eventsRing)
	// Wire the /v1/models invoker so the management endpoint
	// POST /v0/management/models-catalog/sync-from-v1 can probe the live
	// caller-facing model list in-process (no network loopback). The
	// handler wraps the gin engine: when invoked it rewrites the request
	// URL to /v1/models so the full AuthMiddleware + PolicyMiddleware +
	// unifiedModelsHandler pipeline runs exactly as a real caller would
	// experience. The wrapper is a no-op when the engine is nil.
	s.mgmt.SetV1ModelsHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.engine == nil {
			http.Error(w, "server engine not ready", http.StatusServiceUnavailable)
			return
		}
		// http.Handler may receive a Request with /v1/models already set; if
		// not (caller passes a generic path), normalize so engine dispatch
		// finds the route. The original method and headers are preserved.
		if r.URL == nil {
			r.URL = &url.URL{Path: "/v1/models"}
		} else if r.URL.Path == "" || r.URL.Path == "/" {
			r.URL.Path = "/v1/models"
			r.URL.RawPath = ""
		}
		s.engine.ServeHTTP(w, r)
	}))
	s.mountDashboardRoutes()

	// Home heartbeat gate: when home is enabled, block all endpoints with 503 until the
	// subscribe-config heartbeat connection is healthy.
	engine.Use(s.homeHeartbeatMiddleware())
	engine.Use(s.exampleAPIKeySafeModeMiddleware())

	// Setup routes
	s.setupRoutes()

	// Apply additional router configurators from options
	if optionState.routerConfigurator != nil {
		optionState.routerConfigurator(engine, s.handlers, cfg)
	}

	// Register management routes when configuration or environment secrets are available,
	// or when a local management password is provided (e.g. TUI mode).
	hasManagementSecret := cfg.RemoteManagement.SecretKey != "" || envManagementSecret || s.localPassword != ""
	s.managementRoutesEnabled.Store(hasManagementSecret)
	redisqueue.SetEnabled(hasManagementSecret || (cfg != nil && cfg.Home.Enabled))
	if hasManagementSecret {
		s.registerManagementRoutes()
	}
	s.refreshPluginManagementRoutes()
	engine.NoRoute(s.pluginManagementNoRoute)

	if optionState.keepAliveEnabled {
		s.enableKeepAlive(optionState.keepAliveTimeout, optionState.keepAliveOnTimeout)
	}

	// Create HTTP server
	s.server = &http.Server{
		Addr:    fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Handler: engine,
	}

	return s
}

// Start begins listening for and serving HTTP or HTTPS requests.
// It's a blocking call and will only return on an unrecoverable error.
//
// Returns:
//   - error: An error if the server fails to start
func (s *Server) Start() error {
	if s == nil || s.server == nil {
		return fmt.Errorf("failed to start HTTP server: server not initialized")
	}

	addr := s.server.Addr
	listener, errListen := net.Listen("tcp", addr)
	if errListen != nil {
		return fmt.Errorf("failed to start HTTP server: %v", errListen)
	}

	useTLS := s.cfg != nil && s.cfg.TLS.Enable
	if useTLS {
		certPath := strings.TrimSpace(s.cfg.TLS.Cert)
		keyPath := strings.TrimSpace(s.cfg.TLS.Key)
		if certPath == "" || keyPath == "" {
			if errClose := listener.Close(); errClose != nil {
				log.Errorf("failed to close listener after TLS validation failure: %v", errClose)
			}
			return fmt.Errorf("failed to start HTTPS server: tls.cert or tls.key is empty")
		}
		certPair, errLoad := tls.LoadX509KeyPair(certPath, keyPath)
		if errLoad != nil {
			if errClose := listener.Close(); errClose != nil {
				log.Errorf("failed to close listener after TLS key pair load failure: %v", errClose)
			}
			return fmt.Errorf("failed to start HTTPS server: %v", errLoad)
		}

		tlsConfig := &tls.Config{
			Certificates: []tls.Certificate{certPair},
			NextProtos:   []string{"h2", "http/1.1"},
		}
		s.server.TLSConfig = tlsConfig
		if errHTTP2 := http2.ConfigureServer(s.server, &http2.Server{}); errHTTP2 != nil {
			log.Warnf("failed to configure HTTP/2: %v", errHTTP2)
		}
		listener = tls.NewListener(listener, tlsConfig)
		log.Debugf("Starting API server on %s with TLS", addr)
	} else {
		log.Debugf("Starting API server on %s", addr)
	}

	httpListener := newMuxListener(listener.Addr(), 1024)
	s.muxBaseListener = listener
	s.muxHTTPListener = httpListener

	httpErrCh := make(chan error, 1)
	acceptErrCh := make(chan error, 1)

	go func() {
		httpErrCh <- s.server.Serve(httpListener)
	}()
	go func() {
		acceptErrCh <- s.acceptMuxConnections(listener, httpListener)
	}()

	select {
	case errServe := <-httpErrCh:
		if s.muxBaseListener != nil {
			if errClose := s.muxBaseListener.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
				log.Debugf("failed to close shared listener after HTTP serve exit: %v", errClose)
			}
		}
		if s.muxHTTPListener != nil {
			_ = s.muxHTTPListener.Close()
		}
		errAccept := <-acceptErrCh
		errServe = normalizeHTTPServeError(errServe)
		errAccept = normalizeListenerError(errAccept)
		if errServe != nil {
			return fmt.Errorf("failed to start HTTP server: %v", errServe)
		}
		if errAccept != nil {
			return fmt.Errorf("failed to start HTTP server: %v", errAccept)
		}
		return nil
	case errAccept := <-acceptErrCh:
		if s.muxHTTPListener != nil {
			_ = s.muxHTTPListener.Close()
		}
		if s.muxBaseListener != nil {
			if errClose := s.muxBaseListener.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
				log.Debugf("failed to close shared listener after accept loop exit: %v", errClose)
			}
		}
		errServe := <-httpErrCh
		errServe = normalizeHTTPServeError(errServe)
		errAccept = normalizeListenerError(errAccept)
		if errAccept != nil {
			return fmt.Errorf("failed to start HTTP server: %v", errAccept)
		}
		if errServe != nil {
			return fmt.Errorf("failed to start HTTP server: %v", errServe)
		}
		return nil
	}
}

// Stop gracefully shuts down the API server without interrupting any
// active connections.
//
// Parameters:
//   - ctx: The context for graceful shutdown
//
// Returns:
//   - error: An error if the server fails to stop
func (s *Server) Stop(ctx context.Context) error {
	log.Debug("Stopping API server...")

	// Stop the auto-re-enable sweeper first so an in-flight re-enable sweep +
	// re-render cannot race the config/clients teardown below. Idempotent and
	// safe when the sweeper was never started (no PG).
	if s.autoDisableSweeper != nil {
		s.autoDisableSweeper.Stop()
	}

	if s.keepAliveEnabled {
		select {
		case s.keepAliveStop <- struct{}{}:
		default:
		}
	}

	if s.muxHTTPListener != nil {
		_ = s.muxHTTPListener.Close()
	}
	if s.muxBaseListener != nil {
		if errClose := s.muxBaseListener.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
			log.Debugf("failed to close shared listener: %v", errClose)
		}
	}

	// Shutdown the HTTP server.
	errShutdown := s.server.Shutdown(ctx)
	if s.codexLiveHandler != nil {
		s.codexLiveHandler.Close()
	}
	if errShutdown != nil {
		return fmt.Errorf("failed to shutdown HTTP server: %v", errShutdown)
	}

	log.Debug("API server stopped")
	return nil
}

// jevAPIKey unseals the classifier API key from the Jev settings store. It
// returns "" when no key is configured; a nil store (PG not configured) is not
// an error. The plaintext key is used only to build the classifier client and
// is never logged.
func jevAPIKey(jevStore *store.JevStore) (string, error) {
	if jevStore == nil {
		return "", nil
	}
	return jevStore.APIKey(context.Background())
}

// jevBaseURL returns the configured classifier API root, or "" to accept the
// client's default. A read failure is not fatal: the feature is unusable
// without a key anyway, and the stored value can be re-applied from the
// dashboard, so this warns and falls back rather than disabling the gate.
func jevBaseURL(jevStore *store.JevStore) string {
	if jevStore == nil {
		return ""
	}
	set, errGet := jevStore.Get(context.Background())
	if errGet != nil {
		log.WithError(errGet).Warn("api: could not read the Jev base URL; using the default endpoint")
		return ""
	}
	return set.BaseURL
}
