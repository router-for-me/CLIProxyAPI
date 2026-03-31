// Package claude provides authentication functionality for Anthropic's Claude API.
// This file implements a custom HTTP transport using utls to mimic Bun's BoringSSL
// TLS fingerprint, matching the real Claude Code CLI.
package claude

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	tls "github.com/refraction-networking/utls"
)

// utlsRoundTripper implements http.RoundTripper using utls with Bun BoringSSL
// fingerprint to match the real Claude Code CLI's TLS characteristics.
//
// It uses HTTP/1.1 (Bun's ALPN only offers http/1.1) and delegates connection
// pooling to the standard http.Transport.
//
// Proxy support is handled by ProxyDialer (see proxy_dial.go), which establishes
// raw TCP connections through SOCKS5 or HTTP/HTTPS CONNECT proxies before this
// layer applies the utls TLS handshake.
type utlsRoundTripper struct {
	transport *http.Transport
	dialer    *ProxyDialer // handles proxy tunneling for raw TCP connections
}

// newUtlsRoundTripper creates a new utls-based round tripper with optional proxy support.
// The proxyURL parameter is the pre-resolved proxy URL string; an empty string means
// inherit proxy from environment variables (HTTPS_PROXY, HTTP_PROXY, ALL_PROXY).
func newUtlsRoundTripper(proxyURL string) *utlsRoundTripper {
	rt := &utlsRoundTripper{dialer: NewProxyDialer(proxyURL)}

	rt.transport = &http.Transport{
		DialTLSContext:        rt.dialTLS,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return rt
}

// dialTLS establishes a TLS connection using utls with the Bun BoringSSL spec.
// It delegates raw TCP dialing (including proxy tunneling) to ProxyDialer,
// then performs the utls handshake on the resulting connection.
func (t *utlsRoundTripper) dialTLS(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}

	// Step 1: Establish raw TCP connection (proxy tunneling handled internally).
	conn, err := t.dialer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}

	// Step 2: Propagate context deadline to TLS handshake to prevent indefinite hangs.
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
		defer conn.SetDeadline(time.Time{})
	}

	// Step 3: TLS handshake with Bun BoringSSL fingerprint.
	tlsConfig := &tls.Config{ServerName: host}
	tlsConn := tls.UClient(conn, tlsConfig, tls.HelloCustom)
	if err := tlsConn.ApplyPreset(BunBoringSSLSpec()); err != nil {
		conn.Close()
		return nil, fmt.Errorf("apply Bun TLS spec: %w", err)
	}
	if err := tlsConn.Handshake(); err != nil {
		conn.Close()
		return nil, err
	}

	return tlsConn, nil
}

// RoundTrip implements http.RoundTripper.
func (t *utlsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.transport.RoundTrip(req)
}

// anthropicClients caches *http.Client instances keyed by proxyURL string.
// Each unique proxyURL gets a single shared client whose http.Transport maintains
// its own idle connection pool — this avoids a full TLS handshake per request.
// The number of unique proxy URLs is typically very small (1-3), so entries are
// never evicted.
var anthropicClients sync.Map // map[string]*http.Client

// NewAnthropicHttpClient returns a cached HTTP client that uses Bun BoringSSL TLS
// fingerprint for all connections, matching real Claude Code CLI behavior.
//
// Clients are cached per proxyURL so that the underlying http.Transport connection
// pool is reused across requests with the same proxy configuration.
//
// The proxyURL parameter is the pre-resolved proxy URL (e.g. from ResolveProxyURL).
// Pass an empty string to inherit proxy from environment variables.
func NewAnthropicHttpClient(proxyURL string) *http.Client {
	if cached, ok := anthropicClients.Load(proxyURL); ok {
		return cached.(*http.Client)
	}
	client := &http.Client{
		Transport: newUtlsRoundTripper(proxyURL),
	}
	actual, _ := anthropicClients.LoadOrStore(proxyURL, client)
	return actual.(*http.Client)
}
