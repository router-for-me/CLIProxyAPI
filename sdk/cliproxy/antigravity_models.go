package cliproxy

import (
	"context"
	"crypto/sha256"
	"fmt"
	"maps"
	"math/rand/v2"
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
	lastUsed          time.Time
	probeRevision     uint64
	appliedRevision   uint64
	appliedEpoch      uint64
	persistedRevision uint64
	pending           bool
}

// Keep attempt history beyond nextRetry; deleting it at retry time would turn
// exponential backoff back into a fixed interval. Inactive histories expire with
// the same retention horizon as inactive successful catalogs.
type antigravityFailureState struct {
	failures    uint8
	lastFailure time.Time
	nextRetry   time.Time
	terminal    bool
	authID      string
}

type antigravityTerminalFailureRef struct {
	key         string
	authID      string
	lastFailure time.Time
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

func isAntigravityRefreshTerminallyRejected(auth *coreauth.Auth) bool {
	if auth == nil {
		return false
	}
	refreshToken := ""
	if auth.Metadata != nil {
		refreshToken, _ = auth.Metadata["refresh_token"].(string)
		refreshToken = strings.TrimSpace(refreshToken)
		if refreshToken == "" {
			refreshToken, _ = auth.Metadata["refreshToken"].(string)
			refreshToken = strings.TrimSpace(refreshToken)
		}
	}
	rejected := strings.TrimSpace(auth.RejectedRefreshToken)
	if rejected != "" {
		return refreshToken == "" || refreshToken == rejected
	}
	return auth.RequiresSignIn
}

// antigravityTerminalAuthFailureKey scopes confirmed terminal 401 suppression to
// the credential identity, registration epoch, and access_token only, independent
// of route attributes such as User-Agent or proxy URL.
func antigravityTerminalAuthFailureKey(auth *coreauth.Auth, accessToken string) string {
	if auth == nil {
		return ""
	}
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return ""
	}
	tokenHash := fmt.Sprintf("%x", sha256.Sum256([]byte(accessToken)))
	identity := auth.ID
	if identity == "" {
		identity = tokenHash
	}
	return fmt.Sprintf("terminal:%q/%d/%s", identity, auth.RegistrationEpoch, tokenHash)
}

func isAntigravityTokenTerminallySuppressed(auth *coreauth.Auth) bool {
	if !isAntigravityRefreshTerminallyRejected(auth) || auth.Metadata == nil {
		return false
	}
	token, _ := auth.Metadata["access_token"].(string)
	terminalKey := antigravityTerminalAuthFailureKey(auth, token)
	if terminalKey == "" {
		return false
	}
	antigravityCapabilityMu.RLock()
	suppressed := antigravityAuthFailureCache[terminalKey].terminal
	antigravityCapabilityMu.RUnlock()
	return suppressed
}

func shouldSkipAntigravityModelProbe(auth *coreauth.Auth, now time.Time) bool {
	if auth == nil || isAntigravityAuthDisabled(auth) {
		return true
	}
	if coreauth.HasUnauthorizedAuthFailure(auth) {
		return true
	}
	if now.IsZero() {
		now = antigravityNowFunc()
	}
	if !auth.HasValidAccessToken(now) {
		return true
	}
	return isAntigravityTokenTerminallySuppressed(auth)
}

func (s *Service) pruneStaleAntigravityTerminalFailures(candidates []antigravityTerminalFailureRef) {
	if s == nil || s.coreManager == nil || len(candidates) == 0 {
		return
	}
	var stale []antigravityTerminalFailureRef
	for _, cand := range candidates {
		currentAuth, exists := s.coreManager.GetByID(cand.authID)
		if !exists || currentAuth == nil || isAntigravityAuthDisabled(currentAuth) || !isAntigravityRefreshTerminallyRejected(currentAuth) {
			stale = append(stale, cand)
			continue
		}
		currentToken, _ := currentAuth.Metadata["access_token"].(string)
		if antigravityTerminalAuthFailureKey(currentAuth, currentToken) != cand.key {
			stale = append(stale, cand)
		}
	}
	if len(stale) == 0 {
		return
	}
	antigravityCapabilityMu.Lock()
	for _, cand := range stale {
		if entry, ok := antigravityAuthFailureCache[cand.key]; ok && entry.terminal && entry.lastFailure.Equal(cand.lastFailure) {
			delete(antigravityAuthFailureCache, cand.key)
		}
	}
	antigravityCapabilityMu.Unlock()
}

