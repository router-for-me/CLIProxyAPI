package usageapi

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

const (
	// maxReportsPerRequest bounds upstream work triggered by one client request.
	maxReportsPerRequest = 128
	// maxConcurrentFetches bounds parallel quota probes.
	maxConcurrentFetches = 4
	// failedFetchCacheTTL prevents a failing upstream from being retried on
	// every client request.
	failedFetchCacheTTL = 10 * time.Second
	// cacheEntryLimit bounds the report cache after credential rotation.
	cacheEntryLimit = 1024
)

// AuthLister supplies credential snapshots.
type AuthLister interface {
	List() []*coreauth.Auth
}

// QuotaFetcher fetches provider quota for one credential.
type QuotaFetcher interface {
	FetchQuota(ctx context.Context, req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, bool, error)
}

type cacheEntry struct {
	report    *Report
	expiresAt time.Time
	failed    bool
}

// Service builds filtered usage reports from quota providers and observed
// upstream quota signals.
type Service struct {
	config func() *config.UsageAPIConfig
	auths  AuthLister
	quotas QuotaFetcher
	now    func() time.Time

	fetchSem chan struct{}

	cacheMu sync.Mutex
	cache   map[string]cacheEntry
}

// New creates a usage service. cfg must return the current runtime
// configuration; the function is called on every request so hot reloads take
// effect without restarting the server.
func New(cfg func() *config.UsageAPIConfig, auths AuthLister, quotas QuotaFetcher) *Service {
	return &Service{
		config:   cfg,
		auths:    auths,
		quotas:   quotas,
		now:      time.Now,
		fetchSem: make(chan struct{}, 1),
		cache:    make(map[string]cacheEntry),
	}
}

// Enabled reports whether the endpoint is configured and has a provider
// allow-list. An empty provider list is treated as disabled so a partially
// configured feature never exposes every credential.
func (s *Service) Enabled() bool {
	if s == nil {
		return false
	}
	cfg := s.configValue()
	return cfg != nil && cfg.Enabled && len(allowedProviders(cfg)) > 0
}

func (s *Service) configValue() *config.UsageAPIConfig {
	if s == nil || s.config == nil {
		return nil
	}
	return s.config()
}

// Snapshot builds a response for the current configuration. It serializes
// upstream work so concurrent client requests share cache entries instead of
// stampeding quota providers.
func (s *Service) Snapshot(ctx context.Context) Response {
	now := s.currentTime()
	resp := Response{GeneratedAt: now.UnixMilli(), Reports: make([]Report, 0)}
	if !s.Enabled() || s.auths == nil {
		return resp
	}
	cfg := s.configValue()
	if cfg == nil {
		return resp
	}

	select {
	case s.fetchSem <- struct{}{}:
		defer func() { <-s.fetchSem }()
	case <-ctx.Done():
		return resp
	}

	auths := s.selectedAuths(cfg)
	if len(auths) == 0 {
		return resp
	}

	reports := make([]*Report, len(auths))
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxConcurrentFetches)
	for index, selection := range auths {
		wg.Add(1)
		go func(index int, selection selectedAuth) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			reports[index] = s.reportForAuth(ctx, cfg, selection)
		}(index, selection)
	}
	wg.Wait()

	resp.Reports = make([]Report, 0, len(auths))
	for _, report := range reports {
		if report != nil {
			resp.Reports = append(resp.Reports, *report)
		}
	}
	return resp
}

type selectedAuth struct {
	auth        *coreauth.Auth
	providerKey string
	rank        int
}

func (s *Service) selectedAuths(cfg *config.UsageAPIConfig) []selectedAuth {
	providers := allowedProviders(cfg)
	if len(providers) == 0 || s.auths == nil {
		return nil
	}
	providerRank := make(map[string]int, len(providers))
	for index, provider := range providers {
		providerRank[provider] = index
	}
	credentials := credentialAllowList(cfg)

	out := make([]selectedAuth, 0)
	for _, auth := range s.auths.List() {
		if auth == nil || auth.Disabled || auth.Status == coreauth.StatusDisabled {
			continue
		}
		providerKey, rank, ok := matchProvider(auth, providerRank)
		if !ok {
			continue
		}
		auth.EnsureIndex()
		if len(credentials) > 0 && !matchesCredential(auth, credentials) {
			continue
		}
		out = append(out, selectedAuth{auth: auth, providerKey: providerKey, rank: rank})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].rank != out[j].rank {
			return out[i].rank < out[j].rank
		}
		if out[i].providerKey != out[j].providerKey {
			return out[i].providerKey < out[j].providerKey
		}
		return authOrderKey(out[i].auth) < authOrderKey(out[j].auth)
	})
	if len(out) > maxReportsPerRequest {
		out = out[:maxReportsPerRequest]
	}
	return out
}

func allowedProviders(cfg *config.UsageAPIConfig) []string {
	if cfg == nil || len(cfg.Providers) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(cfg.Providers))
	out := make([]string, 0, len(cfg.Providers))
	for _, provider := range cfg.Providers {
		provider = strings.ToLower(strings.TrimSpace(provider))
		if provider == "" {
			continue
		}
		if _, exists := seen[provider]; exists {
			continue
		}
		seen[provider] = struct{}{}
		out = append(out, provider)
	}
	return out
}

