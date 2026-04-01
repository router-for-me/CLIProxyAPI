package proxyutil

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/proxy"
)

// Mode describes how a proxy setting should be interpreted.
type Mode int

const (
	// ModeInherit means no explicit proxy behavior was configured.
	ModeInherit Mode = iota
	// ModeDirect means outbound requests must bypass proxies explicitly.
	ModeDirect
	// ModeProxy means a concrete proxy URL was configured.
	ModeProxy
	// ModeInvalid means the proxy setting is present but malformed or unsupported.
	ModeInvalid
)

// Setting is the normalized interpretation of a proxy configuration value.
type Setting struct {
	Raw  string
	Mode Mode
	URL  *url.URL
}

// Parse normalizes a proxy configuration value into inherit, direct, or proxy modes.
func Parse(raw string) (Setting, error) {
	trimmed := strings.TrimSpace(raw)
	setting := Setting{Raw: trimmed}

	if trimmed == "" {
		setting.Mode = ModeInherit
		return setting, nil
	}

	if strings.EqualFold(trimmed, "direct") || strings.EqualFold(trimmed, "none") {
		setting.Mode = ModeDirect
		return setting, nil
	}

	parsedURL, errParse := url.Parse(trimmed)
	if errParse != nil {
		setting.Mode = ModeInvalid
		return setting, fmt.Errorf("parse proxy URL failed: %w", errParse)
	}
	parsedURL.Scheme = strings.ToLower(parsedURL.Scheme)
	if parsedURL.Scheme == "" || parsedURL.Host == "" {
		setting.Mode = ModeInvalid
		return setting, fmt.Errorf("proxy URL missing scheme/host")
	}

	switch parsedURL.Scheme {
	case "socks5", "http", "https":
		setting.Mode = ModeProxy
		setting.URL = parsedURL
		return setting, nil
	default:
		setting.Mode = ModeInvalid
		return setting, fmt.Errorf("unsupported proxy scheme: %s", parsedURL.Scheme)
	}
}

func cloneDefaultTransport() *http.Transport {
	if transport, ok := http.DefaultTransport.(*http.Transport); ok && transport != nil {
		return transport.Clone()
	}
	return &http.Transport{}
}

// NewDirectTransport returns a transport that bypasses environment proxies.
func NewDirectTransport() *http.Transport {
	clone := cloneDefaultTransport()
	clone.Proxy = nil
	return clone
}

// BuildHTTPTransport constructs an HTTP transport for the provided proxy setting.
func BuildHTTPTransport(raw string) (*http.Transport, Mode, error) {
	setting, errParse := Parse(raw)
	if errParse != nil {
		return nil, setting.Mode, errParse
	}

	switch setting.Mode {
	case ModeInherit:
		return nil, setting.Mode, nil
	case ModeDirect:
		return NewDirectTransport(), setting.Mode, nil
	case ModeProxy:
		if setting.URL.Scheme == "socks5" {
			var proxyAuth *proxy.Auth
			if setting.URL.User != nil {
				username := setting.URL.User.Username()
				password, _ := setting.URL.User.Password()
				proxyAuth = &proxy.Auth{User: username, Password: password}
			}
			dialer, errSOCKS5 := proxy.SOCKS5("tcp", setting.URL.Host, proxyAuth, proxy.Direct)
			if errSOCKS5 != nil {
				return nil, setting.Mode, fmt.Errorf("create SOCKS5 dialer failed: %w", errSOCKS5)
			}
			transport := cloneDefaultTransport()
			transport.Proxy = nil
			transport.DialContext = func(_ context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			}
			return transport, setting.Mode, nil
		}
		transport := cloneDefaultTransport()
		transport.Proxy = http.ProxyURL(setting.URL)
		return transport, setting.Mode, nil
	default:
		return nil, setting.Mode, nil
	}
}

