package proxyutil

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"golang.org/x/net/http/httpproxy"
	"golang.org/x/net/proxy"
)

// EnvironmentProxyCacheKey is the stable cache key for process-environment proxies.
const EnvironmentProxyCacheKey = "@environment"

var proxyEnvironmentKeys = []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy"}

// ClearProxyEnvironment removes proxy-related variables for the current test.
func ClearProxyEnvironment() {
	for _, key := range proxyEnvironmentKeys {
		_ = os.Unsetenv(key)
	}
}

// EnvironmentProxyConfigured reports whether HTTP(S)_PROXY is set in the environment.
func EnvironmentProxyConfigured() bool {
	cfg := httpproxy.FromEnvironment()
	return strings.TrimSpace(cfg.HTTPSProxy) != "" || strings.TrimSpace(cfg.HTTPProxy) != ""
}

// EnvironmentProxyFunc returns a proxy selector that honors NO_PROXY.
func EnvironmentProxyFunc() func(*http.Request) (*url.URL, error) {
	proxyFunc := httpproxy.FromEnvironment().ProxyFunc()
	return func(req *http.Request) (*url.URL, error) {
		if req == nil || req.URL == nil {
			return nil, nil
		}
		return proxyFunc(req.URL)
	}
}

var (
	environmentHTTPTransportOnce sync.Once
	environmentHTTPTransport     *http.Transport
)

func newEnvironmentHTTPTransport() *http.Transport {
	transport := cloneDefaultTransport()
	transport.Proxy = EnvironmentProxyFunc()
	transport.DialTLSContext = buildEnvironmentDialTLSContext(transport)
	return transport
}

// CachedEnvironmentHTTPTransport returns a shared transport using environment proxy rules.
func CachedEnvironmentHTTPTransport() *http.Transport {
	environmentHTTPTransportOnce.Do(func() {
		environmentHTTPTransport = newEnvironmentHTTPTransport()
	})
	return environmentHTTPTransport
}

func environmentHTTPSProxyURLForAddr(addr string) *url.URL {
	for _, key := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		raw := strings.TrimSpace(os.Getenv(key))
		if raw == "" {
			continue
		}
		normalized, errNormalize := NormalizeProxyURL(raw)
		if errNormalize != nil {
			continue
		}
		parsed, errParse := url.Parse(normalized)
		if errParse != nil || parsed.Scheme != "https" {
			continue
		}
		if proxyDialAddr(parsed) == addr {
			return parsed
		}
	}
	return nil
}

func buildEnvironmentDialTLSContext(base *http.Transport) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if proxyURL := environmentHTTPSProxyURLForAddr(addr); proxyURL != nil {
			var proxyTLS *tls.Config
			if base.TLSClientConfig != nil {
				proxyTLS = base.TLSClientConfig.Clone()
			}
			return buildHTTPSProxyDialTLSContext(
				proxyURL,
				proxyTLS,
				base.TLSHandshakeTimeout,
				base.DialContext,
			)(ctx, network, addr)
		}

		rawConn, errDial := base.DialContext(ctx, network, addr)
		if errDial != nil {
			return nil, errDial
		}
		host, _, errSplit := net.SplitHostPort(addr)
		if errSplit != nil {
			host = addr
		}
		tlsConfig := base.TLSClientConfig
		if tlsConfig != nil {
			tlsConfig = tlsConfig.Clone()
		} else {
			tlsConfig = &tls.Config{}
		}
		if tlsConfig.ServerName == "" {
			tlsConfig.ServerName = host
		}
		tlsConn := tls.Client(rawConn, tlsConfig)
		if errHandshake := tlsConn.HandshakeContext(ctx); errHandshake != nil {
			if errClose := rawConn.Close(); errClose != nil {
				return nil, fmt.Errorf("TLS handshake failed: %w; close failed: %v", errHandshake, errClose)
			}
			return nil, fmt.Errorf("TLS handshake failed: %w", errHandshake)
		}
		return tlsConn, nil
	}
}

// NormalizeProxyURL accepts full proxy URLs and host:port environment forms.
func NormalizeProxyURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if setting, errParse := Parse(trimmed); errParse == nil && setting.Mode == ModeProxy && setting.URL != nil {
		return setting.URL.String(), nil
	}
	if !strings.Contains(trimmed, "://") {
		candidate := "http://" + trimmed
		if setting, errParse := Parse(candidate); errParse == nil && setting.Mode == ModeProxy && setting.URL != nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("invalid proxy URL")
}

// EnvironmentProxyURL returns the first non-empty normalized HTTPS_PROXY or HTTP_PROXY value.
func EnvironmentProxyURL() string {
	for _, key := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			normalized, errNormalize := NormalizeProxyURL(value)
			if errNormalize == nil && normalized != "" {
				return normalized
			}
		}
	}
	return ""
}

// EnvironmentDialer routes dials through explicit or environment proxy selection.
type EnvironmentDialer struct {
	ExplicitURL string
}

func (d EnvironmentDialer) Dial(network, addr string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, addr)
}

func (d EnvironmentDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer, errResolve := d.resolveDialer(network, addr)
	if errResolve != nil {
		return nil, errResolve
	}
	contextDialer, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return dialer.Dial(network, addr)
	}
	return contextDialer.DialContext(ctx, network, addr)
}

func (d EnvironmentDialer) resolveDialer(network, addr string) (proxy.Dialer, error) {
	explicit := strings.TrimSpace(d.ExplicitURL)
	if explicit != "" {
		built, mode, errBuild := BuildDialer(explicit)
		if errBuild != nil {
			return nil, errBuild
		}
		if mode == ModeInherit || built == nil {
			return proxy.Direct, nil
		}
		return built, nil
	}
	if !EnvironmentProxyConfigured() {
		return proxy.Direct, nil
	}

	host, port, errSplit := net.SplitHostPort(addr)
	if errSplit != nil {
		host = addr
		port = ""
	}
	scheme := "https"
	if port == "80" {
		scheme = "http"
	}
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	req := &http.Request{
		URL: &url.URL{Scheme: scheme, Host: net.JoinHostPort(host, port)},
	}
	proxyURL, errProxy := EnvironmentProxyFunc()(req)
	if errProxy != nil {
		return nil, errProxy
	}
	if proxyURL == nil {
		return proxy.Direct, nil
	}
	normalized, errNormalize := NormalizeProxyURL(proxyURL.String())
	if errNormalize != nil {
		return nil, errNormalize
	}
	built, mode, errBuild := BuildDialer(normalized)
	if errBuild != nil {
		return nil, errBuild
	}
	if mode == ModeInherit || built == nil {
		return proxy.Direct, nil
	}
	return built, nil
}

// ProxyDialer returns a dialer for an explicit proxy URL or environment rules when empty.
func ProxyDialer(explicitProxyURL string) proxy.Dialer {
	explicitProxyURL = strings.TrimSpace(explicitProxyURL)
	if explicitProxyURL != "" {
		built, mode, errBuild := BuildDialer(explicitProxyURL)
		if errBuild == nil && mode != ModeInherit && built != nil {
			return built
		}
		return proxy.Direct
	}
	if EnvironmentProxyConfigured() {
		return EnvironmentDialer{}
	}
	return proxy.Direct
}

// ProxyCacheKey returns a cache key for explicit or environment proxy settings.
func ProxyCacheKey(explicitProxyURL string) string {
	if strings.TrimSpace(explicitProxyURL) != "" {
		return strings.TrimSpace(explicitProxyURL)
	}
	if EnvironmentProxyConfigured() {
		return EnvironmentProxyCacheKey
	}
	return ""
}
