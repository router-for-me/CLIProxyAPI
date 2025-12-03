package amp

import (
	"net"
	"net/http/httputil"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers/claude"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers/gemini"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers/openai"
	log "github.com/sirupsen/logrus"
)

// parseGeminiModelFromPath extracts the Gemini model name from v1beta1 paths.
// Handles both AMP CLI format (/publishers/google/models/{model}:{action})
// and standard format (/models/{model}:{action}).
// Returns the model name and true if successfully parsed.
func parseGeminiModelFromPath(path string) (model string, ok bool) {
	path = strings.TrimPrefix(path, "/")

	// Try AMP CLI format: .../publishers/google/models/{model}:{action}
	if idx := strings.Index(path, "/publishers/google/models/"); idx >= 0 {
		modelAction := path[idx+len("/publishers/google/models/"):]
		if colonIdx := strings.Index(modelAction, ":"); colonIdx > 0 {
			return modelAction[:colonIdx], true
		}
		// No colon, take everything after models/
		if modelAction != "" {
			return modelAction, true
		}
	}

	// Try standard format: .../models/{model}:{action}
	if idx := strings.Index(path, "/models/"); idx >= 0 {
		modelAction := path[idx+len("/models/"):]
		if colonIdx := strings.Index(modelAction, ":"); colonIdx > 0 {
			return modelAction[:colonIdx], true
		}
		// No colon, take everything after models/
		if modelAction != "" {
			return modelAction, true
		}
	}

	return "", false
}

// shouldRouteToLiteLLM checks if a model should be routed to LiteLLM
// based on the hybrid mode configuration.
func shouldRouteToLiteLLM(cfg *config.Config, model string) bool {
	if cfg == nil || !cfg.LiteLLMHybridMode {
		return false
	}

	// Normalize model name for comparison
	normalizedModel, _ := util.NormalizeGeminiThinkingModel(model)

	// Check if model is in the litellm-models list
	for _, litellmModel := range cfg.LiteLLMModels {
		if strings.EqualFold(normalizedModel, litellmModel) {
			return true
		}
	}

	return false
}

// localhostOnlyMiddleware restricts access to localhost (127.0.0.1, ::1) only.
// Returns 403 Forbidden for non-localhost clients.
//
// Security: Uses RemoteAddr (actual TCP connection) instead of ClientIP() to prevent
// header spoofing attacks via X-Forwarded-For or similar headers. This means the
// middleware will not work correctly behind reverse proxies - users deploying behind
// nginx/Cloudflare should disable this feature and use firewall rules instead.
func localhostOnlyMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Use actual TCP connection address (RemoteAddr) to prevent header spoofing
		// This cannot be forged by X-Forwarded-For or other client-controlled headers
		remoteAddr := c.Request.RemoteAddr

		// RemoteAddr format is "IP:port" or "[IPv6]:port", extract just the IP
		host, _, err := net.SplitHostPort(remoteAddr)
		if err != nil {
			// Try parsing as raw IP (shouldn't happen with standard HTTP, but be defensive)
			host = remoteAddr
		}

		// Parse the IP to handle both IPv4 and IPv6
		ip := net.ParseIP(host)
		if ip == nil {
			log.Warnf("amp management: invalid RemoteAddr %s, denying access", remoteAddr)
			c.AbortWithStatusJSON(403, gin.H{
				"error": "Access denied: management routes restricted to localhost",
			})
			return
		}

		// Check if IP is loopback (127.0.0.1 or ::1)
		if !ip.IsLoopback() {
			log.Warnf("amp management: non-localhost connection from %s attempted access, denying", remoteAddr)
			c.AbortWithStatusJSON(403, gin.H{
				"error": "Access denied: management routes restricted to localhost",
			})
			return
		}

		c.Next()
	}
}

// noCORSMiddleware disables CORS for management routes to prevent browser-based attacks.
// This overwrites any global CORS headers set by the server.
func noCORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Remove CORS headers to prevent cross-origin access from browsers
		c.Header("Access-Control-Allow-Origin", "")
		c.Header("Access-Control-Allow-Methods", "")
		c.Header("Access-Control-Allow-Headers", "")
		c.Header("Access-Control-Allow-Credentials", "")

		// For OPTIONS preflight, deny with 403
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(403)
			return
		}

		c.Next()
	}
}

