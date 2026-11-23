package cliproxy

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"golang.org/x/sync/singleflight"
)

const (
	antigravityModelBaseURLDaily     = sdkauth.AntigravityModelBaseURLDaily
	antigravityModelsPath            = sdkauth.AntigravityModelsPath
	antigravityPersistedHintsMetaKey = sdkauth.AntigravityPersistedHintsMetaKey
	antigravityCapabilityCacheTTL    = registry.ModelsRefreshInterval
	antigravityCapabilityRetryBase   = time.Minute
	antigravityCapabilityRetryMax    = 30 * time.Minute
	antigravityAuthErrorRetryBase    = 5 * time.Minute
	antigravityForbiddenRetryBase    = 5 * time.Minute
	antigravityForbiddenRetryMax     = 60 * time.Minute
	antigravityRefreshScanInterval   = time.Minute
	antigravityFailureMaxCount       = uint8(5)
)

type antigravityProbeStatus = sdkauth.AntigravityProbeStatus

const (
	antigravityProbeStatusSuccess        = sdkauth.AntigravityProbeStatusSuccess
	antigravityProbeStatusAuthError      = sdkauth.AntigravityProbeStatusAuthError
	antigravityProbeStatusTransientError = sdkauth.AntigravityProbeStatusTransientError
	antigravityProbeStatusForbidden      = sdkauth.AntigravityProbeStatusForbidden
)

type antigravityCapabilityCacheEntry struct {
	hints             antigravityModelCapabilityHints
	expiresAt         time.Time
	summaryExpiresAt  time.Time
	lastUsed          time.Time
	catalogProbe      antigravityProbeState
	summaryProbe      antigravityProbeState
	appliedRevision   uint64
	appliedEpoch      uint64
	persistedRevision uint64
}

type antigravityProbeState struct {
	revision uint64
	pending  bool
}

func (e *antigravityCapabilityCacheEntry) probeState(path string) *antigravityProbeState {
	if path == sdkauth.AntigravityQuotaSummaryPath {
		return &e.summaryProbe
	}
	return &e.catalogProbe
}

// Keep attempt history beyond nextRetry; deleting it at retry time would turn
// exponential backoff back into a fixed interval. Inactive histories expire with
// the same retention horizon as inactive successful catalogs.
type antigravityFailureState struct {
	failures    uint8
	lastFailure time.Time
	nextRetry   time.Time
}

func nextAntigravityFailure(previous antigravityFailureState, now time.Time, randomInt64N func(int64) int64) antigravityFailureState {
	// Five failures reach the capped window: 2, 4, 8, 16, 30 minutes.
	return nextAntigravityFailureWithPolicy(previous, now, antigravityCapabilityRetryBase, antigravityCapabilityRetryMax, randomInt64N)
}

func nextAntigravityAuthErrorFailure(previous antigravityFailureState, now time.Time, randomInt64N func(int64) int64) antigravityFailureState {
	// Three failures reach the capped window: 10, 20, 30 minutes while keeping token-scoped recovery on refresh.
	return nextAntigravityFailureWithPolicy(previous, now, antigravityAuthErrorRetryBase, antigravityCapabilityRetryMax, randomInt64N)
}

func nextAntigravityForbiddenFailure(previous antigravityFailureState, now time.Time, randomInt64N func(int64) int64) antigravityFailureState {
	// Four failures reach the capped window: 10, 20, 40, 60 minutes (equal jitter 30-60 minutes at cap).
	return nextAntigravityFailureWithPolicy(previous, now, antigravityForbiddenRetryBase, antigravityForbiddenRetryMax, randomInt64N)
}

func nextAntigravityFailureWithPolicy(previous antigravityFailureState, now time.Time, base, maxWindow time.Duration, randomInt64N func(int64) int64) antigravityFailureState {
	if now.Sub(previous.lastFailure) > 2*antigravityCapabilityCacheTTL {
		previous.failures = 0
	}
	// Advance consecutive failure count independently of when a shorter-ramp policy hits
	// its window cap so mixed 401 -> 5xx sequences on the same token preserve full 5xx backoff.
	failures := min(previous.failures, antigravityFailureMaxCount-1) + 1
	window := min(2*base, maxWindow)
	for step := uint8(1); step < failures && window < maxWindow; step++ {
		if window > maxWindow/2 {
			window = maxWindow
			break
		}
		window *= 2
	}
	// Equal jitter retains a minimum delay, including at the cap.
	half := window / 2
	delay := half + time.Duration(randomInt64N(int64(half)+1))
	return antigravityFailureState{failures: failures, lastFailure: now, nextRetry: now.Add(delay)}
}

func isAntigravityAuthDisabled(auth *coreauth.Auth) bool {
	return auth != nil && (auth.Disabled || auth.Status == coreauth.StatusDisabled)
}

func antigravityTokenFailureKey(cacheKey, accessToken string) string {
	return cacheKey + fmt.Sprintf("/%x", sha256.Sum256([]byte(accessToken)))
}

func antigravityFailureNextRetryLocked(cacheKey, tokenFailureKey, path string) time.Time {
	nextRetry := antigravityAuthFailureCache[cacheKey+path].nextRetry
	for _, key := range []string{tokenFailureKey, tokenFailureKey + path} {
		if retry := antigravityAuthFailureCache[key].nextRetry; retry.After(nextRetry) {
			nextRetry = retry
		}
	}
	return nextRetry
}

var (
	antigravityNowFunc          = time.Now
	antigravityModelUserAgent   = misc.AntigravityUserAgent
	antigravityCapabilityMu     sync.RWMutex
	antigravityCapabilityCache  = make(map[string]antigravityCapabilityCacheEntry)
	antigravityAuthFailureCache = make(map[string]antigravityFailureState)
	antigravityCapabilityGroup  singleflight.Group
	antigravityProbeSequence    atomic.Uint64
	antigravityProbeSlots       = make(chan struct{}, modelRegistrationMaxWorkersPerCategory)
)

