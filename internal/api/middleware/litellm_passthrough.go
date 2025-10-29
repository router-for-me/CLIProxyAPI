// Package middleware provides HTTP middleware components for the CLI Proxy API server.
package middleware

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	log "github.com/sirupsen/logrus"
)

// LiteLLMPassthrough creates a middleware that routes ALL API traffic directly to LiteLLM
// when passthrough mode is enabled. This bypasses OAuth providers and authentication checks.
//
// When enabled, this middleware:
// - Forwards all /v1/* requests to LiteLLM
// - Forwards all /api/provider/* requests to LiteLLM
// - Preserves request method, headers, and body
// - Adds LiteLLM API key if configured
// - Skips normal OAuth routing
//
// When disabled, the middleware does nothing and normal routing applies.
func LiteLLMPassthrough(cfg *config.Config) gin.HandlerFunc {
	// If passthrough mode is disabled, return no-op middleware
	if !cfg.LiteLLMPassthroughMode {
		return func(c *gin.Context) {
			c.Next() // Continue to next handler (normal OAuth routing)
		}
	}

	// Validate configuration
	baseURL := strings.TrimSpace(cfg.LiteLLMBaseURL)
	if baseURL == "" {
		log.Error("LiteLLM passthrough mode enabled but litellm-base-url is not configured")
		return func(c *gin.Context) {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "LiteLLM passthrough mode is enabled but litellm-base-url is not configured",
			})
			c.Abort()
		}
	}

	// Parse LiteLLM base URL
	parsed, err := url.Parse(baseURL)
	if err != nil {
		log.Errorf("Invalid LiteLLM base URL: %v", err)
		return func(c *gin.Context) {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": fmt.Sprintf("Invalid LiteLLM base URL: %v", err),
			})
			c.Abort()
		}
	}

	// Create reverse proxy to LiteLLM
	proxy := httputil.NewSingleHostReverseProxy(parsed)
	originalDirector := proxy.Director

	// Configure custom transport with reasonable timeouts
	proxy.Transport = &http.Transport{
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 0,                 // Disable 100-Continue (send body immediately)
		ResponseHeaderTimeout: 120 * time.Second, // Wait up to 30s for response headers
		DisableKeepAlives:     false,             // Enable keep-alive for better performance
	}

	// Enable streaming with immediate flushing for SSE responses
	proxy.FlushInterval = -1 // Flush immediately for streaming responses

	// Customize proxy director to inject API key and rewrite paths
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = parsed.Host

		// Rewrite path to strip /api/provider/:provider prefix and handle Vertex AI paths
		// This converts Amp CLI paths to LiteLLM-compatible paths with model name mapping
		originalPath := req.URL.Path
		req.URL.Path = rewritePathForLiteLLM(req.URL.Path, cfg)

		// Extract provider name from original path for logging
		provider := extractProvider(originalPath)

		// Generate X-Request-ID for tracing if not present
		requestID := req.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = fmt.Sprintf("litellm-%d", time.Now().UnixNano())
			req.Header.Set("X-Request-ID", requestID)
		}

		// Inject LiteLLM API key if configured
		if cfg.LiteLLMAPIKey != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.LiteLLMAPIKey)
		}

		// Log request details with full URL
		fullURL := fmt.Sprintf("%s://%s%s", req.URL.Scheme, req.URL.Host, req.URL.Path)
		log.Debugf("[%s] LiteLLM passthrough: %s %s (provider=%s, rewritten from %s)",
			requestID, req.Method, fullURL, provider, originalPath)
		log.Debugf("[%s] Target URL: Scheme=%s, Host=%s, Path=%s, ContentLength=%d",
			requestID, req.URL.Scheme, req.URL.Host, req.URL.Path, req.ContentLength)

		// NOTE: Body reading removed - it breaks the reverse proxy's ability to forward requests
		// The body restoration with io.NopCloser causes the proxy to fail silently
	}

	// Error handler for proxy failures
	proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
		requestID := req.Header.Get("X-Request-ID")
		log.Errorf("[%s] LiteLLM passthrough proxy error for %s %s: %v", requestID, req.Method, req.URL.Path, err)
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusBadGateway)
		_, _ = rw.Write([]byte(`{"error":"litellm_passthrough_error","message":"Failed to reach LiteLLM proxy"}`))
	}

	// Response modifier disabled - logging response bodies was adding overhead
	// proxy.ModifyResponse = nil

	log.Infof("✨ LiteLLM passthrough mode ENABLED - ALL traffic routing to: %s", baseURL)

	// Return middleware that proxies matching routes
	return func(c *gin.Context) {
		path := c.Request.URL.Path

		// Only passthrough API routes, skip management routes (/api/auth, /api/user, etc.)
		// and other special routes like /management.html
		if shouldPassthrough(path) {
			log.Debugf("LiteLLM passthrough: handling %s %s", c.Request.Method, path)

			handledAbort := false
			defer func() {
				if rec := recover(); rec != nil {
					if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
						handledAbort = true
						log.Debugf("LiteLLM passthrough: client cancelled stream for %s %s", c.Request.Method, path)
						c.Abort()
						return
					}
					panic(rec)
				}
			}()

			proxy.ServeHTTP(c.Writer, c.Request)
			if !handledAbort {
				c.Abort() // Stop further processing
			}
		} else {
			log.Debugf("LiteLLM passthrough: skipping %s %s (management/special route)", c.Request.Method, path)
			c.Next() // Continue to normal handlers (management routes, etc.)
		}
	}
}

