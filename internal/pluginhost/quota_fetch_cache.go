package pluginhost

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"golang.org/x/sync/singleflight"
)

// quotaFetchCacheTTL bounds how long a successful quota fetch is reused for identical requests.
// Quota views are commonly refreshed by several dashboards or tabs at once; a short window
// collapses those bursts into a single upstream call without noticeably delaying fresh data.
const quotaFetchCacheTTL = 5 * time.Second

// quotaFetchCache debounces quota fetches per plugin and credential.
// Concurrent identical requests share one in-flight call, and successful results are
// reused until they expire. Errors are never cached. The zero value is ready to use.
type quotaFetchCache struct {
	mu         sync.Mutex
	entries    map[string]quotaFetchCacheEntry
	generation uint64
	group      singleflight.Group
	// nowFunc, set by tests, overrides the wall clock.
	nowFunc func() time.Time
}

type quotaFetchCacheEntry struct {
	resp      pluginapi.QuotaFetchResponse
	expiresAt time.Time
}

func (c *quotaFetchCache) now() time.Time {
	if c.nowFunc != nil {
		return c.nowFunc()
	}
	return time.Now()
}

// fetch returns a cached response for key when still fresh, otherwise runs load once for all
// concurrent callers sharing the key. Each caller still honours its own context cancellation.
func (c *quotaFetchCache) fetch(ctx context.Context, key string, load func(context.Context) (pluginapi.QuotaFetchResponse, bool, error)) (pluginapi.QuotaFetchResponse, bool, error) {
	c.mu.Lock()
	if entry, ok := c.entries[key]; ok {
		if c.now().Before(entry.expiresAt) {
			c.mu.Unlock()
			return cloneQuotaFetchResponse(entry.resp), true, nil
		}
		delete(c.entries, key)
	}
	generation := c.generation
	c.mu.Unlock()

	type result struct {
		resp    pluginapi.QuotaFetchResponse
		handled bool
	}
	// Include the generation so callers arriving after an invalidation never join a stale in-flight fetch.
	flightKey := strconv.FormatUint(generation, 10) + "|" + key
	// Detach cancellation so one caller disconnecting does not fail the others sharing the call.
	loadCtx := context.WithoutCancel(ctx)
	ch := c.group.DoChan(flightKey, func() (any, error) {
		resp, handled, errLoad := load(loadCtx)
		if errLoad == nil && handled {
			c.mu.Lock()
			if c.generation == generation {
				if c.entries == nil {
					c.entries = make(map[string]quotaFetchCacheEntry)
				}
				c.pruneExpiredLocked()
				c.entries[key] = quotaFetchCacheEntry{resp: resp, expiresAt: c.now().Add(quotaFetchCacheTTL)}
			}
			c.mu.Unlock()
		}
		return result{resp: resp, handled: handled}, errLoad
	})

	select {
	case <-ctx.Done():
		return pluginapi.QuotaFetchResponse{}, true, ctx.Err()
	case res := <-ch:
		out, _ := res.Val.(result)
		return cloneQuotaFetchResponse(out.resp), out.handled, res.Err
	}
}

// invalidate drops all cached results and detaches in-flight fetches from future callers.
func (c *quotaFetchCache) invalidate() {
	c.mu.Lock()
	c.generation++
	c.entries = nil
	c.mu.Unlock()
}

func (c *quotaFetchCache) pruneExpiredLocked() {
	now := c.now()
	for key, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			delete(c.entries, key)
		}
	}
}

func quotaFetchCacheKey(record capabilityRecord, req pluginapi.QuotaFetchRequest) string {
	return strings.Join([]string{
		record.id,
		record.version,
		normalizeProviderID(req.Provider),
		req.AuthIndex,
		req.AuthID,
	}, "\x00")
}

// cloneQuotaFetchResponse deep-copies a response so callers cannot mutate shared cached data.
func cloneQuotaFetchResponse(resp pluginapi.QuotaFetchResponse) pluginapi.QuotaFetchResponse {
	out := resp
	if resp.Subscription != nil {
		subscription := *resp.Subscription
		out.Subscription = &subscription
	}
	if resp.Summary != nil {
		out.Summary = append([]pluginapi.QuotaMetric(nil), resp.Summary...)
	}
	if resp.Groups != nil {
		out.Groups = make([]pluginapi.QuotaGroup, len(resp.Groups))
		for i, group := range resp.Groups {
			out.Groups[i] = group
			if group.Buckets != nil {
				out.Groups[i].Buckets = append([]pluginapi.QuotaBucket(nil), group.Buckets...)
			}
		}
	}
	return out
}
