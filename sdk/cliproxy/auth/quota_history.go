package auth

import (
	"math"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// QuotaHistoryRetention is how long quota readings are kept.
	QuotaHistoryRetention = 30 * 24 * time.Hour
	// QuotaHistoryFileName names the history file in the auth dir. It must never
	// end in .json: the file store loads every *.json there as a credential.
	QuotaHistoryFileName = "quota-history.jsonl"

	quotaHistoryMaxPoints   = 10000 // per series; oldest dropped first
	quotaHistoryUsedDelta   = 0.005
	quotaHistoryUsedScale   = 1e4 // used fractions are kept to 4 decimals
	quotaHistoryKeepAlive   = time.Hour
	quotaHistoryResetShift  = 2 * time.Minute
	quotaHistoryResetMinGap = 5 * time.Minute // minimum spacing for a reset-only change
	quotaHistoryFutureSkew  = 2 * time.Minute
)

// QuotaHistoryPoint is one observed reading of one quota window.
type QuotaHistoryPoint struct {
	At      time.Time // window.ObservedAt, truncated to the second, UTC
	Used    float64   // 0..1, rounded to 4 decimals
	ResetAt time.Time // zero when unknown
}

// QuotaHistorySeries holds the readings of one window of one credential, oldest first.
type QuotaHistorySeries struct {
	Kind   string
	Scope  string
	Period time.Duration // zero when unknown
	Points []QuotaHistoryPoint
}

type quotaHistoryKey struct {
	authID, kind, scope string
}

// QuotaHistory keeps recent weekly quota readings per credential, optionally
// appended to a JSONL file. Safe for concurrent use.
type QuotaHistory struct {
	// ioMu serializes file I/O and guards the state inside file. It is taken
	// before mu, so readers of the series never wait on the disk.
	ioMu    sync.Mutex
	mu      sync.RWMutex
	series  map[quotaHistoryKey]*QuotaHistorySeries
	pending []quotaHistoryLine
	file    *quotaHistoryFile // nil = memory only; never changes once the store is shared
}

// NewQuotaHistory returns a memory-only store.
func NewQuotaHistory() *QuotaHistory {
	return &QuotaHistory{series: map[quotaHistoryKey]*QuotaHistorySeries{}}
}

// Observe applies the sampling policy to one credential's windows (from
// QuotaObservations). It records only the unscoped weekly window and returns
// the number of points added.
func (h *QuotaHistory) Observe(authID string, windows []QuotaWindow, now time.Time) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	added := 0
	for _, w := range windows {
		if w.Kind != QuotaWindowWeekly || w.Scope != "" {
			continue
		}
		key := quotaHistoryKey{authID: authID, kind: w.Kind, scope: w.Scope}
		series := h.series[key]
		point, ok := sampleQuotaHistory(series, w, now)
		if !ok {
			continue
		}
		if series == nil {
			series = &QuotaHistorySeries{Kind: w.Kind, Scope: w.Scope}
			h.series[key] = series
		}
		if w.Period > 0 {
			series.Period = w.Period
		}
		series.Points = append(series.Points, point)
		if over := len(series.Points) - quotaHistoryMaxPoints; over > 0 {
			series.Points = series.Points[over:]
		}
		if h.file != nil {
			h.pending = append(h.pending, newQuotaHistoryLine(authID, series, point))
		}
		added++
	}
	return added
}

// sampleQuotaHistory returns the point to record for w, if the sampling policy
// accepts it after the newest point of series.
func sampleQuotaHistory(series *QuotaHistorySeries, w QuotaWindow, now time.Time) (QuotaHistoryPoint, bool) {
	if w.ObservedAt.IsZero() {
		return QuotaHistoryPoint{}, false
	}
	at := w.ObservedAt.UTC().Truncate(time.Second)
	if at.After(now.Add(quotaHistoryFutureSkew)) {
		at = now.UTC().Truncate(time.Second)
	}
	point := QuotaHistoryPoint{At: at, Used: roundQuotaUsed(w.Used), ResetAt: w.ResetAt.UTC().Truncate(time.Second)}
	if series == nil || len(series.Points) == 0 {
		return point, true
	}
	last := series.Points[len(series.Points)-1]
	if !at.After(last.At) {
		return QuotaHistoryPoint{}, false
	}
	elapsed := at.Sub(last.At)
	usedChanged := math.Round(math.Abs(point.Used-last.Used)*quotaHistoryUsedScale) >= quotaHistoryUsedDelta*quotaHistoryUsedScale
	resetChanged := quotaResetMoved(point.ResetAt, last.ResetAt) && elapsed >= quotaHistoryResetMinGap
	if usedChanged || resetChanged || elapsed >= quotaHistoryKeepAlive {
		return point, true
	}
	return QuotaHistoryPoint{}, false
}

