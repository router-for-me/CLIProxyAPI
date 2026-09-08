package executor

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func antigravityAuthWithProxy(proxyURL string) *cliproxyauth.Auth {
	return antigravityAuthWithIDAndProxy("antigravity-test", proxyURL)
}

func antigravityAuthWithIDAndProxy(id, proxyURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       id,
		ProxyURL: proxyURL,
		Metadata: map[string]any{
			"access_token": "test-access-token",
			"project_id":   "test-project",
			"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}
}

// TestNewAntigravityHTTPClientSharesTransport is the regression test for the bug where
// every proxied Antigravity request created a new transport, so no keep-alive connection
// was ever reused and every request paid a full TCP + TLS handshake.
func TestNewAntigravityHTTPClientSharesTransport(t *testing.T) {
	enabled := true
	poolCfg := func(sdkCfg ...config.SDKConfig) *config.Config {
		cfg := &config.Config{
			Antigravity: config.AntigravityConfig{
				ConnectionPool: config.AntigravityConnectionPoolConfig{
					Enabled: &enabled,
				},
			},
		}
		if len(sdkCfg) > 0 {
			cfg.SDKConfig = sdkCfg[0]
		}
		return cfg
	}

	cases := []struct {
		name string
		cfg  *config.Config
		auth *cliproxyauth.Auth
	}{
		{"direct", poolCfg(), antigravityAuthWithProxy("")},
		{"auth http proxy", poolCfg(), antigravityAuthWithProxy("http://127.0.0.1:18080")},
		{"auth socks5 proxy", poolCfg(), antigravityAuthWithProxy("socks5://127.0.0.1:18081")},
		{
			"config proxy",
			poolCfg(config.SDKConfig{ProxyURL: "http://127.0.0.1:18082"}),
			antigravityAuthWithProxy(""),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first := newAntigravityHTTPClient(context.Background(), tc.cfg, tc.auth, 0)
			second := newAntigravityHTTPClient(context.Background(), tc.cfg, tc.auth, 0)
			if first.Transport == nil || second.Transport == nil {
				t.Fatal("expected a transport to be configured")
			}
			if first.Transport != second.Transport {
				t.Fatalf("expected a shared transport, got %p and %p", first.Transport, second.Transport)
			}
			transport, ok := first.Transport.(*http.Transport)
			if !ok {
				t.Fatalf("expected *http.Transport, got %T", first.Transport)
			}
			if transport.ForceAttemptHTTP2 {
				t.Fatal("Antigravity transport must not attempt HTTP/2")
			}
			if len(transport.TLSNextProto) != 0 {
				t.Fatal("Antigravity transport must not allow an implicit HTTP/2 upgrade")
			}
			if transport.TLSClientConfig == nil {
				t.Fatal("Antigravity transport must carry an explicit TLS config")
			}
			if len(transport.TLSClientConfig.NextProtos) != 0 {
				t.Fatalf("Antigravity must omit ALPN like the native client, got %v", transport.TLSClientConfig.NextProtos)
			}
			// Native Antigravity uses DefaultMaxIdleConnsPerHost of 2.
			if transport.MaxIdleConnsPerHost < antigravityDefaultMaxIdleConnsPerHost {
				t.Fatalf("MaxIdleConnsPerHost = %d, want >= %d", transport.MaxIdleConnsPerHost, antigravityDefaultMaxIdleConnsPerHost)
			}
			if transport.MaxIdleConns > 0 && transport.MaxIdleConns < transport.MaxIdleConnsPerHost {
				t.Fatalf("MaxIdleConns = %d must not throttle MaxIdleConnsPerHost = %d", transport.MaxIdleConns, transport.MaxIdleConnsPerHost)
			}
			if transport.IdleConnTimeout > 0 && transport.IdleConnTimeout < antigravityDefaultIdleConnTimeout {
				t.Fatalf("IdleConnTimeout = %v, want >= %v", transport.IdleConnTimeout, antigravityDefaultIdleConnTimeout)
			}
			if transport.IdleConnTimeout > antigravityMaxAllowedIdleConnTimeout {
				t.Fatalf("IdleConnTimeout = %v exceeds GFE cutoff %v", transport.IdleConnTimeout, antigravityMaxAllowedIdleConnTimeout)
			}
		})
	}
}

