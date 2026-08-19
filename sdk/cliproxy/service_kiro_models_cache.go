package cliproxy

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

const (
	kiroModelsCacheTTL       = 30 * time.Minute
	kiroModelsRetryCooldown  = 30 * time.Second
	kiroModelsEntryRetention = 24 * time.Hour
)

var errKiroModelsEmpty = errors.New("kiro API returned an empty model catalog")

type kiroModelsCache struct {
	mu      sync.Mutex
	entries map[string]*kiroModelsCacheEntry
	now     func() time.Time
}

type kiroModelsCacheEntry struct {
	models     []*ModelInfo
	expiresAt  time.Time
	retryAfter time.Time
	lastUsed   time.Time
	flight     *kiroModelsFlight
}

type kiroModelsFlight struct {
	done    chan struct{}
	models  []*ModelInfo
	err     error
	waiters int
}

func kiroModelsCacheKey(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	authID := strings.TrimSpace(auth.ID)
	if authID == "" {
		return ""
	}

	// Auth ID keeps catalogs isolated between credential records. Include the full
	// profile ARN so replacing an auth file with a different account or region cannot
	// reuse the previous record's catalog. The auth ID remains part of the key, so this
	// does not share catalogs between credentials that happen to use the same profile.
	if profileARN := authString(auth, "profile_arn"); profileARN != "" {
		return authID + "\x00profile_arn=" + profileARN
	}

	// Builder ID and some social credentials have no profile ARN. Use the available
	// non-secret account identity fields to avoid stale reuse when the same file path
	// is rebound, while retaining auth-ID isolation when those fields are unavailable.
	parts := []string{authID}
	for _, field := range []string{"email", "start_url", "region", "client_id", "auth_method", "provider"} {
		if value := authString(auth, field); value != "" {
			parts = append(parts, field+"="+value)
		}
	}
	return strings.Join(parts, "\x00")
}

func (c *kiroModelsCache) get(
	ctx context.Context,
	key string,
	fetch func(context.Context) ([]*ModelInfo, error),
) ([]*ModelInfo, error) {
	if c == nil || strings.TrimSpace(key) == "" || fetch == nil {
		return nil, errors.New("invalid Kiro model cache request")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	now := c.currentTime()
	c.mu.Lock()
	if c.entries == nil {
		c.entries = make(map[string]*kiroModelsCacheEntry)
	}
	c.pruneLocked(now)
	entry := c.entries[key]
	if entry == nil {
		entry = &kiroModelsCacheEntry{}
		c.entries[key] = entry
	}
	entry.lastUsed = now
	if len(entry.models) > 0 && now.Before(entry.expiresAt) {
		models := cloneKiroModelInfos(entry.models)
		c.mu.Unlock()
		return models, nil
	}
	if entry.flight != nil {
		flight := entry.flight
		flight.waiters++
		c.mu.Unlock()
		return c.waitForFlight(ctx, entry, flight)
	}
	if now.Before(entry.retryAfter) {
		models := cloneKiroModelInfos(entry.models)
		c.mu.Unlock()
		return models, nil
	}

	flight := &kiroModelsFlight{done: make(chan struct{}), waiters: 1}
	entry.flight = flight
	c.mu.Unlock()

	// The bounded upstream request belongs to the cache key, not whichever waiter won
	// the race to start it. Preserve context values but detach cancellation so one
	// canceled registration cannot poison live waiters or the retry cooldown.
	go c.runFetch(key, entry, flight, context.WithoutCancel(ctx), fetch)
	return c.waitForFlight(ctx, entry, flight)
}

func (c *kiroModelsCache) waitForFlight(
	ctx context.Context,
	entry *kiroModelsCacheEntry,
	flight *kiroModelsFlight,
) ([]*ModelInfo, error) {
	select {
	case <-ctx.Done():
		c.mu.Lock()
		models := cloneKiroModelInfos(entry.models)
		flight.waiters--
		c.mu.Unlock()
		return models, ctx.Err()
	case <-flight.done:
		c.mu.Lock()
		flight.waiters--
		c.mu.Unlock()
		return cloneKiroModelInfos(flight.models), flight.err
	}
}

func (c *kiroModelsCache) runFetch(
	key string,
	entry *kiroModelsCacheEntry,
	flight *kiroModelsFlight,
	ctx context.Context,
	fetch func(context.Context) ([]*ModelInfo, error),
) {
	models, err := fetch(ctx)
	if err == nil && len(models) == 0 {
		err = errKiroModelsEmpty
	}

	c.mu.Lock()
	if c.entries[key] != entry || entry.flight != flight {
		flight.err = errors.New("kiro model cache flight was invalidated")
		close(flight.done)
		c.mu.Unlock()
		return
	}
	completedAt := c.currentTime()
	if err == nil {
		entry.models = cloneKiroModelInfos(models)
		entry.expiresAt = completedAt.Add(kiroModelsCacheTTL)
		entry.retryAfter = time.Time{}
	} else {
		entry.retryAfter = completedAt.Add(kiroModelsRetryCooldown)
	}
	flight.models = cloneKiroModelInfos(entry.models)
	flight.err = err
	entry.flight = nil
	close(flight.done)
	c.mu.Unlock()
}

func (c *kiroModelsCache) pruneLocked(now time.Time) {
	for key, entry := range c.entries {
		if entry == nil || entry.flight != nil || entry.lastUsed.IsZero() {
			continue
		}
		if now.Sub(entry.lastUsed) > kiroModelsEntryRetention {
			delete(c.entries, key)
		}
	}
}

func (c *kiroModelsCache) currentTime() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func cloneKiroModelInfos(models []*ModelInfo) []*ModelInfo {
	if len(models) == 0 {
		return nil
	}
	cloned := make([]*ModelInfo, len(models))
	for i, model := range models {
		cloned[i] = cloneKiroModelInfo(model)
	}
	return cloned
}

func cloneKiroModelInfo(model *ModelInfo) *ModelInfo {
	if model == nil {
		return nil
	}
	cloned := *model
	cloned.SupportedGenerationMethods = append([]string(nil), model.SupportedGenerationMethods...)
	cloned.SupportedParameters = append([]string(nil), model.SupportedParameters...)
	cloned.SupportedEndpoints = append([]string(nil), model.SupportedEndpoints...)
	cloned.SupportedInputModalities = append([]string(nil), model.SupportedInputModalities...)
	cloned.SupportedOutputModalities = append([]string(nil), model.SupportedOutputModalities...)
	if model.Thinking != nil {
		thinking := *model.Thinking
		thinking.Levels = append([]string(nil), model.Thinking.Levels...)
		cloned.Thinking = &thinking
	}
	if model.Config != nil {
		modelConfig := *model.Config
		if model.Config.OverrideHeader != nil {
			modelConfig.OverrideHeader = make(map[string]string, len(model.Config.OverrideHeader))
			for key, value := range model.Config.OverrideHeader {
				modelConfig.OverrideHeader[key] = value
			}
		}
		cloned.Config = &modelConfig
	}
	return &cloned
}