func antigravityTokenFailureKey(cacheKey, accessToken string) string {
	return cacheKey + fmt.Sprintf("/%x", sha256.Sum256([]byte(accessToken)))
}

func antigravityFailureNextRetryLocked(cacheKey, tokenFailureKey string) time.Time {
	nextRetry := antigravityAuthFailureCache[tokenFailureKey].nextRetry
	if routeRetry := antigravityAuthFailureCache[cacheKey].nextRetry; routeRetry.After(nextRetry) {
		nextRetry = routeRetry
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
	ModelIDs         map[string]struct{}
	revision         uint64
	updatedAt        time.Time
	expiresAt        time.Time
	routeFingerprint string
}

func (h antigravityModelCapabilityHints) clone() antigravityModelCapabilityHints {
	return antigravityModelCapabilityHints{
		WebSearchModelIDs: maps.Clone(h.WebSearchModelIDs),
		ModelIDs:          maps.Clone(h.ModelIDs),
		revision:          h.revision,
		updatedAt:         h.updatedAt,
		expiresAt:         h.expiresAt,
		routeFingerprint:  h.routeFingerprint,
	}
}

// antigravityCapabilityKey isolates catalogs by account, project, route and client
// fingerprint. Token rotation alone does not invalidate a successful catalog.
func (s *Service) antigravityCapabilityKey(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	return antigravityCapabilityKeyForRoute(auth, antigravityModelBaseURLs(auth)[0], s.antigravityModelFetchProxyURL(auth), antigravityModelUserAgent())
}

func antigravityCapabilityKeyForRoute(auth *coreauth.Auth, endpoint, proxy, userAgent string) string {
	project, _ := auth.Metadata["project_id"].(string)
	identity := auth.ID
	if identity == "" {
		token, _ := auth.Metadata["access_token"].(string)
		identity = fmt.Sprintf("%x", sha256.Sum256([]byte(token)))
	}
	return fmt.Sprintf("%q/%d/%q/%q/%q/%q", identity, auth.RegistrationEpoch, project, endpoint, proxy, userAgent)
}

func (s *Service) cachedAntigravityHints(auth *coreauth.Auth) antigravityModelCapabilityHints {
	if auth == nil || isAntigravityAuthDisabled(auth) {
		return antigravityModelCapabilityHints{}
	}
	endpoint := antigravityModelBaseURLs(auth)[0]
	proxyURL := s.antigravityModelFetchProxyURL(auth)
	userAgent := antigravityModelUserAgent()
	key := antigravityCapabilityKeyForRoute(auth, endpoint, proxyURL, userAgent)
	routeFingerprint := sdkauth.AntigravityCapabilityRouteFingerprint(proxyURL, userAgent)
	return s.cachedAntigravityHintsForKey(auth, endpoint, routeFingerprint, key)
}

func (s *Service) cachedAntigravityHintsForKey(auth *coreauth.Auth, endpoint, routeFingerprint, key string) antigravityModelCapabilityHints {
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
	entry = seedAntigravityCapabilityCacheLocked(key, persisted, routeFingerprint)
	return entry.hints.clone()
}

func persistedAntigravityHintsExpiresAt(persisted antigravityModelCapabilityHints, expectedRouteFingerprint string, now time.Time) time.Time {
	expectedRouteFingerprint = strings.TrimSpace(expectedRouteFingerprint)
	persistedRouteFingerprint := strings.TrimSpace(persisted.routeFingerprint)
	if expectedRouteFingerprint == "" || persistedRouteFingerprint == "" || persistedRouteFingerprint != expectedRouteFingerprint {
		return time.Time{}
	}
	var candidate time.Time
	switch {
	case !persisted.expiresAt.IsZero():
		candidate = persisted.expiresAt
		if !persisted.updatedAt.IsZero() {
			if updatedCap := persisted.updatedAt.Add(antigravityCapabilityCacheTTL); updatedCap.Before(candidate) {
				candidate = updatedCap
			}
		}
	case !persisted.updatedAt.IsZero():
		candidate = persisted.updatedAt.Add(antigravityCapabilityCacheTTL)
	default:
		return time.Time{}
	}
	if maxExpiry := now.Add(antigravityCapabilityCacheTTL); candidate.After(maxExpiry) {
		candidate = maxExpiry
	}
	if !candidate.After(now) {
		return time.Time{}
	}
	return candidate
}

func seedAntigravityCapabilityCacheLocked(cacheKey string, persisted antigravityModelCapabilityHints, expectedRouteFingerprint string) antigravityCapabilityCacheEntry {
	entry := antigravityCapabilityCache[cacheKey]
	if entry.hints.ModelIDs != nil {
		return entry
	}
	now := antigravityNowFunc()
	seeded := persisted.clone()
	seeded.revision = antigravityProbeSequence.Add(1)
	entry.hints = seeded
	entry.persistedRevision = seeded.revision
	entry.expiresAt = persistedAntigravityHintsExpiresAt(seeded, expectedRouteFingerprint, now)
	entry.lastUsed = now
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
		UpdatedAt:         hints.updatedAt,
		ExpiresAt:         hints.expiresAt,
		RouteFingerprint:  hints.routeFingerprint,
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
		updatedAt:         hints.UpdatedAt,
		expiresAt:         hints.ExpiresAt,
		routeFingerprint:  hints.RouteFingerprint,
	}, true
}