// TestAntigravityPoolLimitsGuardsDefaultAndGFECutoff guards that Antigravity pool
// limits defaults to 30s timeout, 2 idle conns per host, and hard-caps idle timeout at 240s
// when connection pooling is enabled.
func TestAntigravityPoolLimitsGuardsDefaultAndGFECutoff(t *testing.T) {
	enabled := true
	poolCfg := &config.Config{
		Antigravity: config.AntigravityConfig{
			ConnectionPool: config.AntigravityConnectionPoolConfig{
				Enabled: &enabled,
			},
		},
	}

	wide := &http.Transport{
		MaxIdleConns:        512,
		MaxIdleConnsPerHost: 256,
		IdleConnTimeout:     time.Hour,
	}
	applyAntigravityPoolLimits(wide, poolCfg)
	// Larger connection limits are kept, but IdleConnTimeout MUST be hard-capped at 240s
	// to avoid exceeding the Google Frontend (GFE / ESF) 240-second cutoff.
	if wide.MaxIdleConnsPerHost != 256 || wide.MaxIdleConns != 512 {
		t.Fatalf("larger connection limits must be preserved, got perHost=%d total=%d",
			wide.MaxIdleConnsPerHost, wide.MaxIdleConns)
	}
	if wide.IdleConnTimeout != antigravityMaxAllowedIdleConnTimeout {
		t.Fatalf("IdleConnTimeout = %v, want capped at %v", wide.IdleConnTimeout, antigravityMaxAllowedIdleConnTimeout)
	}

	// Zero idle timeout (unlimited in Go) must be capped to safe default so it doesn't cause GFE RSTs.
	unlimited := &http.Transport{MaxIdleConns: 0, IdleConnTimeout: 0}
	applyAntigravityPoolLimits(unlimited, poolCfg)
	if unlimited.MaxIdleConns != 0 {
		t.Fatalf("MaxIdleConns = %d, want 0 (unlimited) to stay unlimited", unlimited.MaxIdleConns)
	}
	if unlimited.IdleConnTimeout != antigravityDefaultIdleConnTimeout {
		t.Fatalf("IdleConnTimeout = %v, want default %v", unlimited.IdleConnTimeout, antigravityDefaultIdleConnTimeout)
	}

	// A negative MaxIdleConnsPerHost is how an operator disables idle pooling; Go never
	// pools a connection in that case, so the intent must survive.
	disabled := &http.Transport{MaxIdleConnsPerHost: -1}
	applyAntigravityPoolLimits(disabled, poolCfg)
	if disabled.MaxIdleConnsPerHost != -1 {
		t.Fatalf("MaxIdleConnsPerHost = %d, want -1 (pooling disabled) to be preserved", disabled.MaxIdleConnsPerHost)
	}

	// Go's zero value defaults to 2 and 30s when enabled.
	defaulted := &http.Transport{}
	applyAntigravityPoolLimits(defaulted, poolCfg)
	if defaulted.MaxIdleConnsPerHost != antigravityDefaultMaxIdleConnsPerHost {
		t.Fatalf("MaxIdleConnsPerHost = %d, want %d", defaulted.MaxIdleConnsPerHost, antigravityDefaultMaxIdleConnsPerHost)
	}
	if defaulted.IdleConnTimeout != antigravityDefaultIdleConnTimeout {
		t.Fatalf("IdleConnTimeout = %v, want %v", defaulted.IdleConnTimeout, antigravityDefaultIdleConnTimeout)
	}

	// When cfg is nil (or default enabled: false), short mode is applied: MaxIdleConnsPerHost = -1
	defaultDisabled := &http.Transport{}
	applyAntigravityPoolLimits(defaultDisabled)
	if defaultDisabled.MaxIdleConnsPerHost != -1 {
		t.Fatalf("MaxIdleConnsPerHost = %d, want -1 when pooling is disabled by default", defaultDisabled.MaxIdleConnsPerHost)
	}

	applyAntigravityPoolLimits(nil) // must not panic
}

// TestNewAntigravityHTTPClientRejectsTypedNilContextTransport guards the fingerprint:
// a typed-nil *http.Transport satisfies the interface nil check in
// NewProxyAwareHTTPClient, and leaving it in place would make http.Client fall back to
// http.DefaultTransport, which advertises h2 over ALPN.
func TestNewAntigravityHTTPClientRejectsTypedNilContextTransport(t *testing.T) {
	var typedNil *http.Transport
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(typedNil))
	client := newAntigravityHTTPClient(ctx, &config.Config{}, antigravityAuthWithIDAndProxy("typed-nil", ""), 0)

	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatalf("expected a usable *http.Transport, got %#v", client.Transport)
	}
	if transport.ForceAttemptHTTP2 {
		t.Fatal("fallback transport must not attempt HTTP/2")
	}
	if len(transport.TLSClientConfig.NextProtos) != 0 {
		t.Fatalf("fallback transport must omit ALPN, got %v", transport.TLSClientConfig.NextProtos)
	}
}

// TestNewAntigravityHTTPClientKeepsForeignRoundTripper verifies a RoundTripper that is
// not an *http.Transport is left untouched instead of being replaced.
func TestNewAntigravityHTTPClientKeepsForeignRoundTripper(t *testing.T) {
	foreign := roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("unused")
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(foreign))
	client := newAntigravityHTTPClient(ctx, &config.Config{}, antigravityAuthWithIDAndProxy("foreign-rt", ""), 0)
	if _, isTransport := client.Transport.(*http.Transport); isTransport {
		t.Fatal("a non-*http.Transport RoundTripper must be preserved as-is")
	}
}

