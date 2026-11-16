// Package usagestats is the proxx in-process usage/spend aggregator. It consumes usage
// records as a usage plugin, keeps daily totals per model × provider × client API key plus a
// ring buffer of recent requests, and snapshots them to a JSON file.
// See docs/proxx/decisions/0005-usage-and-panel.md.
package usagestats

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// DefaultRecentLimit is the default size of the recent-request ring buffer.
const DefaultRecentLimit = 500

const dayLayout = "2006-01-02"

// Totals are aggregated counters. Token semantics: input_tokens is the full prompt
// (uncached + cache read + cache write), cached_tokens is cache reads, output_tokens
// includes reasoning, reasoning_tokens is the reasoning share of output.
type Totals struct {
	Requests            int64   `json:"requests"`
	Failures            int64   `json:"failures"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CachedTokens        int64   `json:"cached_tokens"`
	ReasoningTokens     int64   `json:"reasoning_tokens"`
	CostUSD             float64 `json:"cost_usd"`
	UnknownCostRequests int64   `json:"unknown_cost_requests"`
}

func (t *Totals) add(o Totals) {
	t.Requests += o.Requests
	t.Failures += o.Failures
	t.InputTokens += o.InputTokens
	t.OutputTokens += o.OutputTokens
	t.CachedTokens += o.CachedTokens
	t.ReasoningTokens += o.ReasoningTokens
	t.CostUSD += o.CostUSD
	t.UnknownCostRequests += o.UnknownCostRequests
}

// Bucket is one day × model × provider × API key aggregate.
type Bucket struct {
	Day      string `json:"day"`
	Model    string `json:"model"`
	Provider string `json:"provider"`
	APIKey   string `json:"api_key"`
	Totals
}

type bucketKey struct {
	day, model, provider, apiKey string
}

// RecentItem is one request in the recent ring buffer.
type RecentItem struct {
	Timestamp       time.Time `json:"timestamp"`
	Model           string    `json:"model"`
	Alias           string    `json:"alias"`
	Provider        string    `json:"provider"`
	APIKey          string    `json:"api_key"`
	Failed          bool      `json:"failed"`
	StatusCode      int       `json:"status_code"`
	LatencyMS       int64     `json:"latency_ms"`
	Stream          bool      `json:"stream"`
	InputTokens     int64     `json:"input_tokens"`
	OutputTokens    int64     `json:"output_tokens"`
	CachedTokens    int64     `json:"cached_tokens"`
	ReasoningTokens int64     `json:"reasoning_tokens"`
	CostUSD         *float64  `json:"cost_usd"`
	CostSource      string    `json:"cost_source"`
}

// Store aggregates usage records. It is safe for concurrent use.
type Store struct {
	mu          sync.Mutex
	enabled     bool
	buckets     map[bucketKey]*Totals
	recent      []RecentItem // ring buffer, oldest at head when full
	head        int
	recentLimit int
	dirty       bool
	now         func() time.Time
}

// NewStore returns an enabled store with the given recent limit (<=0 uses the default).
func NewStore(recentLimit int) *Store {
	s := &Store{enabled: true, buckets: map[bucketKey]*Totals{}, now: time.Now}
	s.setRecentLimitLocked(recentLimit)
	return s
}

// SetClock replaces the store clock (tests).
func (s *Store) SetClock(now func() time.Time) {
	s.mu.Lock()
	s.now = now
	s.mu.Unlock()
}

// SetEnabled toggles aggregation; disabled stores ignore new records but keep their data.
func (s *Store) SetEnabled(enabled bool) {
	s.mu.Lock()
	s.enabled = enabled
	s.mu.Unlock()
}

// SetRecentLimit resizes the recent ring buffer, keeping the newest items.
func (s *Store) SetRecentLimit(limit int) {
	s.mu.Lock()
	s.setRecentLimitLocked(limit)
	s.mu.Unlock()
}

func (s *Store) setRecentLimitLocked(limit int) {
	if limit <= 0 {
		limit = DefaultRecentLimit
	}
	if limit == s.recentLimit {
		return
	}
	items := s.recentLocked()
	if len(items) > limit {
		items = items[len(items)-limit:]
	}
	s.recent = items
	s.head = 0
	s.recentLimit = limit
}

// recentLocked returns recent items oldest → newest.
func (s *Store) recentLocked() []RecentItem {
	out := make([]RecentItem, 0, len(s.recent))
	if len(s.recent) < s.recentLimit || s.head == 0 {
		return append(out, s.recent...)
	}
	out = append(out, s.recent[s.head:]...)
	return append(out, s.recent[:s.head]...)
}

// HandleUsage implements usage.Plugin. It only touches memory; file I/O happens elsewhere.
func (s *Store) HandleUsage(_ context.Context, record usage.Record) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return
	}
	ts := record.RequestedAt
	if ts.IsZero() {
		ts = s.now()
	}
	ts = ts.UTC()
	in, out, cached, reasoning := tokenCounts(record.Detail)
	var cost *float64
	source := ""
	if cd := record.Detail.CostDetails; cd != nil {
		if cd.FinalCost != nil {
			v := *cd.FinalCost
			cost = &v
		}
		source = cd.CostSource
	}
	if source == "" {
		source = "unknown"
	}
	apiKey := MaskAPIKey(record.APIKey)
	delta := Totals{Requests: 1, InputTokens: in, OutputTokens: out, CachedTokens: cached, ReasoningTokens: reasoning}
	if record.Failed {
		delta.Failures = 1
	}
	if cost != nil {
		rounded := roundCost(*cost)
		cost = &rounded
		delta.CostUSD = *cost
	} else if in+out > 0 {
		delta.UnknownCostRequests = 1
	}
	key := bucketKey{day: ts.Format(dayLayout), model: record.Model, provider: record.Provider, apiKey: apiKey}
	t := s.buckets[key]
	if t == nil {
		t = &Totals{}
		s.buckets[key] = t
	}
	t.add(delta)

	status := 200
	if record.Failed {
		status = record.Fail.StatusCode
	}
	item := RecentItem{
		Timestamp: ts, Model: record.Model, Alias: record.Alias, Provider: record.Provider, APIKey: apiKey,
		Failed: record.Failed, StatusCode: status, LatencyMS: record.Latency.Milliseconds(), Stream: record.Stream,
		InputTokens: in, OutputTokens: out, CachedTokens: cached, ReasoningTokens: reasoning,
		CostUSD: cost, CostSource: source,
	}
	if len(s.recent) < s.recentLimit {
		s.recent = append(s.recent, item)
	} else {
		s.recent[s.head] = item
		s.head = (s.head + 1) % s.recentLimit
	}
	s.dirty = true
}

// TokenCounts returns input (incl. cache reads/writes), output (incl. reasoning), cache-read and
// reasoning token counts from a usage detail, preferring the canonical token breakdown.
func TokenCounts(d usage.Detail) (in, out, cached, reasoning int64) {
	return tokenCounts(d)
}

func tokenCounts(d usage.Detail) (in, out, cached, reasoning int64) {
	b := d.TokenBreakdown
	if b.Valid() && b.Quality == usage.TokenAccountingQualityComplete {
		return b.Input.TotalTokens, b.Output.TotalTokens, b.Input.CacheReadTokens, b.Output.ReasoningTokens
	}
	cached = d.CacheReadTokens
	if cached == 0 {
		cached = d.CachedTokens
	}
	return d.InputTokens, d.OutputTokens, cached, d.ReasoningTokens
}

// MaskAPIKey masks a client API key and appends a short fingerprint so two keys with the
// same visible mask stay separate. The raw key is never stored.
func MaskAPIKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	masked := "***"
	if len(key) > 8 {
		masked = util.HideAPIKey(key)
	}
	sum := sha256.Sum256([]byte(key))
	return masked + "#" + hex.EncodeToString(sum[:3])
}

// Reset clears all totals and recent items.
func (s *Store) Reset() {
	s.mu.Lock()
	s.buckets = map[bucketKey]*Totals{}
	s.recent = nil
	s.head = 0
	s.dirty = true
	s.mu.Unlock()
}

// Row is one grouped summary row.
type Row struct {
	Key string `json:"key"`
	Totals
}

// Summary is the grouped report for an inclusive UTC day range.
type Summary struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Totals Totals `json:"totals"`
	Rows   []Row  `json:"rows"`
}

// GroupBy values accepted by Summary.
var GroupBy = []string{"model", "provider", "api_key", "day"}

// Summary aggregates buckets with from <= day <= to (YYYY-MM-DD) grouped by groupBy.
func (s *Store) Summary(from, to, groupBy string) Summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	grouped := map[string]*Totals{}
	out := Summary{From: from, To: to, Rows: []Row{}}
	for k, t := range s.buckets {
		if k.day < from || k.day > to {
			continue
		}
		var key string
		switch groupBy {
		case "provider":
			key = k.provider
		case "api_key":
			key = k.apiKey
		case "day":
			key = k.day
		default:
			key = k.model
		}
		g := grouped[key]
		if g == nil {
			g = &Totals{}
			grouped[key] = g
		}
		g.add(*t)
		out.Totals.add(*t)
	}
	for key, t := range grouped {
		t.CostUSD = roundCost(t.CostUSD)
		out.Rows = append(out.Rows, Row{Key: key, Totals: *t})
	}
	// Bucket map order is random, so float sums differ in the last bits between calls;
	// round to a fixed precision so reports are stable.
	out.Totals.CostUSD = roundCost(out.Totals.CostUSD)
	sort.Slice(out.Rows, func(i, j int) bool {
		a, b := out.Rows[i], out.Rows[j]
		if groupBy == "day" {
			return a.Key < b.Key
		}
		if a.CostUSD != b.CostUSD {
			return a.CostUSD > b.CostUSD
		}
		if a.Requests != b.Requests {
			return a.Requests > b.Requests
		}
		return a.Key < b.Key
	})
	return out
}

// Recent returns up to limit recent items, newest first.
func (s *Store) Recent(limit int) []RecentItem {
	s.mu.Lock()
	items := s.recentLocked()
	s.mu.Unlock()
	if limit <= 0 || limit > len(items) {
		limit = len(items)
	}
	out := make([]RecentItem, 0, limit)
	for i := len(items) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, items[i])
	}
	return out
}

// Today returns the store clock's current UTC day.
func (s *Store) Today() time.Time {
	s.mu.Lock()
	now := s.now
	s.mu.Unlock()
	return now().UTC()
}

// roundCost rounds a USD amount to 1e-10 (well below any real per-request cost).
func roundCost(v float64) float64 {
	return math.Round(v*1e10) / 1e10
}
