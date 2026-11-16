package apicost

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

const (
	bucketWidth = time.Hour
	// DefaultRetention bounds how long hourly cost buckets are kept.
	DefaultRetention = 90 * 24 * time.Hour
	// maxBuckets caps memory regardless of model/credential cardinality.
	maxBuckets = 200_000
)

func init() {
	coreusage.RegisterPlugin(defaultStore)
}

// Bucket is the estimated cost and token usage of one model on one
// credential within one clock hour. Input buckets are mutually exclusive.
type Bucket struct {
	Hour             time.Time `json:"hour"`
	Model            string    `json:"model"`
	Provider         string    `json:"provider,omitempty"`
	AuthID           string    `json:"auth_id,omitempty"`
	Requests         int64     `json:"requests"`
	UnpricedRequests int64     `json:"unpriced_requests,omitempty"`
	InputTokens      int64     `json:"input_tokens"`
	CacheReadTokens  int64     `json:"cache_read_tokens"`
	CacheWriteTokens int64     `json:"cache_write_tokens"`
	OutputTokens     int64     `json:"output_tokens"`
	CostUSD          float64   `json:"cost_usd"`
}

type bucketKey struct {
	hour     int64
	model    string
	provider string
	authID   string
}

// Store aggregates estimated costs per hour, model and credential.
type Store struct {
	mu        sync.Mutex
	buckets   map[bucketKey]*Bucket
	retention time.Duration
	dirty     bool
	catalog   func() *Catalog
	now       func() time.Time
}

var defaultStore = NewStore()

// Default returns the process-wide store fed by the usage manager.
func Default() *Store { return defaultStore }

// NewStore returns an empty store priced by the active catalog.
func NewStore() *Store {
	return &Store{buckets: make(map[bucketKey]*Bucket), retention: DefaultRetention, catalog: CurrentCatalog, now: time.Now}
}

// HandleUsage implements coreusage.Plugin. It never mutates the record.
func (s *Store) HandleUsage(_ context.Context, record coreusage.Record) {
	if s == nil || !coreusage.GenerateEnabled(record.Generate) {
		return
	}
	detail := coreusage.EnsureTokenBreakdownForProvider(record.Detail, record.Provider, record.ExecutorType)
	breakdown := detail.TokenBreakdown
	if !breakdown.Valid() || breakdown.TotalTokens == 0 {
		return
	}
	usage := Usage{
		Input:         breakdown.Input.UncachedTokens,
		CacheRead:     breakdown.Input.CacheReadTokens,
		CacheWrite:    breakdown.Input.CacheWriteTokens,
		CacheWrite1h:  derefInt64(detail.CacheCreation1hTokens),
		CacheWrite12h: derefInt64(detail.CacheCreation12hTokens),
		Output:        breakdown.Output.TotalTokens,
	}
	model, price, priced := s.resolve(record)
	at := record.RequestedAt
	if at.IsZero() {
		at = s.now()
	}
	key := bucketKey{
		hour:     at.UTC().Truncate(bucketWidth).Unix(),
		model:    model,
		provider: strings.ToLower(strings.TrimSpace(record.Provider)),
		authID:   strings.TrimSpace(record.AuthID),
	}
	var cost float64
	if priced {
		cost = Cost(price, usage, requestTier(record, detail))
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.buckets[key]
	if entry == nil {
		if len(s.buckets) >= maxBuckets {
			// Expire old buckets here too: the routing-state persister that
			// normally prunes does not run on every node (e.g. Home-enabled).
			s.pruneLocked(s.now())
			if len(s.buckets) >= maxBuckets {
				return
			}
		}
		entry = &Bucket{Hour: time.Unix(key.hour, 0).UTC(), Model: key.model, Provider: key.provider, AuthID: key.authID}
		s.buckets[key] = entry
	}
	entry.Requests++
	if !priced {
		entry.UnpricedRequests++
	}
	entry.InputTokens += usage.Input
	entry.CacheReadTokens += usage.CacheRead
	entry.CacheWriteTokens += usage.CacheWrite
	entry.OutputTokens += usage.Output
	entry.CostUSD += cost
	s.dirty = true
}

// resolve picks the billed model name: the upstream-reported model when it
// is priced, otherwise the requested one.
func (s *Store) resolve(record coreusage.Record) (string, Price, bool) {
	catalog := s.catalog()
	for _, candidate := range []string{record.ResponseModel, record.Model} {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		if price, name, ok := catalog.Lookup(candidate); ok {
			return name, price, true
		}
	}
	model := strings.TrimSpace(record.Model)
	if model == "" {
		model = strings.TrimSpace(record.ResponseModel)
	}
	if model == "" {
		model = "unknown"
	}
	return strings.ToLower(model), Price{}, false
}

func requestTier(record coreusage.Record, detail coreusage.Detail) Tier {
	if strings.EqualFold(strings.TrimSpace(detail.Speed), "fast") {
		return TierFast
	}
	tier := record.ResponseServiceTier
	if tier == "" {
		tier = detail.ResponseServiceTier
	}
	if tier == "" {
		tier = record.EffectiveServiceTier
	}
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "priority", "fast":
		return TierFast
	case "flex":
		return TierFlex
	}
	return TierStandard
}