type antigravityModelCapabilityHints struct {
	WebSearchModelIDs map[string]struct{}
	// Nil means unknown; an empty non-nil set is an authoritative empty catalog.
	ModelIDs           map[string]struct{}
	ModelQuotas        map[string]sdkauth.AntigravityModelQuota
	weeklyModelQuotas  map[string]sdkauth.AntigravityModelQuota
	quotaBuckets       []sdkauth.AntigravityQuotaBucket
	summaryRequestedAt time.Time
	lookedUpAt         time.Time
	revision           uint64
	catalogFetched     bool
}

type antigravityModelProbeResult struct {
	hints  antigravityModelCapabilityHints
	cached bool
}

func (h antigravityModelCapabilityHints) clone() antigravityModelCapabilityHints {
	h.WebSearchModelIDs = maps.Clone(h.WebSearchModelIDs)
	h.ModelIDs = maps.Clone(h.ModelIDs)
	h.ModelQuotas = maps.Clone(h.ModelQuotas)
	h.weeklyModelQuotas = maps.Clone(h.weeklyModelQuotas)
	h.quotaBuckets = slices.Clone(h.quotaBuckets)
	return h
}

// antigravityCapabilityKey isolates catalogs by account, project, route and client
// fingerprint. Token rotation alone does not invalidate a successful catalog.
func (s *Service) antigravityCapabilityKey(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	return antigravityCapabilityKeyForRoute(auth, antigravityQuotaSummaryBaseURLs(auth), s.antigravityModelFetchProxyURL(auth), antigravityModelUserAgent())
}

func antigravityCapabilityKeyForRoute(auth *coreauth.Auth, endpoints []string, proxy, userAgent string) string {
	project, _ := auth.Metadata["project_id"].(string)
	identity := auth.ID
	if identity == "" {
		token, _ := auth.Metadata["access_token"].(string)
		identity = fmt.Sprintf("%x", sha256.Sum256([]byte(token)))
	}
	return fmt.Sprintf("%q/%d/%q/%q/%q/%q", identity, auth.RegistrationEpoch, project, endpoints, proxy, userAgent)
}

func (s *Service) cachedAntigravityHints(auth *coreauth.Auth) antigravityModelCapabilityHints {
	if auth == nil || isAntigravityAuthDisabled(auth) {
		return antigravityModelCapabilityHints{}
	}
	endpoint := antigravityModelBaseURLs(auth)[0]
	key := s.antigravityCapabilityKey(auth)
	return s.cachedAntigravityHintsForKey(auth, endpoint, key)
}

func (s *Service) cachedAntigravityHintsForKey(auth *coreauth.Auth, endpoint, key string) antigravityModelCapabilityHints {
	antigravityCapabilityMu.RLock()
	entry := antigravityCapabilityCache[key]
	antigravityCapabilityMu.RUnlock()
	if entry.hints.ModelIDs != nil {
		return entry.hints.clone()
	}
	persisted, ok := s.persistedAntigravityHintsForAuth(auth, endpoint)
	if !ok {
		return entry.hints.clone()
	}
	antigravityCapabilityMu.Lock()
	defer antigravityCapabilityMu.Unlock()
	entry = seedAntigravityCapabilityCacheLocked(key, persisted)
	return entry.hints.clone()
}

func seedAntigravityCapabilityCacheLocked(cacheKey string, persisted antigravityModelCapabilityHints) antigravityCapabilityCacheEntry {
	entry := antigravityCapabilityCache[cacheKey]
	if entry.hints.ModelIDs != nil {
		return entry
	}
	seeded := persisted.clone()
	seeded.revision = antigravityProbeSequence.Add(1)
	entry.hints = seeded
	entry.persistedRevision = seeded.revision
	entry.lastUsed = antigravityNowFunc()
	antigravityCapabilityCache[cacheKey] = entry
	return entry
}

func (s *Service) persistedAntigravityHintsForAuth(auth *coreauth.Auth, endpoint string) (antigravityModelCapabilityHints, bool) {
	if auth == nil || isAntigravityAuthDisabled(auth) {
		return antigravityModelCapabilityHints{}, false
	}
	var current *coreauth.Auth
	if s != nil && s.coreManager != nil && auth.ID != "" {
		var exists bool
		current, exists = s.coreManager.GetByID(auth.ID)
		if !exists || current == nil || isAntigravityAuthDisabled(current) || current.RegistrationEpoch != auth.RegistrationEpoch {
			return antigravityModelCapabilityHints{}, false
		}
	}
	project, _ := auth.Metadata["project_id"].(string)
	if hints, ok := parsePersistedAntigravityCapabilityHints(auth.Metadata, project, endpoint); ok {
		return hints, true
	}
	if current != nil {
		return parsePersistedAntigravityCapabilityHints(current.Metadata, project, endpoint)
	}
	return antigravityModelCapabilityHints{}, false
}