func (s *Service) fetchAntigravityModelCapabilityHintsForAuth(ctx context.Context, auth *coreauth.Auth) antigravityModelCapabilityHints {
	_, hints := s.fetchAntigravityModelCapabilityHints(ctx, auth)
	return hints
}

// Return the key captured with the HTTP User-Agent so publication cannot label a
// result with a version resolved before or after the actual probe snapshot.
func (s *Service) fetchAntigravityModelCapabilityHints(ctx context.Context, auth *coreauth.Auth) (string, antigravityModelCapabilityHints) {
	if auth == nil || auth.Metadata == nil || shouldSkipAntigravityModelProbe(auth, antigravityNowFunc()) || s.antigravityHomeEnabled() {
		return "", antigravityModelCapabilityHints{}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	baseURLs, proxyURL := antigravityModelBaseURLs(auth), s.antigravityModelFetchProxyURL(auth)
	userAgent := antigravityModelUserAgent()
	cacheKey := antigravityCapabilityKeyForRoute(auth, baseURLs[0], proxyURL, userAgent)
	routeFingerprint := sdkauth.AntigravityCapabilityRouteFingerprint(proxyURL, userAgent)
	accessToken, _ := auth.Metadata["access_token"].(string)
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" || ctx.Err() != nil {
		return cacheKey, s.cachedAntigravityHintsForKey(auth, baseURLs[0], routeFingerprint, cacheKey)
	}
	// Keep singleflight and transient/401 failure keys token-scoped, while 403 forbidden
	// failures use cacheKey so routine access_token rotation does not reset backoff.
	flightKey := antigravityTokenFailureKey(cacheKey, accessToken)
	lookup := func() (antigravityModelCapabilityHints, bool) {
		persisted, hasPersisted := s.persistedAntigravityHintsForAuth(auth, baseURLs[0])
		antigravityCapabilityMu.Lock()
		defer antigravityCapabilityMu.Unlock()
		entry, exists := antigravityCapabilityCache[cacheKey]
		if entry.hints.ModelIDs == nil && hasPersisted {
			entry = seedAntigravityCapabilityCacheLocked(cacheKey, persisted, routeFingerprint)
			exists = true
		}
		now := antigravityNowFunc()
		if exists {
			entry.lastUsed = now
			antigravityCapabilityCache[cacheKey] = entry
		}
		return entry.hints.clone(), now.Before(entry.expiresAt) || now.Before(antigravityFailureNextRetryLocked(cacheKey, flightKey))
	}
	if hints, cached := lookup(); cached {
		return cacheKey, hints
	}
	// The initiating caller owns the shared HTTP operation. Followers may cancel
	// their own wait without canceling that operation or blocking a refresh worker.
	resultCh := antigravityCapabilityGroup.DoChan(flightKey, func() (any, error) {
		if hints, cached := lookup(); cached {
			return hints, nil
		}
		ctx, finish, active := s.beginAntigravityAccountProbe(ctx, auth, cacheKey, flightKey, accessToken)
		if !active {
			return s.cachedAntigravityHintsForKey(auth, baseURLs[0], routeFingerprint, cacheKey), nil
		}
		defer finish()
		revision := antigravityProbeSequence.Add(1)
		antigravityCapabilityMu.Lock()
		entry := antigravityCapabilityCache[cacheKey]
		entry.probeRevision, entry.lastUsed, entry.pending = revision, antigravityNowFunc(), true
		antigravityCapabilityCache[cacheKey] = entry
		antigravityCapabilityMu.Unlock()
		defer func() {
			antigravityCapabilityMu.Lock()
			current := antigravityCapabilityCache[cacheKey]
			if current.probeRevision == revision {
				current.pending = false
				antigravityCapabilityCache[cacheKey] = current
			}
			antigravityCapabilityMu.Unlock()
		}()
		select {
		case antigravityProbeSlots <- struct{}{}:
		case <-ctx.Done():
			hints, _ := lookup()
			return hints, nil
		}
		defer func() { <-antigravityProbeSlots }()
		// Re-check current credential eligibility after waiting for a probe slot,
		// since token expiry passes without emitting an auth-change notification.
		probeAuth := auth
		if s.coreManager != nil && auth.ID != "" {
			currentAuth, exists := s.coreManager.GetByID(auth.ID)
			if !exists || currentAuth == nil || currentAuth.RegistrationEpoch != auth.RegistrationEpoch {
				return s.cachedAntigravityHintsForKey(auth, baseURLs[0], routeFingerprint, cacheKey), nil
			}
			currentToken, _ := currentAuth.Metadata["access_token"].(string)
			if strings.TrimSpace(currentToken) != accessToken {
				return s.cachedAntigravityHintsForKey(auth, baseURLs[0], routeFingerprint, cacheKey), nil
			}
			probeAuth = currentAuth
		}
		if ctx.Err() != nil || shouldSkipAntigravityModelProbe(probeAuth, antigravityNowFunc()) {
			return s.cachedAntigravityHintsForKey(auth, baseURLs[0], routeFingerprint, cacheKey), nil
		}
		hints, status := s.probeAntigravityModelCapabilityHints(ctx, auth, baseURLs, proxyURL, accessToken, userAgent)
		terminalFailureKey := antigravityTerminalAuthFailureKey(probeAuth, accessToken)
		terminalAuthError := false
		terminalAuthID := probeAuth.ID
		if status == antigravityProbeStatusAuthError {
			latestAuth := probeAuth
			if s.coreManager != nil && auth.ID != "" {
				if currentAuth, exists := s.coreManager.GetByID(auth.ID); exists && currentAuth != nil && currentAuth.RegistrationEpoch == auth.RegistrationEpoch {
					latestAuth = currentAuth
				}
			}
			terminalAuthError = isAntigravityRefreshTerminallyRejected(latestAuth)
			terminalAuthID = latestAuth.ID
			terminalFailureKey = antigravityTerminalAuthFailureKey(latestAuth, accessToken)
		}
		var staleTerminalCandidates []antigravityTerminalFailureRef
		antigravityCapabilityMu.Lock()
		current := antigravityCapabilityCache[cacheKey]
		if ctx.Err() != nil || current.probeRevision != revision {
			retained := current.hints.clone()
			antigravityCapabilityMu.Unlock()
			return retained, nil
		}
		now := antigravityNowFunc()
		for key, other := range antigravityCapabilityCache {
			if key != cacheKey && !other.pending && now.Sub(other.lastUsed) > 2*antigravityCapabilityCacheTTL {
				delete(antigravityCapabilityCache, key)
			}
		}
		for key, failure := range antigravityAuthFailureCache {
			if failure.terminal {
				if s.coreManager != nil && failure.authID != "" && now.Sub(failure.lastFailure) > 2*antigravityCapabilityCacheTTL {
					staleTerminalCandidates = append(staleTerminalCandidates, antigravityTerminalFailureRef{
						key:         key,
						authID:      failure.authID,
						lastFailure: failure.lastFailure,
					})
				}
				continue
			}
			if now.Sub(failure.lastFailure) > 2*antigravityCapabilityCacheTTL {
				delete(antigravityAuthFailureCache, key)
			}
		}
		if status == antigravityProbeStatusSuccess && hints.ModelIDs != nil {
			expiresAt := now.Add(antigravityCapabilityCacheTTL)
			hints.revision = revision
			hints.updatedAt = now
			hints.expiresAt = expiresAt
			hints.routeFingerprint = routeFingerprint
			current.hints, current.expiresAt, current.lastUsed = hints.clone(), expiresAt, now
			antigravityCapabilityCache[cacheKey] = current
			delete(antigravityAuthFailureCache, flightKey)
			delete(antigravityAuthFailureCache, cacheKey)
			if terminalFailureKey != "" {
				delete(antigravityAuthFailureCache, terminalFailureKey)
			}
			antigravityCapabilityMu.Unlock()
			s.pruneStaleAntigravityTerminalFailures(staleTerminalCandidates)
			return hints, nil
		}
		switch status {
		case antigravityProbeStatusForbidden:
			// 403 forbidden failures persist across routine access_token rotation via cacheKey.
			antigravityAuthFailureCache[cacheKey] = nextAntigravityForbiddenFailure(antigravityAuthFailureCache[cacheKey], now, rand.Int64N)
		case antigravityProbeStatusAuthError:
			if terminalAuthError && terminalFailureKey != "" {
				// Refresh grant is terminally rejected and upstream confirmed this access_token
				// is unauthorized; suppress further probes for this credential+token independent of route metadata.
				delete(antigravityAuthFailureCache, flightKey)
				antigravityAuthFailureCache[terminalFailureKey] = antigravityFailureState{
					failures:    antigravityFailureMaxCount,
					lastFailure: now,
					terminal:    true,
					authID:      terminalAuthID,
				}
			} else {
				// 401 unauthorized failures back off longer for the same token while retrying immediately on token refresh.
				antigravityAuthFailureCache[flightKey] = nextAntigravityAuthErrorFailure(antigravityAuthFailureCache[flightKey], now, rand.Int64N)
			}
		default:
			// Missing models (including legacy capability-only responses) and transient errors
			// are not a successful catalog. Preserve last success and increase the jittered backoff.
			antigravityAuthFailureCache[flightKey] = nextAntigravityFailure(antigravityAuthFailureCache[flightKey], now, rand.Int64N)
		}
		retained := current.hints.clone()
		if status == antigravityProbeStatusSuccess && len(hints.WebSearchModelIDs) > 0 {
			retained.WebSearchModelIDs = hints.WebSearchModelIDs
		}
		antigravityCapabilityMu.Unlock()
		s.pruneStaleAntigravityTerminalFailures(staleTerminalCandidates)
		return retained, nil
	})
	select {
	case <-ctx.Done():
		hints, _ := lookup()
		return cacheKey, hints
	case result := <-resultCh:
		if result.Err != nil || result.Val == nil {
			hints, _ := lookup()
			return cacheKey, hints
		}
		return cacheKey, result.Val.(antigravityModelCapabilityHints).clone()
	}
}

