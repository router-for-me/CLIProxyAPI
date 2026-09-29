package service

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withRelayHTTPTransportSettings(t *testing.T) {
	t.Helper()
	prevMaxIdle := common.RelayMaxIdleConns
	prevPerHost := common.RelayMaxIdleConnsPerHost
	prevTimeout := common.RelayIdleConnTimeout
	common.RelayMaxIdleConns = 500
	common.RelayMaxIdleConnsPerHost = 100
	common.RelayIdleConnTimeout = 90
	t.Cleanup(func() {
		common.RelayMaxIdleConns = prevMaxIdle
		common.RelayMaxIdleConnsPerHost = prevPerHost
		common.RelayIdleConnTimeout = prevTimeout
	})
}

func initDefaultHTTPClientFixture(t *testing.T) *http.Client {
	t.Helper()
	withRelayHTTPTransportSettings(t)
	if httpClient == nil {
		InitHttpClient()
	} else {
		ResetProxyClientCache()
	}
	require.NotNil(t, httpClient)
	t.Cleanup(ResetProxyClientCache)
	return httpClient
}

func TestShardedRoundTripperPerOriginRotation(t *testing.T) {
	s := &shardedRoundTripper{n: 4}
	originA := "https://a.example:443"
	originB := "https://b.example:443"

	gotA := make([]uint32, 0, 8)
	for range 8 {
		gotA = append(gotA, s.pickShard(originA))
	}
	assert.Equal(t, []uint32{0, 1, 2, 3, 0, 1, 2, 3}, gotA)

	gotB := make([]uint32, 0, 4)
	for range 4 {
		gotB = append(gotB, s.pickShard(originB))
	}
	assert.Equal(t, []uint32{0, 1, 2, 3}, gotB, "independent origins must have independent counters")

	var wg sync.WaitGroup
	const workers = 32
	const perWorker = 50
	var badShardCount atomic.Uint32
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for range perWorker {
				idx := s.pickShard(originA)
				if idx >= 4 {
					badShardCount.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, uint32(0), badShardCount.Load())
}

func TestOriginKeyUsesSchemeAndHost(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "HTTPS://Example.COM:8443/path", nil)
	assert.Equal(t, "https://Example.COM:8443", originKey(req))
}

func testTLSClientConfig(t *testing.T, server *httptest.Server) *tls.Config {
	t.Helper()
	pool := x509.NewCertPool()
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	require.True(t, pool.AppendCertsFromPEM(certPEM))
	return &tls.Config{RootCAs: pool}
}

