package pluginhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// quotaFetchCacheTTL bounds how long a successful quota fetch is reused for identical requests.
// Quota views are commonly refreshed by several dashboards or tabs at once; a short window
// collapses those bursts into a single upstream call without noticeably delaying fresh data.
const quotaFetchCacheTTL = 5 * time.Second

// quotaFetchMetadataDigestLimit bounds how many metadata values are digested for a cache key.
// Real credential metadata is far smaller; larger or cyclic values simply bypass debouncing.
const quotaFetchMetadataDigestLimit = 4096

// quotaFetchCache debounces quota fetches per plugin and credential.
// Concurrent identical requests share one in-flight call, and successful results are
// reused until they expire. Errors are never cached. The zero value is ready to use.
type quotaFetchCache struct {
	mu       sync.Mutex
	entries  map[string]quotaFetchCacheEntry
	inflight map[string]*quotaFetchCall
	// nowFunc, set by tests, overrides the wall clock.
	nowFunc func() time.Time
}

type quotaFetchCacheEntry struct {
	resp      pluginapi.QuotaFetchResponse
	expiresAt time.Time
}

// quotaFetchCall is one shared upstream fetch. Its context is canceled once every waiter
// has left, so an abandoned provider call never outlives all of its callers.
type quotaFetchCall struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	resp    pluginapi.QuotaFetchResponse
	handled bool
	err     error
}

func (c *quotaFetchCache) now() time.Time {
	if c.nowFunc != nil {
		return c.nowFunc()
	}
	return time.Now()
}

// fetch returns a cached response for key when still fresh, otherwise joins or starts a shared
// call to load. An empty key disables debouncing for the request.
func (c *quotaFetchCache) fetch(ctx context.Context, key string, load func(context.Context) (pluginapi.QuotaFetchResponse, bool, error)) (pluginapi.QuotaFetchResponse, bool, error) {
	if key == "" {
		return load(ctx)
	}
	// Honour cancellation consistently, whether or not a cached entry exists.
	if errCtx := ctx.Err(); errCtx != nil {
		return pluginapi.QuotaFetchResponse{}, true, errCtx
	}

	// The cache lookup and in-flight registration happen under one lock, and a finished call
	// publishes its result and leaves the in-flight map under the same lock, so a caller can
	// never miss both and start a redundant upstream fetch.
	c.mu.Lock()
	if entry, ok := c.entries[key]; ok {
		if c.now().Before(entry.expiresAt) {
			c.mu.Unlock()
			return cloneQuotaFetchResponse(entry.resp), true, nil
		}
		delete(c.entries, key)
	}
	call := c.inflight[key]
	if call == nil {
		// Detach from this caller's cancellation; the call is canceled when its last waiter leaves.
		callCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		call = &quotaFetchCall{done: make(chan struct{}), cancel: cancel}
		if c.inflight == nil {
			c.inflight = make(map[string]*quotaFetchCall)
		}
		c.inflight[key] = call
		go c.run(callCtx, key, call, load)
	}
	call.waiters++
	c.mu.Unlock()

	select {
	case <-call.done:
		c.mu.Lock()
		call.waiters--
		c.mu.Unlock()
		return cloneQuotaFetchResponse(call.resp), call.handled, call.err
	case <-ctx.Done():
		c.mu.Lock()
		call.waiters--
		if call.waiters == 0 {
			// Nobody is waiting anymore: cancel the provider call and stop new callers joining it.
			call.cancel()
			if c.inflight[key] == call {
				delete(c.inflight, key)
			}
		}
		c.mu.Unlock()
		return pluginapi.QuotaFetchResponse{}, true, ctx.Err()
	}
}

func (c *quotaFetchCache) run(ctx context.Context, key string, call *quotaFetchCall, load func(context.Context) (pluginapi.QuotaFetchResponse, bool, error)) {
	defer call.cancel()
	defer func() {
		if recovered := recover(); recovered != nil {
			call.resp, call.handled, call.err = pluginapi.QuotaFetchResponse{}, false, fmt.Errorf("quota fetch panicked: %v", recovered)
			c.finish(key, call)
		}
	}()
	call.resp, call.handled, call.err = load(ctx)
	c.finish(key, call)
}

