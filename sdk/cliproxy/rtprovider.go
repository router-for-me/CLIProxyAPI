package cliproxy

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
)

// defaultRoundTripperProvider returns a per-auth HTTP RoundTripper based on
// the Auth.ProxyURL value. It caches transports per proxy URL string.
type defaultRoundTripperProvider struct {
	mu    sync.RWMutex
	cache map[string]http.RoundTripper
}

func newDefaultRoundTripperProvider() *defaultRoundTripperProvider {
	return &defaultRoundTripperProvider{cache: make(map[string]http.RoundTripper)}
}

// RoundTripperFor implements coreauth.RoundTripperProvider.
func (p *defaultRoundTripperProvider) RoundTripperFor(auth *coreauth.Auth) http.RoundTripper {
	if auth == nil {
		return nil
	}
	proxyStr := strings.TrimSpace(auth.ProxyURL)
	if proxyStr == "" {
		return nil
	}
	p.mu.RLock()
	rt := p.cache[proxyStr]
	p.mu.RUnlock()
	if rt != nil {
		return rt
	}
	// Parse the proxy URL to determine the scheme.
	proxyURL, errParse := url.Parse(proxyStr)
	if errParse != nil {
		log.Errorf("parse proxy URL failed: %v", errParse)
		return nil
	}
	var transport *http.Transport
	// Handle different proxy schemes.
	if proxyURL.Scheme == "socks5" {
		// Configure SOCKS5 proxy with optional authentication.
		username := proxyURL.User.Username()
		password, _ := proxyURL.User.Password()
		proxyAuth := &proxy.Auth{User: username, Password: password}
		dialer, errSOCKS5 := proxy.SOCKS5("tcp", proxyURL.Host, proxyAuth, proxy.Direct)
		if errSOCKS5 != nil {
			log.Errorf("create SOCKS5 dialer failed: %v", errSOCKS5)
			return nil
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
	} else {
		log.Errorf("unsupported proxy scheme: %s", proxyURL.Scheme)
		return nil
	}
	p.mu.Lock()
	p.cache[proxyStr] = transport
	p.mu.Unlock()
	return transport
}