func (s *Service) probeAntigravityModelCapabilityHints(ctx context.Context, auth *coreauth.Auth, baseURLs []string, proxyURL, accessToken, userAgent string) (antigravityModelCapabilityHints, antigravityProbeStatus) {
	project := ""
	if auth != nil && auth.Metadata != nil {
		project, _ = auth.Metadata["project_id"].(string)
	}
	hints, status := sdkauth.FetchAntigravityAvailableModels(ctx, sdkauth.AntigravityModelProbeRequest{
		BaseURLs:    baseURLs,
		ProxyURL:    proxyURL,
		AccessToken: accessToken,
		ProjectID:   project,
		UserAgent:   userAgent,
	})
	return antigravityModelCapabilityHints{
		ModelIDs:          hints.ModelIDs,
		WebSearchModelIDs: hints.WebSearchModelIDs,
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
	return []string{antigravityModelBaseURLDaily}
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
	s.queueAntigravityModelRefresh(ctx, auth, providerKey)
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
	for _, auth := range s.coreManager.List() {
		if ctx.Err() != nil {
			return
		}
		if auth == nil || shouldSkipAntigravityModelProbe(auth, antigravityNowFunc()) || !strings.EqualFold(auth.Provider, "antigravity") {
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
		fresh := (antigravityNowFunc().Before(entry.expiresAt) && registered && persisted) || antigravityNowFunc().Before(antigravityFailureNextRetryLocked(key, failureKey))
		antigravityCapabilityMu.RUnlock()
		if fresh {
			continue
		}
		s.scheduleAntigravityModelRefresh(ctx, snapshot)
	}
}

func (s *Service) scheduleAntigravityModelRefresh(ctx context.Context, auth *coreauth.Auth) {
	s.queueAntigravityModelRefresh(ctx, auth, "antigravity")
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
	for _, auth := range s.coreManager.List() {
		if auth == nil || shouldSkipAntigravityModelProbe(auth, now) || !strings.EqualFold(auth.Provider, "antigravity") {
			continue
		}
		key := s.antigravityCapabilityKey(auth)
		token, _ := auth.Metadata["access_token"].(string)
		failureKey := antigravityTokenFailureKey(key, strings.TrimSpace(token))
		antigravityCapabilityMu.RLock()
		retry := antigravityFailureNextRetryLocked(key, failureKey)
		expiry := antigravityCapabilityCache[key].expiresAt
		antigravityCapabilityMu.RUnlock()
		for _, deadline := range []time.Time{retry, expiry} {
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
