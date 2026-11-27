package usage

import (
	"sort"
	"strings"
	"sync"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

const (
	hourly      = time.Hour
	daily       = 24 * time.Hour
	rollupAfter = 7 * 24 * time.Hour
)

// Tokens are billable token counts. Input excludes cached tokens.
type Tokens struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	Reasoning  int64 `json:"reasoning"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
}

func (t *Tokens) add(o Tokens) {
	t.Input += o.Input
	t.Output += o.Output
	t.Reasoning += o.Reasoning
	t.CacheRead += o.CacheRead
	t.CacheWrite += o.CacheWrite
}

// Total is every token the request processed.
func (t Tokens) Total() int64 { return t.Input + t.Output + t.CacheRead + t.CacheWrite }

// TokensFromDetail converts a CPA usage detail into billable tokens,
// preferring CPA's normalized breakdown when it is valid.
func TokensFromDetail(provider string, d coreusage.Detail) Tokens {
	if b := d.TokenBreakdown; b.Valid() {
		return Tokens{
			Input: b.Input.UncachedTokens, CacheRead: b.Input.CacheReadTokens, CacheWrite: b.Input.CacheWriteTokens,
			Output: b.Output.TotalTokens, Reasoning: b.Output.ReasoningTokens,
		}
	}
	t := Tokens{Input: d.InputTokens, Output: d.OutputTokens, Reasoning: d.ReasoningTokens, CacheRead: d.CacheReadTokens, CacheWrite: d.CacheCreationTokens}
	if provider != "claude" {
		// OpenAI-style input totals include cached tokens.
		t.Input = max(0, t.Input-t.CacheRead-t.CacheWrite)
	}
	return t
}

// Row is one aggregated bucket.
type Row struct {
	Start    time.Time `json:"start"`
	Provider string    `json:"provider"`
	AuthID   string    `json:"auth_id"`
	Model    string    `json:"model"`
	Client   string    `json:"client"`
	Requests int64     `json:"requests"`
	Failures int64     `json:"failures"`
	Tokens   Tokens    `json:"tokens"`
	CostUSD  float64   `json:"cost_usd"`
	// Unpriced counts requests whose model had no price.
	Unpriced int64 `json:"unpriced"`
}

type rowKey struct {
	start                         int64
	provider, auth, model, client string
}

// State is the persisted ledger.
type State struct {
	Rows []Row `json:"rows"`
}

// Ledger aggregates usage records into hourly (later daily) buckets.
type Ledger struct {
	mu        sync.Mutex
	rows      map[rowKey]*Row
	pricer    *Pricer
	retention time.Duration
	dirty     bool
}

// NewLedger returns an empty ledger.
func NewLedger(pricer *Pricer, retention time.Duration) *Ledger {
	return &Ledger{rows: map[rowKey]*Row{}, pricer: pricer, retention: retention}
}

// Configure replaces pricing and retention. Existing costs are not repriced.
func (l *Ledger) Configure(pricer *Pricer, retention time.Duration) {
	l.mu.Lock()
	l.pricer, l.retention = pricer, retention
	l.mu.Unlock()
}

// Price looks up a model's price.
func (l *Ledger) Price(model string) (Price, bool) {
	l.mu.Lock()
	p := l.pricer
	l.mu.Unlock()
	return p.Lookup(model)
}

// Add records one upstream attempt.
func (l *Ledger) Add(at time.Time, provider, authID, model, client string, failed bool, t Tokens) {
	model = NormalizeModel(model)
	if client == "" {
		client = "other"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	k := rowKey{at.UTC().Truncate(hourly).Unix(), provider, authID, model, client}
	r := l.rows[k]
	if r == nil {
		r = &Row{Start: time.Unix(k.start, 0).UTC(), Provider: provider, AuthID: authID, Model: model, Client: client}
		l.rows[k] = r
	}
	r.Requests++
	if failed {
		r.Failures++
	}
	r.Tokens.add(t)
	if p, ok := l.pricer.Lookup(model); ok {
		r.CostUSD += p.Cost(t)
	} else if t.Total() > 0 {
		r.Unpriced++
	}
	l.dirty = true
}

// Prune rolls buckets older than a week into days and drops expired ones.
func (l *Ledger) Prune(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-l.retention).Unix()
	roll := now.Add(-rollupAfter).Unix()
	for k, r := range l.rows {
		if k.start < cutoff {
			delete(l.rows, k)
			l.dirty = true
			continue
		}
		if k.start >= roll {
			continue
		}
		day := time.Unix(k.start, 0).UTC().Truncate(daily).Unix()
		if day == k.start {
			continue
		}
		dk := k
		dk.start = day
		d := l.rows[dk]
		if d == nil {
			d = &Row{Start: time.Unix(day, 0).UTC(), Provider: r.Provider, AuthID: r.AuthID, Model: r.Model, Client: r.Client}
			l.rows[dk] = d
		}
		mergeRow(d, r)
		delete(l.rows, k)
		l.dirty = true
	}
}

func mergeRow(dst, src *Row) {
	dst.Requests += src.Requests
	dst.Failures += src.Failures
	dst.Unpriced += src.Unpriced
	dst.CostUSD += src.CostUSD
	dst.Tokens.add(src.Tokens)
}

// Query filters and groups rows. groupBy lists any of "provider", "auth",
// "model", "client"; step > 0 also groups by time bucket.
type Query struct {
	Since, Until time.Time
	GroupBy      []string
	Step         time.Duration
	Provider     string
	AuthID       string
}

// Run evaluates q. Rows are sorted by start, then cost descending.
func (l *Ledger) Run(q Query) []Row {
	group := map[string]bool{}
	for _, g := range q.GroupBy {
		group[strings.ToLower(g)] = true
	}
	l.mu.Lock()
	out := map[rowKey]*Row{}
	for k, r := range l.rows {
		t := time.Unix(k.start, 0)
		if (!q.Since.IsZero() && t.Before(q.Since.Truncate(hourly))) || (!q.Until.IsZero() && !t.Before(q.Until)) {
			continue
		}
		if (q.Provider != "" && r.Provider != q.Provider) || (q.AuthID != "" && r.AuthID != q.AuthID) {
			continue
		}
		gk := rowKey{}
		g := &Row{}
		if q.Step > 0 {
			gk.start = t.UTC().Truncate(q.Step).Unix()
			g.Start = time.Unix(gk.start, 0).UTC()
		}
		if group["provider"] {
			gk.provider, g.Provider = r.Provider, r.Provider
		}
		if group["auth"] {
			gk.auth, g.AuthID, gk.provider, g.Provider = r.AuthID, r.AuthID, r.Provider, r.Provider
		}
		if group["model"] {
			gk.model, g.Model = r.Model, r.Model
		}
		if group["client"] {
			gk.client, g.Client = r.Client, r.Client
		}
		if existing := out[gk]; existing != nil {
			g = existing
		} else {
			out[gk] = g
		}
		mergeRow(g, r)
	}
	l.mu.Unlock()
	rows := make([]Row, 0, len(out))
	for _, r := range out {
		rows = append(rows, *r)
	}
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].Start.Equal(rows[j].Start) {
			return rows[i].Start.Before(rows[j].Start)
		}
		return rows[i].CostUSD > rows[j].CostUSD
	})
	return rows
}

// Export returns the rows for persistence.
func (l *Ledger) Export() State {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := State{Rows: make([]Row, 0, len(l.rows))}
	for _, r := range l.rows {
		s.Rows = append(s.Rows, *r)
	}
	return s
}

// Import restores persisted rows.
func (l *Ledger) Import(s State) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range s.Rows {
		k := rowKey{r.Start.UTC().Unix(), r.Provider, r.AuthID, r.Model, r.Client}
		rc := r
		l.rows[k] = &rc
	}
}

// TakeDirty reports and clears whether the ledger changed.
func (l *Ledger) TakeDirty() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	d := l.dirty
	l.dirty = false
	return d
}

// GenerateEnabled reports whether a record represents actual generation
// (token counting and similar probes are excluded).
func GenerateEnabled(rec coreusage.Record) bool {
	return rec.Generate == nil || *rec.Generate
}
