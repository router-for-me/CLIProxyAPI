// Package telemetry keeps bounded usage time series and quota history and
// derives burn-rate estimates from them. It records only counts and token
// totals CPA reports; nothing is inferred.
package telemetry

import (
	"math"
	"sort"
	"sync"
	"time"
)

const (
	fineStep    = 5 * time.Minute
	coarseStep  = time.Hour
	fineHorizon = 24 * time.Hour
	sampleEvery = 5 * time.Minute
	// Burn estimates need two samples in the same window at least this far apart.
	minSpan = 15 * time.Minute
	// Only recent samples describe the current consumption rate.
	rateLookback = 6 * time.Hour
)

// Counts is one bucket of activity.
type Counts struct {
	Requests      int64 `json:"requests"`
	Failures      int64 `json:"failures"`
	RateLimited   int64 `json:"rate_limited"`
	AuthFailures  int64 `json:"auth_failures"`
	InputTokens   int64 `json:"input_tokens"`
	OutputTokens  int64 `json:"output_tokens"`
	Reasoning     int64 `json:"reasoning_tokens"`
	CacheRead     int64 `json:"cache_read_tokens"`
	CacheCreation int64 `json:"cache_creation_tokens"`
	Failovers     int64 `json:"failovers"`
	Rebinds       int64 `json:"rebinds"`
	Affinity      int64 `json:"affinity_hits"`
	NewSessions   int64 `json:"new_sessions"`
	Delegated     int64 `json:"delegated"`
}

func (c *Counts) add(o Counts) {
	c.Requests += o.Requests
	c.Failures += o.Failures
	c.RateLimited += o.RateLimited
	c.AuthFailures += o.AuthFailures
	c.InputTokens += o.InputTokens
	c.OutputTokens += o.OutputTokens
	c.Reasoning += o.Reasoning
	c.CacheRead += o.CacheRead
	c.CacheCreation += o.CacheCreation
	c.Failovers += o.Failovers
	c.Rebinds += o.Rebinds
	c.Affinity += o.Affinity
	c.NewSessions += o.NewSessions
	c.Delegated += o.Delegated
}

// Point is one bucket in a series.
type Point struct {
	At time.Time `json:"at"`
	Counts
}

// Sample is one quota observation of a window.
type Sample struct {
	At       time.Time  `json:"at"`
	Used     float64    `json:"used"`
	ResetsAt *time.Time `json:"resets_at,omitempty"`
}

// Recorder is safe for concurrent use.
type Recorder struct {
	mu        sync.Mutex
	retention time.Duration
	// series is keyed by "provider" or "provider|authID", then bucket start (unix seconds).
	series  map[string]map[int64]*Counts
	samples map[string][]Sample // authID|windowKey
	dirty   bool
}

// New returns an empty recorder.
func New(retention time.Duration) *Recorder {
	return &Recorder{retention: retention, series: map[string]map[int64]*Counts{}, samples: map[string][]Sample{}}
}

// SetRetention changes how long history is kept.
func (r *Recorder) SetRetention(d time.Duration) {
	r.mu.Lock()
	r.retention = d
	r.mu.Unlock()
}

func bucket(at time.Time) int64 { return at.Truncate(fineStep).Unix() }

func (r *Recorder) addLocked(key string, at time.Time, c Counts) {
	m := r.series[key]
	if m == nil {
		m = map[int64]*Counts{}
		r.series[key] = m
	}
	b := bucket(at)
	if m[b] == nil {
		m[b] = &Counts{}
	}
	m[b].add(c)
	r.dirty = true
}

