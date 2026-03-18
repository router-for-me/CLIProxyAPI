// Package claude provides authentication functionality for Anthropic's Claude API.
// This file implements a custom HTTP transport using utls to mimic Bun's BoringSSL
// TLS fingerprint, matching the real Claude Code CLI.
package claude

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
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
// environment variables are respected.
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
		// Inject our custom TLS dial function so every connection uses
		// the Bun BoringSSL fingerprint via utls.
		DialTLSContext: rt.dialTLS,
		// Force HTTP/1.1 — do not attempt h2 upgrade.
		ForceAttemptHTTP2: false,
	}
	return rt
}

// resolveProxy returns the effective proxy URL for a given target address.
// It respects explicit configuration and falls back to environment variables.
func (t *utlsRoundTripper) resolveProxy(targetAddr string) *url.URL {
	switch t.proxyMode {
	case proxyutil.ModeDirect:
		return nil
	case proxyutil.ModeProxy:
		return t.proxyURL
	default:
		// ModeInherit: check environment variables (HTTPS_PROXY, HTTP_PROXY, ALL_PROXY)
		return proxyFromEnv(targetAddr)
	}
}

// proxyFromEnv reads proxy settings from standard environment variables.
// It checks HTTPS_PROXY (and https_proxy), HTTP_PROXY (and http_proxy),
// and ALL_PROXY (and all_proxy), matching http.Transport's default behavior.
func proxyFromEnv(targetAddr string) *url.URL {
	host, _, _ := net.SplitHostPort(targetAddr)
	if host == "" {
		host = targetAddr
	}

	// Respect NO_PROXY
	noProxy := os.Getenv("NO_PROXY")
	if noProxy == "" {
		noProxy = os.Getenv("no_proxy")
	}
	if noProxy == "*" {
		return nil
	}
	for _, pattern := range strings.Split(noProxy, ",") {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if strings.HasPrefix(pattern, ".") {
			if strings.HasSuffix(host, pattern) || host == pattern[1:] {
				return nil
			}
		} else if host == pattern {
			return nil
		}
	}

	// All our targets are HTTPS, so prefer HTTPS_PROXY
	for _, env := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		if v := os.Getenv(env); v != "" {
			if u, err := url.Parse(v); err == nil && u.Host != "" {
				return u
			}
		}
	}
	return nil
}

// dialTLS establishes a TLS connection using utls with the Bun BoringSSL spec.
// This is called by http.Transport for every new TLS connection.
// It handles proxy tunneling (SOCKS5 and HTTP CONNECT) before the TLS handshake,
// and respects context cancellation/deadline throughout.
func (t *utlsRoundTripper) dialTLS(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}

	proxyURL := t.resolveProxy(addr)

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
func dialViaHTTPConnect(ctx context.Context, proxyURL *url.URL, targetAddr string) (net.Conn, error) {
	// Connect to the proxy itself.
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", proxyURL.Host)
	if err != nil {
		return nil, fmt.Errorf("dial HTTP proxy %s: %w", proxyURL.Host, err)
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
		return nil, fmt.Errorf("write CONNECT request: %w", err)
	}

	// Read CONNECT response.
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, connectReq)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read CONNECT response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		conn.Close()
		return nil, fmt.Errorf("proxy CONNECT to %s failed: %s", targetAddr, resp.Status)
	}

	// conn is now a raw TCP tunnel to targetAddr.
	return conn, nil
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
// Pass an empty string to inherit proxy from environment variables.
func NewAnthropicHttpClient(proxyURL string) *http.Client {
	return &http.Client{
		Transport: newUtlsRoundTripper(proxyURL),
	}
}
