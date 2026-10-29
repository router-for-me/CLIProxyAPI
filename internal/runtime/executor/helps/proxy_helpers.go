package helps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// NewProxyAwareHTTPClient creates an HTTP client with proper proxy configuration priority:
// 1. Use the execution-scoped request proxy if configured (highest priority)
// 2. Use auth.ProxyURL if configured
// 3. Use cfg.ProxyURL if auth proxy is not configured
// 4. Use RoundTripper from context if none are configured
//
// Parameters:
//   - ctx: The context containing optional RoundTripper
//   - cfg: The application configuration
//   - auth: The authentication information
//   - timeout: The client timeout (0 means no timeout)
//
// Returns:
//   - *http.Client: An HTTP client with configured proxy or transport
func NewProxyAwareHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) (clientOut *http.Client) {
	defer func() { clientOut = httptransport.Decorate(ctx, clientOut) }()
	httpClient := &http.Client{}
	if timeout > 0 {
		httpClient.Timeout = timeout
	}

	// Priority: request override, then auth.ProxyURL, then cfg.ProxyURL.
	proxyURL := effectiveProxyURL(ctx, cfg, auth)

	// Priority 1 & 2: Explicit proxy URL takes precedence over context RoundTripper.
	if proxyURL != "" {
		if transport := pooledProxyTransport(proxyURL, cfg, auth); transport != nil {
			httpClient.Transport = transport
			return httpClient
		}
		// If proxy setup failed, log and fall through to context RoundTripper
		log.Debugf("failed to setup proxy from URL: %s, falling back to context transport", proxyutil.Redact(proxyURL))
	}

	// Priority 3: Use RoundTripper from context (typically from RoundTripperFor)
	if ctx != nil {
		if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
			httpClient.Transport = rt
			return httpClient
		}
	}

	// Priority 4: A credential may opt into pooling without an explicit proxy-url.
	// With an empty proxy-url the pooled transport keeps the inherited
	// environment-proxy behavior while isolating the credential's pool.
	if transport := pooledProxyTransport("", cfg, auth); transport != nil {
		httpClient.Transport = transport
	}

	return httpClient
}

// proxyPoolKey identifies one cached pool. scope isolates credentials, proxy is
// the normalized proxy setting, and the remaining fields fold in every setting
// that changes the built transport.
type proxyPoolKey struct {
	scope       string
	proxy       string
	idle        time.Duration
	max         int
	invalidIdle string
}

const (
	// proxyPoolDefaultIdleConnTimeout bounds how long an idle pooled connection
	// may survive before net/http closes it.
	//
	// Measured on 2026-10-29 against ollama.com through the local transparent
	// proxy, which is the upstream this pool was introduced for: a pooled
	// HTTP/2 connection was still reused after 120s, 180s, 300s and 600s of
	// idleness, and a bare HTTP/1.1 connection survived an idle 600s without a
	// close. 600s is therefore a lower bound on the upstream window, not a
	// limit. The 90s that used to bound this path was net/http's own
	// DefaultTransport IdleConnTimeout, a client-side timer rather than an
	// upstream cutoff. 300s stays well inside the observed window while still
	// recycling sockets, and it keeps the 31-300s request gaps that a 30s window
	// throws away.
	proxyPoolDefaultIdleConnTimeout = 300 * time.Second

	// proxyPoolDefaultMaxIdleConnsPerHost mirrors net/http's
	// DefaultMaxIdleConnsPerHost, so enabling the pool never retains more idle
	// sockets per upstream host than the standard library default.
	proxyPoolDefaultMaxIdleConnsPerHost = 2

	// proxyPoolKeepAlivePeriod is the TCP keepalive interval stamped on every
	// dialed upstream connection. Keepalive is what turns a silently dead peer
	// (NAT timeout, killed proxy, dropped mobile link) into an error in tens of
	// seconds instead of a request that blocks on TCP retransmits. It matches
	// net/http's DefaultTransport dialer and only probes idle sockets, so it
	// never truncates a long stream that is still flowing.
	proxyPoolKeepAlivePeriod = 30 * time.Second

	// proxyPoolCapacity bounds how many credential-scoped pools stay alive.
	//
	// The pool scales by eviction, not by sizing for the largest deployment:
	//   - 1 key: one entry, warm forever.
	//   - 100 keys: one entry each, nothing is evicted.
	//   - 100000 keys: the 512 most recently used credentials keep their pools
	//     and the rest fall back to the historical one-transport-per-request
	//     behavior, so memory stays at 512 transports and at most
	//     capacity*max idle sockets. Eviction only closes idle connections;
	//     in-flight requests keep their transport to completion.
	proxyPoolCapacity = 512
)

