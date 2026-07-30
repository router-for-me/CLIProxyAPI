package auth

import (
	"context"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

const (
	quotaUsageEndpoint = "https://api.anthropic.com/api/oauth/usage"
	// The usage endpoint is itself rate-limited (observed HTTP 429 with
	// Retry-After: 0 at 4 accounts x 60s from one IP), and quota percentages
	// move slowly. Poll gently and tolerate a couple of missed cycles before
	// treating a snapshot as unknown.
	quotaRefreshInterval = 5 * time.Minute
	quotaStaleAfter      = 30 * time.Minute
	// quotaFetchTimeout bounds each background poll via a stop-cancellable
	// request context (not an http.Client timeout): it keeps a wedged poll
	// from stalling the refresh loop, and Stop() aborts in-flight requests.
	// Never on a request path — background credential-quota polling only.
	quotaFetchTimeout       = 15 * time.Second
	quotaExhaustedThreshold = 95.0

	// Even a stale snapshot is good enough to keep avoiding an exhausted
	// account: weekly tiers reset on a multi-day cadence, so a reading a few
	// hours old that says a weekly tier is fully used is still actionable when
	// everything fresher is unavailable (e.g. during a usage-endpoint
	// rate-limit window).
	quotaExclusionMaxAge = 6 * time.Hour

	// Backoff applied to the poll loop after the usage endpoint returns 429.
	// The endpoint's bucket is shared per-IP across every consumer (other
	// tooling may query it too), so on 429 the poller must yield hard rather
	// than keep draining the shared allowance.
	quotaBackoffInitial = 15 * time.Minute
	quotaBackoffCap     = 2 * time.Hour

	// quotaPollSpacing staggers per-account fetches inside one poll cycle;
	// the endpoint rate-limits bursts from a single IP.
	quotaPollSpacing = 3 * time.Second

	// quotaTargetExpiry drops poll targets whose auth has not been offered to
	// Pick recently (credential removed or token rotated out). Long enough to
	// ride out cooldown windows where an auth is temporarily filtered from the
	// candidate slice, short enough that deleted credentials stop consuming
	// the shared usage-endpoint allowance after a few cycles.
	quotaTargetExpiry = 15 * time.Minute

	// Claude Code fingerprint for usage polls, mirroring the executor's
	// defaults in internal/runtime/executor/helps/claude_device_profile.go
	// (not importable here: import cycle). Keep in sync when those bump.
	quotaPollUserAgent        = "claude-cli/2.1.63 (external, cli)"
	quotaPollStainlessPackage = "0.74.0"
	quotaPollStainlessRuntime = "v24.3.0"
)

// quotaSnapshot captures the last known usage percentages for one auth.
// Percentages are "percent used"; -1 marks an absent limit.
type quotaSnapshot struct {
	fetchedAt time.Time
	session   float64
	weeklyAll float64
	// scoped maps lowercased scope.model.display_name (e.g. "opus") to percent used.
	scoped map[string]float64
}

// quotaPollTarget is the minimal auth info the background poller needs.
// lastSeen records when the auth was last offered to Pick, so targets whose
// credential was removed or rotated out age out instead of being polled
// forever.
type quotaPollTarget struct {
	id       string
	token    string
	lastSeen time.Time
}

// QuotaAwareSelector picks auths proportionally to their remaining Anthropic
// OAuth quota headroom. Pick performs no I/O: a background goroutine polls the
// usage endpoint for each claude-provider auth and maintains a per-auth cache.
// Auths with unknown or stale quota carry zero weight; when every candidate is
// unknown or exhausted, selection degrades to embedded round-robin behavior.
type QuotaAwareSelector struct {
	fallback *RoundRobinSelector

	mu      sync.RWMutex
	quotas  map[string]quotaSnapshot
	targets map[string]quotaPollTarget
	// backoff is the current 429 backoff for the poll loop (0 = healthy).
	backoff time.Duration

	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}

	httpClient *http.Client

	// randFloat and now are injectable for tests.
	randFloat func() float64
	now       func() time.Time
}

