// Package claude provides authentication functionality for Anthropic's Claude API.
// This file implements a custom HTTP transport using utls to bypass TLS fingerprinting.
package claude

import (
	"net/http"
	"net/url"
	"strings"
	"sync"

	tls "github.com/refraction-networking/utls"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/http2"
	"golang.org/x/net/proxy"
)

// utlsRoundTripper implements http.RoundTripper using utls with Firefox fingerprint
// to bypass Cloudflare's TLS fingerprinting on Anthropic domains.
type utlsRoundTripper struct {
	// mu protects the connections map and pending map
	mu sync.Mutex
	// connections caches HTTP/2 client connections per host
	connections map[string]*http2.ClientConn
	// pending tracks hosts that are currently being connected to (prevents race condition)
	pending map[string]*sync.Cond
	// dialer is used to create network connections, supporting proxies
	dialer proxy.Dialer
}

// newUtlsRoundTripper creates a new utls-based round tripper with optional proxy support
func newUtlsRoundTripper(cfg *config.SDKConfig) *utlsRoundTripper {
	var dialer proxy.Dialer = proxy.Direct
	if cfg != nil && cfg.ProxyURL != "" {
		proxyURL, err := url.Parse(cfg.ProxyURL)
		if err != nil {
			log.Errorf("failed to parse proxy URL %q: %v", cfg.ProxyURL, err)
		} else {
			pDialer, err := proxy.FromURL(proxyURL, proxy.Direct)
			if err != nil {
				log.Errorf("failed to create proxy dialer for %q: %v", cfg.ProxyURL, err)
			} else {
				dialer = pDialer
			}
		}
	}

	return &utlsRoundTripper{
		connections: make(map[string]*http2.ClientConn),
		pending:     make(map[string]*sync.Cond),
		dialer:      dialer,
	}
}

// getOrCreateConnection gets an existing connection or creates a new one.
// It uses a per-host locking mechanism to prevent multiple goroutines from
// creating connections to the same host simultaneously.
func (t *utlsRoundTripper) getOrCreateConnection(host, addr string) (*http2.ClientConn, error) {
	t.mu.Lock()

	// Check if connection exists and is usable
	if h2Conn, ok := t.connections[host]; ok && h2Conn.CanTakeNewRequest() {
		t.mu.Unlock()
		return h2Conn, nil
	}

	// Check if another goroutine is already creating a connection
	if cond, ok := t.pending[host]; ok {
		// Wait for the other goroutine to finish
		cond.Wait()
		// Check if connection is now available
		if h2Conn, ok := t.connections[host]; ok && h2Conn.CanTakeNewRequest() {
			t.mu.Unlock()
			return h2Conn, nil
		}
		// Connection still not available, we'll create one
	}

	// Mark this host as pending
	cond := sync.NewCond(&t.mu)
	t.pending[host] = cond
	t.mu.Unlock()

	// Create connection outside the lock
	h2Conn, err := t.createConnection(host, addr)

	t.mu.Lock()
	defer t.mu.Unlock()

	// Remove pending marker and wake up waiting goroutines
	delete(t.pending, host)
	cond.Broadcast()

	if err != nil {
		return nil, err
	}

	// Store the new connection
	t.connections[host] = h2Conn
	return h2Conn, nil
}

// createConnection creates a new HTTP/2 connection with Firefox TLS fingerprint
func (t *utlsRoundTripper) createConnection(host, addr string) (*http2.ClientConn, error) {
	conn, err := t.dialer.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{ServerName: host}
	tlsConn := tls.UClient(conn, tlsConfig, tls.HelloFirefox_Auto)

	if err := tlsConn.Handshake(); err != nil {
		conn.Close()
		return nil, err
	}

	tr := &http2.Transport{}
	h2Conn, err := tr.NewClientConn(tlsConn)
	if err != nil {
		tlsConn.Close()
		return nil, err
	}

	return h2Conn, nil
}

// RoundTrip implements http.RoundTripper
func (t *utlsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Host
	addr := host
	if !strings.Contains(addr, ":") {
		addr += ":443"
	}

	// Get hostname without port for TLS ServerName
	hostname := req.URL.Hostname()

	h2Conn, err := t.getOrCreateConnection(hostname, addr)
	if err != nil {
		return nil, err
	}

	resp, err := h2Conn.RoundTrip(req)
	if err != nil {
		// Connection failed, remove it from cache
		t.mu.Lock()
		if cached, ok := t.connections[hostname]; ok && cached == h2Conn {
			delete(t.connections, hostname)
		}
		t.mu.Unlock()
		return nil, err
	}

	return resp, nil
}

// NewAnthropicHttpClient creates an HTTP client that bypasses TLS fingerprinting
// for Anthropic domains by using utls with Firefox fingerprint.
// It accepts optional SDK configuration for proxy settings.
func NewAnthropicHttpClient(cfg *config.SDKConfig) *http.Client {
	return &http.Client{
		Transport: newUtlsRoundTripper(cfg),
	}
}
