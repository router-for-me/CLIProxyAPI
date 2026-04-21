// Package util provides utility functions for the CLI Proxy API server.
// It includes helper functions for proxy configuration, HTTP client setup,
// log level management, and other common operations used across the application.
package util

import (
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

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

	pool, errCA := misc.CustomRootCAsFromEnv()
	if errCA != nil {
		log.Warnf("custom CA disabled: %v", errCA)
		pool = nil
	}

	if transport == nil && pool == nil {
		return httpClient
	}
	if transport == nil {
		transport = cloneDefaultTransport()
	}
	if pool != nil {
		if customTransport, ok := misc.RoundTripperWithCustomRootCAs(transport, pool).(*http.Transport); ok && customTransport != nil {
			transport = customTransport
		}
	}
	httpClient.Transport = transport
	return httpClient
}

func cloneDefaultTransport() *http.Transport {
	if transport, ok := http.DefaultTransport.(*http.Transport); ok && transport != nil {
		return transport.Clone()
	}
	return &http.Transport{}
}
