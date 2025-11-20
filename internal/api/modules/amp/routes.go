package amp

import (
	"net"
	"net/http/httputil"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers/claude"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers/gemini"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers/openai"
	log "github.com/sirupsen/logrus"
)

// localhostOnlyMiddleware restricts access to localhost (127.0.0.1, ::1) only.
// Returns 403 Forbidden for non-localhost clients.
func localhostOnlyMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		clientIP := c.ClientIP()

		// Parse the IP to handle both IPv4 and IPv6
		ip := net.ParseIP(clientIP)
		if ip == nil {
			log.Warnf("Amp management: invalid client IP %s, denying access", clientIP)
			c.AbortWithStatusJSON(403, gin.H{
				"error": "Access denied: management routes restricted to localhost",
			})
			return
		}

		// Check if IP is loopback (127.0.0.1 or ::1)
		if !ip.IsLoopback() {
			log.Warnf("Amp management: non-localhost IP %s attempted access, denying", clientIP)
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
func (m *AmpModule) registerManagementRoutes(engine *gin.Engine, proxyHandler gin.HandlerFunc, restrictToLocalhost bool) {
	// Create middleware chain for management routes
	var middlewares []gin.HandlerFunc
	middlewares = append(middlewares, noCORSMiddleware())
	if restrictToLocalhost {
		middlewares = append(middlewares, localhostOnlyMiddleware())
		log.Info("Amp management routes restricted to localhost only (CORS disabled)")
	} else {
		log.Warn("⚠️  Amp management routes are NOT restricted to localhost - this is insecure!")
	}

	// Register /auth routes without /api prefix (for Amp CLI authentication flow)
	// These routes are used during initial login via `amp login`
	ampAuth := engine.Group("/auth", middlewares...)
	ampAuth.Any("", proxyHandler)
	ampAuth.Any("/*path", proxyHandler)

	// Register /api/* management routes (for Amp application/IDE usage)
	ampAPI := engine.Group("/api", middlewares...)

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

	// Google v1beta1 passthrough (Gemini native API)
	// NOTE: Commented out to allow hybrid routing via registerProviderAliases v1beta1 routes
	// The new v1beta1 route group with FallbackHandler provides proper LiteLLM hybrid routing
	// ampAPI.Any("/provider/google/v1beta1/*path", proxyHandler)
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
