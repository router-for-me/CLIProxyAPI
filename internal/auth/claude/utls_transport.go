// Package claude provides authentication functionality for Anthropic's Claude API.
// This file implements a custom HTTP transport using utls to bypass TLS fingerprinting.
package claude

import (
	"net"
	"net/http"
	"sync"
	"time"

	tls "github.com/refraction-networking/utls"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/http2"
	"golang.org/x/net/proxy"
)

// utlsRoundTripper implements http.RoundTripper using utls with Chrome fingerprint
// to bypass Cloudflare's TLS fingerprinting on Anthropic domains.
type utlsRoundTripper struct {
	// mu protects the connections map and pending map
	mu sync.Mutex
	// connections caches HTTP/2 client connections per host
	connections map[string][]*utlsClientConn
	// pending tracks hosts that are currently being connected to (prevents race condition)
	pending map[string]*sync.Cond
	// dialer is used to create network connections, supporting proxies
	dialer proxy.Dialer
}

type utlsClientConn struct {
	conn     *http2.ClientConn
	lastUsed time.Time
}

const (
	utlsMaxConnsPerHost = 2
	utlsIdleConnTimeout = proxyutil.DefaultIdleConnTimeout
)

// newUtlsRoundTripper creates a new utls-based round tripper with optional proxy support
func newUtlsRoundTripper(cfg *config.SDKConfig) *utlsRoundTripper {
	var dialer proxy.Dialer = proxy.Direct
	if cfg != nil {
		proxyDialer, mode, errBuild := proxyutil.BuildDialer(cfg.ProxyURL)
		if errBuild != nil {
			log.Errorf("failed to configure proxy dialer for %q: %v", cfg.ProxyURL, errBuild)
		} else if mode != proxyutil.ModeInherit && proxyDialer != nil {
			dialer = proxyDialer
		}
	}

	return &utlsRoundTripper{
		connections: make(map[string][]*utlsClientConn),
		pending:     make(map[string]*sync.Cond),
		dialer:      dialer,
	}
}

// getOrCreateConnection gets an existing connection or creates a new one.
// It uses a per-host locking mechanism to prevent multiple goroutines from
// creating connections to the same host simultaneously.
func (t *utlsRoundTripper) getOrCreateConnection(host, addr string) (*http2.ClientConn, error) {
	t.mu.Lock()
	for {
		now := time.Now()
		t.pruneIdleConnectionsLocked(host, now)

		if h2Conn := t.availableConnectionLocked(host, now); h2Conn != nil {
			t.mu.Unlock()
			return h2Conn, nil
		}

		if h2Conn := t.fallbackConnectionLocked(host, now); h2Conn != nil {
			t.mu.Unlock()
			return h2Conn, nil
		}

		if cond, ok := t.pending[host]; ok {
			cond.Wait()
			continue
		}

		cond := sync.NewCond(&t.mu)
		t.pending[host] = cond
		t.mu.Unlock()

		h2Conn, err := t.createConnection(host, addr)

		t.mu.Lock()
		delete(t.pending, host)
		cond.Broadcast()

		if err != nil {
			t.mu.Unlock()
			return nil, err
		}

		t.connections[host] = append(t.connections[host], &utlsClientConn{conn: h2Conn, lastUsed: time.Now()})
		t.mu.Unlock()
		return h2Conn, nil
	}
}

func (t *utlsRoundTripper) availableConnectionLocked(host string, now time.Time) *http2.ClientConn {
	for _, entry := range t.connections[host] {
		if entry == nil || entry.conn == nil {
			continue
		}
		if entry.conn.CanTakeNewRequest() {
			entry.lastUsed = now
			return entry.conn
		}
	}
	return nil
}

func (t *utlsRoundTripper) fallbackConnectionLocked(host string, now time.Time) *http2.ClientConn {
	conns := t.connections[host]
	if len(conns) < utlsMaxConnsPerHost {
		return nil
	}
	for _, entry := range conns {
		if entry == nil || entry.conn == nil {
			continue
		}
		state := entry.conn.State()
		if state.Closed || state.Closing {
			continue
		}
		entry.lastUsed = now
		return entry.conn
	}
	return nil
}

func (t *utlsRoundTripper) pruneIdleConnectionsLocked(host string, now time.Time) {
	conns := t.connections[host]
	if len(conns) == 0 {
		return
	}
	kept := conns[:0]
	for _, entry := range conns {
		if entry == nil || entry.conn == nil {
			continue
		}
		state := entry.conn.State()
		if state.Closed || state.Closing {
			entry.conn.Close()
			continue
		}
		if state.StreamsActive == 0 && state.StreamsReserved == 0 && state.StreamsPending == 0 {
			lastIdle := entry.lastUsed
			if !state.LastIdle.IsZero() {
				lastIdle = state.LastIdle
			}
			if now.Sub(lastIdle) > utlsIdleConnTimeout {
				entry.conn.Close()
				continue
			}
		}
		kept = append(kept, entry)
	}
	clear(conns[len(kept):])
	if len(kept) == 0 {
		delete(t.connections, host)
		return
	}
	t.connections[host] = kept
}

func (t *utlsRoundTripper) removeConnection(host string, target *http2.ClientConn) {
	if target == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	conns := t.connections[host]
	kept := conns[:0]
	for _, entry := range conns {
		if entry == nil || entry.conn == nil {
			continue
		}
		if entry.conn == target {
			entry.conn.Close()
			continue
		}
		kept = append(kept, entry)
	}
	clear(conns[len(kept):])
	if len(kept) == 0 {
		delete(t.connections, host)
		return
	}
	t.connections[host] = kept
}

// createConnection creates a new HTTP/2 connection with Chrome TLS fingerprint.
// Chrome's TLS fingerprint is closer to Node.js/OpenSSL (which real Claude Code uses)
// than Firefox, reducing the mismatch between TLS layer and HTTP headers.
func (t *utlsRoundTripper) createConnection(host, addr string) (*http2.ClientConn, error) {
	conn, err := t.dialer.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{ServerName: host}
	tlsConn := tls.UClient(conn, tlsConfig, tls.HelloChrome_Auto)

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
	hostname := req.URL.Hostname()
	port := req.URL.Port()
	if port == "" {
		port = "443"
	}
	addr := net.JoinHostPort(hostname, port)

	h2Conn, err := t.getOrCreateConnection(hostname, addr)
	if err != nil {
		return nil, err
	}

	resp, err := h2Conn.RoundTrip(req)
	if err != nil {
		t.removeConnection(hostname, h2Conn)
		return nil, err
	}

	return resp, nil
}

// NewAnthropicHttpClient creates an HTTP client that bypasses TLS fingerprinting
// for Anthropic domains by using utls with Chrome fingerprint.
// It accepts optional SDK configuration for proxy settings.
func NewAnthropicHttpClient(cfg *config.SDKConfig) *http.Client {
	return &http.Client{
		Transport: newUtlsRoundTripper(cfg),
	}
}
