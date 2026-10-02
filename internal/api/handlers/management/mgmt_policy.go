package management

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	log "github.com/sirupsen/logrus"
)

// Context keys for the resolved management token and its policy. When set,
// the caller authenticated via a management token (not the static management
// secret / local password). The per-token policy enforcement + audit
// middleware consume these. Absent keys mean the caller is the master admin
// (static secret / env / localPassword) and bypasses all token policy.
const (
	ctxMgmtToken    = "nixllm.mgmt.token"
	ctxMgmtPolicy   = "nixllm.mgmt.policy"
	ctxMgmtProvided = "nixllm.mgmt.provided"
	// ctxMgmtDefaultUserID carries the token's default-user fallback for the
	// current request path (write scope + endpoint allow-list match). Handlers
	// that require user_id read it via defaultUserIDFromContext when the request
	// omits user_id. Absent means no fallback applies.
	ctxMgmtDefaultUserID = "nixllm.mgmt.default_user_id"
)

// mgmtTokenCache is a short-TTL in-memory cache of token lookups keyed by
// SHA-256 hash, mirroring the policy.Service snapshot pattern. It keeps the
// hot auth path off the database while token mutations invalidate it via
// invalidateMgmtToken. A background sweep drops stale entries.
type mgmtTokenCacheEntry struct {
	token    *store.ManagementToken
	policy   *store.ManagementTokenPolicy
	loadedAt time.Time
}

type mgmtTokenCache struct {
	mu    sync.RWMutex
	cache map[string]mgmtTokenCacheEntry
}

const mgmtTokenCacheTTL = 5 * time.Second

var mgmtTokenCacheInst = &mgmtTokenCache{cache: make(map[string]mgmtTokenCacheEntry)}

func (c *mgmtTokenCache) get(hash string) (*store.ManagementToken, *store.ManagementTokenPolicy, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.cache[hash]
	if !ok {
		return nil, nil, false
	}
	if time.Since(e.loadedAt) > mgmtTokenCacheTTL {
		return nil, nil, false
	}
	return e.token, e.policy, true
}

func (c *mgmtTokenCache) set(hash string, token *store.ManagementToken, policy *store.ManagementTokenPolicy) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[hash] = mgmtTokenCacheEntry{token: token, policy: policy, loadedAt: time.Now()}
}

// invalidateMgmtToken drops a single token from the cache (by hash). Called
// after token/policy mutations so the next request resolves fresh.
func invalidateMgmtToken(hash string) {
	mgmtTokenCacheInst.mu.Lock()
	defer mgmtTokenCacheInst.mu.Unlock()
	delete(mgmtTokenCacheInst.cache, hash)
}

// InvalidateAllMgmtTokens drops the entire token cache. Called after
// mutations where the affected hash is unknown (e.g. status/scope changes).
func InvalidateAllMgmtTokens() {
	mgmtTokenCacheInst.mu.Lock()
	defer mgmtTokenCacheInst.mu.Unlock()
	mgmtTokenCacheInst.cache = make(map[string]mgmtTokenCacheEntry)
}

// authenticateManagementToken attempts to authenticate the provided credential
// as a management API token. Returns the resolved token + policy on success.
// Returns (nil, nil) when the credential is not a known management token (the
// caller should then reject as invalid). A revoked/expired token is treated as
// invalid (returns nil) so it falls through to the standard 401 path.
//
// The lookup is cached (mgmtTokenCache) to keep the auth hot path off the
// database. On a cache miss the PG store is consulted; the result is cached
// for mgmtTokenCacheTTL.
func (h *Handler) authenticateManagementToken(ctx context.Context, provided string) (*store.ManagementToken, *store.ManagementTokenPolicy) {
	if provided == "" {
		return nil, nil
	}
	h.mu.Lock()
	tokens := h.pgMgmtTokens
	h.mu.Unlock()
	if tokens == nil {
		return nil, nil
	}
	hash := store.HashSecret(provided)
	if tok, pol, ok := mgmtTokenCacheInst.get(hash); ok {
		return tok, pol
	}
	tok, pol, err := tokens.LookupByHash(ctx, hash)
	if err != nil {
		// Not found / store error → not a valid token. Do not cache misses
		// so a freshly-created token is usable immediately.
		return nil, nil
	}
	// Enforce status + expiry at lookup time so a revoked/expired token can't
	// be used even before the cache TTL elapses.
	if !mgmtTokenUsable(tok) {
		return nil, nil
	}
	mgmtTokenCacheInst.set(hash, tok, pol)
	return tok, pol
}

