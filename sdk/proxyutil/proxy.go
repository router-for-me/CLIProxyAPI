package proxyutil

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

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
		return setting, fmt.Errorf("parse proxy URL failed")
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
		if setting.URL.Scheme == "socks5" || setting.URL.Scheme == "socks5h" {
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
		if setting.URL.Scheme == "https" {
			// Only the connection *to the proxy* is ours. net/http hands the
			// transport-wide TLSClientConfig — whose NextProtos gain "h2" as soon as
			// HTTP/2 is configured — to that handshake, then writes an HTTP/1.1 CONNECT
			// over whatever ALPN selected; a proxy that picks h2 sees a bogus HTTP/2
			// preface and drops the connection ("unexpected EOF").
			//
			// DialTLSContext is the seam for exactly that: with Proxy set, net/http calls
			// it for the first hop, which is the proxy (connectMethod.addr()). Everything
			// after stays net/http's — CONNECT with its headers and hooks, the bound on
			// that exchange, the target handshake and its own ALPN. Taking any of it over
			// would mean reimplementing it, and then owning every guard it already has.
			baseDialContext := transport.DialContext
			transport.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialProxyTLS(ctx, network, addr, baseDialContext, transport, setting.URL)
			}
		}
		return transport, setting.Mode, nil
	default:
		return nil, setting.Mode, nil
	}
}

// dialProxyTLS makes the TLS connection to an HTTPS proxy, pinning that leg's ALPN to
// http/1.1 so the CONNECT net/http writes next lands on an HTTP/1.1 connection.
//
// TLSClientConfig and TLSHandshakeTimeout are read here rather than captured up front:
// net/http documents that it ignores both once DialTLSContext is set, so honouring them
// falls to this function, and callers set them after the transport is built.
func dialProxyTLS(ctx context.Context, network, addr string,
	baseDialContext func(context.Context, string, string) (net.Conn, error),
	transport *http.Transport, proxyURL *url.URL) (net.Conn, error) {
	dial := baseDialContext
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	conn, errDial := dial(ctx, network, addr)
	if errDial != nil {
		return nil, errDial
	}

	handshakeCtx := ctx
	if timeout := transport.TLSHandshakeTimeout; timeout > 0 {
		var cancelHandshake context.CancelFunc
		handshakeCtx, cancelHandshake = context.WithTimeout(ctx, timeout)
		defer cancelHandshake()
	}
	tlsConn := tls.Client(conn, proxyTLSConfig(transport.TLSClientConfig, proxyURL.Hostname()))
	if errHandshake := tlsConn.HandshakeContext(handshakeCtx); errHandshake != nil {
		if errClose := conn.Close(); errClose != nil {
			return nil, fmt.Errorf("HTTPS proxy TLS handshake failed: %w; close failed: %v", errHandshake, errClose)
		}
		return nil, fmt.Errorf("HTTPS proxy TLS handshake failed: %w", errHandshake)
	}
	return tlsConn, nil
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
				proxyURL:       setting.URL,
				dialer:         proxy.Direct,
				connectTimeout: defaultProxyConnectTimeout,
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

// defaultProxyConnectTimeout bounds the CONNECT exchange with the proxy. net/http applies
// the same one-minute bound in dialConn, for the same reason: a proxy that takes the
// connection and then stops replying would otherwise hold the caller until the request
// context is canceled, and callers on this path pass contexts that never are.
const defaultProxyConnectTimeout = time.Minute

type httpConnectDialer struct {
	proxyURL *url.URL
	dialer   proxy.Dialer
	// connectTimeout bounds the CONNECT exchange, the way net/http bounds its own in
	// dialConn: a proxy that takes the connection and then stops replying would
	// otherwise hold the caller until the context is canceled, and callers here pass
	// contexts that never are. Zero means no bound.
	connectTimeout time.Duration
}

func (d *httpConnectDialer) Dial(network, addr string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, addr)
}