func startHTTP2TLSServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func drainClose(t *testing.T, resp *http.Response) {
	t.Helper()
	require.NotNil(t, resp)
	_, err := io.Copy(io.Discard, resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
}

func TestAutoOneShardNegotiatesHTTP2SingleConnection(t *testing.T) {
	withRelayHTTPTransportSettings(t)

	var mu sync.Mutex
	addrs := make(map[string]struct{})
	var sawHTTP2 atomic.Bool

	server := startHTTP2TLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		addrs[r.RemoteAddr] = struct{}{}
		mu.Unlock()
		if r.ProtoMajor == 2 {
			sawHTTP2.Store(true)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	client := newHTTPClientWithPolicyAndTLS(defaultHTTPTransportPolicy(), testTLSClientConfig(t, server))
	for range 4 {
		resp, err := client.Get(server.URL)
		require.NoError(t, err)
		assert.Equal(t, 2, resp.ProtoMajor)
		drainClose(t, resp)
	}

	mu.Lock()
	defer mu.Unlock()
	assert.True(t, sawHTTP2.Load())
	assert.Len(t, addrs, 1, "auto+1 must reuse a single HTTP/2 connection")
}

func TestFourShardHTTP2ReusesExactlyFourConnections(t *testing.T) {
	withRelayHTTPTransportSettings(t)

	var mu sync.Mutex
	addrs := make(map[string]struct{})
	var nonHTTP2Count atomic.Uint32

	server := startHTTP2TLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		addrs[r.RemoteAddr] = struct{}{}
		mu.Unlock()
		if r.ProtoMajor != 2 {
			nonHTTP2Count.Add(1)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	policy := HTTPTransportPolicy{Protocol: dto.HTTPProtocolAuto, Shards: 4}
	client := newHTTPClientWithPolicyAndTLS(policy, testTLSClientConfig(t, server))
	for range 8 {
		resp, err := client.Get(server.URL)
		require.NoError(t, err)
		assert.Equal(t, 2, resp.ProtoMajor)
		drainClose(t, resp)
	}

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, uint32(0), nonHTTP2Count.Load())
	assert.Len(t, addrs, 4, "four shards must establish and reuse exactly four connections")
}

func TestForcedHTTP1AgainstHTTP2Server(t *testing.T) {
	withRelayHTTPTransportSettings(t)

	var nonHTTP1Count atomic.Uint32
	server := startHTTP2TLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 1 {
			nonHTTP1Count.Add(1)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	policy := HTTPTransportPolicy{Protocol: dto.HTTPProtocolHTTP1, Shards: 1}
	client := newHTTPClientWithPolicyAndTLS(policy, testTLSClientConfig(t, server))
	resp, err := client.Get(server.URL)
	require.NoError(t, err)
	assert.Equal(t, 1, resp.ProtoMajor)
	drainClose(t, resp)
	assert.Equal(t, uint32(0), nonHTTP1Count.Load())

	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	assert.False(t, transport.DisableKeepAlives)
	assert.False(t, transport.ForceAttemptHTTP2)
	assert.NotNil(t, transport.TLSNextProto)
	assert.Len(t, transport.TLSNextProto, 0)
}

func TestForcedHTTP1ConcurrentDistinctConnections(t *testing.T) {
	withRelayHTTPTransportSettings(t)

	const k = 8
	var mu sync.Mutex
	addrs := make(map[string]struct{})
	arrived := make(chan struct{}, k)
	release := make(chan struct{})
	var nonHTTP1Count atomic.Uint32

	server := startHTTP2TLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		addrs[r.RemoteAddr] = struct{}{}
		mu.Unlock()
		if r.ProtoMajor != 1 {
			nonHTTP1Count.Add(1)
		}
		arrived <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	policy := HTTPTransportPolicy{Protocol: dto.HTTPProtocolHTTP1, Shards: 1}
	client := newHTTPClientWithPolicyAndTLS(policy, testTLSClientConfig(t, server))

	errCh := make(chan error, k)
	for range k {
		go func() {
			resp, err := client.Get(server.URL)
			if err != nil {
				errCh <- err
				return
			}
			if resp.ProtoMajor != 1 {
				errCh <- fmt.Errorf("expected HTTP/1.x, got %s", resp.Proto)
				_ = resp.Body.Close()
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			errCh <- nil
		}()
	}

	for range k {
		<-arrived
	}
	mu.Lock()
	activeAddrs := len(addrs)
	mu.Unlock()
	close(release)

	for range k {
		require.NoError(t, <-errCh)
	}
	assert.Equal(t, uint32(0), nonHTTP1Count.Load())
	assert.Equal(t, k, activeAddrs, "all HTTP/1.1 handlers active together must use K distinct connections")
}

func TestHTTPClientCachePolicyAndCompatibility(t *testing.T) {
	defaultClient := initDefaultHTTPClientFixture(t)

	compat, err := GetHttpClientWithProxy("")
	require.NoError(t, err)
	aware, err := GetHttpClientWithProxySettings("", dto.ChannelSettings{})
	require.NoError(t, err)
	assert.Same(t, defaultClient, compat)
	assert.Same(t, compat, aware)
	assert.Same(t, GetHttpClient(), aware)

	http1, err := GetHttpClientWithProxySettings("", dto.ChannelSettings{HTTPProtocol: dto.HTTPProtocolHTTP1})
	require.NoError(t, err)
	assert.NotSame(t, aware, http1)

	sharded, err := GetHttpClientWithProxySettings("", dto.ChannelSettings{HTTP2ConnectionShards: 4})
	require.NoError(t, err)
	assert.NotSame(t, aware, sharded)
	assert.NotSame(t, http1, sharded)

	proxyA := "http://proxy.example:8080"
	proxyAlias := "http://proxy.example:8080/"
	clientA, err := GetHttpClientWithProxy(proxyA)
	require.NoError(t, err)
	clientAlias, err := GetHttpClientWithProxy(proxyAlias)
	require.NoError(t, err)
	assert.Same(t, clientA, clientAlias, "canonical proxy aliases must share the default policy client")

	proxyHTTP1, err := GetHttpClientWithProxySettings(proxyA, dto.ChannelSettings{HTTPProtocol: dto.HTTPProtocolHTTP1})
	require.NoError(t, err)
	assert.NotSame(t, clientA, proxyHTTP1)
}

func TestHTTPClientCacheConcurrentGetOrCreate(t *testing.T) {
	initDefaultHTTPClientFixture(t)

	proxyURL := "http://concurrent-proxy.example:9090"
	const workers = 32
	results := make([]*http.Client, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := range workers {
		i := i
		go func() {
			defer wg.Done()
			client, err := GetHttpClientWithProxySettings(proxyURL, dto.ChannelSettings{HTTP2ConnectionShards: 3})
			errs[i] = err
			results[i] = client
		}()
	}
	wg.Wait()
	for i := range workers {
		require.NoError(t, errs[i])
	}
	for i := 1; i < workers; i++ {
		assert.Same(t, results[0], results[i])
	}
}

type closeCountingRoundTripper struct {
	closes atomic.Int32
}

func (c *closeCountingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       http.NoBody,
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func (c *closeCountingRoundTripper) CloseIdleConnections() {
	c.closes.Add(1)
}

func TestShardedRoundTripperCloseIdleConnectionsFansOut(t *testing.T) {
	trackers := []*closeCountingRoundTripper{{}, {}, {}}
	shards := make([]http.RoundTripper, len(trackers))
	for i, tracker := range trackers {
		shards[i] = tracker
	}
	s := &shardedRoundTripper{shards: shards, n: uint32(len(shards))}
	s.CloseIdleConnections()
	for _, tracker := range trackers {
		assert.Equal(t, int32(1), tracker.closes.Load())
	}
}

func TestInvalidateProxyClientClosesAllPolicyVariants(t *testing.T) {
	initDefaultHTTPClientFixture(t)

	proxyURL := "http://invalidate-proxy.example:8080"
	defaultClient, err := GetHttpClientWithProxy(proxyURL)
	require.NoError(t, err)
	http1Client, err := GetHttpClientWithProxySettings(proxyURL, dto.ChannelSettings{HTTPProtocol: dto.HTTPProtocolHTTP1})
	require.NoError(t, err)
	shardedClient, err := GetHttpClientWithProxySettings(proxyURL, dto.ChannelSettings{HTTP2ConnectionShards: 2})
	require.NoError(t, err)

	InvalidateProxyClient(proxyURL)

	afterDefault, err := GetHttpClientWithProxy(proxyURL)
	require.NoError(t, err)
	afterHTTP1, err := GetHttpClientWithProxySettings(proxyURL, dto.ChannelSettings{HTTPProtocol: dto.HTTPProtocolHTTP1})
	require.NoError(t, err)
	afterSharded, err := GetHttpClientWithProxySettings(proxyURL, dto.ChannelSettings{HTTP2ConnectionShards: 2})
	require.NoError(t, err)

	assert.NotSame(t, defaultClient, afterDefault)
	assert.NotSame(t, http1Client, afterHTTP1)
	assert.NotSame(t, shardedClient, afterSharded)
}

func TestResetProxyClientCacheKeepsDefaultPointerAndRecreatesVariants(t *testing.T) {
	defaultClient := initDefaultHTTPClientFixture(t)

	http1Client, err := GetHttpClientWithProxySettings("", dto.ChannelSettings{HTTPProtocol: dto.HTTPProtocolHTTP1})
	require.NoError(t, err)
	shardedClient, err := GetHttpClientWithProxySettings("", dto.ChannelSettings{HTTP2ConnectionShards: 3})
	require.NoError(t, err)
	proxyClient, err := GetHttpClientWithProxy("http://reset-proxy.example:8080")
	require.NoError(t, err)

	ResetProxyClientCache()

	assert.Same(t, defaultClient, GetHttpClient(), "default httpClient pointer must stay stable across reset")
	aware, err := GetHttpClientWithProxySettings("", dto.ChannelSettings{})
	require.NoError(t, err)
	assert.Same(t, defaultClient, aware)

	afterHTTP1, err := GetHttpClientWithProxySettings("", dto.ChannelSettings{HTTPProtocol: dto.HTTPProtocolHTTP1})
	require.NoError(t, err)
	afterSharded, err := GetHttpClientWithProxySettings("", dto.ChannelSettings{HTTP2ConnectionShards: 3})
	require.NoError(t, err)
	afterProxy, err := GetHttpClientWithProxy("http://reset-proxy.example:8080")
	require.NoError(t, err)
	assert.NotSame(t, http1Client, afterHTTP1)
	assert.NotSame(t, shardedClient, afterSharded)
	assert.NotSame(t, proxyClient, afterProxy)
}

func TestResetProxyClientCacheClosesDefaultIdlePool(t *testing.T) {
	defaultClient := initDefaultHTTPClientFixture(t)
	tracker := &closeCountingRoundTripper{}
	previousTransport := defaultClient.Transport
	defaultClient.Transport = tracker
	t.Cleanup(func() {
		defaultClient.Transport = previousTransport
	})

	ResetProxyClientCache()

	assert.Same(t, defaultClient, GetHttpClient())
	assert.GreaterOrEqual(t, tracker.closes.Load(), int32(1), "reset must close idle connections on the stable default client")
	aware, err := GetHttpClientWithProxySettings("", dto.ChannelSettings{})
	require.NoError(t, err)
	assert.Same(t, defaultClient, aware)
}

func TestResetProxyClientCacheConcurrentWithGetHttpClient(t *testing.T) {
	initDefaultHTTPClientFixture(t)

	const workers = 64
	var wg sync.WaitGroup
	wg.Add(workers * 2)
	for range workers {
		go func() {
			defer wg.Done()
			_ = GetHttpClient()
		}()
		go func() {
			defer wg.Done()
			ResetProxyClientCache()
		}()
	}
	wg.Wait()
	assert.NotNil(t, GetHttpClient())
	aware, err := GetHttpClientWithProxySettings("", dto.ChannelSettings{})
	require.NoError(t, err)
	assert.Same(t, GetHttpClient(), aware)
}

func TestCloseIdleConnectionsRedialsHTTP2(t *testing.T) {
	withRelayHTTPTransportSettings(t)

	var mu sync.Mutex
	addrs := make([]string, 0, 2)

	server := startHTTP2TLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		addrs = append(addrs, r.RemoteAddr)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	client := newHTTPClientWithPolicyAndTLS(defaultHTTPTransportPolicy(), testTLSClientConfig(t, server))
	resp, err := client.Get(server.URL)
	require.NoError(t, err)
	drainClose(t, resp)

	client.CloseIdleConnections()

	resp, err = client.Get(server.URL)
	require.NoError(t, err)
	drainClose(t, resp)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, addrs, 2)
	assert.NotEqual(t, addrs[0], addrs[1], "after CloseIdleConnections the next request must redial")
}

func TestNormalizeHTTPTransportPolicyClampsWithoutPanic(t *testing.T) {
	assert.Equal(t, defaultHTTPTransportPolicy(), NormalizeHTTPTransportPolicy(dto.ChannelSettings{}))
	assert.Equal(t, HTTPTransportPolicy{Protocol: dto.HTTPProtocolAuto, Shards: 1}, NormalizeHTTPTransportPolicy(dto.ChannelSettings{HTTPProtocol: "AUTO"}))
	assert.Equal(t, HTTPTransportPolicy{Protocol: dto.HTTPProtocolHTTP1, Shards: 1}, NormalizeHTTPTransportPolicy(dto.ChannelSettings{HTTPProtocol: "HTTP1", HTTP2ConnectionShards: 8}))
	assert.Equal(t, HTTPTransportPolicy{Protocol: dto.HTTPProtocolAuto, Shards: 1}, NormalizeHTTPTransportPolicy(dto.ChannelSettings{HTTPProtocol: "http3"}))
	assert.Equal(t, HTTPTransportPolicy{Protocol: dto.HTTPProtocolAuto, Shards: 1}, NormalizeHTTPTransportPolicy(dto.ChannelSettings{HTTP2ConnectionShards: -3}))
	assert.Equal(t, HTTPTransportPolicy{Protocol: dto.HTTPProtocolAuto, Shards: 8}, NormalizeHTTPTransportPolicy(dto.ChannelSettings{HTTP2ConnectionShards: 99}))
}