// NewQuotaAwareSelector creates a quota-aware selector. The background quota
// poller starts lazily on the first Pick and stops via Stop.
func NewQuotaAwareSelector() *QuotaAwareSelector {
	return &QuotaAwareSelector{
		fallback:   &RoundRobinSelector{},
		quotas:     make(map[string]quotaSnapshot),
		targets:    make(map[string]quotaPollTarget),
		stopCh:     make(chan struct{}),
		httpClient: &http.Client{},
		randFloat:  rand.Float64,
		now:        time.Now,
	}
}

// Pick selects among available auths with probability proportional to quota
// headroom. It never blocks on network I/O.
func (s *QuotaAwareSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	now := s.now()
	available, err := getAvailableAuths(auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)

	s.updatePollTargets(auths)
	s.startOnce.Do(func() { go s.refreshLoop() })

	entry := selectorLogEntry(ctx)
	weights := make([]float64, len(available))
	total := 0.0
	unknowns := make([]*Auth, 0, len(available))
	for i, candidate := range available {
		headroom, known := s.headroom(candidate, model, now)
		if !known {
			unknowns = append(unknowns, candidate)
			continue
		}
		if headroom < 0 {
			headroom = 0
		}
		weights[i] = headroom
		total += headroom
	}

	if total <= 0 {
		// No weighted candidate. Round-robin over the unknowns only, so
		// accounts KNOWN to be exhausted on this tier stay excluded even
		// while fresh quota data is unavailable.
		if len(unknowns) > 0 && len(unknowns) < len(available) {
			entry.Debugf("quota-aware: weighting unavailable, round-robin over %d unknown of %d candidates | provider=%s model=%s", len(unknowns), len(available), provider, model)
			return s.fallback.Pick(ctx, provider, model, opts, unknowns)
		}
		entry.Debugf("quota-aware: no candidate with known headroom, degrading to round-robin | provider=%s model=%s candidates=%d", provider, model, len(available))
		return s.fallback.Pick(ctx, provider, model, opts, auths)
	}

	r := s.randFloat() * total
	for i, candidate := range available {
		if weights[i] <= 0 {
			continue
		}
		r -= weights[i]
		if r <= 0 {
			entry.Debugf("quota-aware: selected auth=%s headroom=%.1f/%.1f provider=%s model=%s", candidate.ID, weights[i], total, provider, model)
			return candidate, nil
		}
	}
	// Floating point edge: fall back to the last positively weighted candidate.
	for i := len(available) - 1; i >= 0; i-- {
		if weights[i] > 0 {
			entry.Debugf("quota-aware: selected auth=%s (tail) provider=%s model=%s", available[i].ID, provider, model)
			return available[i], nil
		}
	}
	return s.fallback.Pick(ctx, provider, model, opts, auths)
}

// headroom returns the remaining percentage for the auth on the tier relevant
// to model, and whether quota data is known and fresh.
func (s *QuotaAwareSelector) headroom(auth *Auth, model string, now time.Time) (float64, bool) {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "claude") {
		return 0, false
	}
	s.mu.RLock()
	snapshot, ok := s.quotas[auth.ID]
	s.mu.RUnlock()
	if !ok {
		return 0, false
	}
	age := now.Sub(snapshot.fetchedAt)
	if age > quotaStaleAfter {
		// Too old to weight by, but a recent-ish "exhausted" reading on a
		// WEEKLY tier is still trustworthy for exclusion: weekly limits reset
		// on a multi-day cadence. The 5h session window is deliberately not
		// considered here; it may already have reset.
		if age <= quotaExclusionMaxAge {
			if used, seen := snapshot.weeklyUsed(model); seen && used >= quotaExhaustedThreshold {
				return 0, true
			}
		}
		return 0, false
	}

	effective, seen := snapshot.effectiveUsed(model)
	if !seen {
		return 0, false
	}
	if effective >= quotaExhaustedThreshold {
		return 0, true
	}
	return 100 - effective, true
}

