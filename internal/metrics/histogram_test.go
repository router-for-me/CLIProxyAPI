package metrics

import (
	"sync"
	"testing"
)

func TestNewLatencyHistogram(t *testing.T) {
	h := NewLatencyHistogram()
	if h == nil {
		t.Fatal("NewLatencyHistogram returned nil")
	}
	if h.Count != 0 {
		t.Errorf("expected Count=0, got %d", h.Count)
	}
	if h.Sum != 0 {
		t.Errorf("expected Sum=0, got %d", h.Sum)
	}
	if len(h.Buckets) != len(LatencyBuckets) {
		t.Errorf("expected %d buckets, got %d", len(LatencyBuckets), len(h.Buckets))
	}
}

func TestHistogramRecord(t *testing.T) {
	h := NewLatencyHistogram()
	
	values := []int64{10, 50, 100, 150, 500, 1000, 3000, 6000}
	for _, v := range values {
		h.Record(v)
	}
	
	if h.Count != int64(len(values)) {
		t.Errorf("expected Count=%d, got %d", len(values), h.Count)
	}
	
	var expectedSum int64
	for _, v := range values {
		expectedSum += v
	}
	if h.Sum != expectedSum {
		t.Errorf("expected Sum=%d, got %d", expectedSum, h.Sum)
	}
}

func TestHistogramPercentiles(t *testing.T) {
	h := NewLatencyHistogram()
	
	for i := 1; i <= 100; i++ {
		h.Record(int64(i))
	}
	
	p50 := h.P50()
	if p50 < 45 || p50 > 55 {
		t.Errorf("expected P50 around 50, got %.2f", p50)
	}
	
	p90 := h.P90()
	if p90 < 85 || p90 > 95 {
		t.Errorf("expected P90 around 90, got %.2f", p90)
	}
	
	p99 := h.P99()
	if p99 < 95 || p99 > 100 {
		t.Errorf("expected P99 around 99, got %.2f", p99)
	}
}

func TestHistogramPercentileEmpty(t *testing.T) {
	h := NewLatencyHistogram()
	if p := h.Percentile(50); p != 0 {
		t.Errorf("expected P50=0 for empty histogram, got %.2f", p)
	}
}

func TestHistogramAverage(t *testing.T) {
	h := NewLatencyHistogram()
	h.Record(100)
	h.Record(200)
	h.Record(300)
	
	avg := h.Average()
	if avg != 200 {
		t.Errorf("expected average=200, got %.2f", avg)
	}
}

func TestHistogramAverageEmpty(t *testing.T) {
	h := NewLatencyHistogram()
	if avg := h.Average(); avg != 0 {
		t.Errorf("expected average=0 for empty histogram, got %.2f", avg)
	}
}

func TestHistogramMerge(t *testing.T) {
	h1 := NewLatencyHistogram()
	h1.Record(100)
	h1.Record(200)
	
	h2 := NewLatencyHistogram()
	h2.Record(300)
	h2.Record(400)
	
	h1.Merge(h2)
	
	if h1.Count != 4 {
		t.Errorf("expected Count=4 after merge, got %d", h1.Count)
	}
	if h1.Sum != 1000 {
		t.Errorf("expected Sum=1000 after merge, got %d", h1.Sum)
	}
	if len(h1.Values) != 4 {
		t.Errorf("expected 4 values after merge, got %d", len(h1.Values))
	}
}

func TestHistogramMergeNil(t *testing.T) {
	h := NewLatencyHistogram()
	h.Record(100)
	h.Merge(nil)
	if h.Count != 1 {
		t.Errorf("merge with nil should not change histogram")
	}
}

func TestHistogramClone(t *testing.T) {
	h := NewLatencyHistogram()
	h.Record(100)
	h.Record(200)
	
	clone := h.Clone()
	
	if clone.Count != h.Count {
		t.Errorf("clone Count mismatch")
	}
	if clone.Sum != h.Sum {
		t.Errorf("clone Sum mismatch")
	}
	
	h.Record(300)
	if clone.Count == h.Count {
		t.Errorf("clone should be independent of original")
	}
}

func TestHistogramConcurrency(t *testing.T) {
	h := NewLatencyHistogram()
	var wg sync.WaitGroup
	
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(val int64) {
			defer wg.Done()
			h.Record(val)
		}(int64(i))
	}
	
	wg.Wait()
	
	if h.Count != 100 {
		t.Errorf("expected Count=100 after concurrent writes, got %d", h.Count)
	}
}

func BenchmarkHistogramRecord(b *testing.B) {
	h := NewLatencyHistogram()
	for i := 0; i < b.N; i++ {
		h.Record(int64(i % 5000))
	}
}

func BenchmarkHistogramPercentile(b *testing.B) {
	h := NewLatencyHistogram()
	for i := 0; i < 1000; i++ {
		h.Record(int64(i))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.P50()
		h.P90()
	}
}