var proxyTransportCache = NewTransportCache[proxyPoolKey](proxyPoolCapacity)
var proxyPoolGeneration struct {
	sync.RWMutex
	current uint64
}

// AdvanceProxyPoolGeneration retires all older runtime snapshots before publication.
// cfg must be a private snapshot not yet visible to request handlers.
func AdvanceProxyPoolGeneration(cfg *config.Config) {
	if cfg == nil {
		return
	}
	proxyPoolGeneration.Lock()
	proxyPoolGeneration.current++
	cfg.ProxyPoolGeneration = proxyPoolGeneration.current
	proxyTransportCache.Purge()
	proxyPoolGeneration.Unlock()
}

// ResetProxyTransports closes idle connections in cached explicit proxy transports.
func ResetProxyTransports() { proxyTransportCache.Purge() }

func pooledProxyTransport(proxyURL string, cfg *config.Config, auth *cliproxyauth.Auth) *http.Transport {
	// Off unless this credential carries a connection-pool block. There is
	// deliberately no global switch: pooling one api key must never change the
	// transport lifecycle of another provider or OAuth identity. Without the
	// block every request keeps the historical behavior of a fresh transport.
	settings, enabled := resolveProxyPoolSettings(auth)
	if !enabled {
		return buildProxyTransport(proxyURL)
	}
	setting, errParse := proxyutil.Parse(proxyURL)
	if errParse != nil {
		return buildProxyTransport(proxyURL)
	}
	idle := proxyPoolDefaultIdleConnTimeout
	invalidIdle := ""
	if raw := strings.TrimSpace(settings.IdleConnTimeout); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			if d <= 0 {
				return unpooledBaseTransport(proxyURL, true)
			}
			idle = d
		} else {
			invalidIdle = raw
		}
	}
	max := proxyPoolDefaultMaxIdleConnsPerHost
	if settings.MaxIdleConnsPerHost != nil {
		if *settings.MaxIdleConnsPerHost <= 0 {
			return unpooledBaseTransport(proxyURL, true)
		}
		max = *settings.MaxIdleConnsPerHost
	}
	cacheProxy, poolLabel := proxyPoolCacheTarget(setting)
	key := proxyPoolKey{scope: proxyPoolScope(auth), proxy: cacheProxy, idle: idle, max: max, invalidIdle: invalidIdle}
	// Only a snapshot that carries a retired generation is fenced. Generation 0 means the
	// config was never published through the SDK generation fence (direct api callers,
	// helper probes, tests), so it cannot be stale and must still pool; treating it as
	// stale would silently disable pooling for every non-SDK caller that builds its own
	// config.Config.
	fenced := cfg != nil && cfg.ProxyPoolGeneration != 0
	if fenced {
		proxyPoolGeneration.RLock()
		if cfg.ProxyPoolGeneration != proxyPoolGeneration.current {
			proxyPoolGeneration.RUnlock()
			return unpooledBaseTransport(proxyURL, false)
		}
	}
	tr, err := proxyTransportCache.Get(key, func() (*http.Transport, error) {
		tr, errBuild := buildPooledBaseTransport(proxyURL)
		if errBuild != nil {
			return nil, errBuild
		}
		if invalidIdle != "" {
			log.Warnf("proxy connection pool: invalid idle-conn-timeout %q, using %s", invalidIdle, proxyPoolDefaultIdleConnTimeout)
		}
		tr.IdleConnTimeout = idle
		tr.MaxIdleConnsPerHost = max
		// A cloned default transport keeps MaxIdleConns=100, which across 512
		// credential pools would allow 51200 idle sockets. Bound the whole pool
		// of one credential instead: at most max idle sockets in total.
		tr.MaxIdleConns = max
		if dial := tr.DialContext; dial != nil {
			tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				conn, errDial := dial(ctx, network, addr)
				if errDial != nil {
					return nil, errDial
				}
				return stampProxyKeepAlive(conn), nil
			}
		}
		log.Debugf("proxy connection pool: created transport %s scope=%s idle=%s max=%d", poolLabel, key.scope, idle, max)
		return tr, nil
	})
	if fenced {
		proxyPoolGeneration.RUnlock()
	}
	if err != nil {
		log.Errorf("proxy connection pool: %v", err)
		return nil
	}
	return tr
}