func (s *Service) persistAntigravityCapabilityHints(ctx context.Context, auth *coreauth.Auth, expectedKey, endpoint string, hints antigravityModelCapabilityHints) bool {
	if s == nil || s.coreManager == nil || auth == nil || auth.ID == "" || isAntigravityAuthDisabled(auth) || expectedKey == "" || hints.ModelIDs == nil || hints.revision == 0 {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return false
	}
	antigravityCapabilityMu.RLock()
	entry := antigravityCapabilityCache[expectedKey]
	stale := entry.hints.ModelIDs == nil || entry.hints.revision != hints.revision || entry.persistedRevision >= hints.revision
	antigravityCapabilityMu.RUnlock()
	if stale {
		return false
	}
	project, _ := auth.Metadata["project_id"].(string)
	value := buildPersistedAntigravityCapabilityHintsValue(project, endpoint, hints)
	persisted, preempted := s.coreManager.PersistMetadataEntry(ctx, auth.ID, auth.RegistrationEpoch, antigravityPersistedHintsMetaKey, value, func(existing *coreauth.Auth) bool {
		if existing == nil || isAntigravityAuthDisabled(existing) || existing.Provider != auth.Provider || existing.RegistrationEpoch != auth.RegistrationEpoch || s.antigravityCapabilityKey(existing) != expectedKey {
			return false
		}
		antigravityCapabilityMu.RLock()
		currentEntry := antigravityCapabilityCache[expectedKey]
		valid := currentEntry.hints.ModelIDs != nil && currentEntry.hints.revision == hints.revision && currentEntry.persistedRevision < hints.revision
		antigravityCapabilityMu.RUnlock()
		return valid
	})
	if !persisted {
		return preempted
	}
	antigravityCapabilityMu.Lock()
	current := antigravityCapabilityCache[expectedKey]
	if current.hints.revision == hints.revision && current.persistedRevision < hints.revision {
		current.persistedRevision = hints.revision
		antigravityCapabilityCache[expectedKey] = current
	}
	antigravityCapabilityMu.Unlock()
	return false
}

func buildPersistedAntigravityCapabilityHintsValue(project, endpoint string, hints antigravityModelCapabilityHints) map[string]any {
	return sdkauth.BuildPersistedAntigravityCapabilityHints(project, endpoint, sdkauth.AntigravityModelCapabilityHints{
		ModelIDs:          hints.ModelIDs,
		WebSearchModelIDs: hints.WebSearchModelIDs,
	})
}

func parsePersistedAntigravityCapabilityHints(metadata map[string]any, expectedProject, expectedEndpoint string) (antigravityModelCapabilityHints, bool) {
	hints, ok := sdkauth.ParsePersistedAntigravityCapabilityHints(metadata, expectedProject, expectedEndpoint)
	if !ok {
		return antigravityModelCapabilityHints{}, false
	}
	return antigravityModelCapabilityHints{
		ModelIDs:          hints.ModelIDs,
		WebSearchModelIDs: hints.WebSearchModelIDs,
	}, true
}

func (s *Service) fetchAntigravityModelCapabilityHintsForAuth(ctx context.Context, auth *coreauth.Auth) antigravityModelCapabilityHints {
	_, hints := s.fetchAntigravityModelCapabilityHints(ctx, auth)
	return hints
}

// Return the key captured with the HTTP User-Agent so publication cannot label a
// result with a version resolved before or after the actual probe snapshot.
func (s *Service) fetchAntigravityModelCapabilityHints(ctx context.Context, auth *coreauth.Auth) (string, antigravityModelCapabilityHints) {
	key, hints, err := s.fetchAntigravityModelHints(ctx, auth, false)
	if err == nil && s.coreManager != nil {
		if windows := hints.quotaWindows(); len(windows) > 0 {
			_ = s.recordAntigravityQuota(ctx, auth, key, windows, hints.lookedUpAt, hints.revision, true)
		}
	}
	return key, hints
}