// authenticateToken dispatches to the configured token authenticator. It
// defaults to authenticateManagementToken; tests may override the
// tokenAuthenticator field to exercise the token-fallback auth path without a
// PG-backed token store.
func (h *Handler) authenticateToken(ctx context.Context, provided string) (*store.ManagementToken, *store.ManagementTokenPolicy) {
	if h.tokenAuthenticator != nil {
		return h.tokenAuthenticator(ctx, provided)
	}
	return h.authenticateManagementToken(ctx, provided)
}

// mgmtTokenUsable reports whether a token is in a usable state: active status
// and not past its expiry. Expired tokens are rejected; the caller may also
// flip their status to "expired" lazily (handled at the enforcement layer).
func mgmtTokenUsable(tok *store.ManagementToken) bool {
	if tok == nil {
		return false
	}
	if tok.Status != store.MgmtTokenStatusActive {
		return false
	}
	if tok.ExpiresAt != nil && !tok.ExpiresAt.IsZero() && time.Now().After(*tok.ExpiresAt) {
		return false
	}
	return true
}

// EnforceTokenPolicy is the per-token policy enforcement middleware. It runs
// after Middleware() has resolved authentication. When the caller is the master
// admin (no token in context), it allows all. Otherwise it enforces:
//   - read/write scope (write scope required for mutations)
//   - per-endpoint allowlist/blocklist (glob match on "<METHOD> <path>")
//   - RPM (sliding window) and max-parallel-requests (in-flight counter)
//
// Denials are surfaced as 403 (scope/endpoint) or 429 (rate) with the
// standard error envelope.
func (h *Handler) EnforceTokenPolicy() gin.HandlerFunc {
	return func(c *gin.Context) {
		tokVal, tokExists := c.Get(ctxMgmtToken)
		if !tokExists || tokVal == nil {
			// Master admin (static secret / localPassword) — bypass token policy.
			c.Next()
			return
		}
		tok, _ := tokVal.(*store.ManagementToken)
		polVal, _ := c.Get(ctxMgmtPolicy)
		pol, _ := polVal.(*store.ManagementTokenPolicy)
		if tok == nil {
			c.Next()
			return
		}

		method := c.Request.Method
		path := c.Request.URL.Path

		// 1. IP allowlist/blocklist. Checked first (before scope/endpoint/rate)
		// because an IP denial is a hard block that supersedes all other
		// policy dimensions. BlockedIPs takes precedence over AllowedIPs.
		if pol != nil {
			clientIP := c.ClientIP()
			// Deny-first: an explicit block match always rejects, even when the
			// IP is also on the allowlist.
			if matched, pattern := ipMatches(clientIP, pol.BlockedIPs); matched {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": gin.H{
					"type":    "ip_blocked",
					"message": fmt.Sprintf("token policy blocks this source IP (%s)", pattern),
				}})
				return
			}
			// When an allowlist is configured, the client IP must match at least
			// one entry; an empty allowlist means "all IPs allowed".
			if len(pol.AllowedIPs) > 0 {
				if allowed, _ := ipMatches(clientIP, pol.AllowedIPs); !allowed {
					c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": gin.H{
						"type":    "ip_not_allowed",
						"message": "token policy does not allow this source IP",
					}})
					return
				}
			}
		}

		// 2. Read/write scope: write scope required for non-GET methods.
		if method != http.MethodGet && tok.Scope != store.MgmtTokenScopeWrite {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": gin.H{
				"type":    "insufficient_scope",
				"message": fmt.Sprintf("token scope '%s' does not permit %s requests; a 'write' scope token is required", tok.Scope, method),
			}})
			return
		}

		// 2b. Default-user fallback: when this write-scope token names a default
		// Internal User and the request path is on its opt-in allow-list, expose
		// the id to handlers so a request that omits user_id can adopt it.
		if tok.Scope == store.MgmtTokenScopeWrite && tok.DefaultUserID != "" &&
			pathMatchesAny(path, tok.DefaultUserIDEndpoints) {
			c.Set(ctxMgmtDefaultUserID, tok.DefaultUserID)
		}

		// 3. Per-endpoint allowlist/blocklist.
		signature := method + " " + path
		if pol != nil {
			if blocked, reason := endpointMatches(signature, pol.BlockedEndpoints); blocked {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": gin.H{
					"type":    "endpoint_blocked",
					"message": fmt.Sprintf("token policy blocks this endpoint (%s)", reason),
				}})
				return
			}
			// When an allowlist is configured, the signature must match at least
			// one pattern; an empty allowlist means "all allowed".
			if len(pol.AllowedEndpoints) > 0 {
				if allowed, _ := endpointMatches(signature, pol.AllowedEndpoints); !allowed {
					c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": gin.H{
						"type":    "endpoint_not_allowed",
						"message": "token policy does not allow this endpoint",
					}})
					return
				}
			}
		}

		// 4. Rate limits: RPM (sliding window) + max-parallel (in-flight).
		if pol != nil {
			if pol.RPMLimit != nil && *pol.RPMLimit > 0 {
				if !mgmtRPMWindow.AllowAndIncrement(tok.ID, *pol.RPMLimit, time.Minute) {
					c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": gin.H{
						"type":    "rate_limit_exceeded",
						"message": fmt.Sprintf("token RPM limit (%d/min) exceeded", *pol.RPMLimit),
					}})
					return
				}
			}
			if pol.MaxParallelRequests != nil && *pol.MaxParallelRequests > 0 {
				if !mgmtParallel.Acquire(tok.ID, *pol.MaxParallelRequests) {
					c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": gin.H{
						"type":    "parallel_limit_exceeded",
						"message": fmt.Sprintf("token max-parallel-requests limit (%d) exceeded", *pol.MaxParallelRequests),
					}})
					return
				}
				defer mgmtParallel.Release(tok.ID)
			}
		}

		c.Next()
	}
}

