package helps

import (
	"container/list"
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/warprotate"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// proxyHTTPClientPoolSize is the number of reusable *http.Client values kept per
// cache entry. Idle reclaim retains this many ready clients/connections.
const proxyHTTPClientPoolSize = 3

// proxyHTTPClientPoolMaxEntries caps how many proxy/account pool entries are retained.
// Zero means unlimited (no hard eviction by count).
const proxyHTTPClientPoolMaxEntries = 0

// proxyHTTPClientPoolIdleKeep is MaxIdleConnsPerHost for pooled transports, and
// also how many most-recently-used pool entries to retain when reclaiming idle entries.
const proxyHTTPClientPoolIdleKeep = 5

// proxyHTTPClientPoolIdleTTL is how long an unused pool entry may stay cached
// before its idle connections are closed and the entry is considered for eviction.
const proxyHTTPClientPoolIdleTTL = 5 * time.Minute

type proxyHTTPClientPool struct {
	key       string
	clients   []*http.Client
	transport *http.Transport
	cursor    uint64
	lastUsed  atomic.Int64 // unix nano
}

func (p *proxyHTTPClientPool) get() *http.Client {
	if p == nil || len(p.clients) == 0 {
		return &http.Client{}
	}
	p.lastUsed.Store(time.Now().UnixNano())
	idx := atomic.AddUint64(&p.cursor, 1) - 1
	return p.clients[int(idx%uint64(len(p.clients)))]
}

func (p *proxyHTTPClientPool) closeIdle() {
	if p == nil || p.transport == nil {
		return
	}
	p.transport.CloseIdleConnections()
}

type proxyHTTPClientCache struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	order   *list.List // front = most recently used
	stopCh  chan struct{}
	once    sync.Once
}

func newProxyHTTPClientCache() *proxyHTTPClientCache {
	c := &proxyHTTPClientCache{
		entries: make(map[string]*list.Element),
		order:   list.New(),
		stopCh:  make(chan struct{}),
	}
	c.once.Do(func() {
		go c.reclaimLoop()
	})
	return c
}

var sharedProxyHTTPClientCache = newProxyHTTPClientCache()

func (c *proxyHTTPClientCache) reclaimLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.reclaimIdle(time.Now())
		case <-c.stopCh:
			return
		}
	}
}

func (c *proxyHTTPClientCache) reclaimIdle(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cutoff := now.Add(-proxyHTTPClientPoolIdleTTL).UnixNano()
	for e := c.order.Back(); e != nil; {
		if c.order.Len() <= proxyHTTPClientPoolIdleKeep {
			break
		}
		prev := e.Prev()
		pool := e.Value.(*proxyHTTPClientPool)
		if pool.lastUsed.Load() >= cutoff {
			break
		}
		c.removeElement(e)
		pool.closeIdle()
		e = prev
	}
}

func (c *proxyHTTPClientCache) removeElement(e *list.Element) {
	pool := e.Value.(*proxyHTTPClientPool)
	delete(c.entries, pool.key)
	c.order.Remove(e)
}

func (c *proxyHTTPClientCache) getOrCreate(key, proxyURL string, timeout time.Duration) *http.Client {
	c.mu.Lock()
	if el, ok := c.entries[key]; ok {
		c.order.MoveToFront(el)
		pool := el.Value.(*proxyHTTPClientPool)
		c.mu.Unlock()
		return pool.get()
	}
	c.mu.Unlock()

	transport := buildProxyTransport(proxyURL)
	if transport == nil {
		return nil
	}
	tuneProxyTransport(transport)

	clients := make([]*http.Client, 0, proxyHTTPClientPoolSize)
	for i := 0; i < proxyHTTPClientPoolSize; i++ {
		client := &http.Client{Transport: transport}
		if timeout > 0 {
			client.Timeout = timeout
		}
		clients = append(clients, client)
	}
	pool := &proxyHTTPClientPool{
		key:       key,
		clients:   clients,
		transport: transport,
	}
	pool.lastUsed.Store(time.Now().UnixNano())

	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		// Lost the race; reuse the winner and drop the temporary transport.
		c.order.MoveToFront(el)
		transport.CloseIdleConnections()
		return el.Value.(*proxyHTTPClientPool).get()
	}
	el := c.order.PushFront(pool)
	c.entries[key] = el
	if proxyHTTPClientPoolMaxEntries > 0 {
		for c.order.Len() > proxyHTTPClientPoolMaxEntries {
			oldest := c.order.Back()
			if oldest == nil {
				break
			}
			oldPool := oldest.Value.(*proxyHTTPClientPool)
			c.removeElement(oldest)
			oldPool.closeIdle()
		}
	}
	return pool.get()
}

func tuneProxyTransport(transport *http.Transport) {
	if transport == nil {
		return
	}
	if transport.IdleConnTimeout == 0 || transport.IdleConnTimeout > 30*time.Second {
		transport.IdleConnTimeout = 30 * time.Second
	}
	// Unlimited concurrent connections; idle keep-alive capped at pool size.
	transport.MaxConnsPerHost = 0
	transport.MaxIdleConns = 0
	transport.MaxIdleConnsPerHost = proxyHTTPClientPoolIdleKeep
}