func (s *Service) fetchAntigravityModelHints(ctx context.Context, auth *coreauth.Auth, usageProbe bool) (string, antigravityModelCapabilityHints, error) {
	if auth == nil || auth.Metadata == nil || isAntigravityAuthDisabled(auth) || s.antigravityHomeEnabled() {
		return "", antigravityModelCapabilityHints{}, errors.New("antigravity model probe unavailable for credential")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	project, _ := auth.Metadata["project_id"].(string)
	token, _ := auth.Metadata["access_token"].(string)
	req := sdkauth.AntigravityModelProbeRequest{
		BaseURLs: antigravityQuotaSummaryBaseURLs(auth), ProxyURL: s.antigravityModelFetchProxyURL(auth),
		AccessToken: strings.TrimSpace(token), ProjectID: project, UserAgent: antigravityModelUserAgent(),
	}
	key := antigravityCapabilityKeyForRoute(auth, req.BaseURLs, req.ProxyURL, req.UserAgent)
	catalogReq := req
	catalogReq.BaseURLs = req.BaseURLs[:1]
	if !usageProbe {
		hints, err := s.fetchAntigravityProbeHints(ctx, auth, catalogReq, key, antigravityModelsPath, false)
		return key, hints, err
	}
	summary, err := s.fetchAntigravityProbeHints(ctx, auth, req, key, sdkauth.AntigravityQuotaSummaryPath, true)
	if err != nil || len(summary.quotaBuckets) > 0 {
		return key, summary, err
	}
	catalog, err := s.fetchAntigravityProbeHints(ctx, auth, catalogReq, key, antigravityModelsPath, true)
	if err == nil {
		antigravityCapabilityMu.Lock()
		entry := antigravityCapabilityCache[key]
		if entry.summaryProbe.revision == summary.revision {
			entry.summaryExpiresAt = antigravityNowFunc().Add(antigravityCapabilityCacheTTL)
			antigravityCapabilityCache[key] = entry
		}
		antigravityCapabilityMu.Unlock()
		// Order fallback publication by the empty summary's timestamp and revision so a newer summary wins.
		catalog.lookedUpAt, catalog.revision = summary.lookedUpAt, summary.revision
	}
	return key, catalog, err
}

func (s *Service) refreshAntigravityBackgroundUsage(ctx context.Context, auth *coreauth.Auth, catalogKey string, catalogHints antigravityModelCapabilityHints) {
	usage := s.NewAntigravityUsageLookup()
	if !usage.Eligible(auth) || s.coreManager == nil {
		return
	}
	key := s.antigravityCapabilityKey(auth)
	token, _ := auth.Metadata["access_token"].(string)
	token = strings.TrimSpace(token)
	failureKey := antigravityTokenFailureKey(key, token)
	antigravityCapabilityMu.RLock()
	due := !antigravityNowFunc().Before(antigravitySummaryRefreshAtLocked(key, failureKey))
	antigravityCapabilityMu.RUnlock()
	if !due {
		return
	}
	project, _ := auth.Metadata["project_id"].(string)
	req := sdkauth.AntigravityModelProbeRequest{
		BaseURLs: antigravityQuotaSummaryBaseURLs(auth), ProxyURL: s.antigravityModelFetchProxyURL(auth),
		AccessToken: token, ProjectID: project, UserAgent: antigravityModelUserAgent(),
	}
	routeKey := antigravityCapabilityKeyForRoute(auth, req.BaseURLs, req.ProxyURL, req.UserAgent)
	summary, err := s.fetchAntigravityProbeHints(ctx, auth, req, routeKey, sdkauth.AntigravityQuotaSummaryPath, true)
	if err != nil {
		return
	}
	if len(summary.quotaBuckets) > 0 {
		if windows := summary.quotaWindows(); len(windows) > 0 {
			_ = s.recordAntigravityQuota(ctx, auth, routeKey, windows, summary.lookedUpAt, summary.revision, false)
		}
		return
	}
	fallback := catalogHints
	if catalogKey != routeKey || !catalogHints.catalogFetched {
		catalogReq := req
		catalogReq.BaseURLs = req.BaseURLs[:1]
		fallback, err = s.fetchAntigravityProbeHints(ctx, auth, catalogReq, routeKey, antigravityModelsPath, true)
		if err != nil {
			return
		}
	}
	antigravityCapabilityMu.Lock()
	entry := antigravityCapabilityCache[routeKey]
	if entry.summaryProbe.revision == summary.revision {
		entry.summaryExpiresAt = antigravityNowFunc().Add(antigravityCapabilityCacheTTL)
		antigravityCapabilityCache[routeKey] = entry
	}
	antigravityCapabilityMu.Unlock()
	if windows := fallback.quotaWindows(); len(windows) > 0 {
		_ = s.recordAntigravityQuota(ctx, auth, routeKey, windows, summary.lookedUpAt, summary.revision, false)
	}
}

func (s *Service) fetchAntigravityProbeHints(ctx context.Context, auth *coreauth.Auth, req sdkauth.AntigravityModelProbeRequest, cacheKey, path string, bypassTTL bool) (antigravityModelCapabilityHints, error) {
	endpoint := req.BaseURLs[0]
	cached := func() antigravityModelCapabilityHints {
		return s.cachedAntigravityHintsForKey(auth, endpoint, cacheKey)
	}
	if err := ctx.Err(); err != nil {
		return cached(), err
	}
	if req.AccessToken == "" {
		return cached(), errors.New("antigravity model probe requires an access token")
	}
	tokenFailureKey := antigravityTokenFailureKey(cacheKey, req.AccessToken)
	flightKey := tokenFailureKey + path
	checkBackoff := func() error {
		if retry := antigravityFailureNextRetryLocked(cacheKey, tokenFailureKey, path); antigravityNowFunc().Before(retry) {
			return fmt.Errorf("antigravity model probe in backoff until %s", retry.UTC().Format(time.RFC3339))
		}
		return nil
	}
	lookup := func() (antigravityModelCapabilityHints, bool, error) {
		persisted, hasPersisted := s.persistedAntigravityHintsForAuth(auth, endpoint)
		antigravityCapabilityMu.Lock()
		defer antigravityCapabilityMu.Unlock()
		entry, exists := antigravityCapabilityCache[cacheKey]
		if entry.hints.ModelIDs == nil && hasPersisted {
			entry = seedAntigravityCapabilityCacheLocked(cacheKey, persisted)
			exists = true
		}
		now := antigravityNowFunc()
		if exists {
			entry.lastUsed = now
			antigravityCapabilityCache[cacheKey] = entry
		}
		if err := checkBackoff(); err != nil {
			return entry.hints.clone(), true, err
		}
		return entry.hints.clone(), !bypassTTL && now.Before(entry.expiresAt), nil
	}
	// The initiating caller owns the shared HTTP operation. Followers may cancel
	// their own wait without canceling that operation or blocking a refresh worker.
	probe := func() (antigravityModelProbeResult, error) {
		if hints, skip, err := lookup(); skip {
			return antigravityModelProbeResult{hints: hints, cached: err == nil}, err
		}
		ctx, finish, active := s.beginAntigravityAccountProbe(ctx, auth, cacheKey, flightKey, req.AccessToken)
		if !active {
			return antigravityModelProbeResult{hints: cached()}, errors.New("antigravity model probe credential is no longer current")
		}
		defer finish()
		antigravityCapabilityMu.Lock()
		revision := antigravityProbeSequence.Add(1)
		lookedUpAt := antigravityNowFunc()
		entry := antigravityCapabilityCache[cacheKey]
		*entry.probeState(path) = antigravityProbeState{revision: revision, pending: true}
		entry.lastUsed = lookedUpAt
		antigravityCapabilityCache[cacheKey] = entry
		antigravityCapabilityMu.Unlock()
		defer func() {
			antigravityCapabilityMu.Lock()
			current := antigravityCapabilityCache[cacheKey]
			if state := current.probeState(path); state.revision == revision {
				state.pending = false
				antigravityCapabilityCache[cacheKey] = current
			}
			antigravityCapabilityMu.Unlock()
		}()
		select {
		case antigravityProbeSlots <- struct{}{}:
		case <-ctx.Done():
			return antigravityModelProbeResult{hints: cached()}, ctx.Err()
		}
		defer func() { <-antigravityProbeSlots }()
		antigravityCapabilityMu.RLock()
		err := checkBackoff()
		antigravityCapabilityMu.RUnlock()
		if err != nil {
			return antigravityModelProbeResult{hints: cached()}, err
		}
		hints, status := probeAntigravity(ctx, req, path)
		hints.lookedUpAt, hints.revision = lookedUpAt, revision
		hints, err = finishAntigravityModelProbe(ctx, cacheKey, tokenFailureKey, path, hints, status)
		return antigravityModelProbeResult{hints: hints}, err
	}
	for {
		if hints, skip, err := lookup(); skip {
			return hints, err
		}
		resultCh := antigravityCapabilityGroup.DoChan(flightKey, func() (any, error) { return probe() })
		select {
		case <-ctx.Done():
			return cached(), ctx.Err()
		case shared := <-resultCh:
			result := shared.Val.(antigravityModelProbeResult)
			if !bypassTTL || !result.cached {
				return result.hints.clone(), shared.Err
			}
		}
	}
}

func finishAntigravityModelProbe(ctx context.Context, cacheKey, tokenFailureKey, path string, hints antigravityModelCapabilityHints, status antigravityProbeStatus) (antigravityModelCapabilityHints, error) {
	antigravityCapabilityMu.Lock()
	defer antigravityCapabilityMu.Unlock()
	current := antigravityCapabilityCache[cacheKey]
	if err := ctx.Err(); err != nil {
		return current.hints.clone(), err
	}
	if current.probeState(path).revision != hints.revision {
		return current.hints.clone(), errors.New("antigravity model probe was superseded")
	}
	now := antigravityNowFunc()
	for key, other := range antigravityCapabilityCache {
		if key != cacheKey && !other.catalogProbe.pending && !other.summaryProbe.pending && now.Sub(other.lastUsed) > 2*antigravityCapabilityCacheTTL {
			delete(antigravityCapabilityCache, key)
		}
	}
	for key, failure := range antigravityAuthFailureCache {
		if now.Sub(failure.lastFailure) > 2*antigravityCapabilityCacheTTL {
			delete(antigravityAuthFailureCache, key)
		}
	}
	forbiddenKey, transientKey := cacheKey+path, tokenFailureKey+path
	summary := path == sdkauth.AntigravityQuotaSummaryPath
	if status == antigravityProbeStatusSuccess && (summary || hints.ModelIDs != nil) {
		delete(antigravityAuthFailureCache, transientKey)
		delete(antigravityAuthFailureCache, forbiddenKey)
		current.lastUsed = now
		if summary {
			if len(hints.quotaBuckets) > 0 {
				current.summaryExpiresAt = now.Add(antigravityCapabilityCacheTTL)
			}
		} else {
			hints.catalogFetched = true
			current.hints = antigravityModelCapabilityHints{
				ModelIDs: hints.ModelIDs, WebSearchModelIDs: hints.WebSearchModelIDs,
				revision: hints.revision, lookedUpAt: hints.lookedUpAt,
			}
			current.expiresAt = now.Add(antigravityCapabilityCacheTTL)
		}
		antigravityCapabilityCache[cacheKey] = current
		// An unsupported summary does not validate the token. An overlapping probe may also have recorded a new 401.
		if (!summary || len(hints.quotaBuckets) > 0) && !hints.lookedUpAt.Before(antigravityAuthFailureCache[tokenFailureKey].nextRetry) {
			delete(antigravityAuthFailureCache, tokenFailureKey)
		}
		return hints, nil
	}
	probeErr := errors.New("antigravity model probe did not return a catalog")
	if summary {
		probeErr = errors.New("antigravity quota summary probe failed")
	}
	switch status {
	case antigravityProbeStatusForbidden:
		// Endpoint-specific 403s persist across routine token rotation.
		antigravityAuthFailureCache[forbiddenKey] = nextAntigravityForbiddenFailure(antigravityAuthFailureCache[forbiddenKey], now, rand.Int64N)
		probeErr = errors.New("antigravity model probe forbidden (403)")
	case antigravityProbeStatusAuthError:
		// 401 unauthorized failures back off longer for the same token while retrying immediately on token refresh.
		antigravityAuthFailureCache[tokenFailureKey] = nextAntigravityAuthErrorFailure(antigravityAuthFailureCache[tokenFailureKey], now, rand.Int64N)
		probeErr = errors.New("antigravity model probe unauthorized (401)")
	default:
		previous := antigravityAuthFailureCache[transientKey]
		if authFailure := antigravityAuthFailureCache[tokenFailureKey]; authFailure.failures > previous.failures {
			previous = authFailure
		}
		antigravityAuthFailureCache[transientKey] = nextAntigravityFailure(previous, now, rand.Int64N)
	}
	retained := current.hints.clone()
	if status == antigravityProbeStatusSuccess && len(hints.WebSearchModelIDs) > 0 {
		retained.WebSearchModelIDs = hints.WebSearchModelIDs
	}
	return retained, probeErr
}

func probeAntigravity(ctx context.Context, req sdkauth.AntigravityModelProbeRequest, path string) (antigravityModelCapabilityHints, antigravityProbeStatus) {
	if path == sdkauth.AntigravityQuotaSummaryPath {
		requestedAt := antigravityNowFunc()
		buckets, status := sdkauth.FetchAntigravityQuotaSummary(ctx, req)
		return antigravityModelCapabilityHints{quotaBuckets: buckets, summaryRequestedAt: requestedAt}, status
	}
	hints, status := sdkauth.FetchAntigravityAvailableModels(ctx, req)
	return antigravityModelCapabilityHints{
		ModelIDs:          hints.ModelIDs,
		WebSearchModelIDs: hints.WebSearchModelIDs,
		ModelQuotas:       hints.ModelQuotas,
		weeklyModelQuotas: hints.WeeklyModelQuotas,
	}, status
}

func (s *Service) antigravityModelFetchProxyURL(auth *coreauth.Auth) string {
	if auth != nil {
		if proxyURL := strings.TrimSpace(auth.ProxyURL); proxyURL != "" {
			return proxyURL
		}
	}
	if s != nil {
		s.cfgMu.RLock()
		defer s.cfgMu.RUnlock()
		if s.cfg != nil {
			return strings.TrimSpace(s.cfg.ProxyURL)
		}
	}
	return ""
}

func antigravityModelBaseURLs(auth *coreauth.Auth) []string {
	if urls := configuredAntigravityModelBaseURLs(auth); len(urls) > 0 {
		return urls
	}
	return []string{antigravityModelBaseURLDaily}
}

func antigravityQuotaSummaryBaseURLs(auth *coreauth.Auth) []string {
	if urls := configuredAntigravityModelBaseURLs(auth); len(urls) > 0 {
		return urls
	}
	return []string{antigravityModelBaseURLDaily, sdkauth.AntigravityModelBaseURLProd}
}

func configuredAntigravityModelBaseURLs(auth *coreauth.Auth) []string {
	if auth != nil && auth.Attributes != nil {
		if raw := strings.TrimSpace(auth.Attributes["base_urls"]); raw != "" {
			parts := strings.Split(raw, ",")
			urls := make([]string, 0, len(parts))
			for _, p := range parts {
				if trimmed := strings.TrimRight(strings.TrimSpace(p), "/"); trimmed != "" {
					urls = append(urls, trimmed)
				}
			}
			if len(urls) > 0 {
				return urls
			}
		}
	}
	if baseURL := resolveAntigravityModelBaseURL(auth); baseURL != "" {
		return []string{baseURL}
	}
	return nil
}

func resolveAntigravityModelBaseURL(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if value := strings.TrimSpace(auth.Attributes["base_url"]); value != "" {
			return strings.TrimRight(value, "/")
		}
	}
	if auth.Metadata != nil {
		if value, ok := auth.Metadata["base_url"].(string); ok {
			value = strings.TrimSpace(value)
			if value != "" {
				return strings.TrimRight(value, "/")
			}
		}
	}
	return ""
}

