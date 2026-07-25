// Package api provides the HTTP API server implementation for the CLI Proxy API.
// It includes the main server struct, routing setup, middleware for CORS and authentication,
// and integration with various AI API handlers (OpenAI, Claude, Gemini).
// The server supports hot-reloading of clients and configuration.
package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/access"
	managementHandlers "github.com/router-for-me/CLIProxyAPI/v7/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/api/middleware"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	claudemodels "github.com/router-for-me/CLIProxyAPI/v7/internal/client/claude/models"
	codexlive "github.com/router-for-me/CLIProxyAPI/v7/internal/client/codex/live"
	codexmodels "github.com/router-for-me/CLIProxyAPI/v7/internal/client/codex/models"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/managementasset"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/safemode"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers/gemini"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers/openai"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/http2"
	"gopkg.in/yaml.v3"
)

const oauthCallbackSuccessHTML = `<html><head><meta charset="utf-8"><title>Authentication successful</title><script>setTimeout(function(){window.close();},5000);</script></head><body><h1>Authentication successful!</h1><p>You can close this window.</p><p>This window will close automatically in 5 seconds.</p></body></html>`

const codexAlphaSearchSourceFormat = "codex-alpha-search"

var corsExposedResponseHeaders = []string{
	logging.CPATraceIDHeader,
	"X-CPA-VERSION",
	"X-CPA-COMMIT",
	"X-CPA-BUILD-DATE",
	"X-CPA-SUPPORT-PLUGIN",
	"X-CPA-HOME-VERSION",
	"X-CPA-HOME-BUILD-DATE",
	"X-SERVER-VERSION",
	"X-SERVER-BUILD-DATE",
}

var corsExposedResponseHeadersJoined = strings.Join(corsExposedResponseHeaders, ", ")

const (
	exampleAPIKeyManagementPath = "/management.html"
	exampleAPIKeyManagementURL  = "/management.html?safe-mode=configure"
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
	// managementAssetSyncing prevents duplicate background sync goroutines for a missing panel asset.
	managementAssetSyncing atomic.Bool

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
	engine := gin.New()
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
	applySignatureCacheConfig(nil, cfg)
	// Initialize management handler
	s.mgmt = managementHandlers.NewHandler(cfg, configFilePath, authManager)
	s.mgmt.SetPluginHost(optionState.pluginHost)
	s.mgmt.SetConfigReloadHook(optionState.configReloadHook)
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

func (s *Server) homeHeartbeatMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s == nil || s.cfg == nil || !s.cfg.Home.Enabled {
			c.Next()
			return
		}
		if c != nil && c.Request != nil {
			path := c.Request.URL.Path
			if strings.HasPrefix(path, "/v0/management/") || path == "/v0/management" || strings.HasPrefix(path, "/v0/resource/plugins/") || path == "/management.html" {
				c.Next()
				return
			}
		}
		client := home.Current()
		if client == nil || !client.HeartbeatOK() {
			c.AbortWithStatus(http.StatusServiceUnavailable)
			return
		}
		c.Next()
	}
}

func (s *Server) exampleAPIKeySafeModeRequired(cfg *config.Config) bool {
	return s != nil && s.exampleAPIKeySafeModeEnabled && cfg != nil && safemode.HasExampleAPIKeys(cfg.APIKeys)
}

func (s *Server) exampleAPIKeySafeModeMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s == nil || !s.exampleAPIKeySafeModeActive.Load() || c == nil || c.Request == nil || c.Request.URL == nil {
			c.Next()
			return
		}

		path := c.Request.URL.Path
		if path == exampleAPIKeyManagementPath && c.Query("safe-mode") == "configure" {
			c.Next()
			return
		}
		if (path == "/" || path == exampleAPIKeyManagementPath) && (c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead) {
			s.serveExampleAPIKeyWarningPage(c)
			return
		}
		if !isExampleAPIKeySafeModeProxyPath(path) {
			c.Next()
			return
		}

		c.Header("X-CPA-SAFE-MODE", "example-api-key")
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error":   "unsafe_example_api_key",
			"message": "Proxy API endpoints are disabled because api-keys contains template values. Open /management.html?safe-mode=configure, update api-keys in Management, then retry.",
		})
	}
}

func (s *Server) serveExampleAPIKeyWarningPage(c *gin.Context) {
	cfg := s.cfg
	var keys []string
	if cfg != nil {
		keys = safemode.ExampleAPIKeys(cfg.APIKeys)
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	if c.Request.Method == http.MethodHead {
		c.Status(http.StatusOK)
		c.Abort()
		return
	}
	c.String(http.StatusOK, safemode.ExampleAPIKeyWarningPageHTML(keys, exampleAPIKeyManagementURL))
	c.Abort()
}

func isExampleAPIKeySafeModeProxyPath(path string) bool {
	switch {
	case path == "/v1" || strings.HasPrefix(path, "/v1/"):
		return true
	case path == "/v1beta" || strings.HasPrefix(path, "/v1beta/"):
		return true
	case path == "/openai/v1" || strings.HasPrefix(path, "/openai/v1/"):
		return true
	case path == "/backend-api/codex" || strings.HasPrefix(path, "/backend-api/codex/"):
		return true
	default:
		return false
	}
}

// setupRoutes configures the API routes for the server.
// It defines the endpoints and associates them with their respective handlers.
func (s *Server) setupRoutes() {
	healthzHandler := func(c *gin.Context) {
		if c.Request.Method == http.MethodHead {
			c.Status(http.StatusOK)
			return
		}

		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
	s.engine.GET("/healthz", healthzHandler)
	s.engine.HEAD("/healthz", healthzHandler)

	s.engine.GET("/management.html", s.serveManagementControlPanel)
	openaiHandlers := openai.NewOpenAIAPIHandler(s.handlers)
	geminiHandlers := gemini.NewGeminiAPIHandler(s.handlers)
	claudeCodeHandlers := claude.NewClaudeCodeAPIHandler(s.handlers)
	openaiResponsesHandlers := openai.NewOpenAIResponsesAPIHandler(s.handlers)
	s.codexLiveHandler = codexlive.NewHandler(s.handlers.AuthManager, s.cfg)

	// OpenAI compatible API routes
	v1 := s.engine.Group("/v1")
	v1.Use(AuthMiddleware(s.accessManager))
	{
		v1.GET("/models", s.unifiedModelsHandler(openaiHandlers, claudeCodeHandlers))
		v1.POST("/chat/completions", openaiHandlers.ChatCompletions)
		v1.POST("/completions", openaiHandlers.Completions)
		v1.POST("/images/generations", openaiHandlers.ImagesGenerations)
		v1.POST("/images/edits", openaiHandlers.ImagesEdits)
		v1.POST("/videos", openaiHandlers.XAIVideosGenerations)
		v1.POST("/videos/generations", openaiHandlers.XAIVideosGenerations)
		v1.POST("/videos/edits", openaiHandlers.XAIVideosEdits)
		v1.POST("/videos/extensions", openaiHandlers.XAIVideosExtensions)
		v1.GET("/videos/:request_id", openaiHandlers.XAIVideosRetrieve)
		v1.POST("/messages", claudeCodeHandlers.ClaudeMessages)
		v1.POST("/messages/count_tokens", claudeCodeHandlers.ClaudeCountTokens)
		v1.GET("/responses", openaiResponsesHandlers.ResponsesWebsocket)
		v1.POST("/responses", openaiResponsesHandlers.Responses)
		v1.POST("/responses/compact", openaiResponsesHandlers.Compact)
		v1.POST("/alpha/search", s.codexAlphaSearch)
		v1.POST("/live", s.codexLiveHandler.Handle)
		v1.GET("/live/:call_id", s.codexLiveHandler.HandleSideband)
		v1.POST("/realtime/calls", s.codexLiveHandler.Handle)
		v1.GET("/realtime/calls/:call_id", s.codexLiveHandler.HandleSideband)
		v1.GET("/realtime", s.codexLiveHandler.HandleSideband)
	}

	openaiV1 := s.engine.Group("/openai/v1")
	openaiV1.Use(AuthMiddleware(s.accessManager))
	{
		openaiV1.POST("/videos", openaiHandlers.VideosCreate)
		openaiV1.GET("/videos/:video_id/content", openaiHandlers.VideosContent)
		openaiV1.GET("/videos/:video_id", openaiHandlers.VideosRetrieve)
	}

	// Codex CLI direct route aliases (chatgpt_base_url compatible)
	codexDirect := s.engine.Group("/backend-api/codex")
	codexDirect.Use(AuthMiddleware(s.accessManager))
	{
		codexDirect.GET("/responses", openaiResponsesHandlers.ResponsesWebsocket)
		codexDirect.POST("/responses", openaiResponsesHandlers.Responses)
		codexDirect.POST("/responses/compact", openaiResponsesHandlers.Compact)
		codexDirect.POST("/alpha/search", s.codexAlphaSearch)
	}

	// Gemini compatible API routes
	v1beta := s.engine.Group("/v1beta")
	v1beta.Use(AuthMiddleware(s.accessManager))
	{
		v1beta.GET("/models", s.geminiModelsHandler(geminiHandlers))
		v1beta.POST("/interactions", geminiHandlers.Interactions)
		v1beta.POST("/models/*action", geminiHandlers.GeminiHandler)
		v1beta.GET("/models/*action", s.geminiGetHandler(geminiHandlers))
	}

	// Root endpoint
	s.engine.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "CLI Proxy API Server",
			"endpoints": []string{
				"POST /v1/chat/completions",
				"POST /v1/completions",
				"GET /v1/models",
			},
		})
	})

	// OAuth callback endpoints (reuse main server port)
	// These endpoints receive provider redirects and persist
	// the short-lived code/state for the waiting goroutine.
	s.engine.GET("/anthropic/callback", func(c *gin.Context) {
		code := c.Query("code")
		state := c.Query("state")
		errStr := c.Query("error")
		if errStr == "" {
			errStr = c.Query("error_description")
		}
		if state != "" {
			_, _ = managementHandlers.WriteOAuthCallbackFileForPendingSession(s.cfg.AuthDir, "anthropic", state, code, errStr)
		}
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, oauthCallbackSuccessHTML)
	})

	s.engine.GET("/codex/callback", func(c *gin.Context) {
		code := c.Query("code")
		state := c.Query("state")
		errStr := c.Query("error")
		if errStr == "" {
			errStr = c.Query("error_description")
		}
		if state != "" {
			_, _ = managementHandlers.WriteOAuthCallbackFileForPendingSession(s.cfg.AuthDir, "codex", state, code, errStr)
		}
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, oauthCallbackSuccessHTML)
	})

	s.engine.GET("/antigravity/callback", func(c *gin.Context) {
		code := c.Query("code")
		state := c.Query("state")
		errStr := c.Query("error")
		if errStr == "" {
			errStr = c.Query("error_description")
		}
		if state != "" {
			_, _ = managementHandlers.WriteOAuthCallbackFileForPendingSession(s.cfg.AuthDir, "antigravity", state, code, errStr)
		}
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, oauthCallbackSuccessHTML)
	})

	// Management routes are registered lazily by registerManagementRoutes when a secret is configured.
}

func (s *Server) codexAlphaSearchModelRouterHost() handlers.PluginModelRouterHost {
	if s == nil {
		return nil
	}
	if s.pluginHost != nil {
		return s.pluginHost
	}
	if s.handlers != nil && s.handlers.ModelRouterHost != nil {
		return s.handlers.ModelRouterHost
	}
	return nil
}

func (s *Server) codexAlphaSearchSelectionModel(ctx context.Context, c *gin.Context, body []byte, model string) (string, error) {
	host := s.codexAlphaSearchModelRouterHost()
	if host == nil {
		return model, nil
	}

	var headers http.Header
	queryValues := make(map[string][]string)
	requestPath := ""
	if c != nil && c.Request != nil {
		headers = c.Request.Header.Clone()
		if c.Request.URL != nil {
			queryValues = c.Request.URL.Query()
			requestPath = c.Request.URL.Path
		}
	}
	metadata := map[string]any{
		coreexecutor.RequestedModelMetadataKey: model,
	}
	if requestPath != "" {
		metadata[coreexecutor.RequestPathMetadataKey] = requestPath
	}
	resp, handled := host.RouteModel(ctx, pluginapi.ModelRouteRequest{
		SourceFormat:   codexAlphaSearchSourceFormat,
		RequestedModel: model,
		Headers:        headers,
		Query:          queryValues,
		Body:           body,
		Metadata:       metadata,
	})
	if !handled || !resp.Handled {
		return model, nil
	}
	if resp.TargetKind != pluginapi.ModelRouteTargetProvider || !strings.EqualFold(strings.TrimSpace(resp.Target), "codex") {
		return "", fmt.Errorf("unsupported Codex Alpha Search model route target %q (%q)", resp.TargetKind, resp.Target)
	}
	if targetModel := strings.TrimSpace(resp.TargetModel); targetModel != "" {
		return targetModel, nil
	}
	return model, nil
}

func sanitizeCodexAlphaSearchBody(body []byte) []byte {
	var payload map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil || payload == nil {
		return body
	}

	removed := false
	for _, field := range []string{"prompt_cache_key", "prompt_cache_retention"} {
		if _, exists := payload[field]; exists {
			delete(payload, field)
			removed = true
		}
	}
	if !removed {
		return body
	}

	sanitizedBody, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return body
	}
	return sanitizedBody
}

func homeSelectionAttemptContext(ctx context.Context, selection *auth.HomeDispatchSelection) (context.Context, func(), error) {
	if selection == nil {
		return nil, func() {}, errors.New("Home dispatch selection is nil")
	}
	return selection.AttemptContext(ctx)
}

