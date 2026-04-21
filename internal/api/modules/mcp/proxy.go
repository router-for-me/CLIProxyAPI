package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	log "github.com/sirupsen/logrus"
)

func createReverseProxy(upstreamURL string, apiKeyProvider func() string) (*httputil.ReverseProxy, error) {
	parsed, err := url.Parse(upstreamURL)
	if err != nil {
		return nil, fmt.Errorf("invalid mcp upstream url: %w", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(parsed)
	originalDirector := proxy.Director

	proxy.Director = func(req *http.Request) {
		req.URL.Path = stripMCPPrefix(req.URL.Path)
		if req.URL.RawPath != "" {
			req.URL.RawPath = stripMCPPrefix(req.URL.RawPath)
		}
		originalDirector(req)
		req.Host = parsed.Host

		req.Header.Del("Authorization")
		req.Header.Del("X-Api-Key")
		req.Header.Del("X-Goog-Api-Key")
		misc.ScrubProxyAndFingerprintHeaders(req)

		if apiKeyProvider == nil {
			return
		}
		if key := strings.TrimSpace(apiKeyProvider()); key != "" {
			req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", key))
			req.Header.Set("X-Api-Key", key)
		}
	}

	proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
		if errors.Is(err, context.Canceled) {
			return
		}
		log.Errorf("mcp upstream proxy error for %s %s: %v", req.Method, req.URL.Path, err)
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusBadGateway)
		_, _ = rw.Write([]byte(`{"error":"mcp_upstream_proxy_error","message":"Failed to reach MCP upstream"}`))
	}

	return proxy, nil
}

func stripMCPPrefix(path string) string {
	if path == "" {
		return "/"
	}
	if path == "/mcp" || path == "/mcp/" {
		return "/"
	}
	if strings.HasPrefix(path, "/mcp/") {
		return "/" + strings.TrimPrefix(path, "/mcp/")
	}
	return path
}