// Buckets returns copies of the buckets whose hour is at or after since,
// ordered by hour, model and credential.
func (s *Store) Buckets(since time.Time) []Bucket {
	if s == nil {
		return nil
	}
	cutoff := since.UTC().Truncate(bucketWidth).Unix()
	s.mu.Lock()
	out := make([]Bucket, 0, len(s.buckets))
	for key, entry := range s.buckets {
		if key.hour >= cutoff {
			out = append(out, *entry)
		}
	}
	s.mu.Unlock()
	sortBuckets(out)
	return out
}

// Prune drops buckets older than the retention window.
func (s *Store) Prune(now time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
}

func (s *Store) pruneLocked(now time.Time) {
	cutoff := now.Add(-s.retention).Unix()
	for key := range s.buckets {
		if key.hour < cutoff {
			delete(s.buckets, key)
			s.dirty = true
		}
	}
}

// Export returns every bucket and clears the dirty flag.
func (s *Store) Export() []Bucket {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	out := make([]Bucket, 0, len(s.buckets))
	for _, entry := range s.buckets {
		out = append(out, *entry)
	}
	s.dirty = false
	s.mu.Unlock()
	sortBuckets(out)
	return out
}

// Dirty reports whether buckets changed since the last Export.
func (s *Store) Dirty() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dirty
}

// Import adds persisted buckets to the live aggregate. Usage recorded after
// startup for the same hour, model and credential is summed with it.
func (s *Store) Import(buckets []Bucket) {
	if s == nil || len(buckets) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, bucket := range buckets {
		if bucket.Hour.IsZero() || bucket.Model == "" {
			continue
		}
		key := bucketKey{hour: bucket.Hour.UTC().Truncate(bucketWidth).Unix(), model: bucket.Model, provider: bucket.Provider, authID: bucket.AuthID}
		entry := s.buckets[key]
		if entry == nil {
			if len(s.buckets) >= maxBuckets {
				continue
			}
			copied := bucket
			copied.Hour = time.Unix(key.hour, 0).UTC()
			s.buckets[key] = &copied
			continue
		}
		entry.Requests += bucket.Requests
		entry.UnpricedRequests += bucket.UnpricedRequests
		entry.InputTokens += bucket.InputTokens
		entry.CacheReadTokens += bucket.CacheReadTokens
		entry.CacheWriteTokens += bucket.CacheWriteTokens
		entry.OutputTokens += bucket.OutputTokens
		entry.CostUSD += bucket.CostUSD
	}
}

func sortBuckets(buckets []Bucket) {
	sort.Slice(buckets, func(i, j int) bool {
		left, right := buckets[i], buckets[j]
		if !left.Hour.Equal(right.Hour) {
			return left.Hour.Before(right.Hour)
		}
		if left.Model != right.Model {
			return left.Model < right.Model
		}
		if left.Provider != right.Provider {
			return left.Provider < right.Provider
		}
		return left.AuthID < right.AuthID
	})
}

func derefInt64(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