func (d *httpConnectDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	contextDialer, ok := d.dialer.(proxy.ContextDialer)
	if !ok {
		return nil, errors.New("HTTP proxy base dialer does not support context cancellation")
	}
	proxyConn, errDial := contextDialer.DialContext(ctx, network, proxyDialAddr(d.proxyURL))
	if errDial != nil {
		return nil, fmt.Errorf("dial HTTP proxy failed: %w", errDial)
	}

	conn := proxyConn
	cancelDone := make(chan struct{})
	stopCancel := context.AfterFunc(ctx, func() {
		_ = proxyConn.Close()
		close(cancelDone)
	})
	defer func() {
		if !stopCancel() {
			<-cancelDone
		}
	}()
	if d.proxyURL.Scheme == "https" {
		tlsConn := tls.Client(conn, proxyTLSConfig(nil, d.proxyURL.Hostname()))
		if errHandshake := tlsConn.HandshakeContext(ctx); errHandshake != nil {
			if errClose := conn.Close(); errClose != nil {
				return nil, fmt.Errorf("HTTPS proxy TLS handshake failed: %w; close failed: %v", errHandshake, errClose)
			}
			return nil, fmt.Errorf("HTTPS proxy TLS handshake failed: %w", errHandshake)
		}
		conn = tlsConn
	}

	// A socket deadline rather than a second context watcher: cancellation of ctx is
	// already covered above, and the only thing left to bound is the proxy going quiet
	// while ctx never ends. The netpoller enforces this one with no timer, goroutine or
	// channel of its own, and it covers the write as well as the read — a proxy that
	// stops draining blocks req.Write just as thoroughly as one that never answers.
	if d.connectTimeout > 0 {
		if errDeadline := conn.SetDeadline(time.Now().Add(d.connectTimeout)); errDeadline != nil {
			if errClose := conn.Close(); errClose != nil {
				return nil, fmt.Errorf("set CONNECT deadline failed: %w; close failed: %v", errDeadline, errClose)
			}
			return nil, fmt.Errorf("set CONNECT deadline failed: %w", errDeadline)
		}
	}

	req := (&http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Host: addr},
		Host:   addr,
		Header: make(http.Header),
	}).WithContext(ctx)
	if d.proxyURL.User != nil {
		req.Header.Set("Proxy-Authorization", proxyAuthorization(d.proxyURL.User))
	}
	if errWrite := req.Write(conn); errWrite != nil {
		errWrite = preferContextError(ctx, errWrite)
		if errClose := conn.Close(); errClose != nil {
			return nil, fmt.Errorf("write CONNECT request failed: %w; close failed: %v", errWrite, errClose)
		}
		return nil, fmt.Errorf("write CONNECT request failed: %w", errWrite)
	}

	reader := bufio.NewReader(conn)
	resp, errRead := http.ReadResponse(reader, req)
	if errRead != nil {
		errRead = preferContextError(ctx, errRead)
		if errClose := conn.Close(); errClose != nil {
			return nil, fmt.Errorf("read CONNECT response failed: %w; close failed: %v", errRead, errClose)
		}
		return nil, fmt.Errorf("read CONNECT response failed: %w", errRead)
	}
	if resp.StatusCode != http.StatusOK {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
		if errClose := conn.Close(); errClose != nil {
			return nil, fmt.Errorf("proxy CONNECT returned status %s; close failed: %v", resp.Status, errClose)
		}
		return nil, fmt.Errorf("proxy CONNECT returned status %s", resp.Status)
	}

	// The deadline belongs to CONNECT, not to the tunnel. Leaving it on would cut the
	// caller's own request short at whatever was left of the minute.
	if d.connectTimeout > 0 {
		if errDeadline := conn.SetDeadline(time.Time{}); errDeadline != nil {
			if errClose := conn.Close(); errClose != nil {
				return nil, fmt.Errorf("clear CONNECT deadline failed: %w; close failed: %v", errDeadline, errClose)
			}
			return nil, fmt.Errorf("clear CONNECT deadline failed: %w", errDeadline)
		}
	}

	if errContext := ctx.Err(); errContext != nil {
		if errClose := conn.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
			return nil, fmt.Errorf("HTTP proxy context ended: %w; close failed: %v", errContext, errClose)
		}
		return nil, errContext
	}
	if reader.Buffered() > 0 {
		return &bufferedConn{Conn: conn, reader: reader}, nil
	}
	return conn, nil
}

func proxyDialAddr(proxyURL *url.URL) string {
	port := proxyURL.Port()
	if port == "" {
		port = "80"
		if proxyURL.Scheme == "https" {
			port = "443"
		}
	}
	return net.JoinHostPort(proxyURL.Hostname(), port)
}

func proxyAuthorization(user *url.Userinfo) string {
	username := user.Username()
	password, _ := user.Password()
	encoded := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	return "Basic " + encoded
}

// Redact returns a log-safe proxy URL with credentials and path-like data removed.
func Redact(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}

	parsedURL, errParse := url.Parse(trimmed)
	if errParse != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		return "<invalid proxy URL>"
	}

	redacted := &url.URL{
		Scheme: parsedURL.Scheme,
		Host:   parsedURL.Host,
	}
	if parsedURL.User != nil {
		redacted.User = url.User("redacted")
	}
	return redacted.String()
}

// proxyTLSConfig derives the TLS settings for the connection to an HTTPS proxy.
func proxyTLSConfig(base *tls.Config, serverName string) *tls.Config {
	cfg := base.Clone()
	if cfg == nil {
		cfg = &tls.Config{}
	}
	// Filled in only when the caller left it empty, as net/http's addTLS does. A proxy
	// reached at an address its certificate does not carry is the caller's to name.
	if cfg.ServerName == "" {
		cfg.ServerName = serverName
	}
	// ALPN, by contrast, is pinned unconditionally, and must stay that way: it is the fix.
	// CONNECT is written as an HTTP/1.1 request, so a proxy allowed to select h2 receives
	// one on an HTTP/2 connection and drops the whole thing.
	cfg.NextProtos = []string{"http/1.1"}
	return cfg
}

// preferContextError reports the cancellation rather than what closing the connection
// produced. The dialer unblocks a stuck read by closing the connection, so the I/O error
// a caller would otherwise see is "use of closed network connection" — true, and useless
// for telling a canceled request apart from a broken proxy.
func preferContextError(ctx context.Context, err error) error {
	if errContext := ctx.Err(); errContext != nil {
		return errContext
	}
	return err
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	if c.reader.Buffered() > 0 {
		return c.reader.Read(p)
	}
	return c.Conn.Read(p)
}