func quotaResetMoved(a, b time.Time) bool {
	if a.IsZero() || b.IsZero() {
		return a.IsZero() != b.IsZero()
	}
	shift := a.Sub(b)
	if shift < 0 {
		shift = -shift
	}
	return shift > quotaHistoryResetShift
}

func roundQuotaUsed(used float64) float64 {
	return math.Round(min(max(used, 0), 1)*quotaHistoryUsedScale) / quotaHistoryUsedScale
}

// Flush appends the points observed since the last Flush in one write. It is a
// no-op when the store is memory only or a failed write left the file dirty.
func (h *QuotaHistory) Flush() error {
	h.ioMu.Lock()
	defer h.ioMu.Unlock()
	h.mu.Lock()
	lines := h.pending
	h.pending = nil
	h.mu.Unlock()
	if h.file == nil || len(lines) == 0 {
		return nil
	}
	return h.file.append(lines)
}

// Series returns deep copies of authID's series with the points at or after
// since, plus the newest earlier point, so callers know the value at since.
func (h *QuotaHistory) Series(authID string, since time.Time) []QuotaHistorySeries {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []QuotaHistorySeries
	for key, series := range h.series {
		if key.authID != authID {
			continue
		}
		start := sort.Search(len(series.Points), func(i int) bool { return !series.Points[i].At.Before(since) })
		if start > 0 {
			start--
		}
		if start == len(series.Points) {
			continue
		}
		out = append(out, QuotaHistorySeries{
			Kind:   series.Kind,
			Scope:  series.Scope,
			Period: series.Period,
			Points: slices.Clone(series.Points[start:]),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Scope < out[j].Scope
	})
	return out
}

// Compact drops points older than now-QuotaHistoryRetention and empty series,
// applies the per-series cap, and atomically rewrites the file from memory.
// Pruning applies to memory-only stores too; only the rewrite is skipped.
func (h *QuotaHistory) Compact(now time.Time) error {
	h.ioMu.Lock()
	defer h.ioMu.Unlock()
	h.mu.Lock()
	h.pruneLocked(now.Add(-QuotaHistoryRetention))
	var lines []quotaHistoryLine
	if h.file != nil {
		lines = h.linesLocked()
		// The rewrite carries every queued point.
		h.pending = nil
	}
	h.mu.Unlock()
	if h.file == nil {
		return nil
	}
	return h.file.rewrite(lines)
}

func (h *QuotaHistory) pruneLocked(cutoff time.Time) {
	for key, series := range h.series {
		start := sort.Search(len(series.Points), func(i int) bool { return !series.Points[i].At.Before(cutoff) })
		start = max(start, len(series.Points)-quotaHistoryMaxPoints)
		if start == len(series.Points) {
			delete(h.series, key)
			continue
		}
		if start > 0 {
			series.Points = slices.Clone(series.Points[start:])
		}
	}
}

func (h *QuotaHistory) linesLocked() []quotaHistoryLine {
	keys := make([]quotaHistoryKey, 0, len(h.series))
	total := 0
	for key, series := range h.series {
		keys = append(keys, key)
		total += len(series.Points)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].authID != keys[j].authID {
			return keys[i].authID < keys[j].authID
		}
		if keys[i].kind != keys[j].kind {
			return keys[i].kind < keys[j].kind
		}
		return keys[i].scope < keys[j].scope
	})
	lines := make([]quotaHistoryLine, 0, total)
	for _, key := range keys {
		series := h.series[key]
		for _, point := range series.Points {
			lines = append(lines, newQuotaHistoryLine(key.authID, series, point))
		}
	}
	return lines
}

// Dirty reports that a write failed and the file must be rewritten by Compact.
func (h *QuotaHistory) Dirty() bool {
	h.ioMu.Lock()
	defer h.ioMu.Unlock()
	return h.file != nil && h.file.dirty
}

// Close releases the append handle. It does not flush.
func (h *QuotaHistory) Close() error {
	h.ioMu.Lock()
	defer h.ioMu.Unlock()
	if h.file == nil {
		return nil
	}
	return h.file.close()
}

var activeQuotaHistory atomic.Pointer[QuotaHistory]

// SetActiveQuotaHistory publishes the running recorder's store (nil clears).
func SetActiveQuotaHistory(h *QuotaHistory) {
	activeQuotaHistory.Store(h)
}

// ActiveQuotaHistory returns the running recorder's store, or nil when nothing
// records (Home mode, embedders, or not started).
func ActiveQuotaHistory() *QuotaHistory {
	return activeQuotaHistory.Load()
}
