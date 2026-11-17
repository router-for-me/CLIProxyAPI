// Package apikeylimits tracks per client API key token and cost usage in calendar
// windows (day, week, month) and enforces access.api-key-policies limits.
package apikeylimits

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

// Window identifies a calendar usage window.
type Window string

const (
	Day   Window = "day"
	Week  Window = "week"
	Month Window = "month"
	// Total accumulates all usage since counters were first recorded. It never resets
	// and is not used for limits.
	Total Window = "total"
)

var windows = []Window{Day, Week, Month, Total}

// ModelCounter holds one model's usage within a window.
type ModelCounter struct {
	Requests        int64   `json:"requests"`
	Failed          int64   `json:"failed,omitempty"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	CachedTokens    int64   `json:"cached_tokens,omitempty"`
	ReasoningTokens int64   `json:"reasoning_tokens,omitempty"`
	Tokens          int64   `json:"tokens"`
	Cost            float64 `json:"cost"`
}

// Counter holds usage accumulated in one window.
type Counter struct {
	Start    time.Time `json:"start"`
	Tokens   int64     `json:"tokens"`
	Cost     float64   `json:"cost"`
	Requests int64     `json:"requests"`
	Failed   int64     `json:"failed,omitempty"`
	// Models breaks the window down by upstream model name.
	Models map[string]*ModelCounter `json:"models,omitempty"`
}

// clone returns a deep copy so callers never share the Models map with the store.
func (c *Counter) clone() Counter {
	out := *c
	if c.Models != nil {
		out.Models = make(map[string]*ModelCounter, len(c.Models))
		for name, m := range c.Models {
			if m != nil {
				mc := *m
				out.Models[name] = &mc
			}
		}
	}
	return out
}

// Usage is a single request's contribution to the counters.
type Usage struct {
	Model           string
	Failed          bool
	InputTokens     int64
	OutputTokens    int64
	CachedTokens    int64
	ReasoningTokens int64
	Tokens          int64
	Cost            float64
}

type keyCounters map[Window]*Counter

// History retention and bucket key layouts (server local time).
const (
	HistoryDays      = 90
	HistoryHours     = 48
	DailyLayout      = "2006-01-02"
	HourlyLayout     = "2006-01-02T15"
	unknownModelName = "unknown"
)

// modelBuckets maps a time bucket key to per-model counters.
type modelBuckets map[string]map[string]*ModelCounter

// keyHistory keeps per-model usage over time for one client key.
type keyHistory struct {
	Daily  modelBuckets `json:"daily,omitempty"`
	Hourly modelBuckets `json:"hourly,omitempty"`
}

// Bucket is one time slot of a key's usage history.
type Bucket struct {
	Time   string
	Models map[string]ModelCounter
}

// Store keeps usage counters keyed by a SHA-256 fingerprint of the client key.
type Store struct {
	mu       sync.Mutex
	counters map[string]keyCounters
	history  map[string]*keyHistory
	path     string
	dirty    bool
	cfgFn    func() *config.Config
	now      func() time.Time
	stopOnce sync.Once
	stop     chan struct{}
}

var (
	defaultStore     = newStore()
	registerOnce     sync.Once
	flushLoopStarted sync.Once
)

func newStore() *Store {
	return &Store{
		counters: make(map[string]keyCounters),
		history:  make(map[string]*keyHistory),
		now:      time.Now,
		stop:     make(chan struct{}),
	}
}

// Default returns the process-wide store.
func Default() *Store { return defaultStore }

// Fingerprint returns the identifier used for a client key in persisted state.
func Fingerprint(key string) string {
	sum := sha256.Sum256([]byte("cliproxy-api-key-usage:" + key))
	return hex.EncodeToString(sum[:])
}

// Init sets the config source and persistence path, loads saved counters, registers
// the usage plugin and starts periodic flushing. Calling it again updates the sources.
func (s *Store) Init(statePath string, cfgFn func() *config.Config) {
	s.mu.Lock()
	s.cfgFn = cfgFn
	pathChanged := statePath != "" && statePath != s.path
	if pathChanged {
		s.path = statePath
	}
	s.mu.Unlock()
	if pathChanged {
		s.load()
	}
	registerOnce.Do(func() {
		coreusage.RegisterNamedPlugin("api-key-limits", s)
	})
	flushLoopStarted.Do(func() { go s.flushLoop() })
}

func (s *Store) flushLoop() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.Flush()
		case <-s.stop:
			s.Flush()
			return
		}
	}
}

// Close flushes pending counters and stops the flush loop.
func (s *Store) Close() {
	s.stopOnce.Do(func() { close(s.stop) })
}

// WindowStart returns the start of the window containing t, in t's location.
// Weeks start on Monday. The Total window has a zero start.
func WindowStart(w Window, t time.Time) time.Time {
	if w == Total {
		return time.Time{}
	}
	y, m, d := t.Date()
	day := time.Date(y, m, d, 0, 0, 0, 0, t.Location())
	switch w {
	case Week:
		offset := (int(day.Weekday()) + 6) % 7
		return day.AddDate(0, 0, -offset)
	case Month:
		return time.Date(y, m, 1, 0, 0, 0, 0, t.Location())
	default:
		return day
	}
}

// WindowEnd returns the end (exclusive) of the window containing t. The Total
// window never ends and returns the zero time.
func WindowEnd(w Window, t time.Time) time.Time {
	if w == Total {
		return time.Time{}
	}
	start := WindowStart(w, t)
	switch w {
	case Week:
		return start.AddDate(0, 0, 7)
	case Month:
		return start.AddDate(0, 1, 0)
	default:
		return start.AddDate(0, 0, 1)
	}
}

// current returns the counter for the window containing now, resetting stale ones.
// Callers must hold s.mu.
func (s *Store) current(fp string, w Window, now time.Time) *Counter {
	kc := s.counters[fp]
	if kc == nil {
		kc = make(keyCounters)
		s.counters[fp] = kc
	}
	start := WindowStart(w, now)
	c := kc[w]
	if c == nil || !c.Start.Equal(start) {
		c = &Counter{Start: start}
		kc[w] = c
	}
	return c
}

// Add records usage for a client key without a model breakdown.
func (s *Store) Add(key string, tokens int64, cost float64) {
	s.AddUsage(key, Usage{Tokens: tokens, Cost: cost})
}

// AddUsage records one request for a client key, including its per-model breakdown
// when u.Model is set.
func (s *Store) AddUsage(key string, u Usage) {
	if key == "" {
		return
	}
	fp := Fingerprint(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for _, w := range windows {
		c := s.current(fp, w, now)
		c.Requests++
		if u.Failed {
			c.Failed++
		}
		if u.Tokens > 0 {
			c.Tokens += u.Tokens
		}
		if u.Cost > 0 {
			c.Cost += u.Cost
		}
		if u.Model == "" {
			continue
		}
		if c.Models == nil {
			c.Models = make(map[string]*ModelCounter)
		}
		addModel(c.Models, u)
	}
	model := u.Model
	if model == "" {
		model = unknownModelName
	}
	hu := u
	hu.Model = model
	h := s.history[fp]
	if h == nil {
		h = &keyHistory{}
		s.history[fp] = h
	}
	h.Daily = addBucket(h.Daily, now.Format(DailyLayout), hu)
	h.Hourly = addBucket(h.Hourly, now.Format(HourlyLayout), hu)
	s.dirty = true
}

func addModel(models map[string]*ModelCounter, u Usage) {
	m := models[u.Model]
	if m == nil {
		m = &ModelCounter{}
		models[u.Model] = m
	}
	m.Requests++
	if u.Failed {
		m.Failed++
	}
	m.InputTokens += max(u.InputTokens, 0)
	m.OutputTokens += max(u.OutputTokens, 0)
	m.CachedTokens += max(u.CachedTokens, 0)
	m.ReasoningTokens += max(u.ReasoningTokens, 0)
	m.Tokens += max(u.Tokens, 0)
	if u.Cost > 0 {
		m.Cost += u.Cost
	}
}

func addBucket(b modelBuckets, slot string, u Usage) modelBuckets {
	if b == nil {
		b = make(modelBuckets)
	}
	models := b[slot]
	if models == nil {
		models = make(map[string]*ModelCounter)
		b[slot] = models
	}
	addModel(models, u)
	return b
}

// pruneHistory drops buckets older than the retention. Callers must hold s.mu.
func (s *Store) pruneHistory(now time.Time) {
	dayCutoff := now.AddDate(0, 0, -(HistoryDays - 1)).Format(DailyLayout)
	hourCutoff := now.Add(-time.Duration(HistoryHours-1) * time.Hour).Format(HourlyLayout)
	for fp, h := range s.history {
		for slot := range h.Daily {
			if slot < dayCutoff {
				delete(h.Daily, slot)
			}
		}
		for slot := range h.Hourly {
			if slot < hourCutoff {
				delete(h.Hourly, slot)
			}
		}
		if len(h.Daily) == 0 && len(h.Hourly) == 0 {
			delete(s.history, fp)
		}
	}
}

// History returns a key's retained daily and hourly buckets, oldest first.
func (s *Store) History(key string) (daily, hourly []Bucket) {
	fp := Fingerprint(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneHistory(s.now())
	h := s.history[fp]
	if h == nil {
		return nil, nil
	}
	return snapshotBuckets(h.Daily), snapshotBuckets(h.Hourly)
}

func snapshotBuckets(b modelBuckets) []Bucket {
	out := make([]Bucket, 0, len(b))
	for slot, models := range b {
		cp := make(map[string]ModelCounter, len(models))
		for name, m := range models {
			if m != nil {
				cp[name] = *m
			}
		}
		out = append(out, Bucket{Time: slot, Models: cp})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time < out[j].Time })
	return out
}

func cloneBuckets(b modelBuckets) modelBuckets {
	if b == nil {
		return nil
	}
	out := make(modelBuckets, len(b))
	for slot, models := range b {
		cp := make(map[string]*ModelCounter, len(models))
		for name, m := range models {
			if m != nil {
				mc := *m
				cp[name] = &mc
			}
		}
		out[slot] = cp
	}
	return out
}

// Usage returns the current counters for a key (zero values for unseen keys).
func (s *Store) Usage(key string) map[Window]Counter {
	fp := Fingerprint(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	out := make(map[Window]Counter, len(windows))
	for _, w := range windows {
		start := WindowStart(w, now)
		if kc := s.counters[fp]; kc != nil {
			if c := kc[w]; c != nil && c.Start.Equal(start) {
				out[w] = c.clone()
				continue
			}
		}
		out[w] = Counter{Start: start}
	}
	return out
}

// Exceeded describes the first limit a key has reached.
type Exceeded struct {
	Window  Window
	Metric  string
	Limit   float64
	Used    float64
	ResetAt time.Time
}

// Check returns the first reached limit for the key, if any.
func (s *Store) Check(key string, limits config.APIKeyLimits) (Exceeded, bool) {
	if limits.IsZero() {
		return Exceeded{}, false
	}
	usage := s.Usage(key)
	now := s.now()
	type rule struct {
		w      Window
		tokens int64
		cost   float64
	}
	rules := []rule{
		{Day, limits.TokensPerDay, limits.CostPerDay},
		{Week, limits.TokensPerWeek, limits.CostPerWeek},
		{Month, limits.TokensPerMonth, limits.CostPerMonth},
	}
	var found Exceeded
	ok := false
	for _, r := range rules {
		c := usage[r.w]
		var hit Exceeded
		hitOK := false
		if r.tokens > 0 && c.Tokens >= r.tokens {
			hit, hitOK = Exceeded{Window: r.w, Metric: "tokens", Limit: float64(r.tokens), Used: float64(c.Tokens)}, true
		} else if r.cost > 0 && c.Cost >= r.cost {
			hit, hitOK = Exceeded{Window: r.w, Metric: "cost", Limit: r.cost, Used: c.Cost}, true
		}
		if !hitOK {
			continue
		}
		hit.ResetAt = WindowEnd(r.w, now)
		// Report the limit that stays blocked longest so Retry-After is honest.
		if !ok || hit.ResetAt.After(found.ResetAt) {
			found, ok = hit, true
		}
	}
	return found, ok
}

// HandleUsage implements coreusage.Plugin.
func (s *Store) HandleUsage(_ context.Context, record coreusage.Record) {
	if !record.IsNativeKey || record.APIKey == "" {
		return
	}
	s.mu.Lock()
	cfgFn := s.cfgFn
	s.mu.Unlock()
	var cfg *config.Config
	if cfgFn != nil {
		cfg = cfgFn()
	}
	d := record.Detail
	cached := d.CachedTokens
	if cached <= 0 {
		cached = d.CacheReadTokens
	}
	s.AddUsage(record.APIKey, Usage{
		Model:           recordModel(record),
		Failed:          record.Failed,
		InputTokens:     d.InputTokens,
		OutputTokens:    d.OutputTokens,
		CachedTokens:    cached,
		ReasoningTokens: d.ReasoningTokens,
		Tokens:          recordTokens(d),
		Cost:            RecordCost(cfg, record),
	})
}

// recordModel names the model a request was served by: the resolved upstream model,
// then the upstream-reported one, then the client-requested name.
func recordModel(record coreusage.Record) string {
	for _, name := range []string{record.Model, record.ResponseModel, record.Alias} {
		if name = strings.TrimSpace(name); name != "" {
			return name
		}
	}
	return "unknown"
}

func recordTokens(d coreusage.Detail) int64 {
	if d.TotalTokens > 0 {
		return d.TotalTokens
	}
	return d.InputTokens + d.OutputTokens
}

// RecordCost returns the USD cost of a usage record: the upstream-reported cost when
// present, otherwise the access.model-prices estimate (0 when no price matches).
func RecordCost(cfg *config.Config, record coreusage.Record) float64 {
	if cd := record.Detail.CostDetails; cd != nil {
		if cd.Cost != nil && *cd.Cost > 0 {
			return *cd.Cost
		}
		if cd.CostInUSDTicks != nil && *cd.CostInUSDTicks > 0 {
			// xAI reports cost in 1e-10 USD ticks.
			return float64(*cd.CostInUSDTicks) / 1e10
		}
	}
	if cfg == nil {
		return 0
	}
	price, ok := cfg.FindModelPrice(record.Model, record.Alias, record.ResponseModel)
	if !ok {
		return 0
	}
	d := record.Detail
	cached := d.CachedTokens
	if cached <= 0 {
		cached = d.CacheReadTokens
	}
	if cached > d.InputTokens {
		cached = d.InputTokens
	}
	uncached := d.InputTokens - cached
	cachedPrice := price.CachedInput
	if cachedPrice <= 0 {
		cachedPrice = price.Input
	}
	return (float64(uncached)*price.Input + float64(cached)*cachedPrice + float64(d.OutputTokens)*price.Output) / 1e6
}

type persisted struct {
	Version  int                            `json:"version"`
	Counters map[string]map[Window]*Counter `json:"counters"`
	History  map[string]*keyHistory         `json:"history,omitempty"`
}

func (s *Store) load() {
	s.mu.Lock()
	path := s.path
	s.mu.Unlock()
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.WithError(err).Warn("api key limits: failed to read usage counters")
		}
		return
	}
	var state persisted
	if err = json.Unmarshal(data, &state); err != nil {
		log.WithError(err).Warn("api key limits: ignoring unreadable usage counters")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for fp, kc := range state.Counters {
		dst := make(keyCounters, len(kc))
		for w, c := range kc {
			if c != nil {
				copyC := c.clone()
				dst[w] = &copyC
			}
		}
		s.counters[fp] = dst
	}
	for fp, h := range state.History {
		if h != nil {
			s.history[fp] = &keyHistory{Daily: cloneBuckets(h.Daily), Hourly: cloneBuckets(h.Hourly)}
		}
	}
}

// Flush writes counters to disk when they changed since the last flush.
func (s *Store) Flush() {
	s.mu.Lock()
	if !s.dirty || s.path == "" {
		s.mu.Unlock()
		return
	}
	state := persisted{Version: 1, Counters: make(map[string]map[Window]*Counter, len(s.counters))}
	for fp, kc := range s.counters {
		cp := make(map[Window]*Counter, len(kc))
		for w, c := range kc {
			copyC := c.clone()
			cp[w] = &copyC
		}
		state.Counters[fp] = cp
	}
	s.pruneHistory(s.now())
	state.History = make(map[string]*keyHistory, len(s.history))
	for fp, h := range s.history {
		state.History[fp] = &keyHistory{Daily: cloneBuckets(h.Daily), Hourly: cloneBuckets(h.Hourly)}
	}
	path := s.path
	s.dirty = false
	s.mu.Unlock()

	data, err := json.Marshal(state)
	if err == nil {
		tmp := path + ".tmp"
		if err = os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
			if err = os.WriteFile(tmp, data, 0o600); err == nil {
				err = os.Rename(tmp, path)
			}
		}
	}
	if err != nil {
		log.WithError(err).Warn("api key limits: failed to persist usage counters")
		s.mu.Lock()
		s.dirty = true
		s.mu.Unlock()
	}
}