// TestAntigravityConcurrentRequestsReusePooledConnections is the regression test for
// the pool limit: with Go's default of 2 idle connections per host, repeated waves of
// concurrent requests on one credential keep re-handshaking.
func TestAntigravityConcurrentRequestsReusePooledConnections(t *testing.T) {
	var mu sync.Mutex
	remotes := map[string]struct{}{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		remotes[r.RemoteAddr] = struct{}{}
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	auth := antigravityAuthWithIDAndProxy("concurrent-reuse", "")
	enabledPool := true
	maxIdle := 100
	poolCfg := &config.Config{
		Antigravity: config.AntigravityConfig{
			ConnectionPool: config.AntigravityConnectionPoolConfig{
				Enabled:             &enabledPool,
				MaxIdleConnsPerHost: &maxIdle,
			},
		},
	}
	client := &http.Client{Transport: antigravityHTTP11Transport(auth, http.DefaultTransport.(*http.Transport), poolCfg)}

	const (
		waves      = 3
		perWave    = 8
		totalConns = waves * perWave
	)
	for wave := 0; wave < waves; wave++ {
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(perWave)
		for i := 0; i < perWave; i++ {
			go func() {
				defer wg.Done()
				<-start
				resp, errDo := client.Get(srv.URL)
				if errDo != nil {
					t.Error(errDo)
					return
				}
				if _, errDrain := io.Copy(io.Discard, resp.Body); errDrain != nil {
					t.Error(errDrain)
				}
				if errClose := resp.Body.Close(); errClose != nil {
					t.Error(errClose)
				}
			}()
		}
		close(start)
		wg.Wait()
	}

	mu.Lock()
	distinct := len(remotes)
	mu.Unlock()
	// The first wave legitimately opens perWave connections. Later waves must reuse
	// them; with MaxIdleConnsPerHost=2 only two survive each wave and distinct grows
	// towards totalConns instead.
	if distinct > perWave {
		t.Fatalf("%d waves of %d concurrent requests opened %d connections, want at most %d (unpooled worst case is %d)",
			waves, perWave, distinct, perWave, totalConns)
	}
}

// TestAntigravityConcurrentRequestsDefaultPoolLimitsToTwo verifies that when pooling is enabled
// without specifying max-idle-conns-per-host, it defaults to 2 (matching Go's default and official agy).
func TestAntigravityConcurrentRequestsDefaultPoolLimitsToTwo(t *testing.T) {
	var mu sync.Mutex
	remotes := map[string]struct{}{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		remotes[r.RemoteAddr] = struct{}{}
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	auth := antigravityAuthWithIDAndProxy("concurrent-default-2", "")
	enabledPool := true
	poolCfg := &config.Config{
		Antigravity: config.AntigravityConfig{
			ConnectionPool: config.AntigravityConnectionPoolConfig{
				Enabled: &enabledPool,
			},
		},
	}
	client := &http.Client{Transport: antigravityHTTP11Transport(auth, http.DefaultTransport.(*http.Transport), poolCfg)}

	const (
		waves      = 3
		perWave    = 8
		totalConns = waves * perWave
	)
	for wave := 0; wave < waves; wave++ {
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(perWave)
		for i := 0; i < perWave; i++ {
			go func() {
				defer wg.Done()
				<-start
				resp, errDo := client.Get(srv.URL)
				if errDo != nil {
					t.Error(errDo)
					return
				}
				if _, errDrain := io.Copy(io.Discard, resp.Body); errDrain != nil {
					t.Error(errDrain)
				}
				if errClose := resp.Body.Close(); errClose != nil {
					t.Error(errClose)
				}
			}()
		}
		close(start)
		wg.Wait()
	}

	mu.Lock()
	distinct := len(remotes)
	mu.Unlock()
	// With default limit of 2, only 2 survive each wave, so later waves open new conns: distinct > perWave
	if distinct <= perWave || distinct > totalConns {
		t.Fatalf("expected distinct connections between %d and %d for default limit of 2, got %d",
			perWave+1, totalConns, distinct)
	}
}

func TestNewAntigravityHTTPClientDistinctProxiesUseDistinctPools(t *testing.T) {
	cfg := &config.Config{}
	a := newAntigravityHTTPClient(context.Background(), cfg, antigravityAuthWithProxy("http://127.0.0.1:18090"), 0)
	b := newAntigravityHTTPClient(context.Background(), cfg, antigravityAuthWithProxy("http://127.0.0.1:18091"), 0)
	if a.Transport == b.Transport {
		t.Fatal("expected distinct proxies to use distinct connection pools")
	}
}

func TestNewAntigravityHTTPClientScopesPoolsByAuthIdentity(t *testing.T) {
	cfg := &config.Config{}
	const proxyURL = "http://127.0.0.1:18092"

	a1 := newAntigravityHTTPClient(context.Background(), cfg, antigravityAuthWithIDAndProxy("auth-a", proxyURL), 0)
	a2 := newAntigravityHTTPClient(context.Background(), cfg, antigravityAuthWithIDAndProxy("auth-a", proxyURL), 0)
	b := newAntigravityHTTPClient(context.Background(), cfg, antigravityAuthWithIDAndProxy("auth-b", proxyURL), 0)
	if a1.Transport != a2.Transport {
		t.Fatal("the same auth identity must share its connection pool across sessions")
	}
	if a1.Transport == b.Transport {
		t.Fatal("different auth identities must not share a proxied connection pool")
	}

	directA := newAntigravityHTTPClient(context.Background(), cfg, antigravityAuthWithIDAndProxy("direct-a", ""), 0)
	directB := newAntigravityHTTPClient(context.Background(), cfg, antigravityAuthWithIDAndProxy("direct-b", ""), 0)
	if directA.Transport == directB.Transport {
		t.Fatal("different auth identities must not share a direct connection pool")
	}
}

// TestAntigravityHTTP11TransportReusesPoolWithoutAuthID guards the pool cache
// against auths that carry no ID. Allocating a private pool per call would leak a
// connection pool, and the goroutines managing it, on every request, which is the
// pattern the original singleton transport was introduced to remove.
func TestAntigravityHTTP11TransportReusesPoolWithoutAuthID(t *testing.T) {
	base := http.DefaultTransport.(*http.Transport)

	anonymous := &cliproxyauth.Auth{}
	first := antigravityHTTP11Transport(anonymous, base)
	second := antigravityHTTP11Transport(anonymous, base)
	if first == nil || second == nil {
		t.Fatal("expected a transport for an auth without an ID")
	}
	if first != second {
		t.Fatal("an auth without any identity must reuse one shared pool instead of leaking a new pool per request")
	}
	if nilAuth := antigravityHTTP11Transport(nil, base); nilAuth != first {
		t.Fatal("a nil auth carries no credential to isolate and must share the same pool")
	}

	// An auth without an ID but with credential material stays isolated from both the
	// anonymous pool and from a different credential.
	tokenA := antigravityHTTP11Transport(&cliproxyauth.Auth{Metadata: map[string]any{"access_token": "token-a"}}, base)
	tokenB := antigravityHTTP11Transport(&cliproxyauth.Auth{Metadata: map[string]any{"access_token": "token-b"}}, base)
	if tokenA == first || tokenB == first {
		t.Fatal("a credential with an access token must not fall back to the anonymous pool")
	}
	if tokenA == tokenB {
		t.Fatal("different access tokens must not share a connection pool")
	}
	if again := antigravityHTTP11Transport(&cliproxyauth.Auth{Metadata: map[string]any{"access_token": "token-a"}}, base); again != tokenA {
		t.Fatal("the same access token must resolve to the same pool across requests")
	}

	// Identified auths keep sharing their pool.
	identified := &cliproxyauth.Auth{ID: "stable-identity"}
	if antigravityHTTP11Transport(identified, base) != antigravityHTTP11Transport(identified, base) {
		t.Fatal("an auth with a stable ID must reuse its cached pool")
	}
}

func TestAntigravityTransportScopeFallsBackToStableMarkers(t *testing.T) {
	digest := func(prefix, secret string) string {
		sum := sha256.Sum256([]byte(secret))
		return prefix + hex.EncodeToString(sum[:8])
	}
	cases := []struct {
		name string
		auth *cliproxyauth.Auth
		want string
	}{
		{"nil auth", nil, antigravityAnonymousTransportScope},
		{"empty auth", &cliproxyauth.Auth{}, antigravityAnonymousTransportScope},
		{"blank id", &cliproxyauth.Auth{ID: " \t "}, antigravityAnonymousTransportScope},
		{"stable id", &cliproxyauth.Auth{ID: " auth-1 "}, "id:auth-1"},
		{
			"id wins over path",
			&cliproxyauth.Auth{ID: "auth-1", Attributes: map[string]string{cliproxyauth.AttributePath: "/a.json"}},
			"id:auth-1",
		},
		{
			"path fallback",
			&cliproxyauth.Auth{Attributes: map[string]string{cliproxyauth.AttributePath: " /auths/a.json "}},
			"path:/auths/a.json",
		},
		{
			"source fallback",
			&cliproxyauth.Auth{Attributes: map[string]string{cliproxyauth.AttributeSource: "/auths/b.json"}},
			"source:/auths/b.json",
		},
		{
			// Auth.Label is a logging label with no uniqueness guarantee, so it must never
			// become a pool scope on its own.
			"label alone is not an identity",
			&cliproxyauth.Auth{Label: "account-c"},
			antigravityAnonymousTransportScope,
		},
		{
			"refresh token preferred over access token",
			&cliproxyauth.Auth{Metadata: map[string]any{"refresh_token": "r-1", "access_token": "a-1"}},
			digest("refresh:", "r-1"),
		},
		{
			"access token fallback",
			&cliproxyauth.Auth{Metadata: map[string]any{"access_token": "secret-token"}},
			digest("token:", "secret-token"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := antigravityTransportScope(tc.auth); got != tc.want {
				t.Fatalf("antigravityTransportScope() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAntigravityTransportScopeIgnoresNonUniqueLabel is the regression test for using
// Auth.Label as an identity: two different credentials that happen to share a label
// must not end up on the same TCP/TLS pool.
func TestAntigravityTransportScopeIgnoresNonUniqueLabel(t *testing.T) {
	first := &cliproxyauth.Auth{Label: "shared-label", Metadata: map[string]any{"refresh_token": "refresh-a"}}
	second := &cliproxyauth.Auth{Label: "shared-label", Metadata: map[string]any{"refresh_token": "refresh-b"}}
	if antigravityTransportScope(first) == antigravityTransportScope(second) {
		t.Fatal("credentials sharing only a label must not share a pool scope")
	}

	base := http.DefaultTransport.(*http.Transport)
	if antigravityHTTP11Transport(first, base) == antigravityHTTP11Transport(second, base) {
		t.Fatal("credentials sharing only a label must not share a connection pool")
	}
}

// TestAntigravityTransportScopeSurvivesAccessTokenRotation covers the refresh flow:
// refreshing an access token must not move a credential onto a new pool, and a refresh
// request that runs before any access token exists must resolve to the same scope.
func TestAntigravityTransportScopeSurvivesAccessTokenRotation(t *testing.T) {
	refreshOnly := &cliproxyauth.Auth{Metadata: map[string]any{"refresh_token": "stable-refresh"}}
	beforeRotation := &cliproxyauth.Auth{Metadata: map[string]any{"refresh_token": "stable-refresh", "access_token": "access-1"}}
	afterRotation := &cliproxyauth.Auth{Metadata: map[string]any{"refresh_token": "stable-refresh", "access_token": "access-2"}}

	want := antigravityTransportScope(refreshOnly)
	if got := antigravityTransportScope(beforeRotation); got != want {
		t.Fatalf("scope before rotation = %q, want %q", got, want)
	}
	if got := antigravityTransportScope(afterRotation); got != want {
		t.Fatalf("scope after rotation = %q, want %q (access token rotation must not churn pools)", got, want)
	}
}

// TestAntigravityTransportScopeNeverLeaksToken ensures the credential-derived scope
// only carries a short digest, so a pool key can never reveal the credential.
func TestAntigravityTransportScopeNeverLeaksToken(t *testing.T) {
	const (
		accessToken  = "ya29.super-secret-access-token"
		refreshToken = "1//super-secret-refresh-token"
	)
	for _, tc := range []struct {
		name   string
		auth   *cliproxyauth.Auth
		secret string
		prefix string
	}{
		{"access token", &cliproxyauth.Auth{Metadata: map[string]any{"access_token": accessToken}}, accessToken, "token:"},
		{"refresh token", &cliproxyauth.Auth{Metadata: map[string]any{"refresh_token": refreshToken}}, refreshToken, "refresh:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope := antigravityTransportScope(tc.auth)
			if strings.Contains(scope, tc.secret) {
				t.Fatalf("scope %q must not embed the credential", scope)
			}
			if !strings.HasPrefix(scope, tc.prefix) || len(scope) != len(tc.prefix)+16 {
				t.Fatalf("scope = %q, want a short %s digest", scope, tc.prefix)
			}
		})
	}
}

// TestAntigravityTransportCacheEvictsStalePools covers the bounded cache: rotating a
// credential's proxy must not accumulate pools forever.
func TestAntigravityTransportCacheEvictsStalePools(t *testing.T) {
	original := antigravityTransports
	antigravityTransports = helps.NewTransportCache[antigravityTransportKey](4)
	t.Cleanup(func() {
		antigravityTransports.Purge()
		antigravityTransports = original
	})

	cfg := &config.Config{}
	for i := 0; i < 40; i++ {
		auth := antigravityAuthWithIDAndProxy("rotating-auth", fmt.Sprintf("http://127.0.0.1:%d", 19000+i))
		if client := newAntigravityHTTPClient(context.Background(), cfg, auth, 0); client.Transport == nil {
			t.Fatalf("request %d: expected a transport", i)
		}
	}
	if got := antigravityTransports.Len(); got > 4 {
		t.Fatalf("cache holds %d pools, want at most the capacity of 4", got)
	}

	// A per-request base transport from the request context must not grow the cache
	// without bound either.
	auth := antigravityAuthWithIDAndProxy("ctx-auth", "")
	for i := 0; i < 40; i++ {
		fresh := http.DefaultTransport.(*http.Transport).Clone()
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(fresh))
		if client := newAntigravityHTTPClient(ctx, cfg, auth, 0); client.Transport == nil {
			t.Fatalf("ctx request %d: expected a transport", i)
		}
	}
	if got := antigravityTransports.Len(); got > 4 {
		t.Fatalf("cache holds %d pools after context transports, want at most 4", got)
	}
}

// TestAntigravityProxiedHTTP11TransportRejectsInvalidProxy verifies the caller can
// fall back instead of caching a broken pool.
func TestAntigravityProxiedHTTP11TransportRejectsInvalidProxy(t *testing.T) {
	auth := antigravityAuthWithIDAndProxy("invalid-proxy", "ftp://127.0.0.1:1")
	if transport := antigravityProxiedHTTP11Transport(auth, "ftp://127.0.0.1:1"); transport != nil {
		t.Fatal("an unsupported proxy scheme must not produce a transport")
	}
	if transport := antigravityProxiedHTTP11Transport(auth, "   "); transport != nil {
		t.Fatal("a blank proxy must not produce a transport")
	}
	// A failed build must not occupy a cache slot, so a later valid setting still works.
	if transport := antigravityProxiedHTTP11Transport(auth, "http://127.0.0.1:18099"); transport == nil {
		t.Fatal("a valid proxy must produce a transport")
	}
}

func TestAntigravityTransportMatchesNativeTLSProfile(t *testing.T) {
	var clientHelloProtos []string
	var requestProto string
	var tlsVersion uint16
	var negotiatedProtocol string

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestProto = r.Proto
		if r.TLS != nil {
			tlsVersion = r.TLS.Version
			negotiatedProtocol = r.TLS.NegotiatedProtocol
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	server.TLS = &tls.Config{
		MinVersion: tls.VersionTLS13,
		MaxVersion: tls.VersionTLS13,
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			clientHelloProtos = append([]string(nil), hello.SupportedProtos...)
			return nil, nil
		},
	}
	server.StartTLS()
	defer server.Close()

	base := http.DefaultTransport.(*http.Transport).Clone()
	base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	transport := antigravityHTTP11Transport(antigravityAuthWithIDAndProxy("native-tls-profile", ""), base)
	resp, errDo := (&http.Client{Transport: transport}).Get(server.URL)
	if errDo != nil {
		t.Fatalf("GET() error = %v", errDo)
	}
	if errClose := resp.Body.Close(); errClose != nil {
		t.Fatalf("close response body: %v", errClose)
	}

	if len(clientHelloProtos) != 0 {
		t.Fatalf("ClientHello ALPN = %v, want no ALPN extension", clientHelloProtos)
	}
	if requestProto != "HTTP/1.1" {
		t.Fatalf("request protocol = %q, want HTTP/1.1", requestProto)
	}
	if tlsVersion != tls.VersionTLS13 {
		t.Fatalf("TLS version = %#x, want TLS 1.3", tlsVersion)
	}
	if negotiatedProtocol != "" {
		t.Fatalf("negotiated ALPN = %q, want empty", negotiatedProtocol)
	}
}

// TestAntigravityProxiedRequestsReuseOneConnection proves the end-to-end effect:
// repeated Antigravity clients built for the same auth send every request over a
// single pooled connection.
func TestAntigravityProxiedRequestsReuseOneConnection(t *testing.T) {
	var mu sync.Mutex
	remotes := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		remotes[r.RemoteAddr]++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	enabled := true
	cfg := &config.Config{
		Antigravity: config.AntigravityConfig{
			ConnectionPool: config.AntigravityConnectionPoolConfig{
				Enabled: &enabled,
			},
		},
	}
	auth := antigravityAuthWithProxy(srv.URL)
	const requests = 8
	for i := 0; i < requests; i++ {
		client := newAntigravityHTTPClient(context.Background(), cfg, auth, 0)
		req, errReq := http.NewRequest(http.MethodGet, "http://antigravity.invalid/v1internal:streamGenerateContent", nil)
		if errReq != nil {
			t.Fatalf("NewRequest() error = %v", errReq)
		}
		resp, errDo := client.Do(req)
		if errDo != nil {
			t.Fatalf("request %d error = %v", i, errDo)
		}
		_ = resp.Body.Close()
	}

	mu.Lock()
	distinct := len(remotes)
	mu.Unlock()
	if distinct != 1 {
		t.Fatalf("expected %d requests to share one connection, got %d connections", requests, distinct)
	}
}

// TestAntigravityPoolConfigIdleTimeoutCustomAndCap verifies that custom idle-conn-timeout
// is honored (e.g. 10s, 30s) and hard-capped at 210s when configured higher (e.g. 10m),
// and that connection pool is disabled by default (enabled: false).
func TestAntigravityPoolConfigIdleTimeoutCustomAndCap(t *testing.T) {
	enabledTrue := true
	enabledFalse := false
	excessiveMaxIdle := 1000000
	negativeMaxIdle := -5

	cases := []struct {
		name        string
		cfg         *config.Config
		wantTimeout time.Duration
		wantShort   bool
		wantMaxIdle int
	}{
		{
			name:      "default config disables pooling (enabled: false by default)",
			cfg:       &config.Config{},
			wantShort: true,
		},
		{
			name: "explicit enabled false activates short mode",
			cfg: &config.Config{
				Antigravity: config.AntigravityConfig{
					ConnectionPool: config.AntigravityConnectionPoolConfig{
						Enabled: &enabledFalse,
					},
				},
			},
			wantShort: true,
		},
		{
			name: "enabled true defaults to 30s",
			cfg: &config.Config{
				Antigravity: config.AntigravityConfig{
					ConnectionPool: config.AntigravityConnectionPoolConfig{
						Enabled: &enabledTrue,
					},
				},
			},
			wantTimeout: 30 * time.Second,
		},
		{
			name: "enabled true with custom 15s via connection-pool struct",
			cfg: &config.Config{
				Antigravity: config.AntigravityConfig{
					ConnectionPool: config.AntigravityConnectionPoolConfig{
						Enabled:         &enabledTrue,
						IdleConnTimeout: "15s",
					},
				},
			},
			wantTimeout: 15 * time.Second,
		},
		{
			name: "enabled true with custom 45s via connection-pool struct",
			cfg: &config.Config{
				Antigravity: config.AntigravityConfig{
					ConnectionPool: config.AntigravityConnectionPoolConfig{
						Enabled:         &enabledTrue,
						IdleConnTimeout: "45s",
					},
				},
			},
			wantTimeout: 45 * time.Second,
		},
		{
			name: "enabled true with 10m timeout hard-capped at 210s GFE cutoff",
			cfg: &config.Config{
				Antigravity: config.AntigravityConfig{
					ConnectionPool: config.AntigravityConnectionPoolConfig{
						Enabled:         &enabledTrue,
						IdleConnTimeout: "10m",
					},
				},
			},
			wantTimeout: 210 * time.Second,
		},
		{
			name: "enabled true with 500s timeout hard-capped at 210s GFE cutoff",
			cfg: &config.Config{
				Antigravity: config.AntigravityConfig{
					ConnectionPool: config.AntigravityConnectionPoolConfig{
						Enabled:         &enabledTrue,
						IdleConnTimeout: "500s",
					},
				},
			},
			wantTimeout: 210 * time.Second,
		},
		{
			name: "enabled true with 0s timeout activates short mode",
			cfg: &config.Config{
				Antigravity: config.AntigravityConfig{
					ConnectionPool: config.AntigravityConnectionPoolConfig{
						Enabled:         &enabledTrue,
						IdleConnTimeout: "0s",
					},
				},
			},
			wantShort: true,
		},
		{
			name: "enabled true with invalid timeout string falls back to 30s default",
			cfg: &config.Config{
				Antigravity: config.AntigravityConfig{
					ConnectionPool: config.AntigravityConnectionPoolConfig{
						Enabled:         &enabledTrue,
						IdleConnTimeout: "invalid-timeout",
					},
				},
			},
			wantTimeout: 30 * time.Second,
		},
		{
			name: "enabled true with negative max-idle activates short mode",
			cfg: &config.Config{
				Antigravity: config.AntigravityConfig{
					ConnectionPool: config.AntigravityConnectionPoolConfig{
						Enabled:             &enabledTrue,
						MaxIdleConnsPerHost: &negativeMaxIdle,
					},
				},
			},
			wantShort: true,
		},
		{
			name: "enabled true with excessive max-idle capped at 100",
			cfg: &config.Config{
				Antigravity: config.AntigravityConfig{
					ConnectionPool: config.AntigravityConnectionPoolConfig{
						Enabled:             &enabledTrue,
						MaxIdleConnsPerHost: &excessiveMaxIdle,
					},
				},
			},
			wantTimeout: 30 * time.Second,
			wantMaxIdle: 100,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth := antigravityAuthWithIDAndProxy("cfg-test-"+tc.name, "")
			client := newAntigravityHTTPClient(context.Background(), tc.cfg, auth, 0)
			transport, ok := client.Transport.(*http.Transport)
			if !ok {
				t.Fatalf("expected *http.Transport, got %T", client.Transport)
			}
			if tc.wantShort {
				if transport.MaxIdleConnsPerHost != -1 {
					t.Fatalf("MaxIdleConnsPerHost = %d, want -1 for short mode", transport.MaxIdleConnsPerHost)
				}
				if transport.DisableKeepAlives {
					t.Fatal("DisableKeepAlives must be false to avoid adding Connection: close header")
				}
			} else {
				if transport.IdleConnTimeout != tc.wantTimeout {
					t.Fatalf("IdleConnTimeout = %v, want %v", transport.IdleConnTimeout, tc.wantTimeout)
				}
				if tc.wantMaxIdle > 0 && transport.MaxIdleConnsPerHost != tc.wantMaxIdle {
					t.Fatalf("MaxIdleConnsPerHost = %d, want %d", transport.MaxIdleConnsPerHost, tc.wantMaxIdle)
				}
			}
		})
	}
}

// TestAntigravityShortModeNeverAdvertisesConnectionCloseAndDisconnects verifies that
// in short mode (MaxIdleConnsPerHost=-1), repeated requests do not reuse connections,
// but the HTTP wire request never leaks "Connection: close".
func TestAntigravityShortModeNeverAdvertisesConnectionCloseAndDisconnects(t *testing.T) {
	var mu sync.Mutex
	remotes := map[string]int{}
	var connectionHeaders []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		remotes[r.RemoteAddr]++
		connectionHeaders = append(connectionHeaders, r.Header.Get("Connection"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &config.Config{}
	auth := antigravityAuthWithProxy(srv.URL)

	const requests = 4
	for i := 0; i < requests; i++ {
		client := newAntigravityHTTPClient(context.Background(), cfg, auth, 0)
		req, errReq := http.NewRequest(http.MethodGet, "http://antigravity.invalid/v1internal:streamGenerateContent", nil)
		if errReq != nil {
			t.Fatalf("NewRequest() error = %v", errReq)
		}
		resp, errDo := client.Do(req)
		if errDo != nil {
			t.Fatalf("request %d error = %v", i, errDo)
		}
		_ = resp.Body.Close()
	}

	mu.Lock()
	distinct := len(remotes)
	headers := append([]string(nil), connectionHeaders...)
	mu.Unlock()

	// In short mode, every request should use a fresh connection
	if distinct != requests {
		t.Fatalf("short mode: expected %d distinct connections, got %d", requests, distinct)
	}
	// Crucial: wire request must not leak "Connection: close"
	for i, h := range headers {
		if strings.Contains(strings.ToLower(h), "close") {
			t.Fatalf("request %d leaked Connection: close header: %q", i, h)
		}
	}
}

// TestCloseAntigravityAuthIdleTransportsEvictsIdlePool verifies that calling
// closeAntigravityAuthIdleTransports cleans up cached transports for that auth.
func TestCloseAntigravityAuthIdleTransportsEvictsIdlePool(t *testing.T) {
	auth := antigravityAuthWithIDAndProxy("auth-to-close", "")
	cfg := &config.Config{}

	client := newAntigravityHTTPClient(context.Background(), cfg, auth, 0)
	if client.Transport == nil {
		t.Fatal("expected transport to be initialized")
	}

	scope := antigravityTransportScope(auth)
	key := antigravityTransportKey{credential: scope, base: antigravityBaseTransport}

	// Verify it's cached
	if !antigravityTransports.Contains(key) {
		t.Fatal("expected transport to be in cache")
	}

	// Trigger close
	closeAntigravityAuthIdleTransports(auth)

	// Verify it's removed from cache
	if antigravityTransports.Contains(key) {
		t.Fatal("expected transport to be removed from cache after closeAntigravityAuthIdleTransports")
	}
}

// TestAntigravityCountTokens429EvictsIdleTransports verifies that a 429 during count_tokens
// evicts cached transports for that auth.
func TestAntigravityCountTokens429EvictsIdleTransports(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":429,"message":"Resource exhausted","status":"RESOURCE_EXHAUSTED"}}`))
	}))
	defer srv.Close()

	auth := &cliproxyauth.Auth{
		ID:         "auth-tokens-429-evict",
		Provider:   "antigravity",
		Attributes: map[string]string{"base_url": srv.URL},
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}

	enabledPool := true
	cfg := &config.Config{
		Antigravity: config.AntigravityConfig{
			ConnectionPool: config.AntigravityConnectionPoolConfig{
				Enabled: &enabledPool,
			},
		},
	}
	exec := NewAntigravityExecutor(cfg)

	// Build client to populate cache
	client := newAntigravityHTTPClient(context.Background(), cfg, auth, 0)
	if client.Transport == nil {
		t.Fatal("expected transport to be initialized")
	}

	scope := antigravityTransportScope(auth)
	key := antigravityTransportKey{credential: scope, base: antigravityBaseTransport}
	if !antigravityTransports.Contains(key) {
		t.Fatal("expected transport to be in cache before count_tokens")
	}

	// Trigger CountTokens which returns 429
	payload := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
	_, _ = exec.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-3.6-flash-high",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatGemini,
	})

	// Verify transport is evicted
	if antigravityTransports.Contains(key) {
		t.Fatal("expected transport to be evicted after 429 in CountTokens")
	}
}