// registerManagementRoutes registers Amp management proxy routes
// These routes proxy through to the Amp control plane for OAuth, user management, etc.
// If restrictToLocalhost is true, routes will only accept connections from 127.0.0.1/::1.
func (m *AmpModule) registerManagementRoutes(engine *gin.Engine, baseHandler *handlers.BaseAPIHandler, proxyHandler gin.HandlerFunc, restrictToLocalhost bool, cfg *config.Config) {
	ampAPI := engine.Group("/api")

	// Always disable CORS for management routes to prevent browser-based attacks
	ampAPI.Use(noCORSMiddleware())

	// Apply localhost-only restriction if configured
	if restrictToLocalhost {
		ampAPI.Use(localhostOnlyMiddleware())
		log.Info("amp management routes restricted to localhost only (CORS disabled)")
	} else {
		log.Warn("amp management routes are NOT restricted to localhost - this is insecure!")
	}

	// Management routes - these are proxied directly to Amp upstream
	ampAPI.Any("/internal", proxyHandler)
	ampAPI.Any("/internal/*path", proxyHandler)
	ampAPI.Any("/user", proxyHandler)
	ampAPI.Any("/user/*path", proxyHandler)
	ampAPI.Any("/auth", proxyHandler)
	ampAPI.Any("/auth/*path", proxyHandler)
	ampAPI.Any("/meta", proxyHandler)
	ampAPI.Any("/meta/*path", proxyHandler)
	ampAPI.Any("/ads", proxyHandler)
	ampAPI.Any("/telemetry", proxyHandler)
	ampAPI.Any("/telemetry/*path", proxyHandler)
	ampAPI.Any("/threads", proxyHandler)
	ampAPI.Any("/threads/*path", proxyHandler)
	ampAPI.Any("/otel", proxyHandler)
	ampAPI.Any("/otel/*path", proxyHandler)

	// Root-level routes that AMP CLI expects without /api prefix
	// These need the same security middleware as the /api/* routes
	rootMiddleware := []gin.HandlerFunc{noCORSMiddleware()}
	if restrictToLocalhost {
		rootMiddleware = append(rootMiddleware, localhostOnlyMiddleware())
	}
	engine.GET("/threads.rss", append(rootMiddleware, proxyHandler)...)

	// Google v1beta1 passthrough with hybrid routing support
	// AMP CLI uses non-standard paths like /publishers/google/models/...
	// PRECEDENCE ORDER (per Zen architecture):
	// 1. LiteLLM (if hybrid mode enabled + model in config)
	// 2. OAuth (if local providers exist)
	// 3. Upstream proxy (ampcode.com)
	geminiHandlers := gemini.NewGeminiAPIHandler(baseHandler)
	geminiBridge := createGeminiBridgeHandler(geminiHandlers)

	// Create shared FallbackHandler with proper OAuth support
	// This is the same handler logic used by provider aliases for consistency
	geminiV1Beta1Fallback := NewFallbackHandler(
		cfg,
		geminiBridge,
		func() *httputil.ReverseProxy { return m.proxy },
		m.liteLLMProxy,
	)

	// Route v1beta1 requests with intelligent hybrid routing
	ampAPI.Any("/provider/google/v1beta1/*path", func(c *gin.Context) {
		if c.Request.Method == "POST" {
			// STEP 1: Check LiteLLM hybrid routing (highest priority)
			if model, ok := parseGeminiModelFromPath(c.Param("path")); ok {
				if shouldRouteToLiteLLM(cfg, model) {
					log.Debugf("Management route: routing %s to LiteLLM via hybrid mode", model)
					geminiV1Beta1Fallback.WrapHandler(geminiBridge)(c)
					return
				}

				// STEP 2: Check for OAuth providers
				normalized, _ := util.NormalizeGeminiThinkingModel(model)
				if providers := util.GetProviderName(normalized); len(providers) > 0 {
					log.Debugf("Management route: routing %s to OAuth provider", model)
					geminiV1Beta1Fallback.WrapHandler(geminiBridge)(c)
					return
				}
			}
		}

		// STEP 3: Fallback to upstream proxy for non-model routes or GET requests
		proxyHandler(c)
	})
}

