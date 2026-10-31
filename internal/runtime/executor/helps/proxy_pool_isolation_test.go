package helps

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// These tests share the process-wide pool and must not run in parallel.

func enabledPoolConfig() *config.Config { return testConfig() }

// TestProxyPoolIsolatesCredentials pins requirement 2: two keys must never share a
// pool, because a shared TCP/TLS connection would correlate distinct identities
// upstream and would let a rotated key keep using an old socket.
func TestProxyPoolIsolatesCredentials(t *testing.T) {
	ResetProxyTransports()
	t.Cleanup(ResetProxyTransports)
	cfg := enabledPoolConfig()

	first := poolOptInAuth("key-a")
	second := poolOptInAuth("key-b")
	for _, proxyURL := range []string{
		"",
		"direct",
		"http://127.0.0.1:8080",
		"socks5://127.0.0.1:1080",
		"http://user:pass@127.0.0.1:8080",
		"socks5://user:pass@127.0.0.1:1080",
	} {
		firstTransport := pooledProxyTransport(proxyURL, cfg, first)
		secondTransport := pooledProxyTransport(proxyURL, cfg, second)
		if firstTransport == nil || secondTransport == nil {
			t.Fatalf("proxy %q: pooled transports missing", proxyURL)
		}
		if firstTransport == secondTransport {
			t.Fatalf("proxy %q: different credentials shared one pool", proxyURL)
		}
		if pooledProxyTransport(proxyURL, cfg, first) != firstTransport {
			t.Fatalf("proxy %q: the same credential must reuse its pool", proxyURL)
		}
	}
	// Two unauthenticated proxies on the same key must also keep separate pools.
	if pooledProxyTransport("http://127.0.0.1:8080", cfg, first) == pooledProxyTransport("http://127.0.0.1:8081", cfg, first) {
		t.Fatal("different unauthenticated proxies shared one pool")
	}

	// API-key credentials without an ID fall back to a digested key scope.
	keyOne := poolAuth("", poolEnabled())
	keyOne.Attributes = map[string]string{cliproxyauth.AttributeAPIKey: "sk-one"}
	keyTwo := poolAuth("", poolEnabled())
	keyTwo.Attributes = map[string]string{cliproxyauth.AttributeAPIKey: "sk-two"}
	if pooledProxyTransport("direct", cfg, keyOne) == pooledProxyTransport("direct", cfg, keyTwo) {
		t.Fatal("different API keys share one pool")
	}
	if scope := proxyPoolScope(keyOne); strings.Contains(scope, "sk-one") {
		t.Fatalf("pool scope leaks credential material: %q", scope)
	}
	if proxyPoolScope(keyOne) != proxyPoolScope(&cliproxyauth.Auth{Attributes: map[string]string{cliproxyauth.AttributeAPIKey: "sk-one"}}) {
		t.Fatal("identical credentials must map to one scope")
	}
	// A human readable label is not an identity.
	if proxyPoolScope(&cliproxyauth.Auth{Label: "same"}) != proxyPoolScope(&cliproxyauth.Auth{Label: "same"}) {
		t.Fatal("label-only auths must share the anonymous scope")
	}
	if got := proxyPoolScope(&cliproxyauth.Auth{Label: "shared"}); got != proxyPoolAnonymousScope {
		t.Fatalf("label must not become a scope, got %q", got)
	}
}

