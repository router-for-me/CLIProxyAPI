package cliproxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sync/singleflight"
)

const (
	antigravityModelBaseURLDaily   = "https://daily-cloudcode-pa.googleapis.com"
	antigravityModelsPath          = "/v1internal:fetchAvailableModels"
	antigravityCapabilityCacheTTL  = registry.ModelsRefreshInterval
	antigravityCapabilityRetryBase = time.Minute
	antigravityCapabilityRetryMax  = 30 * time.Minute
	antigravityRefreshScanInterval = time.Minute
)

type antigravityProbeStatus int

const (
	antigravityProbeStatusSuccess antigravityProbeStatus = iota
	antigravityProbeStatusAuthError
	antigravityProbeStatusTransientError
)

type antigravityCapabilityCacheEntry struct {
	hints           antigravityModelCapabilityHints
	expiresAt       time.Time
	lastUsed        time.Time
	probeRevision   uint64
	appliedRevision uint64
	appliedEpoch    uint64
	pending         bool
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
	if now.Sub(previous.lastFailure) > 2*antigravityCapabilityCacheTTL {
		previous.failures = 0
	}
	// Five failures reach the capped window: 2, 4, 8, 16, 30 minutes.
	failures := min(previous.failures, 4) + 1
	window := min(2*antigravityCapabilityRetryBase<<(failures-1), antigravityCapabilityRetryMax)
	// Equal jitter retains a minimum delay, including at the cap (15-30 minutes).
	half := window / 2
	delay := half + time.Duration(randomInt64N(int64(half)+1))
	return antigravityFailureState{failures: failures, lastFailure: now, nextRetry: now.Add(delay)}
}

var (
	antigravityNowFunc          = time.Now
	antigravityCapabilityMu     sync.RWMutex
	antigravityCapabilityCache  = make(map[string]antigravityCapabilityCacheEntry)
	antigravityAuthFailureCache = make(map[string]antigravityFailureState)
	antigravityCapabilityGroup  singleflight.Group
	antigravityProbeSequence    atomic.Uint64
	antigravityProbeSlots       = make(chan struct{}, modelRegistrationMaxWorkersPerCategory)
)

type antigravityFetchAvailableModelsResponse struct {
	Models map[string]json.RawMessage `json:"models"`
}

// antigravityModelCapabilityHints carries per-account model entitlements only.
// Model capabilities such as native web search come from the static catalog.
type antigravityModelCapabilityHints struct {
	// Nil means unknown; an empty non-nil set is an authoritative empty catalog.
	ModelIDs map[string]struct{}
	revision uint64
}

func (h antigravityModelCapabilityHints) clone() antigravityModelCapabilityHints {
	return antigravityModelCapabilityHints{ModelIDs: maps.Clone(h.ModelIDs), revision: h.revision}
}

// antigravityCapabilityKey isolates catalogs by account, project and route, not token lifetime.
func (s *Service) antigravityCapabilityKey(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	return antigravityCapabilityKeyForRoute(auth, antigravityModelBaseURLs(auth)[0], s.antigravityModelFetchProxyURL(auth))
}

func antigravityCapabilityKeyForRoute(auth *coreauth.Auth, endpoint, proxy string) string {
	project, _ := auth.Metadata["project_id"].(string)
	identity := auth.ID
	if identity == "" {
		token, _ := auth.Metadata["access_token"].(string)
		identity = fmt.Sprintf("%x", sha256.Sum256([]byte(token)))
	}
	return fmt.Sprintf("%q/%d/%q/%q/%q", identity, auth.RegistrationEpoch, project, endpoint, proxy)
}

func (s *Service) cachedAntigravityHints(auth *coreauth.Auth) antigravityModelCapabilityHints {
	key := s.antigravityCapabilityKey(auth)
	antigravityCapabilityMu.RLock()
	defer antigravityCapabilityMu.RUnlock()
	return antigravityCapabilityCache[key].hints.clone()
}