// endpointMatches reports whether signature matches any pattern in patterns.
// Patterns are glob-like: a trailing "/*" matches any path under the prefix.
// e.g. "GET /usage-stats/*" matches "GET /usage-stats/totals". Returns the
// matched pattern (for diagnostics) when matched.
func endpointMatches(signature string, patterns []string) (bool, string) {
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if p == signature {
			return true, p
		}
		// Normalize: method + " " + path-prefix + "/*"
		if strings.HasSuffix(p, "/*") {
			prefix := strings.TrimSuffix(p, "/*")
			// prefix is e.g. "GET /usage-stats"
			sigPrefix := signature
			// strip trailing path segments after the prefix dir
			if strings.HasPrefix(signature, prefix+"/") {
				return true, p
			}
			// also allow exact prefix match (dir itself)
			if signature == prefix {
				return true, p
			}
			_ = sigPrefix
		}
	}
	return false, ""
}

// pathMatchesAny reports whether path is covered by any allow-list pattern.
// Patterns are absolute management paths ("/v0/management/api-keys-pg") with an
// optional trailing "*" for a prefix match. An empty pattern list matches
// nothing, keeping the default-user fallback strictly opt-in.
func pathMatchesAny(path string, patterns []string) bool {
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.HasSuffix(p, "*") {
			if strings.HasPrefix(path, strings.TrimSuffix(p, "*")) {
				return true
			}
			continue
		}
		if p == path {
			return true
		}
	}
	return false
}

