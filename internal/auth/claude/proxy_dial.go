// Package claude provides authentication functionality for Anthropic's Claude API.
// This file implements proxy tunneling (SOCKS5, HTTP/HTTPS CONNECT, env fallback)
// for establishing raw TCP connections through proxies before the TLS handshake.
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
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
)

// ProxyDialer manages proxy resolution and TCP connection establishment.
// It supports SOCKS5, HTTP CONNECT, and HTTPS CONNECT proxies, as well as
// environment-variable-based proxy configuration (HTTPS_PROXY, HTTP_PROXY, ALL_PROXY).
type ProxyDialer struct {
	proxyURL  *url.URL       // explicit proxy URL (nil = check env per-request)
	proxyMode proxyutil.Mode // inherit (use env), direct, proxy, or invalid
}

// NewProxyDialer creates a ProxyDialer from a proxy URL string.
// An empty string means inherit proxy from environment variables.
func NewProxyDialer(proxyURL string) *ProxyDialer {
	d := &ProxyDialer{proxyMode: proxyutil.ModeInherit}

	if proxyURL != "" {
		setting, errParse := proxyutil.Parse(proxyURL)
		if errParse != nil {
			log.Errorf("failed to parse proxy URL %q: %v", proxyURL, errParse)
		} else {
			d.proxyMode = setting.Mode
			d.proxyURL = setting.URL
		}
	}

	return d
}

// resolveProxy returns the effective proxy URL for a given target host.
// For explicit proxy configuration, it returns the configured URL directly.
// For inherit mode, it delegates to http.ProxyFromEnvironment which correctly
// handles HTTPS_PROXY, HTTP_PROXY, NO_PROXY (including CIDR and wildcards).
func (d *ProxyDialer) resolveProxy(targetHost string) *url.URL {
	switch d.proxyMode {
	case proxyutil.ModeDirect:
		return nil
	case proxyutil.ModeProxy:
		return d.proxyURL
	default:
		// ModeInherit: delegate to Go's standard proxy resolution which
		// reads HTTPS_PROXY, HTTP_PROXY, ALL_PROXY and respects NO_PROXY.
		req := &http.Request{URL: &url.URL{Scheme: "https", Host: targetHost}}
		proxyURL, _ := http.ProxyFromEnvironment(req)
		return proxyURL
	}
}

// DialContext establishes a raw TCP connection to addr, tunneling through a proxy
// if one is configured. It dispatches to direct dialing, SOCKS5, or HTTP CONNECT
// based on the resolved proxy configuration.
func (d *ProxyDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}

	proxyURL := d.resolveProxy(host)

	switch {
	case proxyURL == nil:
		return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	case proxyURL.Scheme == "socks5" || proxyURL.Scheme == "socks5h":
		return dialViaSocks5(ctx, proxyURL, addr)
	case proxyURL.Scheme == "http" || proxyURL.Scheme == "https":
		return dialViaHTTPConnect(ctx, proxyURL, addr)
	default:
		return nil, fmt.Errorf("unsupported proxy scheme: %s", proxyURL.Scheme)
	}
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
