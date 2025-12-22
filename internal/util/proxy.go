// Package util provides utility functions for the CLI Proxy API server.
// It includes helper functions for proxy configuration, HTTP client setup,
// log level management, and other common operations used across the application.
package util

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
)

var oauthProxyEnvVars = []string{
	"CLIPROXY_OAUTH_PROXY",
	"CLI_PROXY_API_OAUTH_PROXY",
}

// ResolveOAuthProxyURL returns the proxy URL to use for OAuth requests.
//
// Priority:
//  1. CLIPROXY_OAUTH_PROXY / CLI_PROXY_API_OAUTH_PROXY env vars
//  2. cfg.ProxyURL
//  3. empty string (net/http defaults apply, including HTTP(S)_PROXY env vars)
func ResolveOAuthProxyURL(cfg *config.SDKConfig) string {
	for _, key := range oauthProxyEnvVars {
		if v, ok := os.LookupEnv(key); ok {
			if trimmed := strings.TrimSpace(v); trimmed != "" {
				return trimmed
			}
		}
	}
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.ProxyURL)
}

// SetOAuthProxy configures the provided HTTP client with proxy settings for OAuth flows.
// It mirrors SetProxy but allows an OAuth-specific override via CLIPROXY_OAUTH_PROXY.
func SetOAuthProxy(cfg *config.SDKConfig, httpClient *http.Client) *http.Client {
	if httpClient == nil {
		httpClient = &http.Client{}
	}

	proxyURLRaw := ResolveOAuthProxyURL(cfg)
	if proxyURLRaw == "" {
		return httpClient
	}

	var transport *http.Transport

	// Attempt to parse the proxy URL from the configuration/env.
	proxyURL, errParse := url.Parse(proxyURLRaw)
	if errParse == nil {
		// Handle different proxy schemes.
		if proxyURL.Scheme == "socks5" {
			// Configure SOCKS5 proxy with optional authentication.
			var proxyAuth *proxy.Auth
			if proxyURL.User != nil {
				username := proxyURL.User.Username()
				password, _ := proxyURL.User.Password()
				proxyAuth = &proxy.Auth{User: username, Password: password}
			}
			dialer, errSOCKS5 := proxy.SOCKS5("tcp", proxyURL.Host, proxyAuth, proxy.Direct)
			if errSOCKS5 != nil {
				log.Errorf("create SOCKS5 dialer failed: %v", errSOCKS5)
				return httpClient
			}
			// Set up a custom transport using the SOCKS5 dialer.
			transport = &http.Transport{
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					return dialer.Dial(network, addr)
				},
			}
		} else if proxyURL.Scheme == "http" || proxyURL.Scheme == "https" {
			// Configure HTTP or HTTPS proxy.
			transport = &http.Transport{Proxy: http.ProxyURL(proxyURL)}
		}
	}

	// If a new transport was created, apply it to the HTTP client.
	if transport != nil {
		httpClient.Transport = transport
	}
	return httpClient
}

// SetProxy configures the provided HTTP client with proxy settings from the configuration.
// It supports SOCKS5, HTTP, and HTTPS proxies. The function modifies the client's transport
// to route requests through the configured proxy server.
func SetProxy(cfg *config.SDKConfig, httpClient *http.Client) *http.Client {
	var transport *http.Transport
	// Attempt to parse the proxy URL from the configuration.
	proxyURL, errParse := url.Parse(cfg.ProxyURL)
	if errParse == nil {
		// Handle different proxy schemes.
		if proxyURL.Scheme == "socks5" {
			// Configure SOCKS5 proxy with optional authentication.
			var proxyAuth *proxy.Auth
			if proxyURL.User != nil {
				username := proxyURL.User.Username()
				password, _ := proxyURL.User.Password()
				proxyAuth = &proxy.Auth{User: username, Password: password}
			}
			dialer, errSOCKS5 := proxy.SOCKS5("tcp", proxyURL.Host, proxyAuth, proxy.Direct)
			if errSOCKS5 != nil {
				log.Errorf("create SOCKS5 dialer failed: %v", errSOCKS5)
				return httpClient
			}
			// Set up a custom transport using the SOCKS5 dialer.
			transport = &http.Transport{
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					return dialer.Dial(network, addr)
				},
			}
		} else if proxyURL.Scheme == "http" || proxyURL.Scheme == "https" {
			// Configure HTTP or HTTPS proxy.
			transport = &http.Transport{Proxy: http.ProxyURL(proxyURL)}
		}
	}
	// If a new transport was created, apply it to the HTTP client.
	if transport != nil {
		httpClient.Transport = transport
	}
	return httpClient
}
