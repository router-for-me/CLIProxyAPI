package engine

import (
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/router"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/telemetry"
)

// ChartPoint is one value of a pool series.
type ChartPoint struct {
	At    time.Time `json:"at"`
	Value float64   `json:"value"`
}

// ChartEvent is a window reset that returns capacity to the pool.
type ChartEvent struct {
	At        time.Time `json:"at"`
	Gain      float64   `json:"gain"`
	Projected bool      `json:"projected"`
}

// ChartAccount is one account's contribution to the pool.
type ChartAccount struct {
	ID          string     `json:"id"`
	Label       string     `json:"label"`
	Remaining   float64    `json:"remaining_percent"`
	ResetsAt    *time.Time `json:"resets_at,omitempty"`
	RatePerHour float64    `json:"rate_percent_per_hour"`
	EmptyAt     *time.Time `json:"empty_at,omitempty"`
	Plan        string     `json:"plan,omitempty"`
	// Weight is the account's share of a full account in the pool (the
	// provider's largest plan counts 1).
	Weight float64 `json:"weight"`
}

// PlanWeightNote explains a weight below 1, e.g. Max 5x = 0.25x.
type PlanWeightNote struct {
	Plan   string  `json:"plan"`
	Weight float64 `json:"weight"`
}

// FirstEmpty is the first account projected to run out before its reset.
type FirstEmpty struct {
	Account string    `json:"account"`
	Label   string    `json:"label"`
	At      time.Time `json:"at"`
}

// PoolChart is the recorded and projected pool of one provider window. Each
// account counts 100% times its plan weight relative to the provider's
// largest plan, so a Max 5x next to Max 20x accounts counts 25%.
type PoolChart struct {
	Provider   string           `json:"provider"`
	Window     string           `json:"window"`
	Now        time.Time        `json:"now"`
	Capacity   float64          `json:"capacity_percent"`
	Current    float64          `json:"current_percent"`
	Accounts   []ChartAccount   `json:"accounts"`
	Recorded   []ChartPoint     `json:"recorded"`
	Projected  []ChartPoint     `json:"projected"`
	Events     []ChartEvent     `json:"events"`
	Drain      []ChartPoint     `json:"drain"`
	DrainPeak  float64          `json:"drain_peak_percent_per_hour"`
	BurnLast3h float64          `json:"burn_last_3h_percent_per_hour"`
	PoolEmpty  *time.Time       `json:"pool_empty_at,omitempty"`
	Horizon    time.Time        `json:"horizon"`
	FirstEmpty *FirstEmpty      `json:"first_empty,omitempty"`
	NextReset  *ChartEvent      `json:"next_reset,omitempty"`
	Weights    []PlanWeightNote `json:"weights,omitempty"`
}

type chartInput struct {
	id, label string
	plan      string
	weight    float64 // 0 means 1
	remaining float64
	resetsAt  *time.Time
	period    time.Duration
	samples   []telemetry.Sample
}

type chartSpan struct {
	back, ahead, step time.Duration
}

func windowPeriod(kind string) time.Duration {
	switch kind {
	case "five_hour":
		return 5 * time.Hour
	case "daily":
		return 24 * time.Hour
	case "monthly":
		return 30 * 24 * time.Hour
	}
	return 7 * 24 * time.Hour
}

func spanFor(kind string) chartSpan {
	switch kind {
	case "five_hour":
		return chartSpan{back: 24 * time.Hour, ahead: 24 * time.Hour, step: 10 * time.Minute}
	case "daily":
		return chartSpan{back: 3 * 24 * time.Hour, ahead: 3 * 24 * time.Hour, step: 30 * time.Minute}
	}
	return chartSpan{back: 4 * 24 * time.Hour, ahead: 10 * 24 * time.Hour, step: 30 * time.Minute}
}