// effectiveUsed returns the worst (highest) percent-used across the session
// window, the all-models weekly limit, and any model-scoped weekly tier whose
// display name matches the model, plus whether any limit was present at all.
func (q quotaSnapshot) effectiveUsed(model string) (float64, bool) {
	weekly, seen := q.weeklyUsed(model)
	effective := weekly
	if q.session >= 0 {
		seen = true
		if q.session > effective {
			effective = q.session
		}
	}
	return effective, seen
}

// weeklyUsed is effectiveUsed without the 5h session window: the worst
// percent-used across the all-models weekly limit and any matching
// model-scoped weekly tier.
func (q quotaSnapshot) weeklyUsed(model string) (float64, bool) {
	effective := 0.0
	seen := false
	if q.weeklyAll >= 0 {
		seen = true
		effective = q.weeklyAll
	}
	lowerModel := strings.ToLower(model)
	for tier, pct := range q.scoped {
		if pct < 0 || tier == "" {
			continue
		}
		if strings.Contains(lowerModel, tier) {
			seen = true
			if pct > effective {
				effective = pct
			}
		}
	}
	return effective, seen
}

// updatePollTargets records the claude auths (and their bearer tokens) the
// background poller should query, and drops targets whose auth has not been
// seen for quotaTargetExpiry (credential removed or token rotated out) so the
// poller does not spend the shared usage-endpoint allowance on dead
// credentials. Called from Pick; cheap map upkeep only.
func (s *QuotaAwareSelector) updatePollTargets(auths []*Auth) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, auth := range auths {
		if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "claude") {
			continue
		}
		token := authMetadataString(auth, "access_token")
		if token == "" {
			token = authMetadataString(auth, "accessToken")
		}
		if token == "" {
			continue
		}
		s.targets[auth.ID] = quotaPollTarget{id: auth.ID, token: token, lastSeen: now}
	}
	for id, target := range s.targets {
		if now.Sub(target.lastSeen) > quotaTargetExpiry {
			delete(s.targets, id)
			delete(s.quotas, id)
		}
	}
}

// Stop terminates the background quota poller.
func (s *QuotaAwareSelector) Stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
}

func (s *QuotaAwareSelector) refreshLoop() {
	// Warm the cache immediately, then poll on the interval, backing off
	// whenever the usage endpoint rate-limits us.
	wait := s.refreshAll()
	for {
		if wait <= 0 {
			wait = quotaRefreshInterval
		}
		select {
		case <-s.stopCh:
			return
		case <-time.After(wait):
			wait = s.refreshAll()
		}
	}
}