func parseAntigravityModelCapabilityHints(body []byte) (antigravityModelCapabilityHints, bool) {
	hints, ok := sdkauth.ParseAntigravityModelCapabilityHints(body)
	if !ok {
		return antigravityModelCapabilityHints{}, false
	}
	return antigravityModelCapabilityHints{
		ModelIDs:          hints.ModelIDs,
		WebSearchModelIDs: hints.WebSearchModelIDs,
		ModelQuotas:       hints.ModelQuotas,
		weeklyModelQuotas: hints.WeeklyModelQuotas,
	}, true
}

func applyAntigravityFetchedModelCapabilities(models []*ModelInfo, hints antigravityModelCapabilityHints) []*ModelInfo {
	if len(models) == 0 || len(hints.WebSearchModelIDs) == 0 {
		return models
	}

	for _, model := range models {
		if model == nil {
			continue
		}
		modelID := normalizeAntigravityFetchedModelID(model.ID)
		if _, ok := hints.WebSearchModelIDs[modelID]; ok {
			model.SupportsWebSearch = true
		}
	}
	return models
}

func normalizeAntigravityFetchedModelID(modelID string) string {
	return sdkauth.NormalizeAntigravityModelID(modelID)
}

// WaitAntigravityProbes waits for any in-flight asynchronous Antigravity capability probes to complete.
func (s *Service) WaitAntigravityProbes() {
	if s == nil {
		return
	}
	s.antigravityProbeWg.Wait()
}

