// Package claude provides authentication functionality for Anthropic's Claude API.
// This file implements proxy dialer construction for HTTP CONNECT and SOCKS5 proxies,
// used by the utls transport to route OAuth refresh requests through a configured proxy.
package claude

import (
	"bufio"
	cryptotls "crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/proxy"
)

// dialerFunc adapts a plain function to the proxy.Dialer interface.
type dialerFunc func(network, addr string) (net.Conn, error)

func (f dialerFunc) Dial(network, addr string) (net.Conn, error) { return f(network, addr) }

// bufferedConn wraps a net.Conn with a bufio.Reader so that any bytes
// pre-fetched during HTTP response parsing are returned before reading
// directly from the underlying connection.
type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

// buildProxyDialer creates a proxy.Dialer for the given proxy URL string.
// It supports socks5, socks5h, http, and https schemes.
// An empty URL returns proxy.Direct (no proxy).
func buildProxyDialer(rawProxyURL string) (proxy.Dialer, error) {
	proxyURL := strings.TrimSpace(rawProxyURL)
	if proxyURL == "" {
		return proxy.Direct, nil
	}

	parsedURL, errParse := url.Parse(proxyURL)
	if errParse != nil {
		return nil, fmt.Errorf("failed to parse proxy URL %q: %w", rawProxyURL, errParse)
	}

	switch parsedURL.Scheme {
	case "socks5", "socks5h":
		proxyDialer, errDialer := proxy.FromURL(parsedURL, proxy.Direct)
		if errDialer != nil {
			return nil, fmt.Errorf("failed to create SOCKS5 dialer for %q: %w", rawProxyURL, errDialer)
		}
		return proxyDialer, nil
	case "http", "https":
		if parsedURL.Host == "" {
			return nil, fmt.Errorf("failed to parse proxy URL %q: missing host", rawProxyURL)
		}
		if parsedURL.Port() == "" {
			defaultPort := "80"
			if parsedURL.Scheme == "https" {
				defaultPort = "443"
			}
			parsedURL.Host = net.JoinHostPort(parsedURL.Hostname(), defaultPort)
		}
		proxyURLCopy := *parsedURL
		return dialerFunc(func(network, addr string) (net.Conn, error) {
			return dialHTTPConnectProxy(&proxyURLCopy, network, addr)
		}), nil
	default:
		return nil, fmt.Errorf("failed to create proxy dialer for %q: unsupported scheme %q", rawProxyURL, parsedURL.Scheme)
	}
}

// dialHTTPConnectProxy establishes a TCP connection through an HTTP(S) proxy
// using the CONNECT method, returning the tunneled connection.
func dialHTTPConnectProxy(proxyURL *url.URL, network, addr string) (net.Conn, error) {
	if network != "tcp" {
		return nil, fmt.Errorf("failed to dial via HTTP proxy: CONNECT only supports tcp, got %q", network)
	}

	proxyConn, errDial := net.Dial("tcp", proxyURL.Host)
	if errDial != nil {
		return nil, fmt.Errorf("failed to dial proxy %q: %w", proxyURL.Host, errDial)
	}

	if proxyURL.Scheme == "https" {
		tlsConn := cryptotls.Client(proxyConn, &cryptotls.Config{ServerName: proxyURL.Hostname()})
		if errHandshake := tlsConn.Handshake(); errHandshake != nil {
			_ = proxyConn.Close()
			return nil, fmt.Errorf("failed to TLS-handshake with proxy %q: %w", proxyURL.Host, errHandshake)
		}
		proxyConn = tlsConn
	}

	tunneledConn, errConnect := establishHTTPConnectTunnel(proxyConn, proxyURL, addr)
	if errConnect != nil {
		_ = proxyConn.Close()
		return nil, errConnect
	}

	return tunneledConn, nil
}

// establishHTTPConnectTunnel sends an HTTP CONNECT request through proxyConn
// and returns the tunneled connection on success.
func establishHTTPConnectTunnel(proxyConn net.Conn, proxyURL *url.URL, addr string) (net.Conn, error) {
	var reqBuf strings.Builder
	reqBuf.WriteString("CONNECT ")
	reqBuf.WriteString(addr)
	reqBuf.WriteString(" HTTP/1.1\r\nHost: ")
	reqBuf.WriteString(addr)
	reqBuf.WriteString("\r\n")

	if proxyURL.User != nil {
		username := proxyURL.User.Username()
		password, _ := proxyURL.User.Password()
		credentials := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
		reqBuf.WriteString("Proxy-Authorization: Basic ")
		reqBuf.WriteString(credentials)
		reqBuf.WriteString("\r\n")
	}

	reqBuf.WriteString("\r\n")

	if _, errWrite := io.WriteString(proxyConn, reqBuf.String()); errWrite != nil {
		return nil, fmt.Errorf("failed to send CONNECT to proxy %q: %w", proxyURL.Host, errWrite)
	}

	reader := bufio.NewReader(proxyConn)
	resp, errResponse := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if errResponse != nil {
		return nil, fmt.Errorf("failed to read CONNECT response from proxy %q: %w", proxyURL.Host, errResponse)
	}

	if resp.StatusCode != http.StatusOK {
		body, errBody := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
		if errBody != nil {
			return nil, fmt.Errorf("proxy CONNECT to %q failed with status %s (failed to read error body: %v)", proxyURL.Host, resp.Status, errBody)
		}
		message := strings.TrimSpace(string(body))
		if message != "" {
			return nil, fmt.Errorf("proxy CONNECT to %q failed with status %s: %s", proxyURL.Host, resp.Status, message)
		}
		return nil, fmt.Errorf("proxy CONNECT to %q failed with status %s", proxyURL.Host, resp.Status)
	}
	// resp.Body is intentionally not closed here: for CONNECT 200 Go sets it
	// to http.NoBody, and the tunnel data follows in `reader`.
	return &bufferedConn{Conn: proxyConn, reader: reader}, nil
}
