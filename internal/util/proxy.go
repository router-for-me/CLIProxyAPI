// Package util provides utility functions for the CLI Proxy API server.
// It includes helper functions for proxy configuration, HTTP client setup,
// log level management, and other common operations used across the application.
package util

import (
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// ResolveProxyURL returns the effective proxy for a provider without a credential.
// Resolution priority:
//  1. proxy-by-provider[provider]
//  2. global proxy-url
//  3. empty string (inherit environment / direct)
//
// Values may be a proxy URL or the literals "direct"/"none"; they are returned
// verbatim so downstream transport construction can honor them.
func ResolveProxyURL(cfg *config.SDKConfig, provider string) string {
	if cfg == nil {
		return ""
	}

	key := strings.ToLower(strings.TrimSpace(provider))
	if key != "" && len(cfg.ProxyByProvider) > 0 {
		if proxyURL := strings.TrimSpace(cfg.ProxyByProvider[key]); proxyURL != "" {
			return proxyURL
		}
	}

	return strings.TrimSpace(cfg.ProxyURL)
}

// SetProxy configures the provided HTTP client with proxy settings from the configuration.
// It supports SOCKS5, HTTP, and HTTPS proxies. The function modifies the client's transport
// to route requests through the configured proxy server.
func SetProxy(cfg *config.SDKConfig, httpClient *http.Client) *http.Client {
	if cfg == nil || httpClient == nil {
		return httpClient
	}

	transport, _, errBuild := proxyutil.BuildHTTPTransport(cfg.ProxyURL)
	if errBuild != nil {
		log.Errorf("%v", errBuild)
	}
	if transport != nil {
		httpClient.Transport = transport
	}
	return httpClient
}