// defaultUserIDFromContext returns the token's default-user fallback resolved
// for the current request by EnforceTokenPolicy, or "" when none applies.
func defaultUserIDFromContext(c *gin.Context) string {
	if c == nil {
		return ""
	}
	v, ok := c.Get(ctxMgmtDefaultUserID)
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

// ipMatches reports whether clientIP matches any entry in patterns. Each
// pattern is either a single IP address ("10.0.0.5") or a CIDR range
// ("10.0.0.0/8", "2001:db8::/32"). IPv4 and IPv6 are both supported. Returns
// the matched pattern (for diagnostics) when matched. Malformed patterns are
// silently skipped so a single bad entry cannot lock out every caller.
//
// clientIP is the value from gin's c.ClientIP() (a string). It is parsed
// once here; invalid client-IP strings simply never match.
func ipMatches(clientIP string, patterns []string) (bool, string) {
	if clientIP == "" {
		return false, ""
	}
	ip := net.ParseIP(clientIP)
	if ip == nil {
		return false, ""
	}
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// CIDR form: parse the network and test containment.
		if strings.Contains(p, "/") {
			_, network, err := net.ParseCIDR(p)
			if err != nil {
				continue
			}
			if network.Contains(ip) {
				return true, p
			}
			continue
		}
		// Single-IP form: compare canonical representations so e.g. "::1"
		// matches "::1" and "10.0.0.1" matches "10.0.0.1".
		patternIP := net.ParseIP(p)
		if patternIP != nil && patternIP.Equal(ip) {
			return true, p
		}
	}
	return false, ""
}

// --- RPM sliding window + parallel limiter ---
// These are minimal, in-memory equivalents of internal/policy/ratelimit.go
// and parallel.go, scoped to management-token enforcement. They avoid wiring
// the full policy.Service (which is keyed by plaintext API-key principal) into
// the management surface.

// mgmtRPMWindow is a sliding-window per-minute rate limiter keyed by token ID.
var mgmtRPMWindow = newMgmtSlidingWindow()

type mgmtSlidingWindow struct {
	mu     sync.Mutex
	counts map[string][]time.Time
}

func newMgmtSlidingWindow() *mgmtSlidingWindow {
	return &mgmtSlidingWindow{counts: make(map[string][]time.Time)}
}

// AllowAndIncrement returns true if adding one request would stay within limit
// within the window, and records it. Otherwise returns false (no increment).
func (w *mgmtSlidingWindow) AllowAndIncrement(key string, limit int, window time.Duration) bool {
	now := time.Now()
	cutoff := now.Add(-window)
	w.mu.Lock()
	defer w.mu.Unlock()
	hits := w.counts[key]
	// drop expired hits
	keep := hits[:0]
	for _, t := range hits {
		if t.After(cutoff) {
			keep = append(keep, t)
		}
	}
	if len(keep) >= limit {
		w.counts[key] = keep
		return false
	}
	keep = append(keep, now)
	w.counts[key] = keep
	return true
}

// mgmtParallel is an in-flight counter keyed by token ID.
var mgmtParallel = newMgmtParallelLimiter()

type mgmtParallelLimiter struct {
	mu     sync.Mutex
	active map[string]int
}

func newMgmtParallelLimiter() *mgmtParallelLimiter {
	return &mgmtParallelLimiter{active: make(map[string]int)}
}

// Acquire returns true if the in-flight count for key is below limit, and
// increments it. Otherwise returns false (no increment).
func (l *mgmtParallelLimiter) Acquire(key string, limit int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active[key] >= limit {
		return false
	}
	l.active[key]++
	return true
}

