package service

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

// shardedRoundTripper fans requests for each origin across N independent
// transports so each origin can keep N reusable HTTP/2 connections.
type shardedRoundTripper struct {
	shards   []http.RoundTripper
	n        uint32
	policy   HTTPTransportPolicy
	counters sync.Map // origin -> *atomic.Uint32
}

func newShardedRoundTripper(policy HTTPTransportPolicy, factory func() *http.Transport) *shardedRoundTripper {
	n := max(policy.Shards, 1)
	shards := make([]http.RoundTripper, n)
	for i := 0; i < n; i++ {
		transport := factory()
		transport.MaxIdleConns = max(1, transport.MaxIdleConns/n)
		transport.MaxIdleConnsPerHost = max(1, transport.MaxIdleConnsPerHost/n)
		shards[i] = transport
	}
	return &shardedRoundTripper{
		shards: shards,
		n:      uint32(n),
		policy: policy,
	}
}

func originKey(req *http.Request) string {
	if req == nil || req.URL == nil {
		return ""
	}
	return strings.ToLower(req.URL.Scheme) + "://" + req.URL.Host
}

func (s *shardedRoundTripper) pickShard(origin string) uint32 {
	if s.n <= 1 {
		return 0
	}
	counterAny, _ := s.counters.LoadOrStore(origin, &atomic.Uint32{})
	counter := counterAny.(*atomic.Uint32)
	return (counter.Add(1) - 1) % s.n
}

func (s *shardedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	origin := originKey(req)
	idx := s.pickShard(origin)
	resp, err := s.shards[idx].RoundTrip(req)
	if common.DebugEnabled {
		proto := ""
		if resp != nil {
			proto = resp.Proto
		}
		host := ""
		if req != nil && req.URL != nil {
			host = req.URL.Host
		}
		logger.LogDebug(
			req.Context(),
			fmt.Sprintf(
				"http transport: host=%s protocol=%s shard=%d/%d policy=%s negotiated=%s",
				host,
				s.policy.Protocol,
				idx,
				s.n,
				s.policy.cacheKeyPart(),
				proto,
			),
		)
	}
	return resp, err
}

func (s *shardedRoundTripper) CloseIdleConnections() {
	for _, shard := range s.shards {
		closeIdleConnections(shard)
	}
}

func closeIdleConnections(rt http.RoundTripper) {
	type idleCloser interface {
		CloseIdleConnections()
	}
	if closer, ok := rt.(idleCloser); ok {
		closer.CloseIdleConnections()
	}
}

// applyHTTP1Force disables automatic HTTP/2 on a never-used transport.
// ForceAttemptHTTP2=false alone is insufficient; a non-nil empty TLSNextProto
// map prevents net/http from wiring HTTP/2.
func applyHTTP1Force(transport *http.Transport) {
	if transport == nil {
		return
	}
	transport.ForceAttemptHTTP2 = false
	transport.DisableKeepAlives = false
	transport.TLSNextProto = make(map[string]func(authority string, c *tls.Conn) http.RoundTripper)
	if transport.TLSClientConfig != nil {
		cfg := transport.TLSClientConfig.Clone()
		cfg.NextProtos = nil
		transport.TLSClientConfig = cfg
	}
}

func applyHTTPTransportPolicy(transport *http.Transport, policy HTTPTransportPolicy) {
	if transport == nil {
		return
	}
	if policy.Protocol == dto.HTTPProtocolHTTP1 {
		applyHTTP1Force(transport)
		return
	}
	transport.ForceAttemptHTTP2 = true
	transport.DisableKeepAlives = false
}