// Native Antigravity reconciliation belongs to fenced registry publication,
// whether from cache or a network refresh. Cache availability alone is not enough.
func (s *Service) reconcileRegisteredModelStates(ctx context.Context, auth *coreauth.Auth) {
	if auth == nil || s.coreManager == nil {
		return
	}
	if strings.EqualFold(auth.Provider, "antigravity") && !s.antigravityHomeEnabled() {
		return
	}
	s.coreManager.ReconcileRegistryModelStates(ctx, auth.ID)
}

func (s *Service) antigravityHomeEnabled() bool {
	if s == nil {
		return false
	}
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg != nil && s.cfg.Home.Enabled
}

func filterAntigravityModels(models []*ModelInfo, hints antigravityModelCapabilityHints) []*ModelInfo {
	filtered := make([]*ModelInfo, 0, len(models))
	for _, model := range models {
		if model != nil {
			if _, ok := hints.ModelIDs[normalizeAntigravityFetchedModelID(model.ID)]; ok {
				filtered = append(filtered, model)
			}
		}
	}
	return filtered
}

func (s *Service) antigravityModelsForHints(auth *coreauth.Auth, hints antigravityModelCapabilityHints) []*ModelInfo {
	s.cfgMu.RLock()
	cfg := s.cfg
	s.cfgMu.RUnlock()
	return s.antigravityModelsForHintsWithConfig(auth, hints, cfg)
}