// shouldPassthrough determines if a request path should be forwarded to LiteLLM
func shouldPassthrough(path string) bool {
	// Routes to passthrough to LiteLLM
	passthroughPrefixes := []string{
		"/v1/chat/completions",
		"/v1/completions",
		"/v1/messages",
		"/v1/models",
		"/v1beta/models",
		"/api/provider/",
	}

	for _, prefix := range passthroughPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}

	return false
}

// rewritePathForLiteLLM strips the /api/provider/:provider prefix from paths
// and handles Vertex AI format paths, converting them to standard Gemini API format.
// It also applies model name mappings from the configuration.
//
// Examples:
//   - /api/provider/anthropic/v1/messages -> /v1/messages
//   - /api/provider/openai/v1/chat/completions -> /v1/chat/completions
//   - /api/provider/google/v1beta1/publishers/google/models/gemini-2.5-flash-preview-09-2025:generateContent
//     -> /v1beta/models/gemini-flash:generateContent (with model mapping)
//   - /v1/messages -> /v1/messages (unchanged)
func rewritePathForLiteLLM(path string, cfg *config.Config) string {
	// Handle Vertex AI Gemini paths: /v1beta1/publishers/google/models/{model}:{action}
	if strings.Contains(path, "/v1beta1/publishers/google/models/") {
		// Extract model and action from Vertex AI path
		// Example: /api/provider/google/v1beta1/publishers/google/models/gemini-2.5-flash-preview-09-2025:generateContent
		parts := strings.Split(path, "/models/")
		if len(parts) >= 2 {
			modelAndAction := parts[1] // gemini-2.5-flash-preview-09-2025:generateContent
			colonIndex := strings.Index(modelAndAction, ":")
			if colonIndex >= 0 {
				modelName := modelAndAction[:colonIndex] // gemini-2.5-flash-preview-09-2025
				action := modelAndAction[colonIndex:]    // :generateContent

				// Apply model name mapping if configured
				if cfg.LiteLLMModelMappings != nil {
					if mappedModel, found := cfg.LiteLLMModelMappings[modelName]; found {
						// Validate that mapped model name is not empty
						if strings.TrimSpace(mappedModel) == "" {
							log.Warnf("LiteLLM path rewrite: mapped model for %s is empty, using original", modelName)
						} else {
							log.Debugf("LiteLLM path rewrite: mapped model %s -> %s", modelName, mappedModel)
							modelName = mappedModel
						}
					}
				}

				// Validate final model name is not empty before constructing path
				if strings.TrimSpace(modelName) == "" {
					log.Warnf("LiteLLM path rewrite: model name is empty in Vertex AI path, returning original path")
					return path
				}

				// Convert to standard Gemini API path
				return "/v1beta/models/" + modelName + action
			}
		}
	}

	// Strip /api/provider/:provider prefix if present
	if strings.HasPrefix(path, "/api/provider/") {
		// Split: ["", "api", "provider", "anthropic", "v1/messages"]
		parts := strings.SplitN(path, "/", 5)
		if len(parts) >= 5 {
			remainingPath := "/" + parts[4] // /v1/messages or /v1beta1/publishers/...

			// If remaining path is Vertex AI format, recursively rewrite
			if strings.Contains(remainingPath, "/v1beta1/publishers/google/models/") {
				return rewritePathForLiteLLM(remainingPath, cfg)
			}

			// Return the last part with leading slash: /v1/messages
			return remainingPath
		}
	}

	// Return path unchanged if it doesn't match any pattern
	return path
}

// extractProvider extracts the provider name from the request path for logging purposes.
// Examples:
//   - /api/provider/anthropic/v1/messages -> "anthropic"
//   - /api/provider/google/v1beta1/... -> "google"
//   - /v1/messages -> "direct"
func extractProvider(path string) string {
	if strings.HasPrefix(path, "/api/provider/") {
		// Split: ["", "api", "provider", "anthropic", ...]
		parts := strings.SplitN(path, "/", 5)
		if len(parts) >= 4 {
			return parts[3] // Return provider name
		}
	}
	return "direct" // Direct API call without provider prefix
}