// resolveProxyPoolSettings resolves the pooling settings carried by one api-key
// credential. Writing the connection-pool block is the opt-in, so an omitted
// `enabled` means enabled and only an explicit `enabled: false` turns pooling off
// (a key opting out of its group). Omitted fields fall back to the pool defaults.
func resolveProxyPoolSettings(auth *cliproxyauth.Auth) (config.ProxyConnectionPoolConfig, bool) {
	override := proxyPoolOverride(auth)
	if override == nil {
		return config.ProxyConnectionPoolConfig{}, false
	}
	if override.Enabled != nil && !*override.Enabled {
		return config.ProxyConnectionPoolConfig{}, false
	}
	return config.ProxyConnectionPoolConfig{
		Enabled:             override.Enabled,
		IdleConnTimeout:     strings.TrimSpace(override.IdleConnTimeout),
		MaxIdleConnsPerHost: override.MaxIdleConnsPerHost,
	}, true
}

func proxyPoolOverride(auth *cliproxyauth.Auth) *cliproxyauth.ProxyConnectionPoolOverride {
	if auth == nil {
		return nil
	}
	return auth.ProxyConnectionPool
}

// proxyPoolCacheTarget returns the cache key and redacted log label for a parsed
// proxy setting. Authenticated proxies fold a short SHA-256 digest of user:pass
// into the cache key instead of raw credentials: each proxy identity (including
// sticky-session residential proxy usernames) keeps its own pool, rotating proxy
// credentials immediately switches pools, and plaintext secrets never sit in the
// cache key or logs.
func proxyPoolCacheTarget(setting proxyutil.Setting) (cacheProxy, poolLabel string) {
	switch setting.Mode {
	case proxyutil.ModeDirect:
		// "direct" and "none" are the same setting and must not consume two slots.
		return "direct", "direct"
	case proxyutil.ModeInherit:
		return "inherit", "inherit"
	default:
		redacted := proxyutil.Redact(setting.Raw)
		if setting.URL != nil && setting.URL.User != nil {
			return redacted + "#" + proxyPoolSecretScope("proxyauth:", setting.URL.User.String()), redacted
		}
		return setting.Raw, redacted
	}
}

// proxyPoolAnonymousScope is the pool scope for requests that carry no credential.
const proxyPoolAnonymousScope = "anonymous"

// proxyPoolScope identifies the credential that owns a pool.
//
// Pooling must never mix accounts on one TCP/TLS connection: a shared connection
// correlates distinct identities upstream, and a rotated key would keep reusing a
// socket that was opened with the old one. Every credential therefore gets its own
// pool, mirroring the Antigravity executor's credential-scoped pools. The scope is
// only ever compared, never logged, so it cannot leak credential material.
func proxyPoolScope(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return proxyPoolAnonymousScope
	}
	if id := strings.TrimSpace(auth.ID); id != "" {
		return "id:" + id
	}
	if auth.Attributes != nil {
		if path := strings.TrimSpace(auth.Attributes[cliproxyauth.AttributePath]); path != "" {
			return "path:" + path
		}
		if source := strings.TrimSpace(auth.Attributes[cliproxyauth.AttributeSource]); source != "" {
			return "source:" + source
		}
	}
	// Auth.Label is deliberately not used: it is an optional human readable label
	// with no uniqueness guarantee, so two identities sharing a label would share a
	// pool. Fall back to digested credential material instead. The refresh token is
	// preferred over the access token because it survives token rotation.
	if refresh := authMetadataString(auth.Metadata, "refresh_token"); refresh != "" {
		return proxyPoolSecretScope("refresh:", refresh)
	}
	if access := authMetadataString(auth.Metadata, "access_token"); access != "" {
		return proxyPoolSecretScope("token:", access)
	}
	if apiKey := strings.TrimSpace(auth.Attributes[cliproxyauth.AttributeAPIKey]); apiKey != "" {
		return proxyPoolSecretScope("apikey:", apiKey)
	}
	return proxyPoolAnonymousScope
}

// proxyPoolSecretScope derives a scope from secret material. Only a short digest is
// retained, so a pool key cannot be turned back into the credential it came from.
func proxyPoolSecretScope(prefix, secret string) string {
	digest := sha256.Sum256([]byte(secret))
	return prefix + hex.EncodeToString(digest[:8])
}

func authMetadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, ok := metadata[key]
	if !ok {
		return ""
	}
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

// unpooledBaseTransport constructs a one-off transport for an opted-in credential
// that must bypass the shared cache (zero/negative pool bounds or a fenced config
// generation). Unlike buildProxyTransport, it still clones DefaultTransport when
// proxyURL is empty so DisableKeepAlives and pool isolation are honoured instead of
// falling back to the process-wide DefaultTransport pool.
func unpooledBaseTransport(proxyURL string, disableKeepAlives bool) *http.Transport {
	tr, errBuild := buildPooledBaseTransport(proxyURL)
	if errBuild != nil {
		log.Errorf("%v", errBuild)
		return nil
	}
	if tr != nil && disableKeepAlives {
		tr.DisableKeepAlives = true
	}
	return tr
}