func (s *Service) fetchAntigravityModelCapabilityHintsForAuth(ctx context.Context, auth *coreauth.Auth) antigravityModelCapabilityHints {
	if auth == nil || auth.Metadata == nil || s.antigravityHomeEnabled() {
		return antigravityModelCapabilityHints{}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	accessToken, _ := auth.Metadata["access_token"].(string)
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" || ctx.Err() != nil {
		return s.cachedAntigravityHints(auth)
	}
	baseURLs, proxyURL := antigravityModelBaseURLs(auth), s.antigravityModelFetchProxyURL(auth)
	cacheKey := antigravityCapabilityKeyForRoute(auth, baseURLs[0], proxyURL)
	failureKey := cacheKey + fmt.Sprintf("/%x", sha256.Sum256([]byte(accessToken)))
	lookup := func() (antigravityModelCapabilityHints, bool) {
		antigravityCapabilityMu.Lock()
		defer antigravityCapabilityMu.Unlock()
		entry, exists := antigravityCapabilityCache[cacheKey]
		now := antigravityNowFunc()
		if exists {
			entry.lastUsed = now
			antigravityCapabilityCache[cacheKey] = entry
		}
		return entry.hints.clone(), now.Before(entry.expiresAt) || now.Before(antigravityAuthFailureCache[failureKey].nextRetry)
	}
	if hints, cached := lookup(); cached {
		return hints
	}
	// The initiating caller owns the shared HTTP operation. Followers may cancel
	// their own wait without canceling that operation or blocking a refresh worker.
	resultCh := antigravityCapabilityGroup.DoChan(failureKey, func() (any, error) {
		if hints, cached := lookup(); cached {
			return hints, nil
		}
		ctx, finish, active := s.beginAntigravityAccountProbe(ctx, auth, cacheKey, failureKey, accessToken)
		if !active {
			return s.cachedAntigravityHints(auth), nil
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
		hints, status := s.probeAntigravityModelCapabilityHints(ctx, auth, baseURLs, proxyURL, accessToken)
		antigravityCapabilityMu.Lock()
		defer antigravityCapabilityMu.Unlock()
		current := antigravityCapabilityCache[cacheKey]
		if ctx.Err() != nil || current.probeRevision != revision {
			return current.hints.clone(), nil
		}
		now := antigravityNowFunc()
		for key, other := range antigravityCapabilityCache {
			if key != cacheKey && !other.pending && now.Sub(other.lastUsed) > 2*antigravityCapabilityCacheTTL {
				delete(antigravityCapabilityCache, key)
			}
		}
		for key, failure := range antigravityAuthFailureCache {
			if now.Sub(failure.lastFailure) > 2*antigravityCapabilityCacheTTL {
				delete(antigravityAuthFailureCache, key)
			}
		}
		if status == antigravityProbeStatusSuccess && hints.ModelIDs != nil {
			hints.revision = revision
			current.hints, current.expiresAt, current.lastUsed = hints.clone(), now.Add(antigravityCapabilityCacheTTL), now
			antigravityCapabilityCache[cacheKey] = current
			delete(antigravityAuthFailureCache, failureKey)
			return hints, nil
		}
		// Missing models (including legacy capability-only responses) are not a
		// successful catalog. Preserve last success and increase the jittered backoff.
		antigravityAuthFailureCache[failureKey] = nextAntigravityFailure(antigravityAuthFailureCache[failureKey], now, rand.Int64N)
		return current.hints.clone(), nil
	})
	select {
	case <-ctx.Done():
		hints, _ := lookup()
		return hints
	case result := <-resultCh:
		if result.Err != nil || result.Val == nil {
			hints, _ := lookup()
			return hints
		}
		return result.Val.(antigravityModelCapabilityHints).clone()
	}
}

func (s *Service) probeAntigravityModelCapabilityHints(ctx context.Context, auth *coreauth.Auth, baseURLs []string, proxyURL string, accessToken string) (antigravityModelCapabilityHints, antigravityProbeStatus) {
	if len(baseURLs) == 0 {
		return antigravityModelCapabilityHints{}, antigravityProbeStatusTransientError
	}
	client := &http.Client{}
	transport, _, errProxy := proxyutil.BuildHTTPTransport(proxyURL)
	if errProxy != nil {
		return antigravityModelCapabilityHints{}, antigravityProbeStatusTransientError
	}
	if transport != nil {
		client.Transport = transport
		defer transport.CloseIdleConnections()
	}
	project, _ := auth.Metadata["project_id"].(string)
	body, errMarshal := json.Marshal(map[string]string{"project": project})
	if errMarshal != nil {
		return antigravityModelCapabilityHints{}, antigravityProbeStatusTransientError
	}
	// Default to daily and use only the first explicitly configured endpoint.
	// Racing catalogs from different endpoints can grant incompatible entitlements.
	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURLs[0], "/")+antigravityModelsPath, bytes.NewReader(body))
	if errReq != nil {
		return antigravityModelCapabilityHints{}, antigravityProbeStatusTransientError
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", misc.AntigravityUserAgent())
	resp, errDo := client.Do(req)
	if errDo != nil {
		return antigravityModelCapabilityHints{}, antigravityProbeStatusTransientError
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debug("antigravity model fetch: response close failed")
		}
	}()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return antigravityModelCapabilityHints{}, antigravityProbeStatusAuthError
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return antigravityModelCapabilityHints{}, antigravityProbeStatusTransientError
	}
	const maxCatalogBytes = 8 << 20
	data, errRead := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBytes+1))
	if errRead != nil || len(data) > maxCatalogBytes {
		return antigravityModelCapabilityHints{}, antigravityProbeStatusTransientError
	}
	hints, ok := parseAntigravityModelCapabilityHints(data)
	if !ok {
		return antigravityModelCapabilityHints{}, antigravityProbeStatusTransientError
	}
	return hints, antigravityProbeStatusSuccess
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
	var parsed antigravityFetchAvailableModelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return antigravityModelCapabilityHints{}, false
	}
	var modelIDs map[string]struct{}
	if parsed.Models != nil {
		modelIDs = make(map[string]struct{}, len(parsed.Models))
		for id := range parsed.Models {
			if normalized := normalizeAntigravityFetchedModelID(id); normalized != "" {
				modelIDs[normalized] = struct{}{}
			}
		}
	}
	return antigravityModelCapabilityHints{ModelIDs: modelIDs}, true
}

