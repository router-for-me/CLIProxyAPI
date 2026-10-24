package cliproxy

import (
	"context"
	"crypto/sha256"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	devinModelCacheTTL      = registry.ModelsRefreshInterval
	devinModelRetryBase     = 2 * time.Minute
	devinModelRetryMax      = 30 * time.Minute
	devinModelRefreshScan   = time.Minute
	devinModelFetchParallel = 4
)

var devinNowFunc = time.Now

// devinModelFetcher is implemented by the Devin executor.
type devinModelFetcher interface {
	FetchModels(ctx context.Context, auth *coreauth.Auth) ([]*ModelInfo, error)
}

// devinModelCacheEntry holds the last upstream model list fetched for one Devin auth.
// Entries are tied to a credential fingerprint so a changed key or route refetches.
type devinModelCacheEntry struct {
	credential string
	models     []*ModelInfo
	expiresAt  time.Time
	failures   int
	nextRetry  time.Time
	inflight   bool
}

// devinModelsForAuth returns the account's upstream model list, falling back to the
// Devin catalog until the first fetch succeeds. A background fetch is queued when due.
func (s *Service) devinModelsForAuth(auth *coreauth.Auth) []*ModelInfo {
	if models := s.cachedDevinModels(auth); len(models) > 0 {
		return models
	}
	return registry.GetDevinModels()
}

func (s *Service) cachedDevinModels(auth *coreauth.Auth) []*ModelInfo {
	if s == nil || auth == nil || auth.ID == "" || s.antigravityHomeEnabled() {
		return nil
	}
	credential := devinModelCredential(auth)
	now := devinNowFunc()

	s.devinModelMu.Lock()
	if s.devinModelCache == nil {
		s.devinModelCache = make(map[string]*devinModelCacheEntry)
	}
	entry := s.devinModelCache[auth.ID]
	if entry == nil || entry.credential != credential {
		entry = &devinModelCacheEntry{credential: credential}
		s.devinModelCache[auth.ID] = entry
	}
	models := make([]*ModelInfo, 0, len(entry.models))
	for _, m := range entry.models {
		clone := *m
		models = append(models, &clone)
	}
	due := !entry.inflight && !now.Before(entry.expiresAt) && !now.Before(entry.nextRetry)
	s.devinModelMu.Unlock()

	if due {
		s.queueDevinModelFetch(auth, credential)
	}
	return models
}

func (s *Service) queueDevinModelFetch(auth *coreauth.Auth, credential string) {
	s.cfgMu.RLock()
	lifetime := s.antigravityContext
	s.cfgMu.RUnlock()
	// Fetches are bound to the running service; registrations before Run use the catalog.
	if lifetime == nil || lifetime.Err() != nil {
		return
	}

	s.devinModelMu.Lock()
	entry := s.devinModelCache[auth.ID]
	if entry == nil || entry.credential != credential || entry.inflight {
		s.devinModelMu.Unlock()
		return
	}
	entry.inflight = true
	if s.devinModelSlots == nil {
		s.devinModelSlots = make(chan struct{}, devinModelFetchParallel)
	}
	slots := s.devinModelSlots
	s.devinModelWg.Add(1)
	s.devinModelMu.Unlock()

	go s.fetchDevinModels(lifetime, slots, auth.Clone(), credential)
}