// TestProxyPoolBoundsIdleSocketsPerCredential covers requirement 4: one credential
// must not be able to hoard idle sockets, no matter how many hosts it talks to.
func TestProxyPoolBoundsIdleSocketsPerCredential(t *testing.T) {
	ResetProxyTransports()
	t.Cleanup(ResetProxyTransports)
	tr := pooledProxyTransport("direct", enabledPoolConfig(), poolOptInAuth("bounded"))
	if tr == nil {
		t.Fatal("pooled transport missing")
	}
	if tr.MaxIdleConns != proxyPoolDefaultMaxIdleConnsPerHost {
		t.Fatalf("MaxIdleConns = %d, want %d", tr.MaxIdleConns, proxyPoolDefaultMaxIdleConnsPerHost)
	}
	if tr.MaxIdleConnsPerHost != proxyPoolDefaultMaxIdleConnsPerHost {
		t.Fatalf("MaxIdleConnsPerHost = %d, want %d", tr.MaxIdleConnsPerHost, proxyPoolDefaultMaxIdleConnsPerHost)
	}
	if tr.IdleConnTimeout != proxyPoolDefaultIdleConnTimeout {
		t.Fatalf("IdleConnTimeout = %s, want %s", tr.IdleConnTimeout, proxyPoolDefaultIdleConnTimeout)
	}
}

// TestProxyPoolCapacityBoundsManyCredentials covers requirement 4 for the large
// deployment: 100000 keys must not mean 100000 pools. The cache stays at its
// capacity and every credential still gets a usable transport.
func TestProxyPoolCapacityBoundsManyCredentials(t *testing.T) {
	ResetProxyTransports()
	t.Cleanup(ResetProxyTransports)
	cfg := enabledPoolConfig()
	total := proxyPoolCapacity + 120
	for i := 0; i < total; i++ {
		auth := poolOptInAuth("key-" + strconv.Itoa(i))
		if tr := pooledProxyTransport("direct", cfg, auth); tr == nil {
			t.Fatalf("credential %d got no transport", i)
		}
	}
	if got := proxyTransportCache.Len(); got != proxyPoolCapacity {
		t.Fatalf("cache holds %d pools, want %d", got, proxyPoolCapacity)
	}
	// A hot credential keeps its pool while cold ones churn through it.
	hot := poolOptInAuth("hot")
	hotTransport := pooledProxyTransport("direct", cfg, hot)
	for i := 0; i < 16; i++ {
		pooledProxyTransport("direct", cfg, poolOptInAuth("cold-"+strconv.Itoa(i)))
	}
	if pooledProxyTransport("direct", cfg, hot) != hotTransport {
		t.Fatal("recently used credential lost its pool")
	}
	if got := proxyTransportCache.Len(); got > proxyPoolCapacity {
		t.Fatalf("cache grew past capacity: %d", got)
	}
}

// TestProxyPoolDropsUnexpectedlyClosedConnection covers requirement 3: when the
// peer disappears mid-life, the pooled connection must be discarded instead of
// being reused, and the following request must succeed on a fresh connection.
func TestProxyPoolDropsUnexpectedlyClosedConnection(t *testing.T) {
	ResetProxyTransports()
	t.Cleanup(ResetProxyTransports)
	var opened atomic.Int32
	var mu sync.Mutex
	var live []net.Conn
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	server.Config.ConnState = func(conn net.Conn, state http.ConnState) {
		if state != http.StateNew {
			return
		}
		opened.Add(1)
		mu.Lock()
		live = append(live, conn)
		mu.Unlock()
	}
	server.Start()
	defer server.Close()

	cfg := enabledPoolConfig()
	auth := poolAuth("dead-peer", poolEnabled())
	auth.ProxyURL = "direct"
	request := func() {
		client := NewProxyAwareHTTPClient(context.Background(), cfg, auth, 0)
		resp, err := client.Get(server.URL)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		if _, errCopy := io.Copy(io.Discard, resp.Body); errCopy != nil {
			t.Fatalf("read body: %v", errCopy)
		}
		if errClose := resp.Body.Close(); errClose != nil {
			t.Fatalf("close body: %v", errClose)
		}
	}

	request()
	if got := opened.Load(); got != 1 {
		t.Fatalf("connections after first request = %d, want 1", got)
	}
	// Kill the pooled socket from the server side, the way a crashed upstream or a
	// NAT timeout would surface.
	mu.Lock()
	for _, conn := range live {
		_ = conn.Close()
	}
	mu.Unlock()
	request()
	if got := opened.Load(); got < 2 {
		t.Fatalf("connections = %d, want a fresh dial after the peer closed", got)
	}
}
