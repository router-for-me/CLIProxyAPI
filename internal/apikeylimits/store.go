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
)

var windows = []Window{Day, Week, Month}

// Counter holds usage accumulated in one window.
type Counter struct {
	Start    time.Time `json:"start"`
	Tokens   int64     `json:"tokens"`
	Cost     float64   `json:"cost"`
	Requests int64     `json:"requests"`
}

type keyCounters map[Window]*Counter

// Store keeps usage counters keyed by a SHA-256 fingerprint of the client key.
type Store struct {
	mu       sync.Mutex
	counters map[string]keyCounters
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
	return &Store{counters: make(map[string]keyCounters), now: time.Now, stop: make(chan struct{})}
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
// Weeks start on Monday.
func WindowStart(w Window, t time.Time) time.Time {
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

// WindowEnd returns the end (exclusive) of the window containing t.
func WindowEnd(w Window, t time.Time) time.Time {
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

// Add records usage for a client key.
func (s *Store) Add(key string, tokens int64, cost float64) {
	if key == "" {
		return
	}
	fp := Fingerprint(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for _, w := range windows {
		c := s.current(fp, w, now)
		if tokens > 0 {
			c.Tokens += tokens
		}
		if cost > 0 {
			c.Cost += cost
		}
		c.Requests++
	}
	s.dirty = true
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
				out[w] = *c
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
	tokens := recordTokens(record.Detail)
	s.Add(record.APIKey, tokens, RecordCost(cfg, record))
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
				copyC := *c
				dst[w] = &copyC
			}
		}
		s.counters[fp] = dst
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
			copyC := *c
			cp[w] = &copyC
		}
		state.Counters[fp] = cp
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