// refreshAll fetches usage for every poll target. It returns the wait before
// the next cycle: the normal interval on success, or an escalating backoff
// when the endpoint answers 429 (its per-IP bucket is shared with any other
// local consumer of the usage endpoint, so we stop draining it immediately).
func (s *QuotaAwareSelector) refreshAll() time.Duration {
	s.mu.RLock()
	targets := make([]quotaPollTarget, 0, len(s.targets))
	for _, target := range s.targets {
		targets = append(targets, target)
	}
	backoff := s.backoff
	s.mu.RUnlock()

	rateLimited := false
	for i, target := range targets {
		if i > 0 {
			select {
			case <-s.stopCh:
				return quotaRefreshInterval
			case <-time.After(quotaPollSpacing):
			}
		}
		select {
		case <-s.stopCh:
			return quotaRefreshInterval
		default:
		}
		snapshot, status, errFetch := s.fetchQuota(target.token)
		if status == http.StatusTooManyRequests {
			// One 429 means the shared bucket is empty; stop the cycle so
			// other consumers are not starved further.
			log.Warnf("quota-aware: usage endpoint rate-limited, backing off | auth=%s", target.id)
			rateLimited = true
			break
		}
		if errFetch != nil {
			// Keep the previous snapshot; it ages into staleness naturally.
			log.Debugf("quota-aware: usage fetch failed | auth=%s err=%v", target.id, errFetch)
			continue
		}
		snapshot.fetchedAt = s.now()
		s.mu.Lock()
		s.quotas[target.id] = snapshot
		s.mu.Unlock()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if rateLimited {
		if backoff <= 0 {
			backoff = quotaBackoffInitial
		} else {
			backoff *= 2
			if backoff > quotaBackoffCap {
				backoff = quotaBackoffCap
			}
		}
		s.backoff = backoff
		return backoff
	}
	s.backoff = 0
	return quotaRefreshInterval
}

func (s *QuotaAwareSelector) fetchQuota(token string) (quotaSnapshot, int, error) {
	// Bound each poll with a context derived from stopCh rather than an
	// http.Client timeout (per the repo's no-new-network-timeouts rule): the
	// deadline keeps a wedged poll from stalling the refresh loop, and Stop()
	// cancels an in-flight request immediately on shutdown or selector swap.
	// This never runs on a request path — background quota polling only.
	ctx, cancel := context.WithDeadline(context.Background(), s.now().Add(quotaFetchTimeout))
	defer cancel()
	go func() {
		select {
		case <-s.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, quotaUsageEndpoint, nil)
	if errRequest != nil {
		return quotaSnapshot{}, 0, errRequest
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("anthropic-version", "2023-06-01")
	// Fingerprint as Claude Code like the executor's OAuth request path does —
	// the usage endpoint aggressively rate-limits generic clients (Go's
	// default User-Agent draws immediate 429s). Values mirror the executor's
	// defaults (internal/runtime/executor/helps/claude_device_profile.go);
	// that package cannot be imported here without an import cycle.
	req.Header.Set("User-Agent", quotaPollUserAgent)
	req.Header.Set("X-App", "cli")
	req.Header.Set("X-Stainless-Package-Version", quotaPollStainlessPackage)
	req.Header.Set("X-Stainless-Runtime", "node")
	req.Header.Set("X-Stainless-Runtime-Version", quotaPollStainlessRuntime)
	req.Header.Set("X-Stainless-Lang", "js")

	resp, errDo := s.httpClient.Do(req)
	if errDo != nil {
		return quotaSnapshot{}, 0, errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("quota-aware: close usage response: %v", errClose)
		}
	}()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if errRead != nil {
		return quotaSnapshot{}, resp.StatusCode, errRead
	}
	if resp.StatusCode != http.StatusOK {
		return quotaSnapshot{}, resp.StatusCode, &Error{Code: "quota_usage_http_error", Message: http.StatusText(resp.StatusCode)}
	}
	return parseQuotaUsage(body), resp.StatusCode, nil
}

// parseQuotaUsage extracts percent-used values from an /api/oauth/usage
// response. Limits arrive as limits[] with kind "session", "weekly_all", or
// "weekly_scoped" (the latter carrying scope.model.display_name).
func parseQuotaUsage(body []byte) quotaSnapshot {
	snapshot := quotaSnapshot{session: -1, weeklyAll: -1, scoped: make(map[string]float64)}
	gjson.GetBytes(body, "limits").ForEach(func(_, limit gjson.Result) bool {
		percent := limit.Get("percent")
		if !percent.Exists() {
			return true
		}
		pct := percent.Float()
		switch limit.Get("kind").String() {
		case "session":
			snapshot.session = pct
		case "weekly_all":
			snapshot.weeklyAll = pct
		case "weekly_scoped":
			if name := strings.ToLower(strings.TrimSpace(limit.Get("scope.model.display_name").String())); name != "" {
				snapshot.scoped[name] = pct
			}
		}
		return true
	})
	return snapshot
}