func normalizeAntigravityFetchedModelID(modelID string) string {
	return strings.ToLower(strings.TrimSpace(modelID))
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

func (s *Service) antigravityCatalogModels() []*ModelInfo {
	if s != nil && s.antigravityCatalog != nil {
		return s.antigravityCatalog()
	}
	return registry.GetAntigravityModels()
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
	models := filterAntigravityModels(s.antigravityCatalogModels(), hints)
	// Search capabilities come exclusively from models.json, never account probes.
	excluded := cfg.OAuthExcludedModels[coreauth.OAuthModelAliasChannel(auth.Provider, auth.AuthKind())]
	if value := strings.TrimSpace(auth.Attributes["excluded_models"]); value != "" {
		excluded = strings.Split(value, ",")
	}
	models = applyExcludedModels(models, excluded)
	models = applyOAuthModelAliasForAuth(cfg, "antigravity", auth.AuthKind(), auth.Attributes, models)
	models = s.appendPluginModels("antigravity", models)
	models = applyOAuthSettingsForAuth(cfg, "antigravity", auth.AuthKind(), models)
	return applyModelPrefixes(models, auth.Prefix, cfg.ForceModelPrefix)
}

func (s *Service) asyncProbeAntigravityCapabilities(ctx context.Context, auth *coreauth.Auth, providerKey string) {
	s.queueAntigravityModelRefresh(ctx, auth, providerKey)
}

func (s *Service) probeAndRegisterAntigravityModels(ctx context.Context, auth *coreauth.Auth, providerKey string, expectedEpoch, expectedRegEpoch uint64) {
	expectedKey := s.antigravityCapabilityKey(auth)
	hints := s.fetchAntigravityModelCapabilityHintsForAuth(ctx, auth)
	s.applyAntigravityModelHints(ctx, auth, providerKey, hints, expectedKey, expectedEpoch, expectedRegEpoch)
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
	if ctx.Err() != nil || s.antigravityHomeEnabled() {
		return
	}
	if hints.ModelIDs == nil {
		return
	}
	if s.coreManager != nil {
		current, exists := s.coreManager.GetByID(auth.ID)
		if !exists || current == nil || current.Disabled || current.Provider != auth.Provider || current.RegistrationEpoch != expectedEpoch || !antigravityAuthModelSettingsEqual(current, auth) || s.antigravityCapabilityKey(current) != expectedKey {
			return
		}
	}
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
	appliedEpoch, updated := reg.ReplaceClientModels(auth.ID, providerKey, expectedRegEpoch, models)
	if updated {
		entry.appliedRevision = hints.revision
		entry.appliedEpoch = appliedEpoch
		antigravityCapabilityCache[expectedKey] = entry
	}
	antigravityCapabilityMu.Unlock()
	s.cfgMu.RUnlock()
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
	for _, auth := range s.coreManager.List() {
		if ctx.Err() != nil {
			return
		}
		if auth == nil || auth.Disabled || !strings.EqualFold(auth.Provider, "antigravity") {
			continue
		}
		snapshot := auth.Clone()
		key := s.antigravityCapabilityKey(snapshot)
		token, _ := snapshot.Metadata["access_token"].(string)
		failureKey := key + fmt.Sprintf("/%x", sha256.Sum256([]byte(strings.TrimSpace(token))))
		epoch := GlobalModelRegistry().ClientRegistrationEpoch(snapshot.ID)
		antigravityCapabilityMu.RLock()
		entry := antigravityCapabilityCache[key]
		registered := entry.appliedRevision == entry.hints.revision && entry.appliedEpoch == epoch
		fresh := (antigravityNowFunc().Before(entry.expiresAt) && registered) || antigravityNowFunc().Before(antigravityAuthFailureCache[failureKey].nextRetry)
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
		if auth == nil || auth.Disabled || !strings.EqualFold(auth.Provider, "antigravity") {
			continue
		}
		key := s.antigravityCapabilityKey(auth)
		token, _ := auth.Metadata["access_token"].(string)
		failureKey := key + fmt.Sprintf("/%x", sha256.Sum256([]byte(strings.TrimSpace(token))))
		antigravityCapabilityMu.RLock()
		retry := antigravityAuthFailureCache[failureKey].nextRetry
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
