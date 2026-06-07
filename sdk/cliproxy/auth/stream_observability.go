package auth

import (
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// ActiveStreamSnapshot captures the current in-memory view of live streaming requests.
type ActiveStreamSnapshot struct {
	ActiveStreamsTotal      int            `json:"active_streams_total"`
	ActiveStreamsByModel    map[string]int `json:"active_streams_by_model"`
	ActiveStreamsByProvider map[string]int `json:"active_streams_by_provider"`
	ActiveStreamsByEndpoint map[string]int `json:"active_streams_by_endpoint"`
	StreamAgeP50Ms          int64          `json:"stream_age_p50_ms"`
	StreamAgeP95Ms          int64          `json:"stream_age_p95_ms"`
	StreamAgeMaxMs          int64          `json:"stream_age_max_ms"`
}

type activeStreamRecord struct {
	provider  string
	model     string
	endpoint  string
	startedAt time.Time
}

type activeStreamTracker struct {
	mu      sync.Mutex
	nextID  uint64
	records map[uint64]activeStreamRecord
}

func newActiveStreamTracker() *activeStreamTracker {
	return &activeStreamTracker{
		records: make(map[uint64]activeStreamRecord),
	}
}

func (t *activeStreamTracker) start(provider, model, endpoint string, startedAt time.Time) uint64 {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	t.nextID++
	t.records[t.nextID] = activeStreamRecord{
		provider:  strings.TrimSpace(provider),
		model:     strings.TrimSpace(model),
		endpoint:  strings.TrimSpace(endpoint),
		startedAt: startedAt,
	}
	return t.nextID
}

func (t *activeStreamTracker) stop(id uint64) {
	if t == nil || id == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.records, id)
}

func (t *activeStreamTracker) snapshot(now time.Time) ActiveStreamSnapshot {
	snapshot := ActiveStreamSnapshot{
		ActiveStreamsByModel:    map[string]int{},
		ActiveStreamsByProvider: map[string]int{},
		ActiveStreamsByEndpoint: map[string]int{},
	}
	if t == nil {
		return snapshot
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	ages := make([]int64, 0, len(t.records))
	for _, record := range t.records {
		snapshot.ActiveStreamsTotal++
		if record.model != "" {
			snapshot.ActiveStreamsByModel[record.model]++
		}
		if record.provider != "" {
			snapshot.ActiveStreamsByProvider[record.provider]++
		}
		if record.endpoint != "" {
			snapshot.ActiveStreamsByEndpoint[record.endpoint]++
		}
		if !record.startedAt.IsZero() && !now.Before(record.startedAt) {
			ages = append(ages, now.Sub(record.startedAt).Milliseconds())
		}
	}
	if len(ages) == 0 {
		return snapshot
	}

	sort.Slice(ages, func(i, j int) bool {
		return ages[i] < ages[j]
	})
	snapshot.StreamAgeP50Ms = percentileMillis(ages, 0.50)
	snapshot.StreamAgeP95Ms = percentileMillis(ages, 0.95)
	snapshot.StreamAgeMaxMs = ages[len(ages)-1]
	return snapshot
}

func percentileMillis(sorted []int64, percentile float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	if percentile <= 0 {
		return sorted[0]
	}
	if percentile >= 1 {
		return sorted[len(sorted)-1]
	}
	idx := int(math.Ceil(float64(len(sorted))*percentile)) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// ActiveStreamSnapshot returns the current live stream tracker snapshot.
func (m *Manager) ActiveStreamSnapshot() ActiveStreamSnapshot {
	if m == nil || m.activeStreams == nil {
		return ActiveStreamSnapshot{
			ActiveStreamsByModel:    map[string]int{},
			ActiveStreamsByProvider: map[string]int{},
			ActiveStreamsByEndpoint: map[string]int{},
		}
	}
	return m.activeStreams.snapshot(time.Now())
}