// Release decrements the in-flight count for key.
func (l *mgmtParallelLimiter) Release(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active[key] > 0 {
		l.active[key]--
		if l.active[key] == 0 {
			delete(l.active, key)
		}
	}
}

// --- Audit middleware ---

// StartMgmtAuditSweep launches a background goroutine that periodically purges
// stale RPM-window entries to bound memory. Call once at startup.
func StartMgmtAuditSweep() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			mgmtRPMWindow.mu.Lock()
			cutoff := time.Now().Add(-time.Minute)
			for k, hits := range mgmtRPMWindow.counts {
				keep := hits[:0]
				for _, t := range hits {
					if t.After(cutoff) {
						keep = append(keep, t)
					}
				}
				if len(keep) == 0 {
					delete(mgmtRPMWindow.counts, k)
				} else {
					mgmtRPMWindow.counts[k] = keep
				}
			}
			mgmtRPMWindow.mu.Unlock()
		}
	}()
}

// AuditTokenCall is the audit-logging middleware. It records every management
// API call made with a management token: the request audit (who/what/when/
// latency), the request/response bodies for mutations, and the error message
// for 4xx/5xx responses. Bodies are sealed at rest via the Sealer when
// PGSTORE_ENCRYPTION_KEY is configured. Master-admin calls (no token) are
// not audited here (only token calls are attribution-bearing).
//
// The insert is fire-and-forget (goroutine) so it never blocks the response.
func (h *Handler) AuditTokenCall() gin.HandlerFunc {
	return func(c *gin.Context) {
		tokVal, tokExists := c.Get(ctxMgmtToken)
		if !tokExists || tokVal == nil {
			c.Next()
			return
		}
		tok, _ := tokVal.(*store.ManagementToken)
		if tok == nil {
			c.Next()
			return
		}

		// For mutations, buffer the request body so we can capture it. The body
		// is read fully then restored via io.NopCloser(bytes.NewReader(...))
		// so downstream handlers still see it.
		method := c.Request.Method
		isMutation := method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
		var reqBody []byte
		if isMutation && c.Request != nil && c.Request.Body != nil {
			reqBody, _ = io.ReadAll(c.Request.Body)
			c.Request.Body = io.NopCloser(strings.NewReader(string(reqBody)))
		}
		start := time.Now()
		c.Next()
		latency := time.Since(start)
		h.recordAudit(c, tok, latency, reqBody, isMutation)
	}
}

// recordAudit persists a single audit-log row for the just-completed call.
// Fire-and-forget so the response is never blocked on DB I/O.
func (h *Handler) recordAudit(c *gin.Context, tok *store.ManagementToken, latency time.Duration, reqBody []byte, isMutation bool) {
	h.mu.Lock()
	tokens := h.pgMgmtTokens
	h.mu.Unlock()
	if tokens == nil {
		return
	}
	status := c.Writer.Status()
	isError := status >= 400
	var errMessage string
	if isError {
		// We don't have the response body here (no recorder); the error
		// envelope is summarized via the status code.
		errMessage = fmt.Sprintf("HTTP %d", status)
	}
	entry := store.ManagementAuditLog{
		TokenID:      tok.ID,
		ActorIP:      c.ClientIP(),
		Method:       c.Request.Method,
		Path:         c.Request.URL.Path,
		StatusCode:   status,
		LatencyMs:    latency.Milliseconds(),
		IsError:      isError,
		ErrorMessage: errMessage,
		OccurredAt:   time.Now().UTC(),
	}
	if isMutation {
		entry.RequestBody = string(reqBody)
	}
	ctx := context.Background()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.WithField("panic", r).Error("management: audit log insert panicked")
			}
		}()
		if err := tokens.InsertAudit(ctx, entry); err != nil {
			log.WithError(err).WithField("token_id", tok.ID).Debug("management: audit log insert failed")
		}
	}()
	// Touch last_used_at best-effort.
	go tokens.TouchLastUsed(ctx, tok.ID)
}
