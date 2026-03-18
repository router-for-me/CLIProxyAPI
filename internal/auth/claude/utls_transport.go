// Package claude provides authentication functionality for Anthropic's Claude API.
// This file implements a custom HTTP transport using utls to mimic Bun's BoringSSL
// TLS fingerprint, matching the real Claude Code CLI.
package claude

import (
	"context"
	"fmt"
	"net"
	"net/http"

	tls "github.com/refraction-networking/utls"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
)

// utlsRoundTripper implements http.RoundTripper using utls with Bun BoringSSL
// fingerprint to match the real Claude Code CLI's TLS characteristics.
//
// It uses HTTP/1.1 (Bun's ALPN only offers http/1.1) and delegates connection
// pooling to the standard http.Transport.
type utlsRoundTripper struct {
	transport *http.Transport
	dialer    proxy.Dialer
}

// newUtlsRoundTripper creates a new utls-based round tripper with optional proxy support.
// The proxyURL parameter is the pre-resolved proxy URL string; an empty string means
// no proxy (direct connection).
func newUtlsRoundTripper(proxyURL string) *utlsRoundTripper {
	var dialer proxy.Dialer = proxy.Direct
	if proxyURL != "" {
		proxyDialer, mode, errBuild := proxyutil.BuildDialer(proxyURL)
		if errBuild != nil {
			log.Errorf("failed to configure proxy dialer for %q: %v", proxyURL, errBuild)
		} else if mode != proxyutil.ModeInherit && proxyDialer != nil {
			dialer = proxyDialer
		}
	}

	rt := &utlsRoundTripper{dialer: dialer}
	rt.transport = &http.Transport{
		// Inject our custom TLS dial function so every connection uses
		// the Bun BoringSSL fingerprint via utls.
		DialTLSContext: rt.dialTLS,
		// Force HTTP/1.1 — do not attempt h2 upgrade.
		ForceAttemptHTTP2: false,
	}
	return rt
}

// dialTLS establishes a TLS connection using utls with the Bun BoringSSL spec.
// This is called by http.Transport for every new TLS connection.
func (t *utlsRoundTripper) dialTLS(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// addr might not have a port; use as-is for ServerName
		host = addr
	}

	conn, err := t.dialer.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}

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

// RoundTrip implements http.RoundTripper by delegating to the underlying
// http.Transport which uses our custom TLS dial function.
func (t *utlsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.transport.RoundTrip(req)
}

// NewAnthropicHttpClient creates an HTTP client that uses Bun BoringSSL TLS
// fingerprint for all connections, matching real Claude Code CLI behavior.
//
// The proxyURL parameter is the pre-resolved proxy URL (e.g. from ResolveProxyURL).
// Pass an empty string for direct connections.
func NewAnthropicHttpClient(proxyURL string) *http.Client {
	return &http.Client{
		Transport: newUtlsRoundTripper(proxyURL),
	}
}