func credentialAllowList(cfg *config.UsageAPIConfig) map[string]struct{} {
	if cfg == nil || len(cfg.Credentials) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(cfg.Credentials))
	for _, credential := range cfg.Credentials {
		credential = strings.ToLower(strings.TrimSpace(credential))
		if credential != "" {
			out[credential] = struct{}{}
		}
	}
	return out
}

func matchProvider(auth *coreauth.Auth, allowed map[string]int) (string, int, bool) {
	if auth == nil {
		return "", 0, false
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	if rank, ok := allowed[provider]; ok {
		return provider, rank, true
	}
	if auth.Attributes == nil {
		return "", 0, false
	}
	compatName := strings.ToLower(strings.TrimSpace(auth.Attributes["compat_name"]))
	if compatName == "" {
		return "", 0, false
	}
	rank, ok := allowed[compatName]
	if !ok {
		return "", 0, false
	}
	return compatName, rank, true
}

func matchesCredential(auth *coreauth.Auth, allowed map[string]struct{}) bool {
	if auth == nil {
		return false
	}
	for _, candidate := range []string{auth.Index, auth.ID, auth.FileName} {
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		if candidate == "" {
			continue
		}
		if _, ok := allowed[candidate]; ok {
			return true
		}
	}
	return false
}

func authOrderKey(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if id := strings.TrimSpace(auth.ID); id != "" {
		return id
	}
	if index := strings.TrimSpace(auth.Index); index != "" {
		return index
	}
	return strings.TrimSpace(auth.Label)
}

func (s *Service) reportForAuth(ctx context.Context, cfg *config.UsageAPIConfig, selection selectedAuth) *Report {
	auth := selection.auth
	if auth == nil {
		return nil
	}
	cacheKey := reportCacheKey(selection)
	if cached, hit, failed := s.cachedReport(cacheKey); hit {
		if failed {
			return reportFromObservedSignals(auth, selection.providerKey)
		}
		return cached
	}

	now := s.currentTime()
	if s.quotas != nil {
		quota, handled, errFetch := s.quotas.FetchQuota(ctx, pluginapi.QuotaFetchRequest{
			AuthIndex:  auth.Index,
			AuthID:     auth.ID,
			Provider:   selection.providerKey,
			Metadata:   auth.Metadata,
			Attributes: auth.Attributes,
		})
		if handled && errFetch == nil {
			if report := reportFromQuota(selection.providerKey, quota, now.UnixMilli()); report != nil {
				s.storeReport(cacheKey, report, cfg.EffectiveCacheSeconds())
				return report
			}
		} else if handled && errFetch != nil {
			if ctx.Err() == nil {
				log.WithError(errFetch).WithFields(log.Fields{
					"provider":   selection.providerKey,
					"auth_index": auth.Index,
				}).Warn("usage api: quota fetch failed")
			}
			s.storeFailure(cacheKey, cfg.EffectiveCacheSeconds())
		}
	}

	return reportFromObservedSignals(auth, selection.providerKey)
}

func reportCacheKey(selection selectedAuth) string {
	auth := selection.auth
	if auth == nil {
		return selection.providerKey
	}
	return strings.Join([]string{
		selection.providerKey,
		strings.TrimSpace(auth.ID),
		strings.TrimSpace(auth.FileName),
		strings.TrimSpace(auth.Index),
		strconv.FormatUint(auth.CredentialVersion, 10),
	}, "\x00")
}

func (s *Service) cachedReport(key string) (*Report, bool, bool) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	entry, ok := s.cache[key]
	if !ok {
		return nil, false, false
	}
	if !entry.expiresAt.After(s.currentTime()) {
		delete(s.cache, key)
		return nil, false, false
	}
	if entry.failed || entry.report == nil {
		return nil, true, true
	}
	return entry.report, true, false
}

func (s *Service) storeReport(key string, report *Report, cacheSeconds int) {
	if report == nil {
		return
	}
	s.storeCacheEntry(key, cacheEntry{
		report:    report,
		expiresAt: s.currentTime().Add(time.Duration(cacheSeconds) * time.Second),
	})
}

func (s *Service) storeFailure(key string, cacheSeconds int) {
	ttl := failedFetchCacheTTL
	if configured := time.Duration(cacheSeconds) * time.Second; configured > 0 && configured < ttl {
		ttl = configured
	}
	s.storeCacheEntry(key, cacheEntry{expiresAt: s.currentTime().Add(ttl), failed: true})
}

func (s *Service) storeCacheEntry(key string, entry cacheEntry) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if len(s.cache) >= cacheEntryLimit {
		now := s.currentTime()
		for existingKey, existing := range s.cache {
			if !existing.expiresAt.After(now) {
				delete(s.cache, existingKey)
			}
		}
		if len(s.cache) >= cacheEntryLimit {
			s.cache = make(map[string]cacheEntry)
		}
	}
	s.cache[key] = entry
}

func (s *Service) currentTime() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now()
}
