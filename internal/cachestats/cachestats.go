// Package cachestats aggregates per-model prompt cache statistics in memory so
// the management API can expose cache hit rates without an external usage sink.
package cachestats

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// retentionHours bounds the in-memory hourly ring per model (one week).
const retentionHours = 168

func init() {
	coreusage.RegisterPlugin(&cacheStatsPlugin{})
}

type cacheStatsPlugin struct{}

func (p *cacheStatsPlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	defaultAggregator.observe(record, time.Now())
}

// Bucket accumulates token and request counters for one model within one hour.
type Bucket struct {
	HourUnix            int64 `json:"hour_unix"`
	Requests            int64 `json:"requests"`
	FailedRequests      int64 `json:"failed_requests"`
	CacheHitRequests    int64 `json:"cache_hit_requests"`
	InputTokens         int64 `json:"input_tokens"`
	UncachedInputTokens int64 `json:"uncached_input_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheWriteTokens    int64 `json:"cache_write_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
}

// ModelStats reports aggregated buckets and derived hit rates for one model.
type ModelStats struct {
	Model            string   `json:"model"`
	Requests         int64    `json:"requests"`
	FailedRequests   int64    `json:"failed_requests"`
	CacheHitRequests int64    `json:"cache_hit_requests"`
	RequestHitRate   float64  `json:"request_hit_rate"`
	InputTokens      int64    `json:"input_tokens"`
	UncachedTokens   int64    `json:"uncached_input_tokens"`
	CacheReadTokens  int64    `json:"cache_read_tokens"`
	CacheWriteTokens int64    `json:"cache_write_tokens"`
	OutputTokens     int64    `json:"output_tokens"`
	TokenHitRate     float64  `json:"token_hit_rate"`
	Hourly           []Bucket `json:"hourly,omitempty"`
}

// Snapshot is the management API payload.
type Snapshot struct {
	WindowHours int          `json:"window_hours"`
	GeneratedAt time.Time    `json:"generated_at"`
	Models      []ModelStats `json:"models"`
}

type aggregator struct {
	mu     sync.Mutex
	models map[string]map[int64]*Bucket
}

var defaultAggregator = &aggregator{models: make(map[string]map[int64]*Bucket)}

func (a *aggregator) observe(record coreusage.Record, now time.Time) {
	model := strings.TrimSpace(record.Model)
	if model == "" {
		model = "unknown"
	}
	at := record.RequestedAt
	if at.IsZero() {
		at = now
	}
	hour := at.UTC().Truncate(time.Hour).Unix()
	detail := coreusage.EnsureTokenBreakdownForProvider(record.Detail, record.Provider, record.ExecutorType)
	breakdown := detail.TokenBreakdown

	a.mu.Lock()
	defer a.mu.Unlock()
	buckets := a.models[model]
	if buckets == nil {
		buckets = make(map[int64]*Bucket)
		a.models[model] = buckets
	}
	bucket := buckets[hour]
	if bucket == nil {
		bucket = &Bucket{HourUnix: hour}
		buckets[hour] = bucket
	}
	bucket.Requests++
	if record.Failed {
		bucket.FailedRequests++
	}
	if breakdown.Input.CacheReadTokens > 0 {
		bucket.CacheHitRequests++
	}
	bucket.InputTokens += breakdown.Input.TotalTokens
	bucket.UncachedInputTokens += breakdown.Input.UncachedTokens
	bucket.CacheReadTokens += breakdown.Input.CacheReadTokens
	bucket.CacheWriteTokens += breakdown.Input.CacheWriteTokens
	bucket.OutputTokens += breakdown.Output.TotalTokens

	cutoff := now.UTC().Truncate(time.Hour).Add(-retentionHours * time.Hour).Unix()
	for h := range buckets {
		if h < cutoff {
			delete(buckets, h)
		}
	}
}

func (a *aggregator) snapshot(windowHours int, includeHourly bool, now time.Time) Snapshot {
	if windowHours <= 0 || windowHours > retentionHours {
		windowHours = retentionHours
	}
	cutoff := now.UTC().Truncate(time.Hour).Add(-time.Duration(windowHours-1) * time.Hour).Unix()

	a.mu.Lock()
	defer a.mu.Unlock()
	models := make([]ModelStats, 0, len(a.models))
	for model, buckets := range a.models {
		stats := ModelStats{Model: model}
		for _, bucket := range buckets {
			if bucket.HourUnix < cutoff {
				continue
			}
			stats.Requests += bucket.Requests
			stats.FailedRequests += bucket.FailedRequests
			stats.CacheHitRequests += bucket.CacheHitRequests
			stats.InputTokens += bucket.InputTokens
			stats.UncachedTokens += bucket.UncachedInputTokens
			stats.CacheReadTokens += bucket.CacheReadTokens
			stats.CacheWriteTokens += bucket.CacheWriteTokens
			stats.OutputTokens += bucket.OutputTokens
			if includeHourly {
				stats.Hourly = append(stats.Hourly, *bucket)
			}
		}
		if stats.Requests == 0 {
			continue
		}
		if stats.Requests > 0 {
			stats.RequestHitRate = float64(stats.CacheHitRequests) / float64(stats.Requests)
		}
		if stats.InputTokens > 0 {
			stats.TokenHitRate = float64(stats.CacheReadTokens) / float64(stats.InputTokens)
		}
		if includeHourly {
			sort.Slice(stats.Hourly, func(i, j int) bool { return stats.Hourly[i].HourUnix < stats.Hourly[j].HourUnix })
		}
		models = append(models, stats)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].Model < models[j].Model })
	return Snapshot{WindowHours: windowHours, GeneratedAt: now.UTC(), Models: models}
}

// GetSnapshot returns aggregated per-model cache statistics for the trailing
// windowHours hours (clamped to the retention window).
func GetSnapshot(windowHours int, includeHourly bool) Snapshot {
	return defaultAggregator.snapshot(windowHours, includeHourly, time.Now())
}
