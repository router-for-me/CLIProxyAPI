package metrics

import (
	"sort"
	"sync"
)

// LatencyBuckets defines the bucket boundaries for latency histograms in ms.
var LatencyBuckets = []int64{0, 50, 100, 200, 500, 1000, 2000, 5000}

// LatencyHistogram provides bucket-based latency tracking for percentile calculation.
type LatencyHistogram struct {
	mu      sync.RWMutex
	Buckets map[int64]int64 `json:"buckets"`
	Values  []int64         `json:"values,omitempty"`
	Count   int64           `json:"count"`
	Sum     int64           `json:"sum"`
}

// NewLatencyHistogram creates a new histogram with initialized buckets.
func NewLatencyHistogram() *LatencyHistogram {
	h := &LatencyHistogram{
		Buckets: make(map[int64]int64),
		Values:  make([]int64, 0, 100),
	}
	for _, b := range LatencyBuckets {
		h.Buckets[b] = 0
	}
	return h
}

// Record adds a latency value to the histogram.
func (h *LatencyHistogram) Record(latencyMs int64) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.Count++
	h.Sum += latencyMs
	h.Values = append(h.Values, latencyMs)

	for i := len(LatencyBuckets) - 1; i >= 0; i-- {
		if latencyMs >= LatencyBuckets[i] {
			h.Buckets[LatencyBuckets[i]]++
			break
		}
	}
}

// Percentile returns the estimated percentile value (0-100).
func (h *LatencyHistogram) Percentile(p float64) float64 {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if len(h.Values) == 0 {
		return 0
	}

	sorted := make([]int64, len(h.Values))
	copy(sorted, h.Values)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	index := int(float64(len(sorted)-1) * p / 100.0)
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return float64(sorted[index])
}

// P50 returns the 50th percentile (median).
func (h *LatencyHistogram) P50() float64 {
	return h.Percentile(50)
}

// P90 returns the 90th percentile.
func (h *LatencyHistogram) P90() float64 {
	return h.Percentile(90)
}

// P99 returns the 99th percentile.
func (h *LatencyHistogram) P99() float64 {
	return h.Percentile(99)
}

// Average returns the mean latency.
func (h *LatencyHistogram) Average() float64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.Count == 0 {
		return 0
	}
	return float64(h.Sum) / float64(h.Count)
}

// Merge combines another histogram into this one.
func (h *LatencyHistogram) Merge(other *LatencyHistogram) {
	if other == nil {
		return
	}
	other.mu.RLock()
	defer other.mu.RUnlock()

	h.mu.Lock()
	defer h.mu.Unlock()

	h.Count += other.Count
	h.Sum += other.Sum
	h.Values = append(h.Values, other.Values...)

	for bucket, count := range other.Buckets {
		h.Buckets[bucket] += count
	}
}

// Clone creates a deep copy of the histogram.
func (h *LatencyHistogram) Clone() *LatencyHistogram {
	h.mu.RLock()
	defer h.mu.RUnlock()

	clone := &LatencyHistogram{
		Buckets: make(map[int64]int64),
		Values:  make([]int64, len(h.Values)),
		Count:   h.Count,
		Sum:     h.Sum,
	}
	copy(clone.Values, h.Values)
	for k, v := range h.Buckets {
		clone.Buckets[k] = v
	}
	return clone
}
