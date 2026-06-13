package proxyutil

import (
	"net/http"
	"sync"
)

// transportPool caches *http.Transport instances keyed by proxy URL string.
// Since *http.Transport contains the connection pool (idle connections, TLS
// state, HTTP/2 framing), sharing a single Transport across callers that use
// the same proxy avoids creating a brand-new connection pool on every request.
//
// The zero value is ready to use.
type transportPool struct {
	mu         sync.Mutex
	transports map[string]*http.Transport
	direct     *http.Transport
}

var defaultPool transportPool

// SharedTransport returns a cached *http.Transport for the given proxy URL.
// If proxyURL is empty, it returns a transport that inherits the default
// proxy behavior (i.e. http.DefaultTransport). If proxyURL is "direct" or
// "none", it returns a transport that bypasses all proxies.
//
// The returned Transport must NOT be mutated by the caller; create a clone
// with Transport.Clone() if you need to modify it.
func SharedTransport(proxyURL string) *http.Transport {
	setting, _ := Parse(proxyURL)

	// ModeInherit: no custom transport needed, caller should use
	// http.DefaultTransport or a context-provided RoundTripper.
	if setting.Mode == ModeInherit {
		return nil
	}

	key := setting.Raw

	defaultPool.mu.Lock()
	defer defaultPool.mu.Unlock()

	if defaultPool.transports == nil {
		defaultPool.transports = make(map[string]*http.Transport)
	}

	if t, ok := defaultPool.transports[key]; ok {
		return t
	}

	t, _, errBuild := BuildHTTPTransport(proxyURL)
	if errBuild != nil || t == nil {
		return nil
	}

	defaultPool.transports[key] = t
	return t
}

// SharedDirectTransport returns a cached transport that bypasses environment proxies.
func SharedDirectTransport() *http.Transport {
	defaultPool.mu.Lock()
	defer defaultPool.mu.Unlock()

	if defaultPool.direct != nil {
		return defaultPool.direct
	}
	defaultPool.direct = NewDirectTransport()
	return defaultPool.direct
}

// SharedHTTPClient returns an *http.Client with a cached Transport for the
// given proxy URL. If proxyURL is empty, the client has no custom Transport
// set (it will use http.DefaultTransport). If a timeout > 0 is provided, the
// client's Timeout field is set accordingly.
//
// The client itself is lightweight; the important shared resource is the
// Transport inside it. Creating a new *http.Client per call is fine as long
// as the Transport is reused.
func SharedHTTPClient(proxyURL string, timeoutSeconds int) *http.Client {
	client := &http.Client{}
	if timeoutSeconds > 0 {
		client.Timeout = httpTimeoutDuration(timeoutSeconds)
	}
	if t := SharedTransport(proxyURL); t != nil {
		client.Transport = t
	}
	return client
}

// ResetTransportPool clears all cached transports. Useful for tests.
func ResetTransportPool() {
	defaultPool.mu.Lock()
	defer defaultPool.mu.Unlock()
	defaultPool.transports = nil
	defaultPool.direct = nil
}
