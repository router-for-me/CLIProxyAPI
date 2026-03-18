// Package claude provides authentication functionality for Anthropic's Claude API.
// This file implements a custom HTTP transport using utls to mimic Bun's BoringSSL
// TLS fingerprint, matching the real Claude Code CLI.
package claude

import (
	"bufio"
	"context"
	stdtls "crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

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
//
// Proxy support: SOCKS5 proxies are handled at the TCP dial layer.
// HTTP/HTTPS proxies use CONNECT tunneling before the TLS handshake.
// When no explicit proxy is configured, HTTPS_PROXY/HTTP_PROXY/ALL_PROXY
// environment variables are respected (via http.ProxyFromEnvironment).
type utlsRoundTripper struct {
	transport *http.Transport
	proxyURL  *url.URL       // explicit proxy URL (nil = check env per-request)
	proxyMode proxyutil.Mode // inherit (use env), direct, proxy, or invalid
}

// newUtlsRoundTripper creates a new utls-based round tripper with optional proxy support.
// The proxyURL parameter is the pre-resolved proxy URL string; an empty string means
// inherit proxy from environment variables (HTTPS_PROXY, HTTP_PROXY, ALL_PROXY).
func newUtlsRoundTripper(proxyURL string) *utlsRoundTripper {
	rt := &utlsRoundTripper{proxyMode: proxyutil.ModeInherit}

	if proxyURL != "" {
		setting, errParse := proxyutil.Parse(proxyURL)
		if errParse != nil {
			log.Errorf("failed to parse proxy URL %q: %v", proxyURL, errParse)
		} else {
			rt.proxyMode = setting.Mode
			rt.proxyURL = setting.URL
		}
	}

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

// resolveProxy returns the effective proxy URL for a given target address.
// For explicit proxy configuration, it returns the configured URL directly.
// For inherit mode, it delegates to http.ProxyFromEnvironment which correctly
// handles HTTPS_PROXY, HTTP_PROXY, NO_PROXY (including CIDR and wildcards).
func (t *utlsRoundTripper) resolveProxy(targetHost string) *url.URL {
	switch t.proxyMode {
	case proxyutil.ModeDirect:
		return nil
	case proxyutil.ModeProxy:
		return t.proxyURL
	default:
		// ModeInherit: delegate to Go's standard proxy resolution which
		// reads HTTPS_PROXY, HTTP_PROXY, ALL_PROXY and respects NO_PROXY.
		req := &http.Request{URL: &url.URL{Scheme: "https", Host: targetHost}}
		proxyURL, _ := http.ProxyFromEnvironment(req)
		return proxyURL
	}
}

// dialTLS establishes a TLS connection using utls with the Bun BoringSSL spec.
// It handles proxy tunneling (SOCKS5 and HTTP CONNECT) before the TLS handshake,
// and respects context cancellation/deadline throughout.
func (t *utlsRoundTripper) dialTLS(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}

	proxyURL := t.resolveProxy(host)

	// Establish raw TCP connection — either direct, via SOCKS5, or via HTTP CONNECT.
	var conn net.Conn
	switch {
	case proxyURL == nil:
		conn, err = (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	case proxyURL.Scheme == "socks5" || proxyURL.Scheme == "socks5h":
		conn, err = dialViaSocks5(ctx, proxyURL, addr)
	case proxyURL.Scheme == "http" || proxyURL.Scheme == "https":
		conn, err = dialViaHTTPConnect(ctx, proxyURL, addr)
	default:
		return nil, fmt.Errorf("unsupported proxy scheme: %s", proxyURL.Scheme)
	}
	if err != nil {
		return nil, err
	}

	// Propagate context deadline to TLS handshake to prevent indefinite hangs.
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
		defer conn.SetDeadline(time.Time{})
	}

	// TLS handshake with Bun BoringSSL fingerprint.
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

// dialViaSocks5 establishes a TCP connection through a SOCKS5 proxy.
func dialViaSocks5(ctx context.Context, proxyURL *url.URL, targetAddr string) (net.Conn, error) {
	var auth *proxy.Auth
	if proxyURL.User != nil {
		username := proxyURL.User.Username()
		password, _ := proxyURL.User.Password()
		auth = &proxy.Auth{User: username, Password: password}
	}
	dialer, err := proxy.SOCKS5("tcp", proxyURL.Host, auth, proxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("create SOCKS5 dialer: %w", err)
	}
	if cd, ok := dialer.(proxy.ContextDialer); ok {
		return cd.DialContext(ctx, "tcp", targetAddr)
	}
	return dialer.Dial("tcp", targetAddr)
}

// dialViaHTTPConnect establishes a TCP tunnel through an HTTP proxy using CONNECT.
// The proxy connection itself is plain TCP (for http:// proxies) or TLS (for https://).
func dialViaHTTPConnect(ctx context.Context, proxyURL *url.URL, targetAddr string) (net.Conn, error) {
	proxyAddr := proxyURL.Host
	// Ensure the proxy address has a port; default to 80/443 based on scheme.
	if _, _, err := net.SplitHostPort(proxyAddr); err != nil {
		if proxyURL.Scheme == "https" {
			proxyAddr = net.JoinHostPort(proxyAddr, "443")
		} else {
			proxyAddr = net.JoinHostPort(proxyAddr, "80")
		}
	}

	// Connect to the proxy.
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("dial proxy %s: %w", proxyAddr, err)
	}

	// HTTPS proxies require a TLS handshake with the proxy itself before
	// sending the CONNECT request. We use standard crypto/tls here (not utls)
	// because this is the proxy connection — fingerprint mimicry is only
	// needed for the final connection to api.anthropic.com.
	if proxyURL.Scheme == "https" {
		proxyHost := proxyURL.Hostname()
		tlsConn := stdtls.Client(conn, &stdtls.Config{ServerName: proxyHost})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, fmt.Errorf("TLS to proxy %s: %w", proxyAddr, err)
		}
		conn = tlsConn
	}

	// Propagate context deadline to the CONNECT handshake so it cannot hang
	// indefinitely if the proxy accepts TCP but never responds.
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
		defer conn.SetDeadline(time.Time{})
	}

	// Send CONNECT request.
	hdr := make(http.Header)
	hdr.Set("Host", targetAddr)
	if proxyURL.User != nil {
		username := proxyURL.User.Username()
		password, _ := proxyURL.User.Password()
		cred := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
		hdr.Set("Proxy-Authorization", "Basic "+cred)
	}
	connectReq := &http.Request{
		Method: "CONNECT",
		URL:    &url.URL{Opaque: targetAddr},
		Host:   targetAddr,
		Header: hdr,
	}
	if err := connectReq.Write(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("write CONNECT: %w", err)
	}

	// Read CONNECT response. Use a bufio.Reader, then check for buffered
	// bytes to avoid data loss if the proxy sent anything beyond the header.
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, connectReq)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read CONNECT response: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		conn.Close()
		return nil, fmt.Errorf("CONNECT to %s via %s: %s", targetAddr, proxyAddr, resp.Status)
	}

	// If the bufio.Reader consumed extra bytes beyond the HTTP response,
	// wrap the connection so those bytes are read first.
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: conn, br: br}, nil
	}
	return conn, nil
}

// bufferedConn wraps a net.Conn with a bufio.Reader to drain any bytes
// that were buffered during the HTTP CONNECT handshake.
type bufferedConn struct {
	net.Conn
	br *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	if c.br.Buffered() > 0 {
		return c.br.Read(p)
	}
	return c.Conn.Read(p)
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
