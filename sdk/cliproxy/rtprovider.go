package cliproxy

import (
	"net/http"
	"strings"
	"sync"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

type proxyLookupFunc func(authID string) string

type defaultRoundTripperProvider struct {
	mu          sync.RWMutex
	cache       map[string]http.RoundTripper
	proxyLookup proxyLookupFunc
}

func newDefaultRoundTripperProvider(lookup proxyLookupFunc) *defaultRoundTripperProvider {
	return &defaultRoundTripperProvider{cache: make(map[string]http.RoundTripper), proxyLookup: lookup}
}

func (p *defaultRoundTripperProvider) RoundTripperFor(auth *coreauth.Auth) http.RoundTripper {
	if auth == nil {
		return nil
	}
	var proxyStr string
	if p.proxyLookup != nil {
		proxyStr = p.proxyLookup(auth.ID)
	}
	if proxyStr == "" {
		proxyStr = strings.TrimSpace(auth.ProxyURL)
	}
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
