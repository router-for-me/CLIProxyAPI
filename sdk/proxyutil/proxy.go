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
		if setting.URL.Scheme == "https" {
			// http.Transport cannot be used to reach an HTTPS proxy: it hands the
			// transport-wide TLSClientConfig — whose NextProtos gain "h2" as soon as
			// HTTP/2 is configured — to the handshake with the proxy itself, and then
			// writes an HTTP/1.1 CONNECT over whatever ALPN selected. A proxy that
			// picks h2 sees a bogus HTTP/2 preface and drops the connection, which
			// surfaces to callers as "unexpected EOF". Tunnel through the CONNECT
			// dialer instead; protocol negotiation with the target is untouched.
			// Reuse the cloned transport's own dialer so the connection to the proxy
			// keeps its TCP dial timeout and keep-alive settings; read it before it
			// is overwritten below.
			baseDialer := proxy.Dialer(proxy.Direct)
			if dialContext := transport.DialContext; dialContext != nil {
				baseDialer = contextDialerFunc(dialContext)
			}
			transport.Proxy = nil
			transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				// Built per dial: TLSClientConfig is still nil here and gets populated
				// later, both by callers and by Go's own HTTP/2 setup.
				dialer := &httpConnectDialer{
					proxyURL:            setting.URL,
					dialer:              baseDialer,
					tlsConfig:           transport.TLSClientConfig,
					tlsHandshakeTimeout: transport.TLSHandshakeTimeout,
					connectTimeout:      defaultProxyConnectTimeout,
				}
				return dialer.DialContext(ctx, network, addr)
			}
			return transport, setting.Mode, nil
		}
		transport.Proxy = http.ProxyURL(setting.URL)
		return transport, setting.Mode, nil
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

type httpConnectDialer struct {
	proxyURL *url.URL
	dialer   proxy.Dialer
	// tlsConfig carries caller-supplied TLS settings (roots, client certs) for the
	// handshake with an HTTPS proxy. Nil means defaults.
	tlsConfig *tls.Config
	// tlsHandshakeTimeout bounds the TLS handshake with an HTTPS proxy. Setting up
	// the tunnel here takes it out of reach of http.Transport.TLSHandshakeTimeout,
	// so callers that rely on that bound must pass it through. Zero means no bound.
	tlsHandshakeTimeout time.Duration
	// connectTimeout bounds the CONNECT exchange. Zero means no bound.
	connectTimeout time.Duration
}

// defaultProxyConnectTimeout bounds the CONNECT exchange with the proxy. net/http applies
// the same one-minute bound in dialConn, for the same reason: a proxy that takes the
// connection and then stops replying would otherwise hold the caller until the request
// context is canceled, and most callers here pass a context that never is. Taking over
// proxy setup means taking over this guard too.
const defaultProxyConnectTimeout = time.Minute

// contextDialerFunc adapts a net.Dialer-style dial function to proxy.Dialer so an
// existing transport's dialer, timeouts included, can carry the proxy connection.
type contextDialerFunc func(ctx context.Context, network, addr string) (net.Conn, error)

func (f contextDialerFunc) Dial(network, addr string) (net.Conn, error) {
	return f(context.Background(), network, addr)
}

func (f contextDialerFunc) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return f(ctx, network, addr)
}

// preferContextError reports the deadline rather than what closing the connection
// produced. The bound above takes effect by closing the connection, so the I/O error
// the caller would otherwise see is "use of closed network connection" — true, and
// useless for telling a slow proxy apart from a broken one.
func preferContextError(ctx context.Context, err error) error {
	if errContext := ctx.Err(); errContext != nil {
		return errContext
	}
	return err
}

func (d *httpConnectDialer) Dial(network, addr string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, addr)
}

// proxyTLSConfig derives the TLS settings for the connection to an HTTPS proxy.
// ALPN is pinned to http/1.1 because CONNECT is written as an HTTP/1.1 request:
// letting the proxy select h2 would put an HTTP/1.1 request on an HTTP/2 connection.
func proxyTLSConfig(base *tls.Config, serverName string) *tls.Config {
	cfg := base.Clone()
	if cfg == nil {
		cfg = &tls.Config{}
	}
	cfg.ServerName = serverName
	cfg.NextProtos = []string{"http/1.1"}
	return cfg
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
		tlsConn := tls.Client(conn, proxyTLSConfig(d.tlsConfig, d.proxyURL.Hostname()))
		handshakeCtx := ctx
		if d.tlsHandshakeTimeout > 0 {
			var cancelHandshake context.CancelFunc
			handshakeCtx, cancelHandshake = context.WithTimeout(ctx, d.tlsHandshakeTimeout)
			defer cancelHandshake()
		}
		if errHandshake := tlsConn.HandshakeContext(handshakeCtx); errHandshake != nil {
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
