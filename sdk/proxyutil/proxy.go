package proxyutil

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

const (
	// DefaultMaxIdleConns keeps more upstream connections warm across auths and models.
	DefaultMaxIdleConns = 512
	// DefaultMaxIdleConnsPerHost raises Go's default of 2, which is too low for a proxy service.
	DefaultMaxIdleConnsPerHost = 64
	// DefaultIdleConnTimeout matches Go's default while documenting the shared pool policy.
	DefaultIdleConnTimeout = 90 * time.Second
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
	if parsedURL.Scheme == "" || parsedURL.Host == "" {
		setting.Mode = ModeInvalid
		return setting, fmt.Errorf("proxy URL missing scheme/host")
	}

	switch parsedURL.Scheme {
	case "socks5", "socks5h", "http", "https":
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

// ApplyHTTPTransportPoolSettings tunes a transport for proxy-server fan-out.
func ApplyHTTPTransportPoolSettings(transport *http.Transport) *http.Transport {
	if transport == nil {
		transport = &http.Transport{}
	}
	transport.MaxIdleConns = DefaultMaxIdleConns
	transport.MaxIdleConnsPerHost = DefaultMaxIdleConnsPerHost
	transport.MaxConnsPerHost = 0
	transport.IdleConnTimeout = DefaultIdleConnTimeout
	transport.ForceAttemptHTTP2 = true
	return transport
}

// NewPooledDefaultTransport clones http.DefaultTransport and applies proxy-server pool settings.
func NewPooledDefaultTransport() *http.Transport {
	return ApplyHTTPTransportPoolSettings(cloneDefaultTransport())
}

// NewDirectTransport returns a transport that bypasses environment proxies.
func NewDirectTransport() *http.Transport {
	clone := cloneDefaultTransport()
	clone.Proxy = nil
	return ApplyHTTPTransportPoolSettings(clone)
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
		if setting.URL.Scheme == "socks5" || setting.URL.Scheme == "socks5h" {
			dialer, errSOCKS5 := proxy.SOCKS5("tcp", setting.URL.Host, proxyAuthFromURL(setting.URL), proxy.Direct)
			if errSOCKS5 != nil {
				return nil, setting.Mode, fmt.Errorf("create SOCKS5 dialer failed: %w", errSOCKS5)
			}
			transport := cloneDefaultTransport()
			transport.Proxy = nil
			transport.DialContext = func(_ context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			}
			return ApplyHTTPTransportPoolSettings(transport), setting.Mode, nil
		}
		transport := cloneDefaultTransport()
		transport.Proxy = http.ProxyURL(setting.URL)
		return ApplyHTTPTransportPoolSettings(transport), setting.Mode, nil
	default:
		return nil, setting.Mode, nil
	}
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
		dialer, errDialer := proxy.FromURL(setting.URL, proxy.Direct)
		if errDialer == nil {
			return dialer, setting.Mode, nil
		}
		switch setting.URL.Scheme {
		case "http", "https":
			return &connectProxyDialer{proxyURL: setting.URL}, setting.Mode, nil
		default:
			return nil, setting.Mode, fmt.Errorf("create proxy dialer failed: %w", errDialer)
		}
	default:
		return nil, setting.Mode, nil
	}
}

type connectProxyDialer struct {
	proxyURL *url.URL
}

func (d *connectProxyDialer) Dial(network, addr string) (net.Conn, error) {
	if d == nil || d.proxyURL == nil {
		return nil, fmt.Errorf("proxy dialer is not configured")
	}
	baseDialer := &net.Dialer{Timeout: 30 * time.Second}
	var conn net.Conn
	var err error
	switch d.proxyURL.Scheme {
	case "https":
		conn, err = tls.DialWithDialer(baseDialer, network, d.proxyURL.Host, &tls.Config{ServerName: d.proxyURL.Hostname()})
	default:
		conn, err = baseDialer.Dial(network, d.proxyURL.Host)
	}
	if err != nil {
		return nil, err
	}

	if _, err = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", addr, addr); err != nil {
		conn.Close()
		return nil, err
	}
	if auth := proxyAuthorizationHeader(d.proxyURL); auth != "" {
		if _, err = fmt.Fprintf(conn, "Proxy-Authorization: %s\r\n", auth); err != nil {
			conn.Close()
			return nil, err
		}
	}
	if _, err = fmt.Fprint(conn, "\r\n"); err != nil {
		conn.Close()
		return nil, err
	}

	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		conn.Close()
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		conn.Close()
		return nil, fmt.Errorf("proxy CONNECT failed: %s", resp.Status)
	}
	return conn, nil
}

func proxyAuthorizationHeader(proxyURL *url.URL) string {
	auth := proxyAuthFromURL(proxyURL)
	if auth == nil {
		return ""
	}
	token := base64.StdEncoding.EncodeToString([]byte(auth.User + ":" + auth.Password))
	return "Basic " + token
}

func proxyAuthFromURL(proxyURL *url.URL) *proxy.Auth {
	if proxyURL == nil || proxyURL.User == nil {
		return nil
	}
	username := proxyURL.User.Username()
	password, _ := proxyURL.User.Password()
	return &proxy.Auth{User: username, Password: password}
}
