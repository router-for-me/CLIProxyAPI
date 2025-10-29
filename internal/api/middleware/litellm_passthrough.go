// Package middleware provides HTTP middleware components for the CLI Proxy API server.
package middleware

import (
	"fmt"
	"net"
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

	// Create reverse proxy to LiteLLM with custom transport for timeouts
	proxy := httputil.NewSingleHostReverseProxy(parsed)
	
	// Configure transport for streaming: timeouts only on connection establishment,
	// NO timeouts on response reading to allow indefinite streaming
	proxy.Transport = &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,  // Timeout for establishing connection
			KeepAlive: 30 * time.Second,  // Keep TCP connection alive
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,  // Timeout for TLS handshake
		ExpectContinueTimeout: 1 * time.Second,   // Timeout for 100-continue
		IdleConnTimeout:       90 * time.Second,  // Close idle connections after 90s
		// NO ResponseHeaderTimeout - allows streaming to take as long as needed
		// NO Timeout on request/response - let client control disconnection
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
	}
	
	originalDirector := proxy.Director

	// Customize proxy director to inject API key and rewrite paths
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = parsed.Host

		// Rewrite path to strip /api/provider/:provider prefix
		// This converts Amp CLI paths to LiteLLM-compatible paths
		originalPath := req.URL.Path
		req.URL.Path = rewritePathForLiteLLM(req.URL.Path)

		// Inject LiteLLM API key if configured
		if cfg.LiteLLMAPIKey != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.LiteLLMAPIKey)
		}

		// Preserve X-Request-ID for tracing
		if req.Header.Get("X-Request-ID") == "" {
			// Could generate one here if needed
		}

		log.Debugf("LiteLLM passthrough: %s %s -> %s%s (rewritten from %s)", req.Method, req.URL.Path, baseURL, req.URL.Path, originalPath)
	}

	// Error handler for proxy failures
	proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
		log.Errorf("LiteLLM passthrough proxy error for %s %s: %v", req.Method, req.URL.Path, err)
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusBadGateway)
		_, _ = rw.Write([]byte(`{"error":"litellm_passthrough_error","message":"Failed to reach LiteLLM proxy"}`))
	}

	log.Infof("✨ LiteLLM passthrough mode ENABLED - ALL traffic routing to: %s", baseURL)

	// Return middleware that proxies matching routes
	return func(c *gin.Context) {
		path := c.Request.URL.Path

		// Only passthrough API routes, skip management routes (/api/auth, /api/user, etc.)
		// and other special routes like /management.html
		if shouldPassthrough(path) {
			log.Debugf("LiteLLM passthrough: handling %s %s", c.Request.Method, path)
			
			// Catch ErrAbortHandler panic from reverse proxy (client disconnect)
			defer func() {
				if err := recover(); err != nil {
					if err == http.ErrAbortHandler {
						// Client disconnected - this is expected for cancelled requests
						log.Debugf("LiteLLM passthrough: client disconnected for %s %s", c.Request.Method, path)
						c.Abort()
					} else {
						// Re-panic for other errors
						panic(err)
					}
				}
			}()
			
			proxy.ServeHTTP(c.Writer, c.Request)
			c.Abort() // Stop further processing
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
// before forwarding to LiteLLM, since LiteLLM expects standard OpenAI-compatible paths.
//
// Examples:
//   - /api/provider/anthropic/v1/messages -> /v1/messages
//   - /api/provider/openai/v1/chat/completions -> /v1/chat/completions
//   - /v1/messages -> /v1/messages (unchanged)
func rewritePathForLiteLLM(path string) string {
	// Strip /api/provider/:provider prefix if present
	if strings.HasPrefix(path, "/api/provider/") {
		// Split: ["", "api", "provider", "anthropic", "v1/messages"]
		parts := strings.SplitN(path, "/", 5)
		if len(parts) >= 5 {
			// Return the last part with leading slash: /v1/messages
			return "/" + parts[4]
		}
	}
	// Return path unchanged if it doesn't match the pattern
	return path
}
