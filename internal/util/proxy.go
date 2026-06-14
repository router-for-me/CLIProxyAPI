// Package util provides utility functions for the CLI Proxy API server.
// It includes helper functions for proxy configuration, HTTP client setup,
// log level management, and other common operations used across the application.
package util

import (
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

// SetProxy configures the provided HTTP client with proxy settings from the configuration.
// It supports SOCKS5, HTTP, and HTTPS proxies. The function modifies the client's transport
// to route requests through the configured proxy server.
// It uses a shared Transport pool so that callers reusing the same proxy URL
// share the same underlying connection pool instead of creating a new one each time.
func SetProxy(cfg *config.SDKConfig, httpClient *http.Client) *http.Client {
	if cfg == nil || httpClient == nil {
		return httpClient
	}

	if t := proxyutil.SharedTransport(cfg.ProxyURL); t != nil {
		httpClient.Transport = t
	}
	return httpClient
}