func (c *quotaFetchCache) finish(key string, call *quotaFetchCall) {
	c.mu.Lock()
	// A call stays in the in-flight map only while it is the current call for its key. Calls
	// abandoned by all waiters or detached by an invalidation were already removed, so their
	// results are never published and cannot overwrite a newer entry.
	current := c.inflight[key] == call
	if current {
		delete(c.inflight, key)
	}
	// Only successful results from the current call are cached.
	if current && call.err == nil && call.handled {
		if c.entries == nil {
			c.entries = make(map[string]quotaFetchCacheEntry)
		}
		c.pruneExpiredLocked()
		c.entries[key] = quotaFetchCacheEntry{resp: call.resp, expiresAt: c.now().Add(quotaFetchCacheTTL)}
	}
	c.mu.Unlock()
	close(call.done)
}

// invalidate drops all cached results and detaches in-flight fetches from future callers.
func (c *quotaFetchCache) invalidate() {
	c.mu.Lock()
	c.entries = nil
	c.inflight = nil
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

// quotaFetchCacheKey identifies requests that may share a result. It is a digest of the plugin
// identity and every request field forwarded to the provider, each length-prefixed so distinct
// requests never map to the same key. It returns "" to disable debouncing when the request has
// no credential identity or the payload cannot be digested.
func quotaFetchCacheKey(record capabilityRecord, req pluginapi.QuotaFetchRequest) string {
	if strings.TrimSpace(req.AuthIndex) == "" && strings.TrimSpace(req.AuthID) == "" {
		return ""
	}
	digest := sha256.New()
	// Use the provider value exactly as forwarded to the plugin, which may route on it.
	for _, field := range []string{record.id, record.path, record.version, req.Provider, req.AuthIndex, req.AuthID} {
		writeDigestField(digest, []byte(field))
	}
	writeDigestField(digest, req.StorageJSON)
	budget := quotaFetchMetadataDigestLimit
	if !writeDigestValue(digest, req.Metadata, &budget) {
		return ""
	}
	attributeKeys := make([]string, 0, len(req.Attributes))
	for key := range req.Attributes {
		attributeKeys = append(attributeKeys, key)
	}
	sort.Strings(attributeKeys)
	for _, key := range attributeKeys {
		writeDigestField(digest, []byte(key))
		writeDigestField(digest, []byte(req.Attributes[key]))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// writeDigestField length-prefixes each field so adjacent fields cannot be confused.
func writeDigestField(digest hash.Hash, field []byte) {
	_, _ = digest.Write(strconv.AppendInt(nil, int64(len(field)), 10))
	_, _ = digest.Write([]byte{':'})
	_, _ = digest.Write(field)
}

// writeDigestValue feeds a decoded metadata value into digest deterministically, with a type tag
// per value and map keys in sorted order. It deliberately avoids JSON serialization so the key is
// stable regardless of encoder behaviour. It reports false for value types it cannot represent
// unambiguously; the caller then skips debouncing rather than risk two credentials sharing a key.
// budget caps the number of values visited, so self-referential or pathologically shared metadata
// (possible for SDK callers) disables debouncing instead of recursing without bound.
func writeDigestValue(digest hash.Hash, value any, budget *int) bool {
	if *budget <= 0 {
		return false
	}
	*budget--
	switch v := value.(type) {
	case nil:
		writeDigestField(digest, []byte("n"))
	case bool:
		writeDigestField(digest, []byte("b"+strconv.FormatBool(v)))
	case string:
		writeDigestField(digest, []byte("s"))
		writeDigestField(digest, []byte(v))
	case float64:
		writeDigestField(digest, []byte("f"+strconv.FormatUint(math.Float64bits(v), 16)))
	case int:
		writeDigestField(digest, []byte("i"+strconv.FormatInt(int64(v), 10)))
	case int64:
		writeDigestField(digest, []byte("i"+strconv.FormatInt(v, 10)))
	case []any:
		writeDigestField(digest, []byte("a"+strconv.Itoa(len(v))))
		for _, item := range v {
			if !writeDigestValue(digest, item, budget) {
				return false
			}
		}
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		writeDigestField(digest, []byte("m"+strconv.Itoa(len(keys))))
		for _, key := range keys {
			writeDigestField(digest, []byte(key))
			if !writeDigestValue(digest, v[key], budget) {
				return false
			}
		}
	default:
		return false
	}
	return true
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