// Usage records one upstream attempt.
func (r *Recorder) Usage(at time.Time, provider, authID string, failed bool, status int, in, out, reasoning, cacheRead, cacheCreation int64) {
	c := Counts{Requests: 1, InputTokens: in, OutputTokens: out, Reasoning: reasoning, CacheRead: cacheRead, CacheCreation: cacheCreation}
	if failed {
		c.Failures = 1
		switch status {
		case 429:
			c.RateLimited = 1
		case 401, 403:
			c.AuthFailures = 1
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.addLocked(provider, at, c)
	if authID != "" {
		r.addLocked(provider+"|"+authID, at, c)
	}
}

// Decision records a routing outcome.
func (r *Recorder) Decision(at time.Time, provider, authID, outcome string) {
	var c Counts
	switch outcome {
	case "failover":
		c.Failovers = 1
	case "rebind":
		c.Rebinds = 1
	case "affinity":
		c.Affinity = 1
	case "new_session", "inherited":
		c.NewSessions = 1
	case "delegated":
		c.Delegated = 1
	default:
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.addLocked(provider, at, c)
	if authID != "" {
		r.addLocked(provider+"|"+authID, at, c)
	}
}

// Quota records a window observation, at most one per five minutes per window.
func (r *Recorder) Quota(at time.Time, authID, windowKey string, used float64, resetsAt *time.Time) {
	key := authID + "|" + windowKey
	r.mu.Lock()
	defer r.mu.Unlock()
	list := r.samples[key]
	if n := len(list); n > 0 {
		last := list[n-1]
		if !at.After(last.At) {
			return
		}
		if at.Sub(last.At) < sampleEvery && sameWindow(last.ResetsAt, resetsAt) {
			list[n-1] = Sample{At: at, Used: used, ResetsAt: copyTime(resetsAt)}
			r.dirty = true
			return
		}
	}
	r.samples[key] = append(list, Sample{At: at, Used: used, ResetsAt: copyTime(resetsAt)})
	r.dirty = true
}

func copyTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	c := *t
	return &c
}

// sameWindow treats resets within two minutes as the same window, since
// reset_after_seconds-based times drift slightly between reads.
func sameWindow(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	d := a.Sub(*b)
	return d < 2*time.Minute && d > -2*time.Minute
}

// Series returns buckets for key ("provider" or "provider|authID") since t,
// aggregated to step (a multiple of five minutes).
func (r *Recorder) Series(key string, since, until time.Time, step time.Duration) []Point {
	if step < fineStep {
		step = fineStep
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	agg := map[int64]*Counts{}
	for b, c := range r.series[key] {
		at := time.Unix(b, 0).UTC()
		if at.Before(since) || at.After(until) {
			continue
		}
		k := at.Truncate(step).Unix()
		if agg[k] == nil {
			agg[k] = &Counts{}
		}
		agg[k].add(*c)
	}
	var out []Point
	for t := since.Truncate(step); !t.After(until); t = t.Add(step) {
		p := Point{At: t}
		if c := agg[t.Unix()]; c != nil {
			p.Counts = *c
		}
		out = append(out, p)
	}
	return out
}

// Totals sums a key's buckets since t.
func (r *Recorder) Totals(key string, since time.Time) Counts {
	r.mu.Lock()
	defer r.mu.Unlock()
	var total Counts
	for b, c := range r.series[key] {
		if !time.Unix(b, 0).Before(since) {
			total.add(*c)
		}
	}
	return total
}

// Estimate is a labeled projection for one window.
type Estimate struct {
	Status string `json:"status"` // ok, insufficient_data, idle
	// RatePerHour is percentage points of the window consumed per hour.
	RatePerHour float64    `json:"rate_percent_per_hour,omitempty"`
	DepletesAt  *time.Time `json:"depletes_at,omitempty"`
	// BeforeReset is true when the projected depletion precedes the reset.
	BeforeReset bool `json:"depletes_before_reset"`
	// UnusedAtReset projects capacity left when the window resets.
	UnusedAtReset *float64 `json:"unused_at_reset_percent,omitempty"`
	Samples       int      `json:"samples"`
	SpanMinutes   float64  `json:"span_minutes"`
}

// Burn estimates consumption of a window from samples in its current period.
func (r *Recorder) Burn(authID, windowKey string, now time.Time, used float64, resetsAt *time.Time) Estimate {
	r.mu.Lock()
	var pts []Sample
	for _, s := range r.samples[authID+"|"+windowKey] {
		if sameWindow(s.ResetsAt, resetsAt) && now.Sub(s.At) <= rateLookback && !s.At.After(now) {
			pts = append(pts, s)
		}
	}
	r.mu.Unlock()
	e := Estimate{Status: "insufficient_data", Samples: len(pts)}
	if len(pts) < 2 {
		return e
	}
	span := pts[len(pts)-1].At.Sub(pts[0].At)
	e.SpanMinutes = math.Round(span.Minutes())
	if span < minSpan {
		return e
	}
	// Least-squares slope of used% over hours.
	var sx, sy, sxx, sxy float64
	n := float64(len(pts))
	base := pts[0].At
	for _, p := range pts {
		x := p.At.Sub(base).Hours()
		sx += x
		sy += p.Used
		sxx += x * x
		sxy += x * p.Used
	}
	den := n*sxx - sx*sx
	if den == 0 {
		return e
	}
	slope := (n*sxy - sx*sy) / den
	if slope <= 0.01 {
		e.Status = "idle"
		if resetsAt != nil {
			left := math.Max(0, 100-used)
			e.UnusedAtReset = &left
		}
		return e
	}
	e.Status = "ok"
	e.RatePerHour = math.Round(slope*100) / 100
	hoursLeft := math.Max(0, 100-used) / slope
	at := now.Add(time.Duration(hoursLeft * float64(time.Hour))).UTC()
	e.DepletesAt = &at
	if resetsAt != nil {
		e.BeforeReset = at.Before(*resetsAt)
		if !e.BeforeReset {
			left := math.Max(0, 100-used-slope*resetsAt.Sub(now).Hours())
			left = math.Round(left*10) / 10
			e.UnusedAtReset = &left
		}
	}
	return e
}

// Prune drops history past retention and folds old fine buckets into hourly ones.
func (r *Recorder) Prune(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := now.Add(-r.retention)
	fineCutoff := now.Add(-fineHorizon)
	for key, m := range r.series {
		for b, c := range m {
			at := time.Unix(b, 0)
			if at.Before(cutoff) {
				delete(m, b)
				r.dirty = true
				continue
			}
			if at.Before(fineCutoff) {
				hour := at.Truncate(coarseStep).Unix()
				if hour != b {
					if m[hour] == nil {
						m[hour] = &Counts{}
					}
					m[hour].add(*c)
					delete(m, b)
					r.dirty = true
				}
			}
		}
		if len(m) == 0 {
			delete(r.series, key)
		}
	}
	sampleCutoff := now.Add(-min(r.retention, 8*24*time.Hour))
	for key, list := range r.samples {
		i := sort.Search(len(list), func(i int) bool { return !list[i].At.Before(sampleCutoff) })
		if i > 0 {
			r.samples[key] = append([]Sample(nil), list[i:]...)
			r.dirty = true
		}
		if len(r.samples[key]) == 0 {
			delete(r.samples, key)
		}
	}
}

// State is the persisted form.
type State struct {
	Version int                          `json:"version"`
	Series  map[string]map[string]Counts `json:"series"`
	Samples map[string][]Sample          `json:"samples"`
}

// Export copies history for persistence.
func (r *Recorder) Export() State {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := State{Version: 1, Series: map[string]map[string]Counts{}, Samples: map[string][]Sample{}}
	for key, m := range r.series {
		out := map[string]Counts{}
		for b, c := range m {
			out[time.Unix(b, 0).UTC().Format(time.RFC3339)] = *c
		}
		s.Series[key] = out
	}
	for key, list := range r.samples {
		s.Samples[key] = append([]Sample(nil), list...)
	}
	return s
}

// Import restores history.
func (r *Recorder) Import(s State) {
	if s.Version != 1 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, m := range s.Series {
		dst := map[int64]*Counts{}
		for ts, c := range m {
			at, err := time.Parse(time.RFC3339, ts)
			if err != nil {
				continue
			}
			c := c
			dst[at.Unix()] = &c
		}
		r.series[key] = dst
	}
	for key, list := range s.Samples {
		sort.Slice(list, func(i, j int) bool { return list[i].At.Before(list[j].At) })
		r.samples[key] = list
	}
}

// TakeDirty reports whether history changed since the last call.
func (r *Recorder) TakeDirty() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.dirty
	r.dirty = false
	return d
}

// ForgetAccount drops history for one account (credential replaced).
func (r *Recorder) ForgetAccount(authID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key := range r.samples {
		if len(key) > len(authID) && key[:len(authID)+1] == authID+"|" {
			delete(r.samples, key)
		}
	}
}
