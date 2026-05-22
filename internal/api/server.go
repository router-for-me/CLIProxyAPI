// Package api provides the HTTP API server implementation for the CLI Proxy API.
// It includes the main server struct, routing setup, middleware for CORS and authentication,
// and integration with various AI API handlers (OpenAI, Claude, Gemini).
// The server supports hot-reloading of clients and configuration.
package api

import (
	"bytes"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/access"
	managementHandlers "github.com/router-for-me/CLIProxyAPI/v7/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/api/middleware"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/api/modules"
	ampmodule "github.com/router-for-me/CLIProxyAPI/v7/internal/api/modules/amp"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/managementasset"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers/gemini"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers/openai"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/http2"
	"gopkg.in/yaml.v3"
)

const oauthCallbackSuccessHTML = `<html><head><meta charset="utf-8"><title>Authentication successful</title><script>setTimeout(function(){window.close();},5000);</script></head><body><h1>Authentication successful!</h1><p>You can close this window.</p><p>This window will close automatically in 5 seconds.</p></body></html>`

type serverOptionConfig struct {
	extraMiddleware      []gin.HandlerFunc
	engineConfigurator   func(*gin.Engine)
	routerConfigurator   func(*gin.Engine, *handlers.BaseAPIHandler, *config.Config)
	requestLoggerFactory func(*config.Config, string) logging.RequestLogger
	localPassword        string
	keepAliveEnabled     bool
	keepAliveTimeout     time.Duration
	keepAliveOnTimeout   func()
	postAuthHook         auth.PostAuthHook
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
	handlers *handlers.BaseAPIHandler

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

	// ampModule is the Amp routing module for model mapping hot-reload
	ampModule *ampmodule.AmpModule

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
		handlers:            handlers.NewBaseAPIHandlers(&cfg.SDKConfig, authManager),
		cfg:                 cfg,
		accessManager:       accessManager,
		requestLogger:       requestLogger,
		loggerToggle:        toggle,
		configFilePath:      configFilePath,
		currentPath:         wd,
		envManagementSecret: envManagementSecret,
		wsRoutes:            make(map[string]struct{}),
	}
	s.wsAuthEnabled.Store(cfg.WebsocketAuth)
	// Save initial YAML snapshot
	s.oldConfigYaml, _ = yaml.Marshal(cfg)
	s.applyAccessConfig(nil, cfg)
	if authManager != nil {
		authManager.SetRetryConfig(cfg.RequestRetry, time.Duration(cfg.MaxRetryInterval)*time.Second, cfg.MaxRetryCredentials)
	}
	managementasset.SetCurrentConfig(cfg)
	auth.SetQuotaCooldownDisabled(cfg.DisableCooling)
	applySignatureCacheConfig(nil, cfg)
	// Initialize management handler
	s.mgmt = managementHandlers.NewHandler(cfg, configFilePath, authManager)
	if optionState.localPassword != "" {
		s.mgmt.SetLocalPassword(optionState.localPassword)
	}
	logDir := logging.ResolveLogDirectory(cfg)
	s.mgmt.SetLogDirectory(logDir)
	if optionState.postAuthHook != nil {
		s.mgmt.SetPostAuthHook(optionState.postAuthHook)
	}
	s.localPassword = optionState.localPassword

	// Home heartbeat gate: when home is enabled, block all endpoints with 503 until the
	// subscribe-config heartbeat connection is healthy.
	engine.Use(s.homeHeartbeatMiddleware())

	// Setup routes
	s.setupRoutes()

	// Register Amp module using V2 interface with Context
	s.ampModule = ampmodule.NewLegacy(accessManager, AuthMiddleware(accessManager))
	ctx := modules.Context{
		Engine:         engine,
		BaseHandler:    s.handlers,
		Config:         cfg,
		AuthMiddleware: AuthMiddleware(accessManager),
	}
	if err := modules.RegisterModule(ctx, s.ampModule); err != nil {
		log.Errorf("Failed to register Amp module: %v", err)
	}

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
			if strings.HasPrefix(path, "/v0/management/") || path == "/v0/management" || path == "/management.html" {
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
	geminiCLIHandlers := gemini.NewGeminiCLIAPIHandler(s.handlers)
	claudeCodeHandlers := claude.NewClaudeCodeAPIHandler(s.handlers)
	openaiResponsesHandlers := openai.NewOpenAIResponsesAPIHandler(s.handlers)

	// OpenAI compatible API routes
	v1 := s.engine.Group("/v1")
	v1.Use(AuthMiddleware(s.accessManager))
	{
		v1.GET("/models", s.unifiedModelsHandler(openaiHandlers, claudeCodeHandlers))
		v1.POST("/chat/completions", openaiHandlers.ChatCompletions)
		v1.POST("/completions", openaiHandlers.Completions)
		v1.POST("/images/generations", openaiHandlers.ImagesGenerations)
		v1.POST("/images/edits", openaiHandlers.ImagesEdits)
		v1.POST("/videos", openaiHandlers.VideosCreate)
		v1.POST("/videos/generations", openaiHandlers.XAIVideosGenerations)
		v1.POST("/videos/edits", openaiHandlers.XAIVideosEdits)
		v1.POST("/videos/extensions", openaiHandlers.XAIVideosExtensions)
		v1.GET("/videos/:request_id", openaiHandlers.XAIVideosRetrieve)
		v1.POST("/messages", claudeCodeHandlers.ClaudeMessages)
		v1.POST("/messages/count_tokens", claudeCodeHandlers.ClaudeCountTokens)
		v1.GET("/responses", openaiResponsesHandlers.ResponsesWebsocket)
		v1.POST("/responses", openaiResponsesHandlers.Responses)
		v1.POST("/responses/compact", openaiResponsesHandlers.Compact)
	}

	// Codex CLI direct route aliases (chatgpt_base_url compatible)
	codexDirect := s.engine.Group("/backend-api/codex")
	codexDirect.Use(AuthMiddleware(s.accessManager))
	{
		codexDirect.GET("/responses", openaiResponsesHandlers.ResponsesWebsocket)
		codexDirect.POST("/responses", openaiResponsesHandlers.Responses)
		codexDirect.POST("/responses/compact", openaiResponsesHandlers.Compact)
	}

	// Gemini compatible API routes
	v1beta := s.engine.Group("/v1beta")
	v1beta.Use(AuthMiddleware(s.accessManager))
	{
		v1beta.GET("/models", s.geminiModelsHandler(geminiHandlers))
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
	s.engine.POST("/v1internal:method", geminiCLIHandlers.CLIHandler)

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

	s.engine.GET("/google/callback", func(c *gin.Context) {
		code := c.Query("code")
		state := c.Query("state")
		errStr := c.Query("error")
		if errStr == "" {
			errStr = c.Query("error_description")
		}
		if state != "" {
			_, _ = managementHandlers.WriteOAuthCallbackFileForPendingSession(s.cfg.AuthDir, "gemini", state, code, errStr)
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

	s.engine.GET("/xai/callback", func(c *gin.Context) {
		code := c.Query("code")
		state := c.Query("state")
		errStr := c.Query("error")
		if errStr == "" {
			errStr = c.Query("error_description")
		}
		if state != "" {
			_, _ = managementHandlers.WriteOAuthCallbackFileForPendingSession(s.cfg.AuthDir, "xai", state, code, errStr)
		}
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, oauthCallbackSuccessHTML)
	})

	// Management routes are registered lazily by registerManagementRoutes when a secret is configured.
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

	mgmt := s.engine.Group("/v0/management")
	mgmt.Use(s.managementAvailabilityMiddleware(), s.mgmt.Middleware())
	{
		mgmt.GET("/config", s.mgmt.GetConfig)
		mgmt.GET("/config.yaml", s.mgmt.GetConfigYAML)
		mgmt.PUT("/config.yaml", s.mgmt.PutConfigYAML)
		mgmt.GET("/latest-version", s.mgmt.GetLatestVersion)

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

		mgmt.POST("/api-call", s.mgmt.APICall)

		mgmt.GET("/quota-exceeded/switch-project", s.mgmt.GetSwitchProject)
		mgmt.PUT("/quota-exceeded/switch-project", s.mgmt.PutSwitchProject)
		mgmt.PATCH("/quota-exceeded/switch-project", s.mgmt.PutSwitchProject)

		mgmt.GET("/quota-exceeded/switch-preview-model", s.mgmt.GetSwitchPreviewModel)
		mgmt.PUT("/quota-exceeded/switch-preview-model", s.mgmt.PutSwitchPreviewModel)
		mgmt.PATCH("/quota-exceeded/switch-preview-model", s.mgmt.PutSwitchPreviewModel)

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

		mgmt.GET("/ampcode", s.mgmt.GetAmpCode)
		mgmt.GET("/ampcode/upstream-url", s.mgmt.GetAmpUpstreamURL)
		mgmt.PUT("/ampcode/upstream-url", s.mgmt.PutAmpUpstreamURL)
		mgmt.PATCH("/ampcode/upstream-url", s.mgmt.PutAmpUpstreamURL)
		mgmt.DELETE("/ampcode/upstream-url", s.mgmt.DeleteAmpUpstreamURL)
		mgmt.GET("/ampcode/upstream-api-key", s.mgmt.GetAmpUpstreamAPIKey)
		mgmt.PUT("/ampcode/upstream-api-key", s.mgmt.PutAmpUpstreamAPIKey)
		mgmt.PATCH("/ampcode/upstream-api-key", s.mgmt.PutAmpUpstreamAPIKey)
		mgmt.DELETE("/ampcode/upstream-api-key", s.mgmt.DeleteAmpUpstreamAPIKey)
		mgmt.GET("/ampcode/restrict-management-to-localhost", s.mgmt.GetAmpRestrictManagementToLocalhost)
		mgmt.PUT("/ampcode/restrict-management-to-localhost", s.mgmt.PutAmpRestrictManagementToLocalhost)
		mgmt.PATCH("/ampcode/restrict-management-to-localhost", s.mgmt.PutAmpRestrictManagementToLocalhost)
		mgmt.GET("/ampcode/model-mappings", s.mgmt.GetAmpModelMappings)
		mgmt.PUT("/ampcode/model-mappings", s.mgmt.PutAmpModelMappings)
		mgmt.PATCH("/ampcode/model-mappings", s.mgmt.PatchAmpModelMappings)
		mgmt.DELETE("/ampcode/model-mappings", s.mgmt.DeleteAmpModelMappings)
		mgmt.GET("/ampcode/force-model-mappings", s.mgmt.GetAmpForceModelMappings)
		mgmt.PUT("/ampcode/force-model-mappings", s.mgmt.PutAmpForceModelMappings)
		mgmt.PATCH("/ampcode/force-model-mappings", s.mgmt.PutAmpForceModelMappings)
		mgmt.GET("/ampcode/upstream-api-keys", s.mgmt.GetAmpUpstreamAPIKeys)
		mgmt.PUT("/ampcode/upstream-api-keys", s.mgmt.PutAmpUpstreamAPIKeys)
		mgmt.PATCH("/ampcode/upstream-api-keys", s.mgmt.PatchAmpUpstreamAPIKeys)
		mgmt.DELETE("/ampcode/upstream-api-keys", s.mgmt.DeleteAmpUpstreamAPIKeys)

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
		mgmt.GET("/auth-files/models", s.mgmt.GetAuthFileModels)
		mgmt.GET("/model-definitions/:channel", s.mgmt.GetStaticModelDefinitions)
		mgmt.GET("/auth-files/download", s.mgmt.DownloadAuthFile)
		mgmt.POST("/auth-files", s.mgmt.UploadAuthFile)
		mgmt.POST("/auth-files/clean-codex-401", s.mgmt.CleanCodex401AuthFiles)
		mgmt.DELETE("/auth-files", s.mgmt.DeleteAuthFile)
		mgmt.PATCH("/auth-files/status", s.mgmt.PatchAuthFileStatus)
		mgmt.PATCH("/auth-files/fields", s.mgmt.PatchAuthFileFields)
		mgmt.POST("/vertex/import", s.mgmt.ImportVertexCredential)

		mgmt.GET("/anthropic-auth-url", s.mgmt.RequestAnthropicToken)
		mgmt.GET("/codex-auth-url", s.mgmt.RequestCodexToken)
		mgmt.GET("/gemini-cli-auth-url", s.mgmt.RequestGeminiCLIToken)
		mgmt.GET("/antigravity-auth-url", s.mgmt.RequestAntigravityToken)
		mgmt.GET("/kimi-auth-url", s.mgmt.RequestKimiToken)
		mgmt.GET("/xai-auth-url", s.mgmt.RequestXAIToken)
		mgmt.POST("/oauth-callback", s.mgmt.PostOAuthCallback)
		mgmt.GET("/get-auth-status", s.mgmt.GetAuthStatus)
	}
}

func (s *Server) managementAvailabilityMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s == nil || s.cfg == nil {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		if s.cfg.Home.Enabled {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		if !s.managementRoutesEnabled.Load() {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		c.Next()
	}
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
			// Synchronously ensure management.html is available with a detached context.
			// Control panel bootstrap should not be canceled by client disconnects.
			if !managementasset.EnsureLatestManagementHTML(context.Background(), managementasset.StaticDir(s.configFilePath), cfg.ProxyURL, cfg.RemoteManagement.PanelGitHubRepository) {
				c.AbortWithStatus(http.StatusNotFound)
				return
			}
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

	patched := injectAuthFilesWarningFilterPatch(data)
	patched = injectModelPriceDropdownClipPatch(patched)
	c.Header("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	c.Header("Pragma", "no-cache")
	c.Header("Expires", "0")
	c.Data(http.StatusOK, "text/html; charset=utf-8", patched)
}

func injectAuthFilesWarningFilterPatch(html []byte) []byte {
	const marker = "__cpa_auth_warning_filter_patch__"
	if len(html) == 0 || bytes.Contains(html, []byte(marker)) {
		return html
	}

	patch := []byte(`<script>
(function () {
  var MARKER = "__cpa_auth_warning_filter_patch__";
  if (window[MARKER]) return;
  window[MARKER] = true;

  var STYLE_ID = "cpa-auth-warning-filter-style";
  var ROOT_ID = "cpa-auth-warning-filter-root";
  var BUTTON_ID = "cpa-auth-clean-401-button";
  var STATUS_ID = "cpa-auth-clean-401-status";
	var CLEAN_ENDPOINT = "/v0/management/auth-files/clean-codex-401";
	var isZh = (document.documentElement && (document.documentElement.lang || "").toLowerCase().indexOf("zh") === 0);
	var cachedAuthHeaderName = "";
	var cachedAuthHeaderValue = "";
	var cleanerBusy = false;
	var mountObserver = null;
	var remountTimer = 0;

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) return;
    var style = document.createElement("style");
    style.id = STYLE_ID;
    style.textContent = "#"+ROOT_ID+"{display:none;gap:8px;align-items:center;flex-wrap:wrap;box-sizing:border-box;background:rgba(18,18,18,.86);color:#fff;padding:8px 10px;border-radius:10px;border:1px solid rgba(255,255,255,.2);font-size:12px;font-family:Arial,sans-serif;max-width:min(100%,560px);box-shadow:0 8px 24px rgba(0,0,0,.18)}#"+ROOT_ID+".before-header-actions{position:relative;z-index:1;margin-left:auto;margin-right:12px;width:fit-content;max-width:min(100%,560px)}#"+ROOT_ID+".topbar-fallback{position:relative;z-index:1;margin:0 0 12px auto;width:fit-content;max-width:min(100%,640px)}#"+ROOT_ID+".floating-fallback{position:fixed;top:72px;right:16px;z-index:2147483647;max-width:min(calc(100vw - 32px),460px)}#"+ROOT_ID+" button{background:#8b1e1e;color:#fff;border:1px solid rgba(255,255,255,.28);border-radius:6px;padding:4px 10px;cursor:pointer}#"+ROOT_ID+" button[disabled]{opacity:.55;cursor:not-allowed}#"+STATUS_ID+"{max-width:230px;line-height:1.35;color:rgba(255,255,255,.88)}#"+STATUS_ID+".error{color:#ffb4b4}@media (max-width: 768px){#"+ROOT_ID+"{width:100%;max-width:100%}#"+ROOT_ID+".before-header-actions,#"+ROOT_ID+".topbar-fallback{margin-left:0;margin-right:0}#"+ROOT_ID+".floating-fallback{left:12px;right:12px;top:auto;bottom:12px;max-width:none}}";
    document.head.appendChild(style);
  }

  function normalizeText(value) {
    return (value || "").toLowerCase().replace(/\s+/g, " ").trim();
  }

  function hasAnyText(value, needles) {
    var text = normalizeText(value);
    if (!text) return false;
    for (var i = 0; i < needles.length; i++) {
      if (text.indexOf(needles[i]) !== -1) return true;
    }
    return false;
  }

  function isVisible(node) {
    if (!node || !node.getBoundingClientRect) return false;
    var rect = node.getBoundingClientRect();
    return rect.width > 0 && rect.height > 0;
  }

  function getButtonLikeText(node) {
    return normalizeText((node && (node.innerText || node.textContent)) || "");
  }

  function isAuthFilesActionText(text) {
    return hasAnyText(text, ["refresh", "upload", "delete", "clean", "refreshing", "\u5237\u65b0", "\u4e0a\u4f20", "\u5220\u9664", "\u6e05\u7406"]);
  }

  function findAuthFilesCardHeader() {
    var headers = document.querySelectorAll(".card-header");
    for (var i = 0; i < headers.length; i++) {
      var header = headers[i];
      if (!isVisible(header)) continue;
      var title = null;
      for (var j = 0; j < header.children.length; j++) {
        var child = header.children[j];
        if (child.classList && child.classList.contains("title")) {
          title = child;
          break;
        }
      }
      if (!title) {
        title = header.querySelector(".title");
      }
      if (!title) continue;
      var titleText = normalizeText(title.innerText || title.textContent || "");
      if (titleText.indexOf("management") !== -1 || titleText.indexOf("\u7ba1\u7406") !== -1) continue;
      if (titleText.indexOf("auth file") !== -1 || titleText.indexOf("auth files") !== -1 || titleText.indexOf("\u8ba4\u8bc1\u6587\u4ef6") !== -1) {
        return header;
      }
    }
    return null;
  }

  function findAuthFilesPageRoot() {
    var cardHeader = findAuthFilesCardHeader();
    if (cardHeader) {
      var current = cardHeader;
      for (var i = 0; i < 7 && current; i++) {
        if (current.querySelectorAll && isVisible(current)) {
          var buttons = current.querySelectorAll("button,[role='button'],a");
          var actionCount = 0;
          for (var j = 0; j < buttons.length && actionCount < 3; j++) {
            if (isAuthFilesActionText(getButtonLikeText(buttons[j]))) actionCount++;
          }
          if (actionCount >= 2) return current;
        }
        current = current.parentElement;
      }
    }

    var mains = document.querySelectorAll("main,section,article");
    for (var k = 0; k < mains.length; k++) {
      var candidate = mains[k];
      if (!candidate.querySelectorAll || !isVisible(candidate)) continue;
      if (!hasAnyText(candidate.innerText || candidate.textContent || "", ["auth file", "auth files", "\u8ba4\u8bc1\u6587\u4ef6"])) continue;
      return candidate;
    }
    return null;
  }

  function findHeaderActionsContainer(cardHeader) {
    if (!cardHeader || !cardHeader.children) return null;
    for (var i = 0; i < cardHeader.children.length; i++) {
      var child = cardHeader.children[i];
      if (!isVisible(child)) continue;
      if (child.classList && child.classList.contains("title")) continue;
      var buttons = child.querySelectorAll ? child.querySelectorAll("button,[role='button'],a") : [];
      if (buttons.length >= 2) return child;
    }
    return null;
  }

  function rememberManagementAuth(name, value) {
    name = (name || "").toLowerCase();
    value = (value || "") + "";
    if (!value) return;
    if (name === "authorization" || name === "x-management-key") {
      cachedAuthHeaderName = name === "authorization" ? "Authorization" : "X-Management-Key";
      cachedAuthHeaderValue = value;
    }
  }

  function captureHeaders(headers) {
    if (!headers) return;
    try {
      if (typeof Headers !== "undefined" && headers instanceof Headers) {
        headers.forEach(function (value, key) { rememberManagementAuth(key, value); });
        return;
      }
      if (Array.isArray(headers)) {
        for (var i = 0; i < headers.length; i++) {
          var pair = headers[i] || [];
          rememberManagementAuth(pair[0], pair[1]);
        }
        return;
      }
      var keys = Object.keys(headers);
      for (var j = 0; j < keys.length; j++) {
        rememberManagementAuth(keys[j], headers[keys[j]]);
      }
    } catch (e) {
      // ignore
    }
  }

  function ensureControl() {
    var root = document.getElementById(ROOT_ID);
    if (root) return root;
    ensureStyle();
    root = document.createElement("div");
    root.id = ROOT_ID;

    var button = document.createElement("button");
    button.type = "button";
    button.id = BUTTON_ID;
    button.textContent = isZh ? "\u6e05\u7406401" : "Clean 401";
    button.addEventListener("click", handleClean401);

    var status = document.createElement("span");
    status.id = STATUS_ID;

    root.appendChild(button);
    root.appendChild(status);
    return root;
  }

  function applyMountMode(root, mode) {
    if (!root) return;
    root.classList.remove("before-header-actions", "topbar-fallback", "floating-fallback");
    root.classList.add(mode || "floating-fallback");
  }

  function mountBeforeHeaderActions(root, cardHeader, actions) {
    if (!root || !cardHeader || !actions) return false;
    if (!cardHeader.contains(actions)) return false;
    if (root.parentElement !== cardHeader || root.nextElementSibling !== actions) {
      cardHeader.insertBefore(root, actions);
    }
    applyMountMode(root, "before-header-actions");
    return true;
  }

  function mountIntoPageRoot(root, pageRoot) {
    if (!root || !pageRoot) return false;
    if (root.parentElement !== pageRoot) {
      pageRoot.insertBefore(root, pageRoot.firstChild);
    }
    applyMountMode(root, "topbar-fallback");
    return true;
  }

  function mountFloatingFallback(root) {
    if (!root) return false;
    if (root.parentElement !== document.body) {
      document.body.appendChild(root);
    }
    applyMountMode(root, "floating-fallback");
    return true;
  }

  function ensureMounted() {
    var root = ensureControl();
    if (!isAuthFilesRoute()) return root;
    var pageRoot = findAuthFilesPageRoot();
    var cardHeader = findAuthFilesCardHeader();
    var actions = findHeaderActionsContainer(cardHeader);
    if (mountBeforeHeaderActions(root, cardHeader, actions)) return root;
    if (mountIntoPageRoot(root, pageRoot)) return root;
    mountFloatingFallback(root);
    return root;
  }

  function scheduleRemount() {
    if (remountTimer) return;
    remountTimer = window.setTimeout(function () {
      remountTimer = 0;
      if (isAuthFilesRoute()) ensureMounted();
    }, 80);
  }

  function ensureMountObserver() {
    if (mountObserver || typeof MutationObserver !== "function" || !document.body) return;
    mountObserver = new MutationObserver(function () {
      scheduleRemount();
    });
    mountObserver.observe(document.body, { childList: true, subtree: true });
  }

  function stopMountObserver() {
    if (mountObserver && mountObserver.disconnect) {
      mountObserver.disconnect();
    }
    mountObserver = null;
    if (remountTimer) {
      window.clearTimeout(remountTimer);
      remountTimer = 0;
    }
  }

  function isAuthFilesRoute() {
    return (window.location.hash || "").indexOf("/auth-files") !== -1;
  }

	function patchFetch() {
	  if (typeof window.fetch !== "function") return;
	  var originalFetch = window.fetch.bind(window);
	  window.fetch = function (input, init) {
	    try {
	      var requestURL = "";
	      if (typeof input === "string") {
	        requestURL = input;
	      } else if (input && input.url) {
	        requestURL = input.url;
	      }
	      if ((requestURL || "").indexOf("/v0/management/") !== -1) {
	        captureHeaders((init && init.headers) || (input && input.headers));
	      }
	    } catch (e) {
	      // ignore patch failure
	    }
	    return originalFetch(input, init);
	  };
	}

	function patchXHR() {
	  if (!window.XMLHttpRequest || !window.XMLHttpRequest.prototype) return;
	  var proto = window.XMLHttpRequest.prototype;
	  var originalOpen = proto.open;
	  var originalSetRequestHeader = proto.setRequestHeader;
	  proto.open = function (method, url) {
	    try {
	      this.__cpaRequestURL = typeof url === "string" ? url : "";
	    } catch (e) {
	      // ignore patch failure
	    }
	    return originalOpen.apply(this, [method, url].concat([].slice.call(arguments, 2)));
	  };
	  proto.setRequestHeader = function (name, value) {
	    try {
	      if (((this && this.__cpaRequestURL) || "").indexOf("/v0/management/") !== -1) {
	        rememberManagementAuth(name, value);
	      }
	    } catch (e) {
	      // ignore
	    }
	    return originalSetRequestHeader.apply(this, arguments);
	  };
	}

  function setCleanerState(busy, message, isError) {
    cleanerBusy = !!busy;
    var button = document.getElementById(BUTTON_ID);
    var status = document.getElementById(STATUS_ID);
    if (button) {
      button.disabled = cleanerBusy;
      button.textContent = cleanerBusy ? (isZh ? "\u6e05\u7406\u4e2d..." : "Cleaning...") : (isZh ? "\u6e05\u7406401" : "Clean 401");
    }
    if (status) {
      status.textContent = message || "";
      status.className = isError ? "error" : "";
    }
  }

  function buildCleanerSummary(payload) {
    if (!payload || typeof payload !== "object") {
      return isZh ? "\u6e05\u7406\u5b8c\u6210" : "Cleanup finished";
    }
    var scanned = Number(payload.scanned || 0);
    var matched = Number(payload.matched_401 || 0);
    var deleted = Number(payload.deleted || 0);
    var failed = Number(payload.failed || 0);
    if (isZh) {
      return "\u5df2\u626b\u63cf " + scanned + " \u4e2a锛?01 " + matched + " \u4e2a锛孿u5df2\u5220\u9664 " + deleted + " \u4e2a锛孿u5931\u8d25 " + failed + " \u4e2a";
    }
    return "Scanned " + scanned + ", matched " + matched + ", deleted " + deleted + ", failed " + failed;
  }

  function looksLikeHTMLDocument(text) {
    var value = ((text || "") + "").trim().toLowerCase();
    if (!value) return false;
    return value.indexOf("<!doctype html") === 0 ||
      value.indexOf("<html") === 0 ||
      value.indexOf("<head") === 0 ||
      value.indexOf("<body") === 0 ||
      value.indexOf("<title") !== -1;
  }

  function buildCleanerErrorMessage(response, payload) {
    var contentType = "";
    try {
      contentType = ((response && response.headers && response.headers.get && response.headers.get("content-type")) || "") + "";
    } catch (e) {
      // ignore
    }
    contentType = contentType.toLowerCase();

    var rawText = "";
    if (payload && typeof payload === "object") {
      if (typeof payload.error === "string") {
        rawText = payload.error;
      } else if (typeof payload.message === "string") {
        rawText = payload.message;
      } else if (typeof payload.rawText === "string") {
        rawText = payload.rawText;
      }
    }
    rawText = ((rawText || "") + "").trim();

    var timeoutLike = !!(response && Number(response.status || 0) === 524);
    if (!timeoutLike) {
      timeoutLike = hasAnyText(rawText, ["timeout occurred", "timed out", "error code 524", "cloudflare 524"]);
    }
    if (timeoutLike) {
      return isZh ? "\u6e05\u7406\u8bf7\u6c42\u8d85\u65f6\uff0c\u8bf7\u7a0d\u540e\u91cd\u8bd5\u3002" : "Cleanup request timed out. Please try again.";
    }

    if (contentType.indexOf("text/html") !== -1 || looksLikeHTMLDocument(rawText)) {
      return isZh ? "\u6e05\u7406\u8bf7\u6c42\u8fd4\u56de\u4e86 HTML \u9519\u8bef\u9875\uff0c\u8bf7\u7a0d\u540e\u91cd\u8bd5\u3002" : "Cleanup request returned an HTML error page. Please try again.";
    }

    if (!rawText) {
      return isZh ? "\u6e05\u7406\u5931\u8d25" : "Cleanup failed";
    }

    rawText = rawText.replace(/\s+/g, " ").trim();
    if (rawText.length > 180) {
      rawText = rawText.slice(0, 177) + "...";
    }
    return rawText;
  }

  function readResponseJSON(response) {
    var contentType = "";
    try {
      contentType = ((response && response.headers && response.headers.get && response.headers.get("content-type")) || "") + "";
    } catch (e) {
      // ignore
    }
    return response.text().then(function (text) {
      if (!text) return { contentType: contentType };
      try {
        var parsed = JSON.parse(text);
        if (parsed && typeof parsed === "object") {
          if (!parsed.contentType) parsed.contentType = contentType;
          return parsed;
        }
      } catch (e) {
        // ignore parse errors and fall back to plain text payload
      }
      return { error: text, rawText: text, contentType: contentType };
    });
  }

  function handleClean401() {
    if (cleanerBusy || !isAuthFilesRoute()) return;
    var confirmed = window.confirm(isZh ? "\u786e\u8ba4\u6e05\u7406\u6240\u6709\u547d\u4e2d 401 \u7684 Codex \u8ba4\u8bc1\u6587\u4ef6\uff1f" : "Delete all Codex auth files that return 401?");
    if (!confirmed) return;
    if (!cachedAuthHeaderName || !cachedAuthHeaderValue) {
      setCleanerState(false, isZh ? "\u672a\u6355\u83b7\u5230\u7ba1\u7406\u7aef\u8ba4\u8bc1\u4fe1\u606f\uff0c\u8bf7\u5148\u5237\u65b0\u5217\u8868\u540e\u518d\u8bd5" : "Management auth header not captured yet. Refresh the list and retry.", true);
      return;
    }
    setCleanerState(true, isZh ? "\u6b63\u5728\u68c0\u6d4b\u5e76\u5220\u9664 401 \u8d26\u53f7..." : "Checking and deleting 401 accounts...", false);
    var headers = { "Content-Type": "application/json" };
    headers[cachedAuthHeaderName] = cachedAuthHeaderValue;
    window.fetch(CLEAN_ENDPOINT, { method: "POST", headers: headers, body: "{}" })
      .then(function (response) {
        return readResponseJSON(response).then(function (payload) {
          if (!response.ok) {
            var message = buildCleanerErrorMessage(response, payload);
            throw new Error(message);
          }
          return payload;
        });
      })
      .then(function (payload) {
        setCleanerState(false, buildCleanerSummary(payload), false);
        setTimeout(triggerRefresh, 120);
      })
      .catch(function (error) {
        setCleanerState(false, buildCleanerErrorMessage(null, { error: (error && error.message) || "" }), true);
      });
  }

  function showOrHideControl() {
    var onAuthFiles = isAuthFilesRoute();
    if (onAuthFiles) {
      ensureMountObserver();
    } else {
      stopMountObserver();
    }
    var root = ensureMounted();
    root.style.display = onAuthFiles ? "inline-flex" : "none";
    if (!onAuthFiles) {
      setCleanerState(false, "", false);
    }
  }

  function triggerRefresh() {
    if (!isAuthFilesRoute()) return;
    var nodes = document.querySelectorAll("button,[role='button'],a");
    for (var i = 0; i < nodes.length; i++) {
      var text = ((nodes[i].innerText || nodes[i].textContent || "") + "").toLowerCase();
      if (text.indexOf("refresh") !== -1 || text.indexOf("\u5237\u65b0") !== -1) {
        nodes[i].click();
        return;
      }
    }
    try {
      window.dispatchEvent(new Event("hashchange"));
      setTimeout(function () { window.dispatchEvent(new Event("hashchange")); }, 60);
      setTimeout(function () {
        if (isAuthFilesRoute()) {
          window.location.reload();
        }
      }, 180);
    } catch (e) {
      // ignore
    }
  }

  patchFetch();
  patchXHR();
  window.addEventListener("hashchange", showOrHideControl, true);
  window.addEventListener("popstate", showOrHideControl, true);

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", showOrHideControl, { once: true });
  } else {
    showOrHideControl();
  }
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

// unifiedModelsHandler creates a unified handler for the /v1/models endpoint
// that routes to different handlers based on the User-Agent header.
// If User-Agent starts with "claude-cli", it routes to Claude handler,
// otherwise it routes to OpenAI handler.
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

		userAgent := c.GetHeader("User-Agent")

		// Route to Claude handler if User-Agent starts with "claude-cli"
		if strings.HasPrefix(userAgent, "claude-cli") {
			// log.Debugf("Routing /v1/models to Claude handler for User-Agent: %s", userAgent)
			claudeHandler.ClaudeModels(c)
		} else {
			// log.Debugf("Routing /v1/models to OpenAI handler for User-Agent: %s", userAgent)
			openaiHandler.OpenAIModels(c)
		}
	}
}

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

	c.JSON(http.StatusOK, openai.CodexClientModelsResponse(models))
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
	id          string
	created     int64
	ownedBy     string
	displayName string
}

func (s *Server) handleHomeModels(c *gin.Context) {
	entries, ok := s.loadHomeModelEntries(c)
	if !ok {
		return
	}

	userAgent := c.GetHeader("User-Agent")
	isClaude := strings.HasPrefix(userAgent, "claude-cli")

	if isClaude {
		out := make([]map[string]any, 0, len(entries))
		for _, entry := range entries {
			model := map[string]any{
				"id":       entry.id,
				"object":   "model",
				"owned_by": entry.ownedBy,
			}
			if entry.created > 0 {
				model["created_at"] = entry.created
			}
			if entry.displayName != "" {
				model["display_name"] = entry.displayName
			}
			out = append(out, model)
		}
		firstID := ""
		lastID := ""
		if len(out) > 0 {
			if id, okID := out[0]["id"].(string); okID {
				firstID = id
			}
			if id, okID := out[len(out)-1]["id"].(string); okID {
				lastID = id
			}
		}
		c.JSON(http.StatusOK, gin.H{
			"data":     out,
			"has_more": false,
			"first_id": firstID,
			"last_id":  lastID,
		})
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

	raw, errGet := client.GetModels(c.Request.Context())
	if errGet != nil {
		c.JSON(http.StatusBadGateway, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: errGet.Error(),
				Type:    "server_error",
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

			created := int64(0)
			switch v := model["created"].(type) {
			case float64:
				created = int64(v)
			case int64:
				created = v
			case int:
				created = int64(v)
			case json.Number:
				if n, err := v.Int64(); err == nil {
					created = n
				}
			}

			ownedBy, _ := model["owned_by"].(string)
			ownedBy = strings.TrimSpace(ownedBy)
			displayName, _ := model["display_name"].(string)
			displayName = strings.TrimSpace(displayName)
			if displayName == "" {
				displayName, _ = model["displayName"].(string)
				displayName = strings.TrimSpace(displayName)
			}

			out = append(out, homeModelEntry{
				id:          id,
				created:     created,
				ownedBy:     ownedBy,
				displayName: displayName,
			})
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	if len(out) == 0 {
		return nil, fmt.Errorf("home models payload contains no models")
	}
	return out, nil
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
	if err := s.server.Shutdown(ctx); err != nil {
		return fmt.Errorf("failed to shutdown HTTP server: %v", err)
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

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

func (s *Server) applyAccessConfig(oldCfg, newCfg *config.Config) {
	if s == nil || s.accessManager == nil || newCfg == nil {
		return
	}
	if _, err := access.ApplyAccessProviders(s.accessManager, oldCfg, newCfg); err != nil {
		return
	}
}

// UpdateClients updates the server's client list and configuration.
// This method is called when the configuration or authentication tokens change.
//
// Parameters:
//   - clients: The new slice of AI service clients
//   - cfg: The new application configuration
func (s *Server) UpdateClients(cfg *config.Config) {
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

	s.applyAccessConfig(oldCfg, cfg)
	s.cfg = cfg
	s.wsAuthEnabled.Store(cfg.WebsocketAuth)
	if oldCfg != nil && s.wsAuthChanged != nil && oldCfg.WebsocketAuth != cfg.WebsocketAuth {
		s.wsAuthChanged(oldCfg.WebsocketAuth, cfg.WebsocketAuth)
	}
	managementasset.SetCurrentConfig(cfg)
	// Save YAML snapshot for next comparison
	s.oldConfigYaml, _ = yaml.Marshal(cfg)

	s.handlers.UpdateClients(&cfg.SDKConfig)

	if s.mgmt != nil {
		s.mgmt.SetConfig(cfg)
		s.mgmt.SetAuthManager(s.handlers.AuthManager)
	}

	// Notify Amp module only when Amp config has changed.
	ampConfigChanged := oldCfg == nil || !reflect.DeepEqual(oldCfg.AmpCode, cfg.AmpCode)
	if ampConfigChanged {
		if s.ampModule != nil {
			log.Debugf("triggering amp module config update")
			if err := s.ampModule.OnConfigUpdated(cfg); err != nil {
				log.Errorf("failed to update Amp module config: %v", err)
			}
		} else {
			log.Warnf("amp module is nil, skipping config update")
		}
	}

	// Count client sources from configuration and auth store.
	authEntries := 0
	if cfg != nil && !cfg.Home.Enabled {
		tokenStore := sdkAuth.GetTokenStore()
		if dirSetter, ok := tokenStore.(interface{ SetBaseDir(string) }); ok {
			dirSetter.SetBaseDir(cfg.AuthDir)
		}
		authEntries = util.CountAuthFiles(context.Background(), tokenStore)
	}
	geminiAPIKeyCount := len(cfg.GeminiKey)
	claudeAPIKeyCount := len(cfg.ClaudeKey)
	codexAPIKeyCount := len(cfg.CodexKey)
	vertexAICompatCount := len(cfg.VertexCompatAPIKey)
	openAICompatCount := 0
	for i := range cfg.OpenAICompatibility {
		entry := cfg.OpenAICompatibility[i]
		if entry.Disabled {
			continue
		}
		openAICompatCount += len(entry.APIKeyEntries)
	}

	total := authEntries + geminiAPIKeyCount + claudeAPIKeyCount + codexAPIKeyCount + vertexAICompatCount + openAICompatCount
	fmt.Printf("server clients and configuration updated: %d clients (%d auth entries + %d Gemini API keys + %d Claude API keys + %d Codex keys + %d Vertex-compat + %d OpenAI-compat)\n",
		total,
		authEntries,
		geminiAPIKeyCount,
		claudeAPIKeyCount,
		codexAPIKeyCount,
		vertexAICompatCount,
		openAICompatCount,
	)
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
