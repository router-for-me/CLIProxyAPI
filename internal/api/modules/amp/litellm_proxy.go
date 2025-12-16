// litellm_proxy.go - LiteLLM reverse proxy with enhanced error handling.
// This file is part of our fork-specific features and should never conflict with upstream.
// See FORK_MAINTENANCE.md for architecture details.
package amp

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/api/util"
	log "github.com/sirupsen/logrus"
)

// CreateLiteLLMProxy creates a reverse proxy for LiteLLM with enhanced error handling.
// This is the primary constructor used by amp.go for LiteLLM integration.
func CreateLiteLLMProxy(liteLLMCfg *LiteLLMConfig) (*httputil.ReverseProxy, error) {
	baseURL := liteLLMCfg.GetBaseURL()
	if baseURL == "" {
		return nil, fmt.Errorf("litellm-base-url not configured")
	}

	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid litellm-base-url: %w", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(parsed)
	originalDirector := proxy.Director

	apiKey := liteLLMCfg.GetAPIKey()
	cfg := liteLLMCfg.GetConfig()

	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = parsed.Host

		// Generate/propagate request ID for distributed tracing (uses shared helper)
		requestID := GetOrGenerateRequestID(req)
		req.Header.Set("X-Request-ID", requestID)

		// Strip /api/provider/{provider} prefix and normalize path for LiteLLM
		originalPath := req.URL.Path
		path := req.URL.Path

		// First strip the /api/provider/{provider} prefix if present
		if strings.HasPrefix(path, "/api/provider/") {
			parts := strings.SplitN(path, "/", 5) // ["", "api", "provider", "{provider}", "rest..."]
			if len(parts) >= 5 {
				path = "/" + parts[4] // Stripped path
			} else if len(parts) == 4 {
				path = "/" // Just root if no path after provider
			}
		}

		// Apply LiteLLM-specific path transformations (Vertex AI -> standard Gemini format)
		if cfg != nil {
			req.URL.Path = util.RewritePathForLiteLLM(path, cfg)
		} else {
			req.URL.Path = path
		}

		// Inject LiteLLM API key if configured
		if apiKey != "" {
			req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiKey))
		}

		log.Debugf("litellm proxy: forwarding %s %s (original: %s)",
			req.Method, req.URL.Path, originalPath)
	}

	// Enhanced error handler using shared factory
	proxy.ErrorHandler = NewProxyErrorHandler("litellm", "litellm_proxy_error", "Failed to reach LiteLLM proxy")

	return proxy, nil
}