func (s *Service) antigravityModelsForHintsWithConfig(auth *coreauth.Auth, hints antigravityModelCapabilityHints, cfg *config.Config) []*ModelInfo {
	if cfg == nil {
		cfg = &config.Config{}
	}
	models := filterAntigravityModels(registry.GetAntigravityModels(), hints)
	models = applyAntigravityFetchedModelCapabilities(models, hints)
	excluded := cfg.OAuthExcludedModels[coreauth.OAuthModelAliasChannel(auth.Provider, auth.AuthKind())]
	if value := strings.TrimSpace(auth.Attributes["excluded_models"]); value != "" {
		excluded = strings.Split(value, ",")
	}
	models = applyExcludedModels(models, excluded)
	models = applyOAuthModelAliasForAuth(cfg, "antigravity", auth.AuthKind(), auth.Attributes, models)
	models = s.appendPluginModels("antigravity", models, excluded)
	models = applyOAuthSettingsForAuth(cfg, "antigravity", auth.AuthKind(), models)
	return applyModelPrefixes(models, auth.Prefix, cfg.ForceModelPrefix)
}

func (s *Service) asyncProbeAntigravityCapabilities(ctx context.Context, auth *coreauth.Auth, providerKey string) {
	s.queueAntigravityModelRefresh(ctx, auth, providerKey, true)
}

func (s *Service) probeAndRegisterAntigravityModels(ctx context.Context, auth *coreauth.Auth, providerKey string, expectedEpoch, expectedRegEpoch uint64) (string, antigravityModelCapabilityHints) {
	expectedKey, hints := s.fetchAntigravityModelCapabilityHints(ctx, auth)
	s.applyAntigravityModelHints(ctx, auth, providerKey, hints, expectedKey, expectedEpoch, expectedRegEpoch)
	return expectedKey, hints
}

// Request results change Generation, not these registration inputs. Compare the
// actual model settings so exclusions and account aliases cannot be replayed stale.
func antigravityAuthModelSettingsEqual(a, b *coreauth.Auth) bool {
	return a != nil && b != nil && a.Provider == b.Provider && a.Prefix == b.Prefix &&
		a.RegistrationEpoch == b.RegistrationEpoch && a.Disabled == b.Disabled &&
		(a.Status == coreauth.StatusDisabled) == (b.Status == coreauth.StatusDisabled) &&
		a.AuthKind() == b.AuthKind() && maps.Equal(a.Attributes, b.Attributes)
}

func (s *Service) applyAntigravityModelHints(ctx context.Context, auth *coreauth.Auth, providerKey string, hints antigravityModelCapabilityHints, expectedKey string, expectedEpoch, expectedRegEpoch uint64) {
	if ctx.Err() != nil || isAntigravityAuthDisabled(auth) || s.antigravityHomeEnabled() || s.antigravityCapabilityKey(auth) != expectedKey {
		return
	}
	if hints.ModelIDs == nil && len(hints.WebSearchModelIDs) == 0 {
		return
	}
	if s.coreManager != nil {
		current, exists := s.coreManager.GetByID(auth.ID)
		if !exists || current == nil || isAntigravityAuthDisabled(current) || current.Provider != auth.Provider || current.RegistrationEpoch != expectedEpoch || !antigravityAuthModelSettingsEqual(current, auth) || s.antigravityCapabilityKey(current) != expectedKey {
			return
		}
	}
	var updated bool
	if hints.ModelIDs != nil {
		// Keep the config snapshot stable through publication. Registry CAS must
		// remain strict: an epoch from another publisher may use newer auth settings.
		s.cfgMu.RLock()
		models := s.antigravityModelsForHintsWithConfig(auth, hints, s.cfg)
		// Fence the cache-to-registry publication as well as the network result.
		// A newer successful catalog must never be replaced by an older caller.
		antigravityCapabilityMu.Lock()
		entry := antigravityCapabilityCache[expectedKey]
		if entry.hints.revision != hints.revision {
			antigravityCapabilityMu.Unlock()
			s.cfgMu.RUnlock()
			return
		}
		reg := registry.GetGlobalRegistry()
		var appliedEpoch uint64
		appliedEpoch, updated = reg.ReplaceClientModels(auth.ID, providerKey, expectedRegEpoch, models)
		if updated {
			entry.appliedRevision = hints.revision
			entry.appliedEpoch = appliedEpoch
			antigravityCapabilityCache[expectedKey] = entry
		}
		antigravityCapabilityMu.Unlock()
		s.cfgMu.RUnlock()
	} else {
		// Older capability-only responses cannot revoke model entitlements.
		aliasMap := s.buildAntigravityReverseAliasMap(auth)
		updated = GlobalModelRegistry().ApplyClientModelCapabilities(auth.ID, expectedRegEpoch, func(id string, info *ModelInfo) {
			if _, ok := hints.WebSearchModelIDs[resolveAntigravityUpstreamModelID(id, auth.Prefix, aliasMap)]; ok {
				info.SupportsWebSearch = true
			}
		})
	}
	if updated && s.coreManager != nil {
		s.coreManager.ReconcileRegistryModelStates(ctx, auth.ID)
		s.coreManager.RefreshSchedulerEntry(auth.ID)
	}
}