type httpConnectDialer struct {
	proxyURL *url.URL
	forward  proxy.Dialer
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

func (d *httpConnectDialer) Dial(network, addr string) (net.Conn, error) {
	if d == nil || d.proxyURL == nil {
		return nil, fmt.Errorf("http proxy dialer is not configured")
	}
	if d.forward == nil {
		d.forward = proxy.Direct
	}
	if !strings.HasPrefix(network, "tcp") {
		return nil, fmt.Errorf("unsupported network for HTTP proxy dialer: %s", network)
	}

	proxyAddr := proxyAddress(d.proxyURL)
	conn, errDial := d.forward.Dial(network, proxyAddr)
	if errDial != nil {
		return nil, fmt.Errorf("dial proxy %s failed: %w", proxyAddr, errDial)
	}

	if d.proxyURL.Scheme == "https" {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: d.proxyURL.Hostname()})
		if errHandshake := tlsConn.Handshake(); errHandshake != nil {
			conn.Close()
			return nil, fmt.Errorf("TLS handshake with HTTPS proxy failed: %w", errHandshake)
		}
		conn = tlsConn
	}

	if errConnect := writeConnectRequest(conn, addr, d.proxyURL); errConnect != nil {
		conn.Close()
		return nil, errConnect
	}

	reader := bufio.NewReader(conn)
	resp, errResponse := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if errResponse != nil {
		conn.Close()
		return nil, fmt.Errorf("read CONNECT response failed: %w", errResponse)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		resp.Body.Close()
		conn.Close()
		if len(body) > 0 {
			return nil, fmt.Errorf("proxy CONNECT failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
		}
		return nil, fmt.Errorf("proxy CONNECT failed: %s", resp.Status)
	}

	return &bufferedConn{Conn: conn, reader: reader}, nil
}

func proxyAddress(proxyURL *url.URL) string {
	if proxyURL == nil {
		return ""
	}
	if proxyURL.Port() != "" {
		return proxyURL.Host
	}

	switch proxyURL.Scheme {
	case "http":
		return net.JoinHostPort(proxyURL.Hostname(), "80")
	case "https":
		return net.JoinHostPort(proxyURL.Hostname(), "443")
	case "socks5":
		return net.JoinHostPort(proxyURL.Hostname(), "1080")
	default:
		return proxyURL.Host
	}
}

func writeConnectRequest(conn net.Conn, addr string, proxyURL *url.URL) error {
	var requestBuilder strings.Builder
	requestBuilder.Grow(len(addr) + 128)
	requestBuilder.WriteString("CONNECT ")
	requestBuilder.WriteString(addr)
	requestBuilder.WriteString(" HTTP/1.1\r\nHost: ")
	requestBuilder.WriteString(addr)
	requestBuilder.WriteString("\r\n")
	if proxyURL != nil && proxyURL.User != nil {
		username := proxyURL.User.Username()
		password, _ := proxyURL.User.Password()
		token := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
		requestBuilder.WriteString("Proxy-Authorization: Basic ")
		requestBuilder.WriteString(token)
		requestBuilder.WriteString("\r\n")
	}
	requestBuilder.WriteString("\r\n")

	if _, errWrite := io.WriteString(conn, requestBuilder.String()); errWrite != nil {
		return fmt.Errorf("write CONNECT request failed: %w", errWrite)
	}
	return nil
}

// BuildDialer constructs a proxy dialer for settings that operate at the connection layer.
func BuildDialer(raw string) (proxy.Dialer, Mode, error) {
	setting, errParse := Parse(raw)
	if errParse != nil {
		return nil, setting.Mode, errParse
	}

	switch setting.Mode {
	case ModeInherit:
		return nil, setting.Mode, nil
	case ModeDirect:
		return proxy.Direct, setting.Mode, nil
	case ModeProxy:
		if setting.URL.Scheme == "http" || setting.URL.Scheme == "https" {
			return &httpConnectDialer{
				proxyURL: setting.URL,
				forward:  proxy.Direct,
			}, setting.Mode, nil
		}
		dialer, errDialer := proxy.FromURL(setting.URL, proxy.Direct)
		if errDialer != nil {
			return nil, setting.Mode, fmt.Errorf("create proxy dialer failed: %w", errDialer)
		}
		return dialer, setting.Mode, nil
	default:
		return nil, setting.Mode, nil
	}
}