// buildPooledBaseTransport builds the transport a pool entry starts from. An empty
// proxy setting keeps net/http's inherited environment-proxy behavior and only takes
// ownership of the pool; explicit settings go through proxyutil as before.
func buildPooledBaseTransport(proxyURL string) (*http.Transport, error) {
	if strings.TrimSpace(proxyURL) == "" {
		if base, ok := http.DefaultTransport.(*http.Transport); ok && base != nil {
			return base.Clone(), nil
		}
		return &http.Transport{}, nil
	}
	tr, _, errBuild := proxyutil.BuildHTTPTransport(proxyURL)
	if errBuild != nil {
		return nil, errBuild
	}
	return tr, nil
}

// stampProxyKeepAlive enables TCP keepalive on a freshly dialed upstream
// connection. Direct and HTTP-proxy transports inherit net/http's keepalive dialer,
// but the SOCKS5 path dials through proxy.Direct, which leaves keepalive at the OS
// default of hours; a peer that died silently would then pin a pooled connection for
// that long. Keepalive only probes idle sockets, so a live stream is never cut.
func stampProxyKeepAlive(conn net.Conn) net.Conn {
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		return conn
	}
	if err := tcp.SetKeepAlive(true); err != nil {
		return conn
	}
	if err := tcp.SetKeepAlivePeriod(proxyPoolKeepAlivePeriod); err != nil {
		return conn
	}
	return conn
}

var devinTransportCache = NewTransportCache[string](DefaultTransportCacheCapacity)

// NewDevinHTTPClient creates an HTTP client customized for Devin Connect-RPC upstream.
// Suppresses automatic Accept-Encoding: gzip while preserving connection reuse across requests.
func NewDevinHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) (clientOut *http.Client) {
	defer func() { clientOut = httptransport.Decorate(ctx, clientOut) }()
	// A request proxy replaces both the injected round tripper and credential/global proxy.
	// Respect explicitly injected context RoundTripper only when no request override is set.
	if cliproxyexecutor.RequestProxyURL(ctx) == "" && ctx != nil {
		if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
			if tr, ok := rt.(*http.Transport); ok {
				key := fmt.Sprintf("rt:%p", tr)
				cloned, err := devinTransportCache.Get(key, func() (*http.Transport, error) {
					c := tr.Clone()
					c.DisableCompression = true
					return c, nil
				})
				if err == nil && cloned != nil {
					return &http.Client{
						Transport: cloned,
						Timeout:   timeout,
					}
				}
			}
			return &http.Client{
				Transport: devinNoGzipRoundTripper{base: rt},
				Timeout:   timeout,
			}
		}
	}

	proxyURL := effectiveProxyURL(ctx, cfg, auth)

	tr, err := devinTransportCache.Get(proxyURL, func() (*http.Transport, error) {
		var base *http.Transport
		if proxyURL != "" {
			base = buildProxyTransport(proxyURL)
		}
		if base == nil {
			if dt, ok := http.DefaultTransport.(*http.Transport); ok {
				base = dt.Clone()
			} else {
				base = &http.Transport{}
			}
		}
		base.DisableCompression = true
		return base, nil
	})
	if err != nil || tr == nil {
		tr = &http.Transport{DisableCompression: true}
	}

	return &http.Client{
		Transport: tr,
		Timeout:   timeout,
	}
}

type devinNoGzipRoundTripper struct {
	base http.RoundTripper
}

func (rt devinNoGzipRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("Accept-Encoding") == "" {
		req.Header.Set("Accept-Encoding", "identity")
	}
	return rt.base.RoundTrip(req)
}

func effectiveProxyURL(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) string {
	if proxyURL := cliproxyexecutor.RequestProxyURL(ctx); proxyURL != "" {
		return proxyURL
	}
	if auth != nil {
		if proxyURL := strings.TrimSpace(auth.ProxyURL); proxyURL != "" {
			return proxyURL
		}
	}
	if cfg != nil {
		return strings.TrimSpace(cfg.ProxyURL)
	}
	return ""
}

// buildProxyTransport creates an HTTP transport configured for the given proxy URL.
// It supports SOCKS5, HTTP, and HTTPS proxy protocols.
//
// Parameters:
//   - proxyURL: The proxy URL string (e.g., "socks5://user:pass@host:port", "http://host:port")
//
// Returns:
//   - *http.Transport: A configured transport, or nil if the proxy URL is invalid
func buildProxyTransport(proxyURL string) *http.Transport {
	transport, _, errBuild := proxyutil.BuildHTTPTransport(proxyURL)
	if errBuild != nil {
		log.Errorf("%v", errBuild)
		return nil
	}
	return transport
}