// registerProviderAliases registers /api/provider/{provider}/... routes
// These allow Amp CLI to route requests like:
//
//	/api/provider/openai/v1/chat/completions
//	/api/provider/anthropic/v1/messages
//	/api/provider/google/v1beta/models
func (m *AmpModule) registerProviderAliases(engine *gin.Engine, baseHandler *handlers.BaseAPIHandler, auth gin.HandlerFunc, restrictToLocalhost bool, cfg *config.Config) {
	// Create handler instances for different providers
	openaiHandlers := openai.NewOpenAIAPIHandler(baseHandler)
	geminiHandlers := gemini.NewGeminiAPIHandler(baseHandler)
	claudeCodeHandlers := claude.NewClaudeCodeAPIHandler(baseHandler)
	openaiResponsesHandlers := openai.NewOpenAIResponsesAPIHandler(baseHandler)

	// Create a dummy OAuth handler that just calls the base handlers
	// This will be wrapped by the fallback handler for intelligent routing
	oauthHandler := func(c *gin.Context) {
		// The actual handler will be determined by the route
		c.Next()
	}

	// Create fallback handler wrapper with hybrid routing support
	// - Routes explicit models to LiteLLM (if configured)
	// - Tries OAuth with fallback to LiteLLM on errors
	// - Falls back to ampcode.com when no providers available
	fallbackHandler := NewFallbackHandler(
		cfg,
		oauthHandler,
		func() *httputil.ReverseProxy { return m.proxy },
		m.liteLLMProxy,
	)

	// Provider-specific routes under /api/provider/:provider
	// Note: These routes do NOT use auth middleware because:
	// 1. Amp CLI has its own authentication with the Amp upstream
	// 2. Provider authentication uses OAuth tokens from auth files
	// 3. Adding API key auth causes 401 errors for Amp CLI requests
	var ampProviders *gin.RouterGroup
	if restrictToLocalhost {
		ampProviders = engine.Group("/api/provider", localhostOnlyMiddleware())
		log.Info("Amp client endpoints (/api/provider/*) restricted to localhost only")
	} else {
		ampProviders = engine.Group("/api/provider")
		log.Debug("Amp client endpoints (/api/provider/*) accepting remote connections")
	}

	provider := ampProviders.Group("/:provider")

	// Dynamic models handler - routes to appropriate provider based on path parameter
	ampModelsHandler := func(c *gin.Context) {
		providerName := strings.ToLower(c.Param("provider"))

		switch providerName {
		case "anthropic":
			claudeCodeHandlers.ClaudeModels(c)
		case "google":
			geminiHandlers.GeminiModels(c)
		default:
			// Default to OpenAI-compatible (works for openai, groq, cerebras, etc.)
			openaiHandlers.OpenAIModels(c)
		}
	}

	// Root-level routes (for providers that omit /v1, like groq/cerebras)
	// Wrap handlers with fallback logic to forward to ampcode.com when provider not found
	provider.GET("/models", ampModelsHandler) // Models endpoint doesn't need fallback (no body to check)
	provider.POST("/chat/completions", fallbackHandler.WrapHandler(openaiHandlers.ChatCompletions))
	provider.POST("/completions", fallbackHandler.WrapHandler(openaiHandlers.Completions))
	provider.POST("/responses", fallbackHandler.WrapHandler(openaiResponsesHandlers.Responses))

	// /v1 routes (OpenAI/Claude-compatible endpoints)
	v1Amp := provider.Group("/v1")
	{
		v1Amp.GET("/models", ampModelsHandler) // Models endpoint doesn't need fallback

		// OpenAI-compatible endpoints with fallback
		v1Amp.POST("/chat/completions", fallbackHandler.WrapHandler(openaiHandlers.ChatCompletions))
		v1Amp.POST("/completions", fallbackHandler.WrapHandler(openaiHandlers.Completions))
		v1Amp.POST("/responses", fallbackHandler.WrapHandler(openaiResponsesHandlers.Responses))

		// Claude/Anthropic-compatible endpoints with fallback
		v1Amp.POST("/messages", fallbackHandler.WrapHandler(claudeCodeHandlers.ClaudeMessages))
		v1Amp.POST("/messages/count_tokens", fallbackHandler.WrapHandler(claudeCodeHandlers.ClaudeCountTokens))
	}

	// /v1beta routes (Gemini native API)
	// Note: Gemini handler extracts model from URL path, so fallback logic needs special handling
	v1betaAmp := provider.Group("/v1beta")
	{
		v1betaAmp.GET("/models", geminiHandlers.GeminiModels)
		v1betaAmp.POST("/models/:action", fallbackHandler.WrapHandler(geminiHandlers.GeminiHandler))
		v1betaAmp.GET("/models/:action", geminiHandlers.GeminiGetHandler)
	}

	// /v1beta1 routes (Vertex AI native format with full publisher path)
	// Pattern: /v1beta1/publishers/google/models/{model}:{action}
	// This route group enables hybrid routing for v1beta1 requests by using FallbackHandler
	// Note: Must be registered here (before registerManagementRoutes passthrough) for route precedence
	v1beta1Amp := provider.Group("/v1beta1/publishers/google/models")
	{
		// Vertex AI format captures: "gemini-3-pro-preview:streamGenerateContent"
		// FallbackHandler extracts model from URL path and checks litellm-models config
		v1beta1Amp.POST("/*modelAction", fallbackHandler.WrapHandler(geminiHandlers.GeminiHandler))
		v1beta1Amp.GET("/*modelAction", geminiHandlers.GeminiGetHandler)
	}
}
