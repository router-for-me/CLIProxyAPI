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

	log "github.com/sirupsen/logrus"
)

// CreateLiteLLMProxy creates a reverse proxy for LiteLLM with enhanced error handling
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

	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = parsed.Host

		// Generate/propagate request ID for distributed tracing
		requestID := GetOrGenerateRequestID(req)
		req.Header.Set("X-Request-ID", requestID)

		// Strip /api/provider/{provider} prefix if present
		// Example: /api/provider/openai/v1/chat/completions -> /v1/chat/completions
		originalPath := req.URL.Path
		path := req.URL.Path
		if strings.HasPrefix(path, "/api/provider/") {
			parts := strings.SplitN(path, "/", 5) // ["", "api", "provider", "{provider}", "rest..."]
			if len(parts) >= 5 {
				req.URL.Path = "/" + parts[4]
			} else if len(parts) == 4 {
				req.URL.Path = "/"
			}
		}

		// Inject LiteLLM API key if configured
		if apiKey != "" {
			req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiKey))
		}

		log.Debugf("litellm proxy: forwarding %s %s (original: %s)",
			req.Method, req.URL.Path, originalPath)
	}

	// Enhanced error handler with classification
	proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
		LogProxyError("litellm", req, err)

		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusBadGateway)
		_, _ = rw.Write([]byte(`{"error":"litellm_proxy_error","message":"Failed to reach LiteLLM proxy"}`))
	}

	return proxy, nil
}