// PoolCharts builds a chart per provider for one window kind.
func (e *Engine) PoolCharts(kind string) []PoolChart {
	var accounts []router.AccountView
	if r := e.activeRouter(); r != nil {
		accounts = append(accounts, r.Accounts()...)
	}
	if mon := e.Monitor(); mon != nil {
		accounts = append(accounts, mon.Views()...)
	}
	e.mu.Lock()
	tel := e.tel
	e.mu.Unlock()
	byProv := map[string][]chartInput{}
	for _, a := range accounts {
		if !a.Present || a.Disabled {
			continue
		}
		for _, w := range a.Windows {
			if w.Kind != kind {
				continue
			}
			rem := w.Remaining
			if w.Passed {
				rem = 100
			}
			label := a.Alias
			if label == "" {
				label = a.Display
			}
			in := chartInput{id: a.ID, label: label, plan: a.Plan, weight: PlanWeight(a.Provider, a.Plan), remaining: rem, period: windowPeriod(kind)}
			if w.ResetsAt != nil && !w.Passed {
				t := *w.ResetsAt
				in.resetsAt = &t
			}
			if tel != nil {
				in.samples = tel.Samples(a.ID, w.Key)
			}
			byProv[a.Provider] = append(byProv[a.Provider], in)
			break
		}
	}
	now := e.now()
	out := make([]PoolChart, 0, len(byProv))
	for prov, in := range byProv {
		out = append(out, buildPoolChart(prov, kind, in, now, spanFor(kind)))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out
}

// remainingAt is the recorded remaining percent at t: the last observation at
// or before t (the earliest one before history starts), and 100 once that
// observation's window has reset.
func remainingAt(in chartInput, t time.Time) float64 {
	if len(in.samples) == 0 {
		return in.remaining
	}
	s := in.samples[0]
	for _, c := range in.samples {
		if c.At.After(t) {
			break
		}
		s = c
	}
	if s.ResetsAt != nil && !t.Before(*s.ResetsAt) && !s.At.After(t) {
		return 100
	}
	return clampPct(100 - s.Used)
}

// consumed sums quota used between observations whose later point falls in
// (from, to], skipping pairs that straddle a reset.
func consumed(samples []telemetry.Sample, from, to time.Time) float64 {
	var sum float64
	for i := 1; i < len(samples); i++ {
		a, b := samples[i-1], samples[i]
		if !b.At.After(from) || b.At.After(to) {
			continue
		}
		if !sameReset(a.ResetsAt, b.ResetsAt) {
			continue
		}
		if d := b.Used - a.Used; d > 0 {
			sum += d
		}
	}
	return sum
}

func sameReset(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	d := a.Sub(*b)
	return d < 2*time.Minute && d > -2*time.Minute
}

// projectedAt assumes a constant burn rate and a full window after each reset.
func projectedAt(in chartInput, rate float64, now, t time.Time) float64 {
	if in.resetsAt == nil || t.Before(*in.resetsAt) {
		return clampPct(in.remaining - rate*t.Sub(now).Hours())
	}
	since := t.Sub(*in.resetsAt)
	if in.period > 0 {
		since %= in.period
	}
	return clampPct(100 - rate*since.Hours())
}

func clampPct(v float64) float64 { return math.Max(0, math.Min(100, v)) }

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// planDisplay turns plan ids like "max_20x" into "Max 20x".
func planDisplay(plan string) string {
	words := strings.Fields(strings.NewReplacer("_", " ", "-", " ").Replace(strings.TrimSpace(plan)))
	for i, w := range words {
		if w != "" {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

// normalizeWeights scales plan weights so the largest is 1 and lists the
// plans that count less.
func normalizeWeights(in []chartInput) []PlanWeightNote {
	top := 0.0
	for _, a := range in {
		if a.weight <= 0 {
			continue
		}
		top = math.Max(top, a.weight)
	}
	if top <= 0 {
		for i := range in {
			in[i].weight = 1
		}
		return nil
	}
	seen := map[string]bool{}
	var notes []PlanWeightNote
	for i := range in {
		if in[i].weight <= 0 {
			in[i].weight = top
		}
		in[i].weight /= top
		if in[i].weight < 0.999 {
			label := planDisplay(in[i].plan)
			if label == "" {
				label = "Unknown plan"
			}
			if !seen[label] {
				seen[label] = true
				notes = append(notes, PlanWeightNote{Plan: label, Weight: math.Round(in[i].weight*1000) / 1000})
			}
		}
	}
	sort.Slice(notes, func(i, j int) bool { return notes[i].Weight > notes[j].Weight })
	return notes
}

func buildPoolChart(provider, kind string, in []chartInput, now time.Time, span chartSpan) PoolChart {
	sort.Slice(in, func(i, j int) bool { return in[i].label < in[j].label })
	c := PoolChart{Provider: provider, Window: kind, Now: now, Horizon: now.Add(span.ahead)}
	c.Weights = normalizeWeights(in)
	rates := make([]float64, len(in))
	for i, a := range in {
		c.Capacity += 100 * a.weight
		c.Current += a.remaining * a.weight
		// Project with the last day's average so a short burst does not
		// empty the pool on paper.
		rates[i] = consumed(a.samples, now.Add(-24*time.Hour), now) / 24
		c.BurnLast3h += consumed(a.samples, now.Add(-3*time.Hour), now) / 3 * a.weight
		ca := ChartAccount{ID: a.id, Label: a.label, Remaining: round1(a.remaining), ResetsAt: a.resetsAt, RatePerHour: round1(rates[i]), Plan: planDisplay(a.plan), Weight: math.Round(a.weight*1000) / 1000}
		if rates[i] > 0 {
			at := now.Add(time.Duration(a.remaining / rates[i] * float64(time.Hour)))
			if a.resetsAt == nil || at.Before(*a.resetsAt) {
				ca.EmptyAt = &at
				if c.FirstEmpty == nil || at.Before(c.FirstEmpty.At) {
					c.FirstEmpty = &FirstEmpty{Account: a.id, Label: a.label, At: at}
				}
			}
		}
		c.Accounts = append(c.Accounts, ca)
	}
	c.Current = round1(c.Current)
	c.Capacity = round1(c.Capacity)
	c.BurnLast3h = round1(c.BurnLast3h)

	// Recorded: the pool at each step, then the live value at now.
	for t := now.Add(-span.back).Truncate(span.step); t.Before(now); t = t.Add(span.step) {
		var v float64
		for _, a := range in {
			v += remainingAt(a, t) * a.weight
		}
		c.Recorded = append(c.Recorded, ChartPoint{At: t, Value: round1(v)})
	}
	c.Recorded = append(c.Recorded, ChartPoint{At: now, Value: c.Current})
	for i := 1; i < len(c.Recorded); i++ {
		if g := c.Recorded[i].Value - c.Recorded[i-1].Value; g >= 1 {
			c.Events = append(c.Events, ChartEvent{At: c.Recorded[i].At, Gain: round1(g)})
		}
	}

	// Projected: constant burn, resets every period.
	for t := now; !t.After(c.Horizon); t = t.Add(span.step) {
		var v float64
		for i, a := range in {
			v += projectedAt(a, rates[i], now, t) * a.weight
		}
		c.Projected = append(c.Projected, ChartPoint{At: t, Value: round1(v)})
		if c.PoolEmpty == nil && len(in) > 0 && v < 0.5 {
			at := t
			c.PoolEmpty = &at
		}
	}
	var future []ChartEvent
	for i, a := range in {
		if a.resetsAt == nil {
			continue
		}
		for r := *a.resetsAt; !r.After(c.Horizon); r = r.Add(a.period) {
			if gain := (100 - projectedAt(a, rates[i], now, r.Add(-time.Second))) * a.weight; gain >= 0.5 {
				future = append(future, ChartEvent{At: r, Gain: gain, Projected: true})
			}
			if a.period <= 0 {
				break
			}
		}
	}
	sort.Slice(future, func(i, j int) bool { return future[i].At.Before(future[j].At) })
	// Resets within an hour of each other read as one step on the chart.
	var merged []ChartEvent
	for _, ev := range future {
		if n := len(merged); n > 0 && ev.At.Sub(merged[n-1].At) < time.Hour {
			merged[n-1].Gain += ev.Gain
			continue
		}
		merged = append(merged, ev)
	}
	for i := range merged {
		merged[i].Gain = round1(merged[i].Gain)
	}
	if len(merged) > 0 {
		first := merged[0]
		c.NextReset = &first
	}
	c.Events = append(c.Events, merged...)

	// Drain: trailing one-hour consumption, sampled every 15 minutes.
	drainStep := 15 * time.Minute
	for t := now.Add(-span.back).Truncate(drainStep); !t.After(now); t = t.Add(drainStep) {
		var v float64
		for _, a := range in {
			v += consumed(a.samples, t.Add(-time.Hour), t) * a.weight
		}
		c.Drain = append(c.Drain, ChartPoint{At: t, Value: round1(v)})
		c.DrainPeak = math.Max(c.DrainPeak, round1(v))
	}
	return c
}

func (e *Engine) handlePoolChart(c *gin.Context) {
	kind := c.DefaultQuery("window", "weekly")
	switch kind {
	case "five_hour", "daily", "weekly", "monthly":
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "window must be five_hour, daily, weekly or monthly"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"now": e.now(), "charts": e.PoolCharts(kind)})
}