// codexAlphaSearch forwards the standalone search endpoint used by current
// Codex clients. Unlike /responses, this payload is already in Codex search
// format and must not pass through a protocol translator.
func (s *Server) codexAlphaSearch(c *gin.Context) {
	if s == nil || s.handlers == nil || s.handlers.AuthManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Codex auth manager unavailable"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 16<<20))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to read search request"})
		return
	}

	var routing struct {
		ID    string `json:"id"`
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &routing)
	upstreamRequestBody := sanitizeCodexAlphaSearchBody(body)

	selectionHeaders := c.Request.Header.Clone()
	if sessionID := strings.TrimSpace(routing.ID); sessionID != "" {
		selectionHeaders.Set("X-Session-ID", sessionID)
	}
	ctx := context.WithValue(c.Request.Context(), "gin", c)
	selectionModel, errRoute := s.codexAlphaSearchSelectionModel(ctx, c, body, strings.TrimSpace(routing.Model))
	if errRoute != nil {
		log.WithError(errRoute).Warn("codex alpha search: model router returned an unsupported target")
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": errRoute.Error()})
		return
	}
	selectionOpts := coreexecutor.Options{Headers: selectionHeaders, OriginalRequest: body}
	var selection *auth.HomeDispatchSelection
	var selected *auth.Auth
	if s.handlers.AuthManager.HomeEnabled() {
		selection, err = s.handlers.AuthManager.SelectHomeAuthByKind(ctx, "codex", selectionModel, auth.AuthKindOAuth, selectionOpts)
		if selection != nil {
			selected = selection.CloneAuth()
		}
	} else {
		selected, err = s.handlers.AuthManager.SelectAuthByKind(ctx, "codex", selectionModel, auth.AuthKindOAuth, selectionOpts)
	}
	if err != nil {
		status := http.StatusServiceUnavailable
		if statusError, ok := err.(interface{ StatusCode() int }); ok && statusError.StatusCode() > 0 {
			status = statusError.StatusCode()
		}
		for _, value := range auth.SafeResponseHeaders(err).Values("Retry-After") {
			c.Writer.Header().Add("Retry-After", value)
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	if selected == nil {
		if selection != nil {
			selection.End("missing_auth")
		}
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Codex auth unavailable"})
		return
	}
	var releaseAttempt func()
	if selection != nil {
		attemptCtx, release, errBind := homeSelectionAttemptContext(ctx, selection)
		if errBind != nil {
			selection.End("attempt_bind_failed")
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": errBind.Error()})
			return
		}
		ctx = attemptCtx
		releaseAttempt = release
		defer releaseAttempt()
	}
	logging.SetGinCPATraceID(c, selected.EnsureIndex())

	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("Accept", "application/json")
	headers.Set("Originator", "codex_cli_rs")
	for _, name := range []string{"Version", "User-Agent", "Session_id", "X-Client-Request-Id"} {
		if value := strings.TrimSpace(c.GetHeader(name)); value != "" {
			headers.Set(name, value)
		}
	}
	if accountID, ok := selected.Metadata["account_id"].(string); ok && strings.TrimSpace(accountID) != "" {
		headers.Set("Chatgpt-Account-Id", accountID)
	}

	const upstreamURL = "https://chatgpt.com/backend-api/codex/alpha/search"
	req, err := s.handlers.AuthManager.NewHttpRequest(
		ctx, selected, http.MethodPost, upstreamURL, upstreamRequestBody, headers,
	)
	if err != nil {
		if selection != nil {
			selection.End("request_build_failed")
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	var authID, authLabel, authType, authValue string
	if selected != nil {
		authID = selected.ID
		authLabel = selected.Label
		authType, authValue = selected.AccountInfo()
	}
	helpHeaders := req.Header.Clone()
	helps.RecordAPIRequest(ctx, s.cfg, helps.UpstreamRequestLog{
		URL:       upstreamURL,
		Method:    http.MethodPost,
		Headers:   helpHeaders,
		Body:      upstreamRequestBody,
		Provider:  "codex",
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	if errCtx := ctx.Err(); errCtx != nil {
		if selection != nil {
			selection.End("attempt_canceled")
		}
		c.JSON(http.StatusRequestTimeout, gin.H{"error": errCtx.Error()})
		return
	}
	resp, err := s.handlers.AuthManager.HttpRequest(ctx, selected, req)
	if err != nil {
		if selection != nil {
			selection.End("request_failed")
		}
		helps.RecordAPIResponseError(ctx, s.cfg, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	closeResponseBody := func() error {
		errClose := resp.Body.Close()
		if errClose != nil {
			log.Errorf("codex alpha search: close response body error: %v", errClose)
		}
		return errClose
	}
	if selection != nil {
		if errBind := selection.Bind(closeResponseBody); errBind != nil {
			selection.End("response_bind_failed")
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": errBind.Error()})
			return
		}
		defer selection.End("response_closed")
	} else {
		defer func() { _ = closeResponseBody() }()
	}
	helps.RecordAPIResponseMetadata(ctx, s.cfg, resp.StatusCode, resp.Header.Clone())
	upstreamBody, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		helps.RecordAPIResponseError(ctx, s.cfg, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to read Codex search response"})
		return
	}
	helps.AppendAPIResponseChunk(ctx, s.cfg, upstreamBody)
	if contentType := resp.Header.Get("Content-Type"); contentType != "" {
		c.Header("Content-Type", contentType)
	}
	c.Status(resp.StatusCode)
	_, _ = c.Writer.Write(upstreamBody)
}

// AttachWebsocketRoute registers a websocket upgrade handler on the primary Gin engine.
// The handler is served as-is without additional middleware beyond the standard stack already configured.
func (s *Server) AttachWebsocketRoute(path string, handler http.Handler) {
	if s == nil || s.engine == nil || handler == nil {
		return
	}
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		trimmed = "/v1/ws"
	}
	if !strings.HasPrefix(trimmed, "/") {
		trimmed = "/" + trimmed
	}
	s.wsRouteMu.Lock()
	if _, exists := s.wsRoutes[trimmed]; exists {
		s.wsRouteMu.Unlock()
		return
	}
	s.wsRoutes[trimmed] = struct{}{}
	s.wsRouteMu.Unlock()

	authMiddleware := AuthMiddleware(s.accessManager)
	conditionalAuth := func(c *gin.Context) {
		if !s.wsAuthEnabled.Load() {
			c.Next()
			return
		}
		authMiddleware(c)
	}
	finalHandler := func(c *gin.Context) {
		handler.ServeHTTP(c.Writer, c.Request)
		c.Abort()
	}

	s.engine.GET(trimmed, conditionalAuth, finalHandler)
}

func (s *Server) registerManagementRoutes() {
	if s == nil || s.engine == nil || s.mgmt == nil {
		return
	}
	if !s.managementRoutesRegistered.CompareAndSwap(false, true) {
		return
	}

	log.Info("management routes registered after secret key configuration")

	s.engine.POST("/v0/management/oauth-callback", s.managementAvailabilityMiddleware(), s.mgmt.PostOAuthCallback)
	s.engine.GET("/v0/management/oauth-callback", s.managementAvailabilityMiddleware(), s.mgmt.GetOAuthCallback)

	mgmt := s.engine.Group("/v0/management")
	mgmt.Use(s.managementAvailabilityMiddleware(), s.mgmt.Middleware())
	{
		mgmt.GET("/config", s.mgmt.GetConfig)
		mgmt.GET("/config.yaml", s.mgmt.GetConfigYAML)
		mgmt.PUT("/config.yaml", s.mgmt.PutConfigYAML)
		mgmt.GET("/latest-version", s.mgmt.GetLatestVersion)
		mgmt.GET("/plugins", s.mgmt.ListPlugins)
		mgmt.GET("/plugin-store", s.mgmt.ListPluginStore)
		mgmt.POST("/plugin-store/:id/install", s.mgmt.InstallPluginFromStore)
		mgmt.DELETE("/plugins/:id", s.mgmt.DeletePlugin)
		mgmt.PATCH("/plugins/:id/enabled", s.mgmt.PatchPluginEnabled)
		mgmt.GET("/plugins/:id/config", s.mgmt.GetPluginConfig)
		mgmt.PUT("/plugins/:id/config", s.mgmt.PutPluginConfig)
		mgmt.PATCH("/plugins/:id/config", s.mgmt.PatchPluginConfig)

		mgmt.GET("/debug", s.mgmt.GetDebug)
		mgmt.PUT("/debug", s.mgmt.PutDebug)
		mgmt.PATCH("/debug", s.mgmt.PutDebug)

		mgmt.GET("/logging-to-file", s.mgmt.GetLoggingToFile)
		mgmt.PUT("/logging-to-file", s.mgmt.PutLoggingToFile)
		mgmt.PATCH("/logging-to-file", s.mgmt.PutLoggingToFile)

		mgmt.GET("/logs-max-total-size-mb", s.mgmt.GetLogsMaxTotalSizeMB)
		mgmt.PUT("/logs-max-total-size-mb", s.mgmt.PutLogsMaxTotalSizeMB)
		mgmt.PATCH("/logs-max-total-size-mb", s.mgmt.PutLogsMaxTotalSizeMB)

		mgmt.GET("/error-logs-max-files", s.mgmt.GetErrorLogsMaxFiles)
		mgmt.PUT("/error-logs-max-files", s.mgmt.PutErrorLogsMaxFiles)
		mgmt.PATCH("/error-logs-max-files", s.mgmt.PutErrorLogsMaxFiles)

		mgmt.GET("/usage-statistics-enabled", s.mgmt.GetUsageStatisticsEnabled)
		mgmt.PUT("/usage-statistics-enabled", s.mgmt.PutUsageStatisticsEnabled)
		mgmt.PATCH("/usage-statistics-enabled", s.mgmt.PutUsageStatisticsEnabled)

		mgmt.GET("/proxy-url", s.mgmt.GetProxyURL)
		mgmt.PUT("/proxy-url", s.mgmt.PutProxyURL)
		mgmt.PATCH("/proxy-url", s.mgmt.PutProxyURL)
		mgmt.DELETE("/proxy-url", s.mgmt.DeleteProxyURL)

		mgmt.GET("/proxy-by-provider", s.mgmt.GetProxyByProvider)
		mgmt.PUT("/proxy-by-provider/:provider", s.mgmt.PutProxyByProvider)
		mgmt.PATCH("/proxy-by-provider/:provider", s.mgmt.PutProxyByProvider)
		mgmt.DELETE("/proxy-by-provider/:provider", s.mgmt.DeleteProxyByProvider)

		mgmt.POST("/api-call", s.mgmt.APICall)

		mgmt.GET("/quota-exceeded/switch-project", s.mgmt.GetSwitchProject)
		mgmt.PUT("/quota-exceeded/switch-project", s.mgmt.PutSwitchProject)
		mgmt.PATCH("/quota-exceeded/switch-project", s.mgmt.PutSwitchProject)

		mgmt.GET("/quota-exceeded/switch-preview-model", s.mgmt.GetSwitchPreviewModel)
		mgmt.PUT("/quota-exceeded/switch-preview-model", s.mgmt.PutSwitchPreviewModel)
		mgmt.PATCH("/quota-exceeded/switch-preview-model", s.mgmt.PutSwitchPreviewModel)
		mgmt.POST("/reset-quota", s.mgmt.ResetQuota)

		mgmt.GET("/api-keys", s.mgmt.GetAPIKeys)
		mgmt.PUT("/api-keys", s.mgmt.PutAPIKeys)
		mgmt.PATCH("/api-keys", s.mgmt.PatchAPIKeys)
		mgmt.DELETE("/api-keys", s.mgmt.DeleteAPIKeys)
		mgmt.GET("/api-key-usage", s.mgmt.GetAPIKeyUsage)
		mgmt.GET("/usage-queue", s.mgmt.GetUsageQueue)

		mgmt.GET("/gemini-api-key", s.mgmt.GetGeminiKeys)
		mgmt.PUT("/gemini-api-key", s.mgmt.PutGeminiKeys)
		mgmt.PATCH("/gemini-api-key", s.mgmt.PatchGeminiKey)
		mgmt.DELETE("/gemini-api-key", s.mgmt.DeleteGeminiKey)

		mgmt.GET("/interactions-api-key", s.mgmt.GetInteractionsKeys)
		mgmt.PUT("/interactions-api-key", s.mgmt.PutInteractionsKeys)
		mgmt.PATCH("/interactions-api-key", s.mgmt.PatchInteractionsKey)
		mgmt.DELETE("/interactions-api-key", s.mgmt.DeleteInteractionsKey)

		mgmt.GET("/logs", s.mgmt.GetLogs)
		mgmt.DELETE("/logs", s.mgmt.DeleteLogs)
		mgmt.GET("/request-error-logs", s.mgmt.GetRequestErrorLogs)
		mgmt.GET("/request-error-logs/:name", s.mgmt.DownloadRequestErrorLog)
		mgmt.GET("/request-log-by-id/:id", s.mgmt.GetRequestLogByID)
		mgmt.GET("/request-log", s.mgmt.GetRequestLog)
		mgmt.PUT("/request-log", s.mgmt.PutRequestLog)
		mgmt.PATCH("/request-log", s.mgmt.PutRequestLog)
		mgmt.GET("/ws-auth", s.mgmt.GetWebsocketAuth)
		mgmt.PUT("/ws-auth", s.mgmt.PutWebsocketAuth)
		mgmt.PATCH("/ws-auth", s.mgmt.PutWebsocketAuth)

		mgmt.GET("/request-retry", s.mgmt.GetRequestRetry)
		mgmt.PUT("/request-retry", s.mgmt.PutRequestRetry)
		mgmt.PATCH("/request-retry", s.mgmt.PutRequestRetry)
		mgmt.GET("/max-retry-interval", s.mgmt.GetMaxRetryInterval)
		mgmt.PUT("/max-retry-interval", s.mgmt.PutMaxRetryInterval)
		mgmt.PATCH("/max-retry-interval", s.mgmt.PutMaxRetryInterval)

		mgmt.GET("/force-model-prefix", s.mgmt.GetForceModelPrefix)
		mgmt.PUT("/force-model-prefix", s.mgmt.PutForceModelPrefix)
		mgmt.PATCH("/force-model-prefix", s.mgmt.PutForceModelPrefix)

		mgmt.GET("/routing/strategy", s.mgmt.GetRoutingStrategy)
		mgmt.PUT("/routing/strategy", s.mgmt.PutRoutingStrategy)
		mgmt.PATCH("/routing/strategy", s.mgmt.PutRoutingStrategy)

		mgmt.GET("/claude-api-key", s.mgmt.GetClaudeKeys)
		mgmt.PUT("/claude-api-key", s.mgmt.PutClaudeKeys)
		mgmt.PATCH("/claude-api-key", s.mgmt.PatchClaudeKey)
		mgmt.DELETE("/claude-api-key", s.mgmt.DeleteClaudeKey)

		mgmt.GET("/codex-api-key", s.mgmt.GetCodexKeys)
		mgmt.PUT("/codex-api-key", s.mgmt.PutCodexKeys)
		mgmt.PATCH("/codex-api-key", s.mgmt.PatchCodexKey)
		mgmt.DELETE("/codex-api-key", s.mgmt.DeleteCodexKey)

		mgmt.GET("/xai-api-key", s.mgmt.GetXAIKeys)
		mgmt.PUT("/xai-api-key", s.mgmt.PutXAIKeys)
		mgmt.PATCH("/xai-api-key", s.mgmt.PatchXAIKey)
		mgmt.DELETE("/xai-api-key", s.mgmt.DeleteXAIKey)

		mgmt.GET("/openai-compatibility", s.mgmt.GetOpenAICompat)
		mgmt.PUT("/openai-compatibility", s.mgmt.PutOpenAICompat)
		mgmt.PATCH("/openai-compatibility", s.mgmt.PatchOpenAICompat)
		mgmt.DELETE("/openai-compatibility", s.mgmt.DeleteOpenAICompat)

		mgmt.GET("/vertex-api-key", s.mgmt.GetVertexCompatKeys)
		mgmt.PUT("/vertex-api-key", s.mgmt.PutVertexCompatKeys)
		mgmt.PATCH("/vertex-api-key", s.mgmt.PatchVertexCompatKey)
		mgmt.DELETE("/vertex-api-key", s.mgmt.DeleteVertexCompatKey)

		mgmt.GET("/oauth-excluded-models", s.mgmt.GetOAuthExcludedModels)
		mgmt.PUT("/oauth-excluded-models", s.mgmt.PutOAuthExcludedModels)
		mgmt.PATCH("/oauth-excluded-models", s.mgmt.PatchOAuthExcludedModels)
		mgmt.DELETE("/oauth-excluded-models", s.mgmt.DeleteOAuthExcludedModels)

		mgmt.GET("/oauth-model-alias", s.mgmt.GetOAuthModelAlias)
		mgmt.PUT("/oauth-model-alias", s.mgmt.PutOAuthModelAlias)
		mgmt.PATCH("/oauth-model-alias", s.mgmt.PatchOAuthModelAlias)
		mgmt.DELETE("/oauth-model-alias", s.mgmt.DeleteOAuthModelAlias)

		mgmt.GET("/auth-files", s.mgmt.ListAuthFiles)
		mgmt.GET("/codex-refresh-auth-files", s.mgmt.ListCodexRefreshAuthFiles)
		mgmt.GET("/auth-files/models", s.mgmt.GetAuthFileModels)
		mgmt.GET("/model-definitions/:channel", s.mgmt.GetStaticModelDefinitions)
		mgmt.GET("/auth-files/download", s.mgmt.DownloadAuthFile)
		mgmt.POST("/auth-files", s.mgmt.UploadAuthFile)
		mgmt.DELETE("/auth-files", s.mgmt.DeleteAuthFile)
		mgmt.PATCH("/auth-files/status", s.mgmt.PatchAuthFileStatus)
		mgmt.PATCH("/auth-files/fields", s.mgmt.PatchAuthFileFields)
		mgmt.POST("/vertex/import", s.mgmt.ImportVertexCredential)

		mgmt.GET("/anthropic-auth-url", s.mgmt.RequestAnthropicToken)
		mgmt.GET("/codex-auth-url", s.mgmt.RequestCodexToken)
		mgmt.GET("/antigravity-auth-url", s.mgmt.RequestAntigravityToken)
		mgmt.GET("/kimi-auth-url", s.mgmt.RequestKimiToken)
		mgmt.GET("/xai-auth-url", s.mgmt.RequestXAIToken)
		mgmt.GET("/get-auth-status", s.mgmt.GetAuthStatus)
		mgmt.DELETE("/oauth-session", s.mgmt.CancelAuthSession)

		mgmt.POST("/codex-free-refresh", s.mgmt.RefreshCodexFreeAccounts)
		mgmt.GET("/codex-free-refresh/:taskId", s.mgmt.GetRefreshCodexFreeStatus)

		mgmt.GET("/xai-refresh-auth-files", s.mgmt.ListXAIRefreshAuthFiles)
		mgmt.POST("/xai-free-refresh", s.mgmt.RefreshXAIFreeAccounts)
		mgmt.GET("/xai-free-refresh/:taskId", s.mgmt.GetRefreshXAIFreeStatus)
	}
}

func (s *Server) managementAvailabilityMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !s.managementAvailable(c) {
			return
		}
		c.Next()
	}
}

func (s *Server) managementAvailable(c *gin.Context) bool {
	if s == nil || s.cfg == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return false
	}
	if s.cfg.Home.Enabled {
		c.AbortWithStatus(http.StatusNotFound)
		return false
	}
	if !s.managementRoutesEnabled.Load() {
		c.AbortWithStatus(http.StatusNotFound)
		return false
	}
	return true
}

func (s *Server) refreshPluginManagementRoutes() {
	if s == nil || s.pluginHost == nil || s.engine == nil {
		return
	}
	s.pluginHost.RegisterManagementRoutes(context.Background(), s.registeredManagementRouteKeys())
}

// RefreshPluginManagementRoutes rebuilds plugin-owned Management API routes.
func (s *Server) RefreshPluginManagementRoutes() {
	s.refreshPluginManagementRoutes()
}

func (s *Server) registeredManagementRouteKeys() map[string]struct{} {
	out := make(map[string]struct{})
	if s == nil || s.engine == nil {
		return out
	}
	for _, route := range s.engine.Routes() {
		if strings.HasPrefix(route.Path, "/v0/management/") || route.Path == "/v0/management" {
			out[strings.ToUpper(strings.TrimSpace(route.Method))+" "+route.Path] = struct{}{}
		}
	}
	return out
}

func (s *Server) pluginManagementNoRoute(c *gin.Context) {
	if s == nil || c == nil || c.Request == nil || c.Request.URL == nil {
		if c != nil {
			c.AbortWithStatus(http.StatusNotFound)
		}
		return
	}
	path := c.Request.URL.Path
	if strings.HasPrefix(path, "/v0/resource/plugins/") {
		s.pluginResourceNoRoute(c)
		return
	}
	if path != "/v0/management" && !strings.HasPrefix(path, "/v0/management/") {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if s.pluginHost == nil || s.mgmt == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if !s.managementAvailable(c) {
		return
	}
	s.mgmt.Middleware()(c)
	if c.IsAborted() {
		return
	}
	if s.mgmt.ServePluginAuthURL(c) {
		c.Abort()
		return
	}
	if s.pluginHost.ServeManagementHTTP(c.Writer, c.Request) {
		c.Abort()
		return
	}
	c.AbortWithStatus(http.StatusNotFound)
}

func (s *Server) pluginResourceNoRoute(c *gin.Context) {
	if s == nil || c == nil || c.Request == nil || c.Request.URL == nil {
		if c != nil {
			c.AbortWithStatus(http.StatusNotFound)
		}
		return
	}
	if s.cfg == nil || s.cfg.Home.Enabled || s.pluginHost == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if s.pluginHost.ServeResourceHTTP(c.Writer, c.Request) {
		c.Abort()
		return
	}
	c.AbortWithStatus(http.StatusNotFound)
}

func (s *Server) serveManagementControlPanel(c *gin.Context) {
	cfg := s.cfg
	if cfg == nil || cfg.Home.Enabled || cfg.RemoteManagement.DisableControlPanel {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	filePath := managementasset.FilePath(s.configFilePath)
	if strings.TrimSpace(filePath) == "" {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	if _, err := os.Stat(filePath); err != nil {
		if os.IsNotExist(err) {
			s.startManagementAssetSync(cfg)
			s.serveManagementLoadingBootstrap(c)
			return
		} else {
			log.WithError(err).Error("failed to stat management control panel asset")
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
	}

	data, errRead := os.ReadFile(filePath)
	if errRead != nil {
		log.WithError(errRead).Error("failed to read management control panel asset")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	codexRefreshToken := ""
	xaiRefreshToken := ""
	if s.mgmt != nil {
		codexRefreshToken = s.mgmt.SignCodexRefreshActionToken()
		xaiRefreshToken = s.mgmt.SignXAIRefreshActionToken()
		s.mgmt.TryIssueSessionCookie(c)
	}
	patched := injectModelPriceDropdownClipPatch(data)
	patched = injectCodexFreeRefreshPatch(patched, codexRefreshToken)
	patched = injectXAIRefreshPatch(patched, xaiRefreshToken)

	etag := fmt.Sprintf(`"%x"`, sha256.Sum256(patched))
	c.Header("ETag", etag)
	c.Header("Cache-Control", "no-cache")

	if match := c.GetHeader("If-None-Match"); match != "" && strings.Contains(match, etag) {
		c.AbortWithStatus(http.StatusNotModified)
		return
	}

	c.Data(http.StatusOK, "text/html; charset=utf-8", patched)
}

func (s *Server) startManagementAssetSync(cfg *config.Config) {
	if s == nil || cfg == nil || !s.managementAssetSyncing.CompareAndSwap(false, true) {
		return
	}
	configPath := s.configFilePath
	proxyURL := cfg.ProxyURL
	panelRepository := cfg.RemoteManagement.PanelGitHubRepository
	go func() {
		defer s.managementAssetSyncing.Store(false)
		// Control panel bootstrap should not be canceled by client disconnects.
		managementasset.EnsureLatestManagementHTML(context.Background(), managementasset.StaticDir(configPath), proxyURL, panelRepository)
	}()
}

func (s *Server) serveManagementLoadingBootstrap(c *gin.Context) {
	c.Header("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	c.Header("Pragma", "no-cache")
	c.Header("Expires", "0")
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(managementLoadingBootstrapHTML))
}

const managementLoadingBootstrapHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Management panel is loading</title>
<style>body{font-family:Arial,sans-serif;margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;background:#0f172a;color:#e2e8f0}.card{max-width:520px;padding:28px;border:1px solid rgba(148,163,184,.35);border-radius:14px;background:rgba(15,23,42,.86);box-shadow:0 24px 80px rgba(0,0,0,.35)}h1{font-size:22px;margin:0 0 12px}p{line-height:1.5;color:#cbd5e1;margin:0 0 10px}</style>
</head>
<body>
<div class="card"><h1>Management panel is loading</h1><p>The management UI is being downloaded in the background. This page will refresh automatically.</p><p>If it does not load soon, refresh this page manually.</p></div>
<script>setTimeout(function(){ location.reload(); }, 2000);</script>
</body>
</html>`

func injectModelPriceDropdownClipPatch(html []byte) []byte {
	const marker = "__cpa_model_price_dropdown_clip_patch__"
	if len(html) == 0 || bytes.Contains(html, []byte(marker)) {
		return html
	}

	patch := []byte(`<script>
(function () {
  var MARKER = "__cpa_model_price_dropdown_clip_patch__";
  if (window[MARKER]) return;
  window[MARKER] = true;

  var SECTION_ZH = "\u6a21\u578b\u4ef7\u683c\u8bbe\u7f6e";
  var SECTION_EN = "model price settings";
  var LABEL_ZH = "\u6a21\u578b\u540d\u79f0";
  var LABEL_EN = "model name";
  var SELECT_LABEL_ZH = "\u9009\u62e9\u6a21\u578b";
  var SELECT_LABEL_EN = "select model";
  var COMBO_SELECTOR = "select,[role='combobox'],input[list],button[aria-haspopup='listbox'],button[aria-expanded]";

  function normalizeText(text) {
    return (text || "").toLowerCase().replace(/\s+/g, " ").trim();
  }

  function matchesNeedle(text, needles) {
    var value = normalizeText(text);
    if (!value) return false;
    for (var i = 0; i < needles.length; i++) {
      if (value.indexOf(normalizeText(needles[i])) !== -1) return true;
    }
    return false;
  }

  function readShortText(el) {
    if (!el) return "";
    var text = (el.innerText || el.textContent || "").trim();
    if (text.length > 120) return "";
    return text;
  }

  function elementHasNeedle(el, needles) {
    return matchesNeedle(readShortText(el), needles);
  }

  function hasComboLike(el) {
    if (!el || !el.querySelector) return false;
    return !!el.querySelector(COMBO_SELECTOR);
  }

  function countCombos(el) {
    if (!el || !el.querySelectorAll) return 0;
    return el.querySelectorAll(COMBO_SELECTOR).length;
  }

  function findTextElement(needles, root) {
    var scope = root || document;
    var nodes = scope.querySelectorAll("h1,h2,h3,h4,h5,h6,label,legend,span,div,p,strong,th,td");
    for (var i = 0; i < nodes.length; i++) {
      if (elementHasNeedle(nodes[i], needles)) {
        return nodes[i];
      }
    }
    return null;
  }

  function closestContainer(node) {
    var current = node;
    for (var i = 0; i < 8 && current; i++) {
      if (current.matches && current.matches("section,article,form,fieldset,.card,.panel,[class*='card'],[class*='panel']")) {
        return current;
      }
      if (hasComboLike(current)) {
        return current;
      }
      current = current.parentElement;
    }
    return node ? node.parentElement : null;
  }

  function findModelPriceSection() {
    var sectionNeedles = [SECTION_ZH, SECTION_EN];
    var labelNeedles = [LABEL_ZH, LABEL_EN, SELECT_LABEL_ZH, SELECT_LABEL_EN];

    var heading = findTextElement(sectionNeedles, document);
    if (heading) {
      return closestContainer(heading);
    }

    var label = findTextElement(labelNeedles, document);
    if (!label) return null;
    var current = label;
    for (var i = 0; i < 8 && current; i++) {
      if (matchesNeedle(current.textContent || "", sectionNeedles)) {
        return current;
      }
      if (hasComboLike(current)) {
        return current;
      }
      current = current.parentElement;
    }
    return closestContainer(label);
  }

  function findFirstComboRow(section) {
    if (!section || !section.querySelector) return null;
    var trigger = section.querySelector(COMBO_SELECTOR);
    if (!trigger) return null;
    var candidate = trigger;
    var current = trigger.parentElement;
    for (var i = 0; i < 6 && current && current !== section; i++) {
      if (countCombos(current) !== 1) break;
      candidate = current;
      current = current.parentElement;
    }
    return candidate;
  }

  function findModelNameRow(section) {
    if (!section) return null;
    var labelNeedles = [LABEL_ZH, LABEL_EN, SELECT_LABEL_ZH, SELECT_LABEL_EN];
    var label = findTextElement(labelNeedles, section);
    if (label) {
      var current = label;
      for (var i = 0; i < 6 && current; i++) {
        if (hasComboLike(current)) {
          return current;
        }
        current = current.parentElement;
      }
      if (label.parentElement) {
        return label.parentElement;
      }
    }
    return findFirstComboRow(section);
  }

  function isVisibleCombo(node) {
    if (!node || !node.getBoundingClientRect) return false;
    var rect = node.getBoundingClientRect();
    if (!rect || rect.width === 0 || rect.height === 0) return false;
    var style = window.getComputedStyle ? window.getComputedStyle(node) : null;
    if (style && (style.display === "none" || style.visibility === "hidden")) return false;
    return true;
  }

  function findLowestVisibleCombo() {
    var combos = document.querySelectorAll(COMBO_SELECTOR);
    var best = null;
    var bestTop = -Infinity;
    for (var i = 0; i < combos.length; i++) {
      var combo = combos[i];
      if (!isVisibleCombo(combo)) continue;
      var rect = combo.getBoundingClientRect();
      if (rect.top >= bestTop) {
        bestTop = rect.top;
        best = combo;
      }
    }
    return best;
  }

  function relaxNode(node, minZIndex) {
    if (!node || !node.style || !window.getComputedStyle) return;
    var style = window.getComputedStyle(node);
    var overflow = ((style.overflow || "") + " " + (style.overflowX || "") + " " + (style.overflowY || "")).toLowerCase();
    if (overflow.indexOf("hidden") !== -1 || overflow.indexOf("clip") !== -1 || overflow.indexOf("auto") !== -1 || overflow.indexOf("scroll") !== -1) {
      node.style.setProperty("overflow", "visible", "important");
      node.style.setProperty("overflow-x", "visible", "important");
      node.style.setProperty("overflow-y", "visible", "important");
    }
    if (style.position === "static") {
      node.style.setProperty("position", "relative", "important");
    }
    if (minZIndex > 0) {
      var z = parseInt(style.zIndex, 10);
      if (style.zIndex === "auto" || isNaN(z) || z < minZIndex) {
        node.style.setProperty("z-index", String(minZIndex), "important");
      }
    }
  }

  function relaxChain(start, depth, baseZ) {
    var current = start;
    for (var i = 0; i < depth && current && current !== document.body; i++) {
      relaxNode(current, baseZ + i);
      current = current.parentElement;
    }
  }

  function isUsageRoute() {
    var hash = normalizeText(window.location.hash || "");
    return hash.indexOf("/usage") !== -1 || hash.indexOf("usage") !== -1 || hash.indexOf("\u7edf\u8ba1") !== -1;
  }

  function patchModelPriceDropdown() {
    if (!isUsageRoute()) return false;
    var section = findModelPriceSection();
    var row = null;
    if (section) {
      relaxChain(section, 10, 1200);
      row = findModelNameRow(section);
    }
    if (!row) {
      var combo = findLowestVisibleCombo();
      if (!combo) return false;
      row = combo.parentElement || combo;
      relaxChain(combo, 8, 1300);
      relaxChain(row, 6, 1310);
    }
    relaxChain(row, 6, 1300);

    var trigger = row.querySelector(COMBO_SELECTOR);
    if (trigger && trigger.style) {
      trigger.style.setProperty("position", "relative", "important");
      trigger.style.setProperty("z-index", "1400", "important");
    }
    return true;
  }

  var observer = null;
  var scheduled = false;
  var patchApplied = false;
  var lastScheduleAt = 0;
  var scheduleGapMs = 180;

  function stopObserver() {
    if (!observer) return;
    observer.disconnect();
    observer = null;
  }

  function schedulePatch() {
    if (!isUsageRoute()) {
      stopObserver();
      return;
    }
    var now = Date.now();
    if (scheduled || now - lastScheduleAt < scheduleGapMs) return;
    scheduled = true;
		lastScheduleAt = now;
		setTimeout(function () {
			scheduled = false;
			patchApplied = patchModelPriceDropdown() || patchApplied;
		}, 30);
	}

  function setupObserver() {
    if (!isUsageRoute()) {
      stopObserver();
      return;
    }
    if (!window.MutationObserver || !document.body || observer) return;
    observer = new MutationObserver(function () {
      schedulePatch();
    });
    observer.observe(document.body, { childList: true, subtree: true });
  }

  function handleRouteChange() {
    patchApplied = false;
    setupObserver();
    schedulePatch();
  }

  window.addEventListener("hashchange", handleRouteChange, true);
  window.addEventListener("popstate", handleRouteChange, true);
  window.addEventListener("resize", schedulePatch, true);

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", function () {
      handleRouteChange();
    }, { once: true });
  } else {
    handleRouteChange();
  }

	setTimeout(handleRouteChange, 300);
	setTimeout(handleRouteChange, 1200);
	setTimeout(handleRouteChange, 2500);
})();
</script>`)

	lower := bytes.ToLower(html)
	bodyClose := []byte("</body>")
	if idx := bytes.LastIndex(lower, bodyClose); idx >= 0 {
		out := make([]byte, 0, len(html)+len(patch))
		out = append(out, html[:idx]...)
		out = append(out, patch...)
		out = append(out, html[idx:]...)
		return out
	}
	return append(html, patch...)
}

func injectCodexFreeRefreshPatch(html []byte, codexRefreshToken string) []byte {
	const marker = "__cpa_codex_free_refresh_patch__"
	if len(html) == 0 || bytes.Contains(html, []byte(marker)) {
		return html
	}

	patch := []byte(`<script>
(function () {
  var MARKER = "__cpa_codex_free_refresh_patch__";
  if (window[MARKER]) return;
  window[MARKER] = true;
  var CODEX_REFRESH_TOKEN = "__CPA_CODEX_REFRESH_TOKEN__";

  function normalizeText(value) {
    return String(value == null ? "" : value).toLowerCase().replace(/\s+/g, " ").trim();
  }

  function decodeRoutePart(value) {
    var text = String(value == null ? "" : value);
    try {
      return decodeURIComponent(text);
    } catch (err) {
      return text;
    }
  }

  function normalizeRouteText(value) {
    return normalizeText(decodeRoutePart(value)).replace(/[\-_]+/g, " ");
  }

  function routeHasToken(text, token) {
    if (!text || !token) return false;
    if (text.indexOf(token) === -1) return false;
    if (token.charAt(0) === "/") return true;
    var idx = text.indexOf(token);
    while (idx !== -1) {
      var before = idx === 0 ? "" : text.charAt(idx - 1);
      var after = idx + token.length >= text.length ? "" : text.charAt(idx + token.length);
      if ((!before || /[\s/#?&=.]/.test(before)) && (!after || /[\s/#?&=.]/.test(after))) return true;
      idx = text.indexOf(token, idx + token.length);
    }
    return false;
  }

  function isAuthRoute() {
    var rawLocationText = (window.location.hash || "") + " " + (window.location.pathname || "") + " " + (window.location.search || "");
    var locationText = normalizeRouteText(rawLocationText);
    var routeTokens = ["auth files", "/auth", "认证文件", "凭证"];
    for (var i = 0; i < routeTokens.length; i++) {
      if (routeHasToken(locationText, normalizeRouteText(routeTokens[i]))) return true;
    }
    return activeAuthRouteFromNavigation() || !!findAuthSection();
  }

  function activeAuthRouteFromNavigation() {
    var selectors = ["[aria-current='page']", "[aria-selected='true']", "[data-state='active']", "[role='tab']", "nav [class*='active']", "aside [class*='active']", "[role='navigation'] [class*='active']"];
    for (var i = 0; i < selectors.length; i++) {
      var nodes = document.querySelectorAll(selectors[i]);
      for (var j = 0; j < nodes.length; j++) {
        var node = nodes[j];
        if (!isLayoutChrome(node) && !(node.getAttribute && normalizeText(node.getAttribute("role")) === "tab")) continue;
        var text = normalizeRouteText((node.innerText || node.textContent || "") + " " + (node.getAttribute ? (node.getAttribute("aria-label") || node.getAttribute("title") || "") : ""));
        if (routeHasToken(text, "auth files") || routeHasToken(text, "认证文件") || routeHasToken(text, "凭证")) return true;
      }
    }
    return false;
  }

  function isLayoutChrome(node) {
    var current = node;
    for (var i = 0; i < 6 && current; i++) {
      if (current.matches && current.matches("nav,aside,header,footer,[role='navigation'],[class*='sidebar'],[class*='menu']")) return true;
      current = current.parentElement;
    }
    return false;
  }

  function findAuthSection() {
    var roots = document.querySelectorAll("main,[role='main'],[class*='content'],[class*='page']");
    var titleSelectors = "h1,h2,h3,[role='heading'],[data-page-title],[class*='title']";
    for (var r = 0; r < roots.length; r++) {
      var root = roots[r];
      if (isLayoutChrome(root)) continue;
      var titles = root.querySelectorAll(titleSelectors);
      for (var i = 0; i < titles.length; i++) {
        if (isLayoutChrome(titles[i])) continue;
        var title = normalizeText(titles[i].innerText || titles[i].textContent || "");
        if (title.indexOf("auth files") === -1 && title.indexOf("认证文件") === -1 && title.indexOf("凭证") === -1) continue;
        var container = titles[i];
        for (var depth = 0; depth < 5 && container && container !== root; depth++) {
          if (container.matches && container.matches("section,article,.card,.panel,[class*='card'],[class*='panel'],[class*='page']")) break;
          container = container.parentElement;
        }
        return container || root;
      }
    }

    var selectors = ["main [class*='auth']", "main [id*='auth']", "main section", "main .card", "main .panel", "[role='main'] [class*='auth']", "[role='main'] [id*='auth']", "[role='main'] section", "[role='main'] .card", "[role='main'] .panel", "[class*='auth']", "[id*='auth']", "section", ".card", ".panel"];
    for (var s = 0; s < selectors.length; s++) {
      var nodes = document.querySelectorAll(selectors[s]);
      for (var j = 0; j < nodes.length; j++) {
        var node = nodes[j];
        if (isLayoutChrome(node)) continue;
        var text = normalizeText(node.innerText || node.textContent || "");
        if ((text.indexOf("codex") !== -1 || text.indexOf("auth") !== -1 || text.indexOf("认证文件") !== -1 || text.indexOf("凭证") !== -1) && (text.indexOf("auth") !== -1 || text.indexOf("认证文件") !== -1 || text.indexOf("凭证") !== -1 || text.indexOf("provider") !== -1)) {
          return node;
        }
      }
    }
    return null;
  }

  function createButton() {
    var btn = document.createElement("button");
    btn.id = "codex-free-refresh-btn";
    btn.textContent = "⟳ Refresh Free Accounts";
    btn.style.cssText = "margin:8px 0;padding:6px 14px;border:1px solid #475569;border-radius:6px;background:#1e293b;color:#e2e8f0;cursor:pointer;font-size:13px;";
    btn.onmouseenter = function () { btn.style.background = "#334155"; };
    btn.onmouseleave = function () { btn.style.background = "#1e293b"; };
    btn.onclick = startRefresh;
    return btn;
  }

  function createStatusEl() {
    var el = document.createElement("div");
    el.id = "codex-free-refresh-status";
    el.style.cssText = "margin:4px 0 8px;font-size:12px;color:#94a3b8;white-space:pre-wrap;";
    return el;
  }

  function getMgmtBase() {
    var scripts = document.querySelectorAll("script[src]");
    for (var i = 0; i < scripts.length; i++) {
      var src = scripts[i].src || "";
      var idx = src.indexOf("/management.");
      if (idx !== -1) {
        return src.substring(0, src.lastIndexOf("/"));
      }
    }
    return "/v0/management";
  }

  function apiHeaders(status) {
    return {
      "Content-Type": "application/json",
      "X-Codex-Refresh-Token": CODEX_REFRESH_TOKEN
    };
  }

  function getSelectedCodexAuthIndices() {
    var checkboxes = document.querySelectorAll(".codex-refresh-checkbox:checked");
    var indices = [];
    for (var i = 0; i < checkboxes.length; i++) {
      var idx = checkboxes[i].getAttribute("data-auth-index");
      if (idx) indices.push(idx);
    }
    return indices;
  }

  function startRefresh() {
    var btn = document.getElementById("codex-free-refresh-btn");
    var status = document.getElementById("codex-free-refresh-status");
    if (!btn || !status) return;

    var selectedIndices = getSelectedCodexAuthIndices();
    var body = {};
    if (selectedIndices.length > 0) {
      body.auth_indices = selectedIndices;
    }

    btn.disabled = true;
    btn.textContent = "Starting...";
    status.textContent = "";

    var headers = apiHeaders(status);
    if (!headers) {
      btn.disabled = false;
      btn.textContent = "⟳ Refresh Free Accounts";
      return;
    }

    fetch(getMgmtBase() + "/codex-free-refresh", { method: "POST", headers: headers, credentials: "same-origin", body: JSON.stringify(body) })
      .then(function (r) {
          if (!r.ok) throw new Error("HTTP " + r.status);
          return r.json();
        })
      .then(function (data) {
        if (data.error) {
          status.textContent = "Error: " + data.error;
          btn.disabled = false;
          btn.textContent = "⟳ Refresh Free Accounts";
          return;
        }
        if (data.total === 0) {
          status.textContent = "No free codex accounts found.";
          btn.disabled = false;
          btn.textContent = "⟳ Refresh Free Accounts";
          return;
        }
        status.textContent = "Started: " + data.total + " accounts. Polling...";
        pollStatus(data.task_id, data.total);
      })
      .catch(function (err) {
        status.textContent = "Request failed: " + err;
        btn.disabled = false;
        btn.textContent = "⟳ Refresh Free Accounts";
      });
  }

  function pollStatus(taskId, total) {
    var btn = document.getElementById("codex-free-refresh-btn");
    var status = document.getElementById("codex-free-refresh-status");
    if (!btn || !status) return;

    var interval = setInterval(function () {
      btn = document.getElementById("codex-free-refresh-btn");
      status = document.getElementById("codex-free-refresh-status");
      if (!btn || !status) {
        clearInterval(interval);
        return;
      }
      var headers = apiHeaders(status);
      if (!headers) {
        clearInterval(interval);
        btn.disabled = false;
        btn.textContent = "⟳ Refresh Free Accounts";
        return;
      }
      fetch(getMgmtBase() + "/codex-free-refresh/" + encodeURIComponent(taskId), { headers: headers, credentials: "same-origin" })
        .then(function (r) {
          if (!r.ok) throw new Error("HTTP " + r.status);
          return r.json();
        })
        .then(function (data) {
          if (data.error) {
            clearInterval(interval);
            status.textContent = "Error: " + data.error;
            btn.disabled = false;
            btn.textContent = "⟳ Refresh Free Accounts";
            return;
          }
          var lines = [];
          lines.push("Progress: " + data.done + "/" + data.total + " | Success: " + data.success + " | Failed: " + data.failed);
          if (data.results && data.results.length > 0) {
            for (var i = 0; i < data.results.length; i++) {
              var r = data.results[i];
              var icon = r.success ? "✓" : "✗";
              var detail = r.success ? "" : " (" + (r.error || "unknown") + ")";
              lines.push("  " + icon + " " + (r.email || r.name || "?") + detail);
            }
          }
          status.textContent = lines.join("\n");

          if (data.status === "completed") {
            clearInterval(interval);
            btn.disabled = false;
            btn.textContent = "⟳ Refresh Free Accounts";
          }
        })
        .catch(function (err) {
          clearInterval(interval);
          status.textContent = "Poll failed: " + err;
          btn.disabled = false;
          btn.textContent = "⟳ Refresh Free Accounts";
        });
    }, 2000);
  }

  var authFilesCache = null;
  var authFilesCacheAt = 0;
  var authFilesPending = null;

  function normalizeValue(value) {
    return String(value == null ? "" : value).toLowerCase().replace(/\s+/g, " ").trim();
  }

  function isCodexAuthFile(file) {
    if (!file) return false;
    return normalizeValue(file.provider) === "codex" || normalizeValue(file.type) === "codex";
  }

  function getAuthFiles(status) {
    var now = Date.now();
    if (authFilesCache && now - authFilesCacheAt < 10000) return Promise.resolve(authFilesCache);
    if (authFilesPending) return authFilesPending;
    authFilesPending = fetchAuthFilesAttempt(status, 0);
    return authFilesPending;
  }

  function fetchAuthFilesAttempt(status, attempt) {
    var headers = apiHeaders(status);
    if (!headers) return Promise.resolve([]);
    return fetch(getMgmtBase() + "/codex-refresh-auth-files", { headers: headers, credentials: "same-origin" })
      .then(function (r) {
        if (r.status === 401 && attempt < 8) {
          return new Promise(function (resolve) {
            setTimeout(function () { resolve(fetchAuthFilesAttempt(status, attempt + 1)); }, 500 + attempt * 250);
          });
        }
        if (!r.ok) throw new Error("HTTP " + r.status);
        return r.json();
      })
      .then(function (data) {
        var files = data && data.files && data.files.slice ? data.files : [];
        authFilesCache = files;
        authFilesCacheAt = Date.now();
        authFilesPending = null;
        return files;
      })
      .catch(function (err) {
        authFilesPending = null;
        if (status) status.textContent = "Auth files request failed: " + err;
        return authFilesCache || [];
      });
  }

  function fileMatchValues(file) {
    var keys = ["auth_index", "name", "email", "label", "account", "id", "project_id"];
    var values = [];
    for (var i = 0; i < keys.length; i++) {
      var value = normalizeValue(file && file[keys[i]]);
      if (value && value.length >= 3) values.push(value);
    }
    return values;
  }

  function isVisibleElement(el) {
    if (!el || !el.getBoundingClientRect) return false;
    var rect = el.getBoundingClientRect();
    if (!rect || rect.width === 0 || rect.height === 0) return false;
    var style = window.getComputedStyle ? window.getComputedStyle(el) : null;
    return !(style && (style.display === "none" || style.visibility === "hidden"));
  }

  function rowMatchesAuthFile(row, file) {
    if (!row || !file || !isVisibleElement(row)) return false;
    var text = normalizeValue(row.innerText || row.textContent || "");
    if (!text || text.length > 5000) return false;
    var provider = normalizeValue(file.provider || file.type);
    if (provider && text.indexOf(provider) === -1 && text.indexOf("codex") === -1) return false;
    var values = fileMatchValues(file);
    for (var i = 0; i < values.length; i++) {
      if (text.indexOf(values[i]) !== -1) return true;
    }
    return false;
  }

  function candidateAuthRows() {
    var selectors = ["tr", "[role='row']", "li", "article", "section", ".card", ".panel", "[class*='card']", "[class*='row']", "[class*='item']"];
    var seen = [];
    var rows = [];
    var section = findAuthSection();
    var scopes = section ? [section, document] : [document];
    for (var scopeIndex = 0; scopeIndex < scopes.length; scopeIndex++) {
      var scope = scopes[scopeIndex];
      for (var i = 0; i < selectors.length; i++) {
        var nodes = scope.querySelectorAll(selectors[i]);
        for (var j = 0; j < nodes.length; j++) {
          var node = nodes[j];
          if (seen.indexOf(node) !== -1 || node.id === "codex-free-refresh-wrapper") continue;
          seen.push(node);
          if (isVisibleElement(node)) rows.push(node);
        }
      }
    }
    return rows;
  }

  function findRowForAuthFile(file, rows, used) {
    var best = null;
    var bestArea = Infinity;
    for (var i = 0; i < rows.length; i++) {
      var row = rows[i];
      if (used.indexOf(row) !== -1 || !rowMatchesAuthFile(row, file)) continue;
      var rect = row.getBoundingClientRect();
      var area = rect.width * rect.height;
      if (area > 0 && area < bestArea) {
        best = row;
        bestArea = area;
      }
    }
    return best;
  }

  function singleStatusText(data) {
    if (!data) return "Waiting...";
    var lines = [];
    lines.push("Progress: " + (data.done || 0) + "/" + (data.total || 1) + " | Success: " + (data.success || 0) + " | Failed: " + (data.failed || 0));
    if (data.results && data.results.length > 0) {
      for (var i = 0; i < data.results.length; i++) {
        var r = data.results[i];
        var icon = r.success ? "✓" : "✗";
        var detail = r.success ? "" : " (" + (r.error || "unknown") + ")";
        lines.push(icon + " " + (r.email || r.name || "?") + detail);
      }
    }
    return lines.join("\n");
  }

  function pollSingleStatus(taskId, btn, status) {
    var interval = setInterval(function () {
      if (!document.body.contains(btn) || !document.body.contains(status)) {
        clearInterval(interval);
        return;
      }
      var headers = apiHeaders(status);
      if (!headers) {
        clearInterval(interval);
        btn.disabled = false;
        btn.textContent = "Refresh";
        return;
      }
      fetch(getMgmtBase() + "/codex-free-refresh/" + encodeURIComponent(taskId), { headers: headers, credentials: "same-origin" })
        .then(function (r) {
          if (!r.ok) throw new Error("HTTP " + r.status);
          return r.json();
        })
        .then(function (data) {
          if (data.error) {
            clearInterval(interval);
            status.textContent = "Error: " + data.error;
            btn.disabled = false;
            btn.textContent = "Refresh";
            return;
          }
          status.textContent = singleStatusText(data);
          if (data.status === "completed") {
            clearInterval(interval);
            btn.disabled = false;
            btn.textContent = "Refresh";
          }
        })
        .catch(function (err) {
          clearInterval(interval);
          status.textContent = "Poll failed: " + err;
          btn.disabled = false;
          btn.textContent = "Refresh";
        });
    }, 2000);
  }

  function startSingleRefresh(file, btn, status) {
    if (!file || !file.auth_index || !btn || !status) return;
    btn.disabled = true;
    btn.textContent = "Starting...";
    status.textContent = "";
    var headers = apiHeaders(status);
    if (!headers) {
      btn.disabled = false;
      btn.textContent = "Refresh";
      return;
    }
    fetch(getMgmtBase() + "/codex-free-refresh", {
      method: "POST",
      headers: headers,
      credentials: "same-origin",
      body: JSON.stringify({ auth_index: file.auth_index })
    })
      .then(function (r) {
          if (!r.ok) throw new Error("HTTP " + r.status);
          return r.json();
        })
      .then(function (data) {
        if (data.error) {
          status.textContent = "Error: " + data.error;
          btn.disabled = false;
          btn.textContent = "Refresh";
          return;
        }
        if (!data.task_id) {
          status.textContent = "No refresh task was started.";
          btn.disabled = false;
          btn.textContent = "Refresh";
          return;
        }
        status.textContent = "Started. Polling...";
        pollSingleStatus(data.task_id, btn, status);
      })
      .catch(function (err) {
        status.textContent = "Request failed: " + err;
        btn.disabled = false;
        btn.textContent = "Refresh";
      });
  }

  function createSingleRefreshButton(file, status) {
    var btn = document.createElement("button");
    btn.className = "codex-single-refresh-btn";
    btn.type = "button";
    btn.textContent = "Refresh";
    btn.style.cssText = "margin:4px;padding:3px 8px;border:1px solid #475569;border-radius:5px;background:#1e293b;color:#e2e8f0;cursor:pointer;font-size:12px;line-height:1.2;";
    btn.onmouseenter = function () { if (!btn.disabled) btn.style.background = "#334155"; };
    btn.onmouseleave = function () { btn.style.background = "#1e293b"; };
    btn.onclick = function (event) {
      if (event && event.preventDefault) event.preventDefault();
      if (event && event.stopPropagation) event.stopPropagation();
      startSingleRefresh(file, btn, status);
    };
    return btn;
  }

  function attachSingleRefresh(row, file) {
    if (!row || !file || !file.auth_index) return false;
    var existing = row.querySelector ? row.querySelector(".codex-single-refresh-wrapper[data-auth-index='" + String(file.auth_index).replace(/'/g, "\\'") + "']") : null;
    if (existing) return false;
    var holder = document.createElement("span");
    holder.className = "codex-single-refresh-wrapper";
    holder.style.cssText = "display:inline-flex;align-items:center;gap:4px;flex-wrap:wrap;margin-left:4px;";
    holder.setAttribute("data-auth-index", file.auth_index);
    var checkbox = document.createElement("input");
    checkbox.type = "checkbox";
    checkbox.className = "codex-refresh-checkbox";
    checkbox.setAttribute("data-auth-index", file.auth_index);
    checkbox.checked = true;
    checkbox.style.cssText = "margin:0 2px;cursor:pointer;";
    var status = document.createElement("span");
    status.className = "codex-single-refresh-status";
    status.style.cssText = "font-size:11px;color:#94a3b8;white-space:pre-wrap;margin-left:2px;";
    var btn = createSingleRefreshButton(file, status);
    btn.setAttribute("data-auth-index", file.auth_index);
    holder.appendChild(checkbox);
    holder.appendChild(btn);
    holder.appendChild(status);
    if (row.tagName && row.tagName.toLowerCase() === "tr") {
      var cell = document.createElement("td");
      cell.appendChild(holder);
      row.appendChild(cell);
    } else {
      row.appendChild(holder);
    }
    return true;
  }

  function injectSingleRefreshButtons() {
    if (!isAuthRoute()) return;
    var status = document.getElementById("codex-free-refresh-status");
    getAuthFiles(status).then(function (files) {
      if (!isAuthRoute()) { removeInjectedUI(); return; }
      var codexFiles = [];
      for (var i = 0; i < files.length; i++) {
        if (isCodexAuthFile(files[i]) && files[i].auth_index) codexFiles.push(files[i]);
      }
      if (codexFiles.length === 0) return;
      var rows = candidateAuthRows();
      var used = [];
      for (var j = 0; j < codexFiles.length; j++) {
        var row = findRowForAuthFile(codexFiles[j], rows, used);
        if (!row) continue;
        if (attachSingleRefresh(row, codexFiles[j])) used.push(row);
      }
    });
  }

  function removeInjectedUI() {
    var wrapper = document.getElementById("codex-free-refresh-wrapper");
    if (wrapper && wrapper.parentElement) wrapper.parentElement.removeChild(wrapper);
    var singles = document.querySelectorAll(".codex-single-refresh-wrapper");
    for (var i = 0; i < singles.length; i++) {
      if (singles[i].parentElement) singles[i].parentElement.removeChild(singles[i]);
    }
  }

  function createSelectAllBtn() {
    var btn = document.createElement("button");
    btn.id = "codex-select-all-btn";
    btn.textContent = "Select All";
    btn.type = "button";
    btn.style.cssText = "margin:8px 0 8px 6px;padding:6px 10px;border:1px solid #475569;border-radius:6px;background:#1e293b;color:#e2e8f0;cursor:pointer;font-size:12px;";
    btn.onmouseenter = function () { btn.style.background = "#334155"; };
    btn.onmouseleave = function () { btn.style.background = "#1e293b"; };
    btn.onclick = function () {
      var checkboxes = document.querySelectorAll(".codex-refresh-checkbox");
      var allChecked = true;
      for (var i = 0; i < checkboxes.length; i++) {
        if (!checkboxes[i].checked) { allChecked = false; break; }
      }
      for (var j = 0; j < checkboxes.length; j++) {
        checkboxes[j].checked = !allChecked;
      }
      btn.textContent = allChecked ? "Select All" : "Deselect All";
    };
    return btn;
  }

  function injectUI() {
    if (!isAuthRoute()) { removeInjectedUI(); return; }
    if (document.getElementById("codex-free-refresh-btn")) return;

    var section = findAuthSection();
    var target = section || document.body;
    if (!target) return;

    var wrapper = document.createElement("div");
    wrapper.id = "codex-free-refresh-wrapper";
    wrapper.style.cssText = "display:flex;align-items:center;flex-wrap:wrap;gap:4px;";
    wrapper.appendChild(createButton());
    wrapper.appendChild(createSelectAllBtn());
    var statusEl = createStatusEl();
    statusEl.style.marginTop = "4px";
    wrapper.appendChild(statusEl);
    target.insertBefore(wrapper, target.firstChild);
    injectSingleRefreshButtons();
  }

  var observer = null;
  var scheduled = false;

  function stopObserver() {
    if (!observer) return;
    observer.disconnect();
    observer = null;
  }

  function scheduleAuthPatch() {
    if (scheduled) return;
    scheduled = true;
    setTimeout(function () {
      scheduled = false;
      if (!isAuthRoute()) { removeInjectedUI(); return; }
      injectUI();
      injectSingleRefreshButtons();
    }, 80);
  }

  function setupObserver() {
    if (observer || !window.MutationObserver || !document.body) return;
    observer = new MutationObserver(function () {
      scheduleAuthPatch();
    });
    observer.observe(document.body, { childList: true, subtree: true });
  }

  function handleRouteChange() {
    setupObserver();
    scheduleAuthPatch();
    setTimeout(scheduleAuthPatch, 300);
    setTimeout(scheduleAuthPatch, 1200);
    setTimeout(scheduleAuthPatch, 2500);
  }

  window.addEventListener("hashchange", handleRouteChange, true);
  window.addEventListener("popstate", handleRouteChange, true);
  if (window.history) {
    ["pushState", "replaceState"].forEach(function (name) {
      var original = window.history[name];
      if (typeof original !== "function") return;
      window.history[name] = function () {
        var result = original.apply(this, arguments);
        setTimeout(handleRouteChange, 0);
        return result;
      };
    });
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", function () { handleRouteChange(); }, { once: true });
  } else {
    handleRouteChange();
  }
})();
</script>`)

	patch = bytes.ReplaceAll(patch, []byte("__CPA_CODEX_REFRESH_TOKEN__"), []byte(codexRefreshToken))

	lower := bytes.ToLower(html)
	bodyClose := []byte("</body>")
	if idx := bytes.LastIndex(lower, bodyClose); idx >= 0 {
		out := make([]byte, 0, len(html)+len(patch))
		out = append(out, html[:idx]...)
		out = append(out, patch...)
		out = append(out, html[idx:]...)
		return out
	}
	return append(html, patch...)
}

func injectXAIRefreshPatch(html []byte, xaiRefreshToken string) []byte {
	const marker = "__cpa_xai_refresh_patch__"
	if len(html) == 0 || bytes.Contains(html, []byte(marker)) {
		return html
	}

	patch := []byte(`<script>
(function () {
  var MARKER = "__cpa_xai_refresh_patch__";
  if (window[MARKER]) return;
  window[MARKER] = true;
  var XAI_REFRESH_TOKEN = "__CPA_XAI_REFRESH_TOKEN__";

  function normalizeText(value) {
    return String(value == null ? "" : value).toLowerCase().replace(/\s+/g, " ").trim();
  }

  function decodeRoutePart(value) {
    var text = String(value == null ? "" : value);
    try {
      return decodeURIComponent(text);
    } catch (err) {
      return text;
    }
  }

  function normalizeRouteText(value) {
    return normalizeText(decodeRoutePart(value)).replace(/[\-_]+/g, " ");
  }

  function routeHasToken(text, token) {
    if (!text || !token) return false;
    if (text.indexOf(token) === -1) return false;
    if (token.charAt(0) === "/") return true;
    var idx = text.indexOf(token);
    while (idx !== -1) {
      var before = idx === 0 ? "" : text.charAt(idx - 1);
      var after = idx + token.length >= text.length ? "" : text.charAt(idx + token.length);
      if ((!before || /[\s/#?&=.]/.test(before)) && (!after || /[\s/#?&=.]/.test(after))) return true;
      idx = text.indexOf(token, idx + token.length);
    }
    return false;
  }

  function isAuthRoute() {
    var rawLocationText = (window.location.hash || "") + " " + (window.location.pathname || "") + " " + (window.location.search || "");
    var locationText = normalizeRouteText(rawLocationText);
    var routeTokens = ["auth files", "/auth", "认证文件", "凭证"];
    for (var i = 0; i < routeTokens.length; i++) {
      if (routeHasToken(locationText, normalizeRouteText(routeTokens[i]))) return true;
    }
    return activeAuthRouteFromNavigation() || !!findAuthSection();
  }

  function activeAuthRouteFromNavigation() {
    var selectors = ["[aria-current='page']", "[aria-selected='true']", "[data-state='active']", "[role='tab']", "nav [class*='active']", "aside [class*='active']", "[role='navigation'] [class*='active']"];
    for (var i = 0; i < selectors.length; i++) {
      var nodes = document.querySelectorAll(selectors[i]);
      for (var j = 0; j < nodes.length; j++) {
        var node = nodes[j];
        if (!isLayoutChrome(node) && !(node.getAttribute && normalizeText(node.getAttribute("role")) === "tab")) continue;
        var text = normalizeRouteText((node.innerText || node.textContent || "") + " " + (node.getAttribute ? (node.getAttribute("aria-label") || node.getAttribute("title") || "") : ""));
        if (routeHasToken(text, "auth files") || routeHasToken(text, "认证文件") || routeHasToken(text, "凭证")) return true;
      }
    }
    return false;
  }

  function isLayoutChrome(node) {
    var current = node;
    for (var i = 0; i < 6 && current; i++) {
      if (current.matches && current.matches("nav,aside,header,footer,[role='navigation'],[class*='sidebar'],[class*='menu']")) return true;
      current = current.parentElement;
    }
    return false;
  }

  function findAuthSection() {
    var roots = document.querySelectorAll("main,[role='main'],[class*='content'],[class*='page']");
    var titleSelectors = "h1,h2,h3,[role='heading'],[data-page-title],[class*='title']";
    for (var r = 0; r < roots.length; r++) {
      var root = roots[r];
      if (isLayoutChrome(root)) continue;
      var titles = root.querySelectorAll(titleSelectors);
      for (var i = 0; i < titles.length; i++) {
        if (isLayoutChrome(titles[i])) continue;
        var title = normalizeText(titles[i].innerText || titles[i].textContent || "");
        if (title.indexOf("auth files") === -1 && title.indexOf("认证文件") === -1 && title.indexOf("凭证") === -1) continue;
        var container = titles[i];
        for (var depth = 0; depth < 5 && container && container !== root; depth++) {
          if (container.matches && container.matches("section,article,.card,.panel,[class*='card'],[class*='panel'],[class*='page']")) break;
          container = container.parentElement;
        }
        return container || root;
      }
    }

    var selectors = ["main [class*='auth']", "main [id*='auth']", "main section", "main .card", "main .panel", "[role='main'] [class*='auth']", "[role='main'] [id*='auth']", "[role='main'] section", "[role='main'] .card", "[role='main'] .panel", "[class*='auth']", "[id*='auth']", "section", ".card", ".panel"];
    for (var s = 0; s < selectors.length; s++) {
      var nodes = document.querySelectorAll(selectors[s]);
      for (var j = 0; j < nodes.length; j++) {
        var node = nodes[j];
        if (isLayoutChrome(node)) continue;
        var text = normalizeText(node.innerText || node.textContent || "");
        if ((text.indexOf("xai") !== -1 || text.indexOf("auth") !== -1 || text.indexOf("认证文件") !== -1 || text.indexOf("凭证") !== -1) && (text.indexOf("auth") !== -1 || text.indexOf("认证文件") !== -1 || text.indexOf("凭证") !== -1 || text.indexOf("provider") !== -1)) {
          return node;
        }
      }
    }
    return null;
  }

  function createButton() {
    var btn = document.createElement("button");
    btn.id = "xai-free-refresh-btn";
    btn.textContent = "⟳ Refresh xAI Accounts";
    btn.style.cssText = "margin:8px 0;padding:6px 14px;border:1px solid #475569;border-radius:6px;background:#1e293b;color:#e2e8f0;cursor:pointer;font-size:13px;";
    btn.onmouseenter = function () { btn.style.background = "#334155"; };
    btn.onmouseleave = function () { btn.style.background = "#1e293b"; };
    btn.onclick = startRefresh;
    return btn;
  }

  function createStatusEl() {
    var el = document.createElement("div");
    el.id = "xai-free-refresh-status";
    el.style.cssText = "margin:4px 0 8px;font-size:12px;color:#94a3b8;white-space:pre-wrap;";
    return el;
  }

  function getMgmtBase() {
    var scripts = document.querySelectorAll("script[src]");
    for (var i = 0; i < scripts.length; i++) {
      var src = scripts[i].src || "";
      var idx = src.indexOf("/management.");
      if (idx !== -1) {
        return src.substring(0, src.lastIndexOf("/"));
      }
    }
    return "/v0/management";
  }

  function apiHeaders(status) {
    return {
      "Content-Type": "application/json",
      "X-XAI-Refresh-Token": XAI_REFRESH_TOKEN
    };
  }

  function getSelectedXAIAuthIndices() {
    var checkboxes = document.querySelectorAll(".xai-refresh-checkbox:checked");
    var indices = [];
    for (var i = 0; i < checkboxes.length; i++) {
      var idx = checkboxes[i].getAttribute("data-auth-index");
      if (idx) indices.push(idx);
    }
    return indices;
  }

  function startRefresh() {
    var btn = document.getElementById("xai-free-refresh-btn");
    var status = document.getElementById("xai-free-refresh-status");
    if (!btn || !status) return;

    var selectedIndices = getSelectedXAIAuthIndices();
    var body = {};
    if (selectedIndices.length > 0) {
      body.auth_indices = selectedIndices;
    }

    btn.disabled = true;
    btn.textContent = "Starting...";
    status.textContent = "";

    var headers = apiHeaders(status);
    if (!headers) {
      btn.disabled = false;
      btn.textContent = "⟳ Refresh xAI Accounts";
      return;
    }

    fetch(getMgmtBase() + "/xai-free-refresh", { method: "POST", headers: headers, credentials: "same-origin", body: JSON.stringify(body) })
      .then(function (r) {
          if (!r.ok) throw new Error("HTTP " + r.status);
          return r.json();
        })
      .then(function (data) {
        if (data.error) {
          status.textContent = "Error: " + data.error;
          btn.disabled = false;
          btn.textContent = "⟳ Refresh xAI Accounts";
          return;
        }
        if (data.total === 0) {
          status.textContent = "No xAI accounts found.";
          btn.disabled = false;
          btn.textContent = "⟳ Refresh xAI Accounts";
          return;
        }
        status.textContent = "Started: " + data.total + " accounts. Polling...";
        pollStatus(data.task_id, data.total);
      })
      .catch(function (err) {
        status.textContent = "Request failed: " + err;
        btn.disabled = false;
        btn.textContent = "⟳ Refresh xAI Accounts";
      });
  }

  function pollStatus(taskId, total) {
    var btn = document.getElementById("xai-free-refresh-btn");
    var status = document.getElementById("xai-free-refresh-status");
    if (!btn || !status) return;

    var interval = setInterval(function () {
      btn = document.getElementById("xai-free-refresh-btn");
      status = document.getElementById("xai-free-refresh-status");
      if (!btn || !status) {
        clearInterval(interval);
        return;
      }
      var headers = apiHeaders(status);
      if (!headers) {
        clearInterval(interval);
        btn.disabled = false;
        btn.textContent = "⟳ Refresh xAI Accounts";
        return;
      }
      fetch(getMgmtBase() + "/xai-free-refresh/" + encodeURIComponent(taskId), { headers: headers, credentials: "same-origin" })
        .then(function (r) {
          if (!r.ok) throw new Error("HTTP " + r.status);
          return r.json();
        })
        .then(function (data) {
          if (data.error) {
            clearInterval(interval);
            status.textContent = "Error: " + data.error;
            btn.disabled = false;
            btn.textContent = "⟳ Refresh xAI Accounts";
            return;
          }
          var lines = [];
          lines.push("Progress: " + data.done + "/" + data.total + " | Success: " + data.success + " | Failed: " + data.failed);
          if (data.results && data.results.length > 0) {
            for (var i = 0; i < data.results.length; i++) {
              var r = data.results[i];
              var icon = r.success ? "✓" : "✗";
              var detail = r.success ? "" : " (" + (r.error || "unknown") + ")";
              lines.push("  " + icon + " " + (r.email || r.name || "?") + detail);
              // Sync result to the corresponding card's status element.
              var cardStatus = findCardStatusForResult(r);
              if (cardStatus) {
                cardStatus.textContent = r.success ? "✓ Refreshed" : "✗ " + (r.error || "failed");
                cardStatus.style.color = r.success ? "#4ade80" : "#f87171";
                var wrapper = cardStatus.parentElement;
                if (wrapper) {
                  var cardBtn = wrapper.querySelector(".xai-single-refresh-btn");
                  if (cardBtn) {
                    cardBtn.disabled = false;
                    cardBtn.textContent = "Refresh";
                  }
                }
              }
            }
          }
          status.textContent = lines.join("\n");

          if (data.status === "completed") {
            clearInterval(interval);
            btn.disabled = false;
            btn.textContent = "⟳ Refresh xAI Accounts";
          }
        })
        .catch(function (err) {
          clearInterval(interval);
          status.textContent = "Poll failed: " + err;
          btn.disabled = false;
          btn.textContent = "⟳ Refresh xAI Accounts";
        });
    }, 2000);
  }

  var authFilesCache = null;
  var authFilesCacheAt = 0;
  var authFilesPending = null;
  var xaiAuthIndexByName = {};

  function normalizeValue(value) {
    return String(value == null ? "" : value).toLowerCase().replace(/\s+/g, " ").trim();
  }

  function isXAIAuthFile(file) {
    if (!file) return false;
    return normalizeValue(file.provider) === "xai" || normalizeValue(file.type) === "xai";
  }

  function buildXAIAuthIndexMap(files) {
    xaiAuthIndexByName = {};
    for (var i = 0; i < files.length; i++) {
      var f = files[i];
      if (!f || !f.auth_index) continue;
      if (f.name) xaiAuthIndexByName[normalizeValue(f.name)] = f.auth_index;
      if (f.email) xaiAuthIndexByName[normalizeValue(f.email)] = f.auth_index;
      if (f.label) xaiAuthIndexByName[normalizeValue(f.label)] = f.auth_index;
    }
  }

  function findCardStatusForResult(result) {
    if (!result) return null;
    var candidates = [result.name, result.email];
    for (var i = 0; i < candidates.length; i++) {
      var key = normalizeValue(candidates[i]);
      if (!key) continue;
      var authIndex = xaiAuthIndexByName[key];
      if (!authIndex) continue;
      var el = document.querySelector(".xai-single-refresh-status[data-auth-index='" + String(authIndex).replace(/'/g, "\\'") + "']");
      if (el) return el;
      var wrapper = document.querySelector(".xai-single-refresh-wrapper[data-auth-index='" + String(authIndex).replace(/'/g, "\\'") + "']");
      if (wrapper) {
        var span = wrapper.querySelector(".xai-single-refresh-status");
        if (span) return span;
      }
    }
    return null;
  }

  function getAuthFiles(status) {
    var now = Date.now();
    if (authFilesCache && now - authFilesCacheAt < 10000) return Promise.resolve(authFilesCache);
    if (authFilesPending) return authFilesPending;
    authFilesPending = fetchAuthFilesAttempt(status, 0);
    return authFilesPending;
  }

  function fetchAuthFilesAttempt(status, attempt) {
    var headers = apiHeaders(status);
    if (!headers) return Promise.resolve([]);
    return fetch(getMgmtBase() + "/xai-refresh-auth-files", { headers: headers, credentials: "same-origin" })
      .then(function (r) {
        if (r.status === 401 && attempt < 8) {
          return new Promise(function (resolve) {
            setTimeout(function () { resolve(fetchAuthFilesAttempt(status, attempt + 1)); }, 500 + attempt * 250);
          });
        }
        if (!r.ok) throw new Error("HTTP " + r.status);
        return r.json();
      })
      .then(function (data) {
        var files = data && data.files && data.files.slice ? data.files : [];
        authFilesCache = files;
        authFilesCacheAt = Date.now();
        authFilesPending = null;
        return files;
      })
      .catch(function (err) {
        authFilesPending = null;
        if (status) status.textContent = "Auth files request failed: " + err;
        return authFilesCache || [];
      });
  }

  function fileMatchValues(file) {
    var keys = ["auth_index", "name", "email", "label", "account", "id", "project_id"];
    var values = [];
    for (var i = 0; i < keys.length; i++) {
      var value = normalizeValue(file && file[keys[i]]);
      if (value && value.length >= 3) values.push(value);
    }
    return values;
  }

  function isVisibleElement(el) {
    if (!el || !el.getBoundingClientRect) return false;
    var rect = el.getBoundingClientRect();
    if (!rect || rect.width === 0 || rect.height === 0) return false;
    var style = window.getComputedStyle ? window.getComputedStyle(el) : null;
    return !(style && (style.display === "none" || style.visibility === "hidden"));
  }

  function rowMatchesAuthFile(row, file) {
    if (!row || !file || !isVisibleElement(row)) return false;
    var text = normalizeValue(row.innerText || row.textContent || "");
    if (!text || text.length > 5000) return false;
    var provider = normalizeValue(file.provider || file.type);
    if (provider && text.indexOf(provider) === -1 && text.indexOf("xai") === -1) return false;
    var values = fileMatchValues(file);
    for (var i = 0; i < values.length; i++) {
      if (text.indexOf(values[i]) !== -1) return true;
    }
    return false;
  }

  function candidateAuthRows() {
    var selectors = ["tr", "[role='row']", "li", "article", "section", ".card", ".panel", "[class*='card']", "[class*='row']", "[class*='item']"];
    var seen = [];
    var rows = [];
    var section = findAuthSection();
    var scopes = section ? [section, document] : [document];
    for (var scopeIndex = 0; scopeIndex < scopes.length; scopeIndex++) {
      var scope = scopes[scopeIndex];
      for (var i = 0; i < selectors.length; i++) {
        var nodes = scope.querySelectorAll(selectors[i]);
        for (var j = 0; j < nodes.length; j++) {
          var node = nodes[j];
          if (seen.indexOf(node) !== -1 || node.id === "xai-free-refresh-wrapper") continue;
          seen.push(node);
          if (isVisibleElement(node)) rows.push(node);
        }
      }
    }
    return rows;
  }

  function findRowForAuthFile(file, rows, used) {
    var best = null;
    var bestArea = Infinity;
    for (var i = 0; i < rows.length; i++) {
      var row = rows[i];
      if (used.indexOf(row) !== -1 || !rowMatchesAuthFile(row, file)) continue;
      var rect = row.getBoundingClientRect();
      var area = rect.width * rect.height;
      if (area > 0 && area < bestArea) {
        best = row;
        bestArea = area;
      }
    }
    return best;
  }

  function singleStatusText(data) {
    if (!data) return "Waiting...";
    var lines = [];
    lines.push("Progress: " + (data.done || 0) + "/" + (data.total || 1) + " | Success: " + (data.success || 0) + " | Failed: " + (data.failed || 0));
    if (data.results && data.results.length > 0) {
      for (var i = 0; i < data.results.length; i++) {
        var r = data.results[i];
        var icon = r.success ? "✓" : "✗";
        var detail = r.success ? "" : " (" + (r.error || "unknown") + ")";
        lines.push(icon + " " + (r.email || r.name || "?") + detail);
      }
    }
    return lines.join("\n");
  }

  function pollSingleStatus(taskId, btn, status) {
    var interval = setInterval(function () {
      if (!document.body.contains(btn) || !document.body.contains(status)) {
        clearInterval(interval);
        return;
      }
      var headers = apiHeaders(status);
      if (!headers) {
        clearInterval(interval);
        btn.disabled = false;
        btn.textContent = "Refresh";
        return;
      }
      fetch(getMgmtBase() + "/xai-free-refresh/" + encodeURIComponent(taskId), { headers: headers, credentials: "same-origin" })
        .then(function (r) {
          if (!r.ok) throw new Error("HTTP " + r.status);
          return r.json();
        })
        .then(function (data) {
          if (data.error) {
            clearInterval(interval);
            status.textContent = "Error: " + data.error;
            btn.disabled = false;
            btn.textContent = "Refresh";
            return;
          }
          status.textContent = singleStatusText(data);
          if (data.status === "completed") {
            clearInterval(interval);
            btn.disabled = false;
            btn.textContent = "Refresh";
          }
        })
        .catch(function (err) {
          clearInterval(interval);
          status.textContent = "Poll failed: " + err;
          btn.disabled = false;
          btn.textContent = "Refresh";
        });
    }, 2000);
  }

  function startSingleRefresh(file, btn, status) {
    if (!file || !file.auth_index || !btn || !status) return;
    btn.disabled = true;
    btn.textContent = "Starting...";
    status.textContent = "";
    var headers = apiHeaders(status);
    if (!headers) {
      btn.disabled = false;
      btn.textContent = "Refresh";
      return;
    }
    fetch(getMgmtBase() + "/xai-free-refresh", {
      method: "POST",
      headers: headers,
      credentials: "same-origin",
      body: JSON.stringify({ auth_index: file.auth_index })
    })
      .then(function (r) {
          if (!r.ok) throw new Error("HTTP " + r.status);
          return r.json();
        })
      .then(function (data) {
        if (data.error) {
          status.textContent = "Error: " + data.error;
          btn.disabled = false;
          btn.textContent = "Refresh";
          return;
        }
        if (!data.task_id) {
          status.textContent = "No refresh task was started.";
          btn.disabled = false;
          btn.textContent = "Refresh";
          return;
        }
        status.textContent = "Started. Polling...";
        pollSingleStatus(data.task_id, btn, status);
      })
      .catch(function (err) {
        status.textContent = "Request failed: " + err;
        btn.disabled = false;
        btn.textContent = "Refresh";
      });
  }

  function createSingleRefreshButton(file, status) {
    var btn = document.createElement("button");
    btn.className = "xai-single-refresh-btn";
    btn.type = "button";
    btn.textContent = "Refresh";
    btn.style.cssText = "margin:4px;padding:3px 8px;border:1px solid #475569;border-radius:5px;background:#1e293b;color:#e2e8f0;cursor:pointer;font-size:12px;line-height:1.2;";
    btn.onmouseenter = function () { if (!btn.disabled) btn.style.background = "#334155"; };
    btn.onmouseleave = function () { btn.style.background = "#1e293b"; };
    btn.onclick = function (event) {
      if (event && event.preventDefault) event.preventDefault();
      if (event && event.stopPropagation) event.stopPropagation();
      startSingleRefresh(file, btn, status);
    };
    return btn;
  }

  function attachSingleRefresh(row, file) {
    if (!row || !file || !file.auth_index) return false;
    var existing = row.querySelector ? row.querySelector(".xai-single-refresh-wrapper[data-auth-index='" + String(file.auth_index).replace(/'/g, "\\'") + "']") : null;
    if (existing) return false;
    var holder = document.createElement("span");
    holder.className = "xai-single-refresh-wrapper";
    holder.style.cssText = "display:inline-flex;align-items:center;gap:4px;flex-wrap:wrap;margin-left:4px;";
    holder.setAttribute("data-auth-index", file.auth_index);
    var checkbox = document.createElement("input");
    checkbox.type = "checkbox";
    checkbox.className = "xai-refresh-checkbox";
    checkbox.setAttribute("data-auth-index", file.auth_index);
    checkbox.checked = true;
    checkbox.style.cssText = "margin:0 2px;cursor:pointer;";
    var status = document.createElement("span");
    status.className = "xai-single-refresh-status";
    status.style.cssText = "font-size:11px;color:#94a3b8;white-space:pre-wrap;margin-left:2px;";
    var btn = createSingleRefreshButton(file, status);
    btn.setAttribute("data-auth-index", file.auth_index);
    holder.appendChild(checkbox);
    holder.appendChild(btn);
    holder.appendChild(status);
    if (row.tagName && row.tagName.toLowerCase() === "tr") {
      var cell = document.createElement("td");
      cell.appendChild(holder);
      row.appendChild(cell);
    } else {
      row.appendChild(holder);
    }
    return true;
  }

  function injectSingleRefreshButtons() {
    if (!isAuthRoute()) return;
    var status = document.getElementById("xai-free-refresh-status");
    getAuthFiles(status).then(function (files) {
      if (!isAuthRoute()) { removeInjectedUI(); return; }
      var xaiFiles = [];
      for (var i = 0; i < files.length; i++) {
        if (isXAIAuthFile(files[i]) && files[i].auth_index) xaiFiles.push(files[i]);
      }
      if (xaiFiles.length === 0) return;
      buildXAIAuthIndexMap(xaiFiles);
      var rows = candidateAuthRows();
      var used = [];
      for (var j = 0; j < xaiFiles.length; j++) {
        var row = findRowForAuthFile(xaiFiles[j], rows, used);
        if (!row) continue;
        if (attachSingleRefresh(row, xaiFiles[j])) used.push(row);
      }
    });
  }

  function removeInjectedUI() {
    var wrapper = document.getElementById("xai-free-refresh-wrapper");
    if (wrapper && wrapper.parentElement) wrapper.parentElement.removeChild(wrapper);
    var singles = document.querySelectorAll(".xai-single-refresh-wrapper");
    for (var i = 0; i < singles.length; i++) {
      if (singles[i].parentElement) singles[i].parentElement.removeChild(singles[i]);
    }
  }

  function createSelectAllBtn() {
    var btn = document.createElement("button");
    btn.id = "xai-select-all-btn";
    btn.textContent = "Select All";
    btn.type = "button";
    btn.style.cssText = "margin:8px 0 8px 6px;padding:6px 10px;border:1px solid #475569;border-radius:6px;background:#1e293b;color:#e2e8f0;cursor:pointer;font-size:12px;";
    btn.onmouseenter = function () { btn.style.background = "#334155"; };
    btn.onmouseleave = function () { btn.style.background = "#1e293b"; };
    btn.onclick = function () {
      var checkboxes = document.querySelectorAll(".xai-refresh-checkbox");
      var allChecked = true;
      for (var i = 0; i < checkboxes.length; i++) {
        if (!checkboxes[i].checked) { allChecked = false; break; }
      }
      for (var j = 0; j < checkboxes.length; j++) {
        checkboxes[j].checked = !allChecked;
      }
      btn.textContent = allChecked ? "Select All" : "Deselect All";
    };
    return btn;
  }

  function injectUI() {
    if (!isAuthRoute()) { removeInjectedUI(); return; }
    if (document.getElementById("xai-free-refresh-btn")) return;

    var section = findAuthSection();
    var target = section || document.body;
    if (!target) return;

    var wrapper = document.createElement("div");
    wrapper.id = "xai-free-refresh-wrapper";
    wrapper.style.cssText = "display:flex;align-items:center;flex-wrap:wrap;gap:4px;";
    wrapper.appendChild(createButton());
    wrapper.appendChild(createSelectAllBtn());
    var statusEl = createStatusEl();
    statusEl.style.marginTop = "4px";
    wrapper.appendChild(statusEl);
    target.insertBefore(wrapper, target.firstChild);
    injectSingleRefreshButtons();
  }

  var observer = null;
  var scheduled = false;

  function stopObserver() {
    if (!observer) return;
    observer.disconnect();
    observer = null;
  }

  function scheduleAuthPatch() {
    if (scheduled) return;
    scheduled = true;
    setTimeout(function () {
      scheduled = false;
      if (!isAuthRoute()) { removeInjectedUI(); return; }
      injectUI();
      injectSingleRefreshButtons();
    }, 80);
  }

  function setupObserver() {
    if (observer || !window.MutationObserver || !document.body) return;
    observer = new MutationObserver(function () {
      scheduleAuthPatch();
    });
    observer.observe(document.body, { childList: true, subtree: true });
  }

  function handleRouteChange() {
    setupObserver();
    scheduleAuthPatch();
    setTimeout(scheduleAuthPatch, 300);
    setTimeout(scheduleAuthPatch, 1200);
    setTimeout(scheduleAuthPatch, 2500);
  }

  window.addEventListener("hashchange", handleRouteChange, true);
  window.addEventListener("popstate", handleRouteChange, true);

  if (window.history) {
    ["pushState", "replaceState"].forEach(function (name) {
      var original = window.history[name];
      if (typeof original !== "function") return;
      window.history[name] = function () {
        var result = original.apply(this, arguments);
        setTimeout(handleRouteChange, 0);
        return result;
      };
    });
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", function () { handleRouteChange(); }, { once: true });
  } else {
    handleRouteChange();
  }
})();
</script>`)

	patch = bytes.ReplaceAll(patch, []byte("__CPA_XAI_REFRESH_TOKEN__"), []byte(xaiRefreshToken))

	lower := bytes.ToLower(html)
	bodyClose := []byte("</body>")
	if idx := bytes.LastIndex(lower, bodyClose); idx >= 0 {
		out := make([]byte, 0, len(html)+len(patch))
		out = append(out, html[:idx]...)
		out = append(out, patch...)
		out = append(out, html[idx:]...)
		return out
	}
	return append(html, patch...)
}

func (s *Server) enableKeepAlive(timeout time.Duration, onTimeout func()) {
	if timeout <= 0 || onTimeout == nil {
		return
	}

	s.keepAliveEnabled = true
	s.keepAliveTimeout = timeout
	s.keepAliveOnTimeout = onTimeout
	s.keepAliveHeartbeat = make(chan struct{}, 1)
	s.keepAliveStop = make(chan struct{}, 1)

	s.engine.GET("/keep-alive", s.handleKeepAlive)

	go s.watchKeepAlive()
}

func (s *Server) handleKeepAlive(c *gin.Context) {
	if s.localPassword != "" {
		provided := strings.TrimSpace(c.GetHeader("Authorization"))
		if provided != "" {
			parts := strings.SplitN(provided, " ", 2)
			if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
				provided = parts[1]
			}
		}
		if provided == "" {
			provided = strings.TrimSpace(c.GetHeader("X-Local-Password"))
		}
		if subtle.ConstantTimeCompare([]byte(provided), []byte(s.localPassword)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid password"})
			return
		}
	}

	s.signalKeepAlive()
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (s *Server) signalKeepAlive() {
	if !s.keepAliveEnabled {
		return
	}
	select {
	case s.keepAliveHeartbeat <- struct{}{}:
	default:
	}
}

func (s *Server) watchKeepAlive() {
	if !s.keepAliveEnabled {
		return
	}

	timer := time.NewTimer(s.keepAliveTimeout)
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			log.Warnf("keep-alive endpoint idle for %s, shutting down", s.keepAliveTimeout)
			if s.keepAliveOnTimeout != nil {
				s.keepAliveOnTimeout()
			}
			return
		case <-s.keepAliveHeartbeat:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(s.keepAliveTimeout)
		case <-s.keepAliveStop:
			return
		}
	}
}

// isAnthropicModelsRequest reports whether a /v1/models request should be served in
// Anthropic format. Anthropic API clients send the Anthropic-Version header; Claude
// Code additionally uses a claude-cli User-Agent.
func isAnthropicModelsRequest(c *gin.Context) bool {
	if c.GetHeader("Anthropic-Version") != "" {
		return true
	}
	return strings.HasPrefix(c.GetHeader("User-Agent"), "claude-cli")
}

// unifiedModelsHandler creates a unified handler for the /v1/models endpoint
// that routes to different handlers based on the request.
// Anthropic API requests (Anthropic-Version header, or a claude-cli User-Agent)
// route to the Claude handler, otherwise they route to the OpenAI handler.
func (s *Server) unifiedModelsHandler(openaiHandler *openai.OpenAIAPIHandler, claudeHandler *claude.ClaudeCodeAPIHandler) gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, ok := c.Request.URL.Query()["client_version"]; ok {
			if s != nil && s.cfg != nil && s.cfg.Home.Enabled {
				s.handleHomeCodexClientModels(c)
				return
			}
			openaiHandler.OpenAIModels(c)
			return
		}

		if s != nil && s.cfg != nil && s.cfg.Home.Enabled {
			s.handleHomeModels(c)
			return
		}

		// Route to Claude handler for Anthropic API requests.
		if isAnthropicModelsRequest(c) {
			claudeHandler.ClaudeModels(c)
		} else {
			openaiHandler.OpenAIModels(c)
		}
	}
}

// handleHomeCodexClientModels builds the Codex client catalog from Home model IDs.
// Template metadata still comes from the local/remote codex_client_models catalog.
func (s *Server) handleHomeCodexClientModels(c *gin.Context) {
	entries, ok := s.loadHomeModelEntries(c)
	if !ok {
		return
	}

	models := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		model := map[string]any{
			"id":     entry.id,
			"object": "model",
		}
		if entry.created > 0 {
			model["created"] = entry.created
		}
		if entry.ownedBy != "" {
			model["owned_by"] = entry.ownedBy
		}
		if entry.displayName != "" {
			model["display_name"] = entry.displayName
			model["description"] = entry.displayName
		}
		models = append(models, model)
	}

	c.JSON(http.StatusOK, codexmodels.BuildResponse(models, nil, s.cfg.Codex.OptimizeMultiAgentV2))
}

func (s *Server) geminiModelsHandler(geminiHandler *gemini.GeminiAPIHandler) gin.HandlerFunc {
	return func(c *gin.Context) {
		if s != nil && s.cfg != nil && s.cfg.Home.Enabled {
			s.handleHomeGeminiModels(c)
			return
		}

		geminiHandler.GeminiModels(c)
	}
}

func (s *Server) geminiGetHandler(geminiHandler *gemini.GeminiAPIHandler) gin.HandlerFunc {
	return func(c *gin.Context) {
		if s != nil && s.cfg != nil && s.cfg.Home.Enabled {
			s.handleHomeGeminiModel(c)
			return
		}

		geminiHandler.GeminiGetHandler(c)
	}
}

type homeModelEntry struct {
	id                  string
	created             int64
	ownedBy             string
	displayName         string
	contextLength       int
	maxCompletionTokens int
}

func (s *Server) handleHomeModels(c *gin.Context) {
	entries, ok := s.loadHomeModelEntries(c)
	if !ok {
		return
	}

	isClaude := isAnthropicModelsRequest(c)

	if isClaude {
		c.JSON(http.StatusOK, claudemodels.BuildResponse(formatHomeClaudeModels(entries)))
		return
	}

	filtered := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		model := map[string]any{
			"id":     entry.id,
			"object": "model",
		}
		if entry.created > 0 {
			model["created"] = entry.created
		}
		if entry.ownedBy != "" {
			model["owned_by"] = entry.ownedBy
		}
		filtered = append(filtered, model)
	}
	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data":   filtered,
	})
}

func formatHomeClaudeModels(entries []homeModelEntry) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		out = append(out, formatHomeClaudeModel(entry))
	}
	return out
}

func formatHomeClaudeModel(entry homeModelEntry) map[string]any {
	displayName := entry.displayName
	if displayName == "" {
		displayName = entry.id
	}
	maxInput := entry.contextLength
	if maxInput <= 0 {
		maxInput = registry.DefaultClaudeMaxInputTokens
	}
	maxOutput := entry.maxCompletionTokens
	if maxOutput <= 0 {
		maxOutput = registry.DefaultClaudeMaxOutputTokens
	}
	model := map[string]any{
		"id":               entry.id,
		"object":           "model",
		"owned_by":         entry.ownedBy,
		"type":             "model",
		"display_name":     displayName,
		"max_input_tokens": maxInput,
		"max_tokens":       maxOutput,
	}
	if entry.created > 0 {
		model["created_at"] = time.Unix(entry.created, 0).UTC().Format(time.RFC3339)
	}
	return model
}

func (s *Server) handleHomeGeminiModels(c *gin.Context) {
	entries, ok := s.loadHomeModelEntries(c)
	if !ok {
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"models": formatHomeGeminiModels(entries),
	})
}

func (s *Server) handleHomeGeminiModel(c *gin.Context) {
	entries, ok := s.loadHomeModelEntries(c)
	if !ok {
		return
	}

	action := strings.TrimPrefix(c.Param("action"), "/")
	action = strings.TrimSpace(action)
	for _, entry := range entries {
		if homeGeminiModelMatches(entry, action) {
			c.JSON(http.StatusOK, formatHomeGeminiModel(entry))
			return
		}
	}

	c.JSON(http.StatusNotFound, handlers.ErrorResponse{
		Error: handlers.ErrorDetail{
			Message: "Not Found",
			Type:    "not_found",
		},
	})
}

func (s *Server) loadHomeModelEntries(c *gin.Context) ([]homeModelEntry, bool) {
	if s == nil || c == nil || c.Request == nil {
		return nil, false
	}
	client := home.Current()
	if client == nil {
		c.JSON(http.StatusServiceUnavailable, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "home control center unavailable",
				Type:    "server_error",
			},
		})
		return nil, false
	}

	raw, errGet := client.GetModels(c.Request.Context(), c.Request.Header, c.Request.URL.Query())
	if errGet != nil {
		c.JSON(http.StatusBadGateway, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: errGet.Error(),
				Type:    "server_error",
			},
		})
		return nil, false
	}

	if statusCode, ok := homeModelsAuthStatus(raw); ok {
		c.JSON(statusCode, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: homeModelsErrorMessage(raw),
				Type:    "authentication_error",
			},
		})
		return nil, false
	}

	entries, errDecode := decodeHomeModels(raw)
	if errDecode != nil {
		c.JSON(http.StatusBadGateway, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: errDecode.Error(),
				Type:    "server_error",
			},
		})
		return nil, false
	}

	return entries, true
}

func formatHomeGeminiModels(entries []homeModelEntry) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		out = append(out, formatHomeGeminiModel(entry))
	}
	return out
}

func formatHomeGeminiModel(entry homeModelEntry) map[string]any {
	name := entry.id
	if !strings.HasPrefix(name, "models/") {
		name = "models/" + name
	}
	displayName := entry.displayName
	if displayName == "" {
		displayName = entry.id
	}
	return map[string]any{
		"name":                       name,
		"displayName":                displayName,
		"description":                displayName,
		"supportedGenerationMethods": []string{"generateContent"},
	}
}

func homeGeminiModelMatches(entry homeModelEntry, action string) bool {
	id := strings.TrimSpace(entry.id)
	if id == "" || action == "" {
		return false
	}
	normalizedAction := strings.TrimPrefix(action, "models/")
	normalizedID := strings.TrimPrefix(id, "models/")
	return action == id || action == "models/"+id || normalizedAction == normalizedID
}

// homeModelsAuthStatus inspects a home models response for an authentication/error envelope.
// It returns the HTTP status code to surface (401 for credential issues, 502 otherwise)
// and true when the payload is an error response rather than model data.
func homeModelsAuthStatus(raw []byte) (int, bool) {
	errType := homeModelsErrorType(raw)
	if errType == "" {
		return 0, false
	}
	if errType == "no_credentials" || errType == "invalid_credential" {
		return http.StatusUnauthorized, true
	}
	return http.StatusBadGateway, true
}

func homeModelsErrorType(raw []byte) string {
	top, ok := unmarshalHomeModelsTopLevel(raw)
	if !ok {
		return ""
	}
	rawErr, exists := top["error"]
	if !exists {
		return ""
	}
	var errObj struct {
		Type string `json:"type"`
	}
	if errUnmarshal := json.Unmarshal(rawErr, &errObj); errUnmarshal != nil {
		return ""
	}
	return strings.TrimSpace(errObj.Type)
}

func homeModelsErrorMessage(raw []byte) string {
	top, ok := unmarshalHomeModelsTopLevel(raw)
	if !ok {
		return "home models request failed"
	}
	rawErr, exists := top["error"]
	if !exists {
		return "home models request failed"
	}
	var errObj struct {
		Message string `json:"message"`
	}
	if errUnmarshal := json.Unmarshal(rawErr, &errObj); errUnmarshal != nil {
		return "home models request failed"
	}
	if msg := strings.TrimSpace(errObj.Message); msg != "" {
		return msg
	}
	return "home models request failed"
}

func unmarshalHomeModelsTopLevel(raw []byte) (map[string]json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var top map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(raw, &top); errUnmarshal != nil {
		return nil, false
	}
	return top, true
}

func decodeHomeModels(raw []byte) ([]homeModelEntry, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("home models payload is empty")
	}

	var bySection map[string][]map[string]any
	if err := json.Unmarshal(raw, &bySection); err != nil {
		return nil, fmt.Errorf("parse home models payload: %w", err)
	}
	if len(bySection) == 0 {
		return nil, fmt.Errorf("home models payload has no sections")
	}

	seen := make(map[string]struct{})
	out := make([]homeModelEntry, 0, 256)
	for _, models := range bySection {
		for _, model := range models {
			id, _ := model["id"].(string)
			id = strings.TrimSpace(id)
			if id == "" {
				name, _ := model["name"].(string)
				name = strings.TrimSpace(name)
				id = strings.TrimPrefix(name, "models/")
			}
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}

			ownedBy, _ := model["owned_by"].(string)
			ownedBy = strings.TrimSpace(ownedBy)
			displayName, _ := model["display_name"].(string)
			displayName = strings.TrimSpace(displayName)
			if displayName == "" {
				displayName, _ = model["displayName"].(string)
				displayName = strings.TrimSpace(displayName)
			}

			out = append(out, homeModelEntry{
				id:                  id,
				created:             homeModelInt64Value(model, "created"),
				ownedBy:             ownedBy,
				displayName:         displayName,
				contextLength:       int(homeModelInt64Value(model, "context_length", "contextLength", "inputTokenLimit", "max_input_tokens")),
				maxCompletionTokens: int(homeModelInt64Value(model, "max_completion_tokens", "maxCompletionTokens", "outputTokenLimit", "max_tokens")),
			})
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	if len(out) == 0 {
		return nil, fmt.Errorf("home models payload contains no models")
	}
	return out, nil
}

func homeModelInt64Value(model map[string]any, keys ...string) int64 {
	for _, key := range keys {
		switch value := model[key].(type) {
		case float64:
			return int64(value)
		case int64:
			return value
		case int:
			return int64(value)
		case json.Number:
			if n, errInt := value.Int64(); errInt == nil {
				return n
			}
		case string:
			if n, errParse := strconv.ParseInt(strings.TrimSpace(value), 10, 64); errParse == nil {
				return n
			}
		}
	}
	return 0
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

// corsMiddleware returns a Gin middleware handler that adds CORS headers
// to every response, allowing cross-origin requests.
//
// Returns:
//   - gin.HandlerFunc: The CORS middleware handler
func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "*")
		c.Header("Access-Control-Expose-Headers", corsExposedResponseHeadersJoined)

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

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

// (management handlers moved to internal/api/handlers/management)

// AuthMiddleware returns a Gin middleware handler that authenticates requests
// using the configured authentication providers. When no providers are available,
// it allows all requests (legacy behaviour).
func AuthMiddleware(manager *sdkaccess.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		if manager == nil {
			c.Next()
			return
		}

		result, err := manager.Authenticate(c.Request.Context(), c.Request)
		if err == nil {
			if result != nil {
				c.Set("userApiKey", result.Principal)
				c.Set("accessProvider", result.Provider)
				if len(result.Metadata) > 0 {
					c.Set("accessMetadata", result.Metadata)
				}
			}
			c.Next()
			return
		}

		statusCode := err.HTTPStatusCode()
		if statusCode >= http.StatusInternalServerError {
			log.Errorf("authentication middleware error: %v", err)
		}
		c.AbortWithStatusJSON(statusCode, gin.H{"error": err.Message})
	}
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