// refreshAntigravityModels schedules account refreshes without waiting for the batch.
// One unfinished request must not block other accounts' retry or expiry deadlines.
// Network concurrency remains bounded by antigravityProbeSlots.
func (s *Service) refreshAntigravityModels(ctx context.Context) {
	if s == nil || s.coreManager == nil || s.antigravityHomeEnabled() {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// A version change also invalidates connected probes. Cancel them before
	// queuing successors so a stalled old request cannot hold its account worker.
	s.cancelStaleAntigravityProbes("")
	usage := s.NewAntigravityUsageLookup()
	for _, auth := range s.coreManager.List() {
		if ctx.Err() != nil {
			return
		}
		if auth == nil || isAntigravityAuthDisabled(auth) || !strings.EqualFold(auth.Provider, "antigravity") {
			continue
		}
		snapshot := auth.Clone()
		key := s.antigravityCapabilityKey(snapshot)
		token, _ := snapshot.Metadata["access_token"].(string)
		failureKey := antigravityTokenFailureKey(key, strings.TrimSpace(token))
		epoch := GlobalModelRegistry().ClientRegistrationEpoch(snapshot.ID)
		antigravityCapabilityMu.RLock()
		entry := antigravityCapabilityCache[key]
		registered := entry.appliedRevision == entry.hints.revision && entry.appliedEpoch == epoch
		persisted := s.coreManager == nil || entry.hints.ModelIDs == nil || entry.persistedRevision == entry.hints.revision
		now := antigravityNowFunc()
		catalogDue := !(now.Before(entry.expiresAt) && registered && persisted) &&
			!now.Before(antigravityFailureNextRetryLocked(key, failureKey, antigravityModelsPath))
		summaryDue := usage.Eligible(snapshot) && !now.Before(antigravitySummaryRefreshAtLocked(key, failureKey))
		antigravityCapabilityMu.RUnlock()
		if catalogDue || summaryDue {
			s.scheduleAntigravityModelRefresh(ctx, snapshot)
		}
	}
}

func (s *Service) scheduleAntigravityModelRefresh(ctx context.Context, auth *coreauth.Auth) {
	s.queueAntigravityModelRefresh(ctx, auth, "antigravity", true)
}

func antigravitySummaryRefreshAtLocked(cacheKey, tokenFailureKey string) time.Time {
	entry := antigravityCapabilityCache[cacheKey]
	expiresAt := entry.summaryExpiresAt
	if retry := antigravityFailureNextRetryLocked(cacheKey, tokenFailureKey, sdkauth.AntigravityQuotaSummaryPath); retry.After(expiresAt) {
		return retry
	}
	return expiresAt
}

// nextAntigravityModelRefreshDelay preserves retry jitter in actual scheduling,
// rather than rounding every account's deadline up to the same minute tick.
// The upper bound still discovers accounts/config changes that bypass a wakeup.
func (s *Service) nextAntigravityModelRefreshDelay() time.Duration {
	now := antigravityNowFunc()
	next := now.Add(antigravityRefreshScanInterval)
	if s == nil || s.coreManager == nil || s.antigravityHomeEnabled() {
		return antigravityRefreshScanInterval
	}
	usage := s.NewAntigravityUsageLookup()
	for _, auth := range s.coreManager.List() {
		if auth == nil || isAntigravityAuthDisabled(auth) || !strings.EqualFold(auth.Provider, "antigravity") {
			continue
		}
		key := s.antigravityCapabilityKey(auth)
		token, _ := auth.Metadata["access_token"].(string)
		failureKey := antigravityTokenFailureKey(key, strings.TrimSpace(token))
		antigravityCapabilityMu.RLock()
		retry := antigravityFailureNextRetryLocked(key, failureKey, antigravityModelsPath)
		expiry := antigravityCapabilityCache[key].expiresAt
		var summary time.Time
		if usage.Eligible(auth) {
			summary = antigravitySummaryRefreshAtLocked(key, failureKey)
		}
		antigravityCapabilityMu.RUnlock()
		for _, deadline := range []time.Time{retry, expiry, summary} {
			if deadline.After(now) && deadline.Before(next) {
				next = deadline
			}
		}
	}
	return next.Sub(now)
}

func (s *Service) runAntigravityModelRefresh(ctx context.Context) {
	wake := s.antigravityModelRefreshWake()
	delay := s.nextAntigravityModelRefreshDelay()
	deadline := antigravityNowFunc().Add(delay)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
			// Wakeups may advance a timer, never postpone an already due retry.
			delay = s.nextAntigravityModelRefreshDelay()
			earlier := antigravityNowFunc().Add(delay)
			if earlier.Before(deadline) {
				deadline = earlier
				timer.Reset(delay)
			}
			continue
		case <-timer.C:
			s.refreshAntigravityModels(ctx)
		}
		delay = s.nextAntigravityModelRefreshDelay()
		deadline = antigravityNowFunc().Add(delay)
		timer.Reset(delay)
	}
}

func (s *Service) buildAntigravityReverseAliasMap(auth *coreauth.Auth) map[string]string {
	if auth == nil {
		return nil
	}
	var cfg *config.Config
	if s != nil {
		s.cfgMu.RLock()
		cfg = s.cfg
		s.cfgMu.RUnlock()
	}
	channel := coreauth.OAuthModelAliasChannel(auth.Provider, auth.AuthKind())
	aliases := oauthModelAliasesForAuth(cfg, channel, auth.Attributes)
	if len(aliases) == 0 {
		return nil
	}
	aliasMap := make(map[string]string, len(aliases))
	for _, entry := range aliases {
		aliasName := strings.ToLower(strings.TrimSpace(entry.Alias))
		upstreamName := strings.ToLower(strings.TrimSpace(entry.Name))
		if aliasName != "" && upstreamName != "" {
			aliasMap[aliasName] = upstreamName
		}
	}
	return aliasMap
}

func resolveAntigravityUpstreamModelID(modelID string, prefix string, aliasMap map[string]string) string {
	modelID = strings.ToLower(strings.TrimSpace(modelID))
	prefix = strings.ToLower(strings.Trim(strings.TrimSpace(prefix), "/"))
	unprefixed := modelID
	if prefix != "" && strings.HasPrefix(modelID, prefix+"/") {
		unprefixed = modelID[len(prefix)+1:]
	}
	if len(aliasMap) > 0 {
		if upstream, ok := aliasMap[unprefixed]; ok && upstream != "" {
			return upstream
		}
	}
	return unprefixed
}
