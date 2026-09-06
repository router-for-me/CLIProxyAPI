package cliproxy

import (
	"net/http"
	"strings"
	"sync"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
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
	// Relay-bound auths come first: a relay base replaces proxy semantics
	// entirely. Cache key is prefixed so relay URLs never collide with
	// proxy URLs sharing the same string.
	if relayBase := strings.TrimSpace(auth.RelayBaseURL); relayBase != "" {
		key := "relay:" + relayBase
		p.mu.RLock()
		rt := p.cache[key]
		p.mu.RUnlock()
		if rt != nil {
			return rt
		}
		relayRT, errRelay := proxyutil.NewRelayTransport(relayBase)
		if errRelay != nil {
			log.Errorf("%v", errRelay)
			return nil
		}
		p.mu.Lock()
		p.cache[key] = relayRT
		p.mu.Unlock()
		return relayRT
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
	transport, _, errBuild := proxyutil.BuildHTTPTransport(proxyStr)
	if errBuild != nil {
		log.Errorf("%v", errBuild)
		return nil
	}
	if transport == nil {
		return nil
	}
	p.mu.Lock()
	p.cache[proxyStr] = transport
	p.mu.Unlock()
	return transport
}
