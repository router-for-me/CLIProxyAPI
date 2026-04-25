package cliproxy

import (
	"net/http"
	"strings"
	"sync"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sync/singleflight"
)

// defaultRoundTripperProvider returns a per-auth HTTP RoundTripper based on
// the Auth.ProxyURL value. It caches transports per proxy URL string.
type defaultRoundTripperProvider struct {
	mu     sync.RWMutex
	cache  map[string]http.RoundTripper
	builds singleflight.Group
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

	result, errBuild, _ := p.builds.Do(proxyStr, func() (any, error) {
		p.mu.RLock()
		rt := p.cache[proxyStr]
		p.mu.RUnlock()
		if rt != nil {
			return rt, nil
		}

		transport, _, err := proxyutil.BuildHTTPTransport(proxyStr)
		if err != nil || transport == nil {
			return transport, err
		}

		p.mu.Lock()
		if existing := p.cache[proxyStr]; existing != nil {
			p.mu.Unlock()
			return existing, nil
		}
		p.cache[proxyStr] = transport
		p.mu.Unlock()
		return transport, nil
	})
	if errBuild != nil {
		log.Errorf("%v", errBuild)
		return nil
	}
	rt, _ = result.(http.RoundTripper)
	return rt
}
