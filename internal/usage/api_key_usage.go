package usage

import (
	"context"
	"strings"
	"sync"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// apiKeyUsageBucketWindow is the width of one rolling-window slot. The
// frontend chart at static/management.html reads exactly this width
// (`ip = 600 * 1e3` ms) — keep them in sync.
const apiKeyUsageBucketWindow = 10 * time.Minute

// apiKeyUsageBucketCount is the maximum number of slots we retain per
// (provider, baseURL, apiKey) tuple — matches the frontend `rp = 20`.
const apiKeyUsageBucketCount = 20

// APIKeyUsagePlugin tracks per-(provider, baseURL, apiKey) success/failure
// counts and the last 20 ten-minute slots. It produces the JSON shape that
// the management UI's "AI Providers" cards consume via GET /api-key-usage.
//
// This is a separate aggregator from RequestStatistics (which keys by API
// key alone) so the two views can evolve independently.
type APIKeyUsagePlugin struct {
	mu      sync.RWMutex
	entries map[string]*apiKeyUsageEntry // key = provider|baseURL|apiKey
}

type apiKeyUsageEntry struct {
	provider string
	baseURL  string
	apiKey   string
	success  int64
	failed   int64
	buckets  []apiKeyUsageBucket // ring; oldest first
}

type apiKeyUsageBucket struct {
	start   time.Time
	success int64
	failed  int64
}

var defaultAPIKeyUsage = NewAPIKeyUsagePlugin()

// GetAPIKeyUsagePlugin returns the package-level plugin instance.
func GetAPIKeyUsagePlugin() *APIKeyUsagePlugin { return defaultAPIKeyUsage }

// NewAPIKeyUsagePlugin constructs an empty plugin.
func NewAPIKeyUsagePlugin() *APIKeyUsagePlugin {
	return &APIKeyUsagePlugin{entries: make(map[string]*apiKeyUsageEntry)}
}

// HandleUsage implements coreusage.Plugin. It is invoked by the usage
// dispatcher for every record published by an executor.
func (p *APIKeyUsagePlugin) HandleUsage(_ context.Context, record coreusage.Record) {
	if p == nil {
		return
	}
	if !statisticsEnabled.Load() {
		return
	}
	provider := strings.ToLower(strings.TrimSpace(record.Provider))
	apiKey := strings.TrimSpace(record.APIKey)
	if provider == "" || apiKey == "" {
		// Without a stable (provider, apiKey) tuple the frontend has no row
		// to attach the count to — skip silently. RequestStatistics still
		// records the request via its fallback identifier.
		return
	}
	baseURL := strings.TrimSpace(record.BaseURL)
	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}

	key := apiKeyUsageKey(provider, baseURL, apiKey)

	p.mu.Lock()
	defer p.mu.Unlock()

	entry, ok := p.entries[key]
	if !ok {
		entry = &apiKeyUsageEntry{
			provider: provider,
			baseURL:  baseURL,
			apiKey:   apiKey,
		}
		p.entries[key] = entry
	}

	if record.Failed {
		entry.failed++
	} else {
		entry.success++
	}

	bucketStart := timestamp.Truncate(apiKeyUsageBucketWindow)
	if n := len(entry.buckets); n > 0 && entry.buckets[n-1].start.Equal(bucketStart) {
		if record.Failed {
			entry.buckets[n-1].failed++
		} else {
			entry.buckets[n-1].success++
		}
		return
	}

	entry.buckets = append(entry.buckets, apiKeyUsageBucket{
		start:   bucketStart,
		success: boolToCount(!record.Failed),
		failed:  boolToCount(record.Failed),
	})
	if len(entry.buckets) > apiKeyUsageBucketCount {
		entry.buckets = entry.buckets[len(entry.buckets)-apiKeyUsageBucketCount:]
	}
}

func boolToCount(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// APIKeyUsageBucketSnapshot is the JSON shape consumed by the frontend
// for each entry in `recent_requests`. The `Time` field is RFC3339 so the
// browser can parse it back into a Date for the chart axis labels.
type APIKeyUsageBucketSnapshot struct {
	Time    string `json:"time"`
	Success int64  `json:"success"`
	Failed  int64  `json:"failed"`
}

// APIKeyUsageEntrySnapshot is one (provider, baseURL, apiKey) row.
type APIKeyUsageEntrySnapshot struct {
	Success        int64                       `json:"success"`
	Failed         int64                       `json:"failed"`
	RecentRequests []APIKeyUsageBucketSnapshot `json:"recent_requests"`
}

// Snapshot returns a {provider: {baseURL|apiKey: entry}} nested map ready
// to be JSON-encoded for GET /v0/management/api-key-usage. The composite
// key matches the frontend's `sp(baseUrl, apiKey)` => "<baseURL>|<apiKey>".
func (p *APIKeyUsagePlugin) Snapshot() map[string]map[string]APIKeyUsageEntrySnapshot {
	result := make(map[string]map[string]APIKeyUsageEntrySnapshot)
	if p == nil {
		return result
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, entry := range p.entries {
		bucket, ok := result[entry.provider]
		if !ok {
			bucket = make(map[string]APIKeyUsageEntrySnapshot)
			result[entry.provider] = bucket
		}
		bucket[apiKeyUsageCompositeKey(entry.baseURL, entry.apiKey)] = APIKeyUsageEntrySnapshot{
			Success:        entry.success,
			Failed:         entry.failed,
			RecentRequests: snapshotBuckets(entry.buckets),
		}
	}
	return result
}

// Reset clears all aggregated data. Exposed for tests and for a future
// "clear stats" management endpoint.
func (p *APIKeyUsagePlugin) Reset() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.entries = make(map[string]*apiKeyUsageEntry)
}

func snapshotBuckets(buckets []apiKeyUsageBucket) []APIKeyUsageBucketSnapshot {
	out := make([]APIKeyUsageBucketSnapshot, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, APIKeyUsageBucketSnapshot{
			Time:    b.start.UTC().Format(time.RFC3339),
			Success: b.success,
			Failed:  b.failed,
		})
	}
	return out
}

func apiKeyUsageKey(provider, baseURL, apiKey string) string {
	return provider + "|" + baseURL + "|" + apiKey
}

func apiKeyUsageCompositeKey(baseURL, apiKey string) string {
	return baseURL + "|" + apiKey
}