func (s *Service) fetchDevinModels(ctx context.Context, slots chan struct{}, auth *coreauth.Auth, credential string) {
	defer s.devinModelWg.Done()

	var models []*ModelInfo
	var errFetch error
	select {
	case slots <- struct{}{}:
		models, errFetch = s.fetchDevinModelsFromUpstream(ctx, auth)
		<-slots
	case <-ctx.Done():
		errFetch = ctx.Err()
	}

	now := devinNowFunc()
	s.devinModelMu.Lock()
	entry := s.devinModelCache[auth.ID]
	if entry == nil || entry.credential != credential {
		s.devinModelMu.Unlock()
		return
	}
	entry.inflight = false
	if errFetch != nil {
		entry.failures++
		entry.nextRetry = now.Add(devinModelRetryDelay(entry.failures))
		s.devinModelMu.Unlock()
		if ctx.Err() == nil {
			log.WithField("auth_id", auth.ID).Warnf("devin model list fetch failed, keeping current models: %v", errFetch)
		}
		return
	}
	changed := !reflect.DeepEqual(entry.models, models)
	entry.models = models
	entry.expiresAt = now.Add(devinModelCacheTTL)
	entry.failures = 0
	entry.nextRetry = time.Time{}
	s.devinModelMu.Unlock()

	registry.RecordDevinUpstreamModels(models)
	if !changed || ctx.Err() != nil || s.coreManager == nil {
		return
	}
	current, ok := s.coreManager.GetByID(auth.ID)
	if !ok || current == nil || current.Disabled || devinModelCredential(current) != credential {
		return
	}
	log.WithField("auth_id", auth.ID).Infof("devin model list updated from upstream (%d models)", len(models))
	s.refreshModelRegistrationForAuthWithContext(ctx, current, nil)
}

func (s *Service) fetchDevinModelsFromUpstream(ctx context.Context, auth *coreauth.Auth) ([]*ModelInfo, error) {
	if s.coreManager == nil {
		return nil, fmt.Errorf("auth manager unavailable")
	}
	exec, ok := s.coreManager.Executor("devin")
	if !ok || exec == nil {
		return nil, fmt.Errorf("devin executor not registered")
	}
	fetcher, ok := exec.(devinModelFetcher)
	if !ok {
		return nil, fmt.Errorf("devin executor does not support model listing")
	}
	return fetcher.FetchModels(ctx, auth)
}

// runDevinModelRefresh periodically refreshes expired model lists and drops
// cache entries for removed auths.
func (s *Service) runDevinModelRefresh(ctx context.Context) {
	ticker := time.NewTicker(devinModelRefreshScan)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refreshDevinModels()
		}
	}
}

func (s *Service) refreshDevinModels() {
	if s == nil || s.coreManager == nil {
		return
	}
	active := make(map[string]struct{})
	for _, auth := range s.coreManager.List() {
		if auth == nil || auth.Disabled || !strings.EqualFold(strings.TrimSpace(auth.Provider), "devin") {
			continue
		}
		active[auth.ID] = struct{}{}
		if pluginHostHasAuthModelProvider(s.pluginHost, auth.Provider) {
			continue
		}
		// An expired cache must be republished through registration so exclusions,
		// aliases and prefixes are applied; the fetch completion does that.
		s.cachedDevinModels(auth)
	}
	s.devinModelMu.Lock()
	for id, entry := range s.devinModelCache {
		if _, ok := active[id]; !ok && !entry.inflight {
			delete(s.devinModelCache, id)
		}
	}
	s.devinModelMu.Unlock()
}

func devinModelRetryDelay(failures int) time.Duration {
	delay := devinModelRetryBase
	for i := 1; i < failures && delay < devinModelRetryMax; i++ {
		delay *= 2
	}
	return min(delay, devinModelRetryMax)
}

// devinModelCredential fingerprints the inputs that determine an account's upstream model list.
func devinModelCredential(auth *coreauth.Auth) string {
	h := sha256.New()
	for _, key := range []string{"api_key", "session_token", "token", "base_url"} {
		value := ""
		if auth.Attributes != nil {
			value = strings.TrimSpace(auth.Attributes[key])
		}
		if value == "" && auth.Metadata != nil {
			if v, ok := auth.Metadata[key].(string); ok {
				value = strings.TrimSpace(v)
			}
		}
		_, _ = fmt.Fprintf(h, "%s=%q;", key, value)
	}
	_, _ = fmt.Fprintf(h, "proxy=%q", strings.TrimSpace(auth.ProxyURL))
	return fmt.Sprintf("%x", h.Sum(nil))
}