func resolveUpstreamProxyURL(cfg *config.Config, auth *cliproxyauth.Auth) string {
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

func resolveUpstreamAccountScope(auth *cliproxyauth.Auth, proxyURL string) string {
	if auth == nil {
		return ""
	}
	// Only isolate by account when the auth carries its own proxy override.
	// Global proxy-url traffic shares one pool across accounts.
	if strings.TrimSpace(auth.ProxyURL) == "" {
		return ""
	}
	if id := strings.TrimSpace(auth.ID); id != "" {
		return id
	}
	if idx := strings.TrimSpace(auth.Index); idx != "" {
		return idx
	}
	if fileName := strings.TrimSpace(auth.FileName); fileName != "" {
		return fileName
	}
	return ""
}

func proxyHTTPClientPoolKey(proxyURL, accountScope string, timeout time.Duration) string {
	return fmt.Sprintf("%s\x00%s\x00%d", proxyURL, accountScope, int64(timeout))
}

// getPooledProxyHTTPClient returns a reusable client for the proxy/account scope.
func getPooledProxyHTTPClient(proxyURL, accountScope string, timeout time.Duration) *http.Client {
	key := proxyHTTPClientPoolKey(proxyURL, accountScope, timeout)
	return sharedProxyHTTPClientCache.getOrCreate(key, proxyURL, timeout)
}

// CloseIdleProxyConnections closes idle keep-alive connections for the
// effective proxy of cfg/auth so the next request opens a fresh LB path.
func CloseIdleProxyConnections(cfg *config.Config, auth *cliproxyauth.Auth) {
	proxyURL := resolveUpstreamProxyURL(cfg, auth)
	if proxyURL == "" {
		sharedProxyHTTPClientCache.closeAllIdle()
		return
	}
	sharedProxyHTTPClientCache.closeIdleForProxy(proxyURL)
}

func (c *proxyHTTPClientCache) closeIdleForProxy(proxyURL string) {
	if c == nil || proxyURL == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for e := c.order.Front(); e != nil; e = e.Next() {
		pool := e.Value.(*proxyHTTPClientPool)
		if strings.HasPrefix(pool.key, proxyURL+"\x00") {
			pool.closeIdle()
		}
	}
}

func (c *proxyHTTPClientCache) closeAllIdle() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for e := c.order.Front(); e != nil; e = e.Next() {
		e.Value.(*proxyHTTPClientPool).closeIdle()
	}
}

// PrepareUpstreamForProxy optionally claims a Warp LB rotate key, drains that
// backend, closes idle proxy connections, and kicks off async restart.
func PrepareUpstreamForProxy(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) {
	if cfg == nil {
		return
	}
	base := strings.TrimSpace(cfg.WarpRotateURL)
	if base == "" {
		return
	}
	warprotate.PrepareBeforeUpstream(ctx, base, func() {
		CloseIdleProxyConnections(cfg, auth)
	})
}

// HTTPUpstreamDo is the unified upstream request helper.
// It reuses a pooled http.Client/Transport keyed by effective proxy URL and,
// when an auth-level proxy override is present, by account identity.
func HTTPUpstreamDo(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, req *http.Request, timeout time.Duration) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("http upstream: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	PrepareUpstreamForProxy(ctx, cfg, auth)
	httpReq := req.WithContext(ctx)
	client := NewProxyAwareHTTPClient(ctx, cfg, auth, timeout)
	return client.Do(httpReq)
}

// NewProxyAwareHTTPClient creates an HTTP client with proper proxy configuration priority:
// 1. Use auth.ProxyURL if configured (highest priority)
// 2. Use cfg.ProxyURL if auth proxy is not configured
// 3. Use RoundTripper from context if neither are configured
//
// When a proxy URL is configured, clients are taken from a process-wide pool with
// LRU eviction and idle reclaim so keep-alive connections are reused.
func NewProxyAwareHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	proxyURL := resolveUpstreamProxyURL(cfg, auth)
	if proxyURL != "" {
		accountScope := resolveUpstreamAccountScope(auth, proxyURL)
		if client := getPooledProxyHTTPClient(proxyURL, accountScope, timeout); client != nil {
			return client
		}
		log.Debugf("failed to setup proxy from URL: %s, falling back to context transport", proxyutil.Redact(proxyURL))
	}

	httpClient := &http.Client{}
	if timeout > 0 {
		httpClient.Timeout = timeout
	}
	if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
		httpClient.Transport = rt
	}
	return httpClient
}

// buildProxyTransport creates an HTTP transport configured for the given proxy URL.
func buildProxyTransport(proxyURL string) *http.Transport {
	transport, _, errBuild := proxyutil.BuildHTTPTransport(proxyURL)
	if errBuild != nil {
		log.Errorf("%v", errBuild)
		return nil
	}
	return transport
}
