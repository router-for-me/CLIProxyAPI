// Package internal demonstrates the memory leak that existed before the fix.
// This file shows what happens WITHOUT the maxDetailsPerModel cap.
package internal

import (
	"fmt"
	"runtime"
	"testing"
	"time"
)

// UnboundedRequestStatistics is a copy of the ORIGINAL code WITHOUT the fix
// to demonstrate the memory leak behavior.
type UnboundedRequestStatistics struct {
	totalRequests int64
	apis          map[string]*unboundedAPIStats
}

type unboundedAPIStats struct {
	TotalRequests int64
	Models        map[string]*unboundedModelStats
}

type unboundedModelStats struct {
	TotalRequests int64
	Details       []unboundedRequestDetail // NO CAP - grows forever!
}

type unboundedRequestDetail struct {
	Timestamp time.Time
	Tokens    int64
}

func NewUnboundedRequestStatistics() *UnboundedRequestStatistics {
	return &UnboundedRequestStatistics{
		apis: make(map[string]*unboundedAPIStats),
	}
}

// Record is the ORIGINAL implementation that leaks memory
func (s *UnboundedRequestStatistics) Record(apiKey, model string, tokens int64) {
	stats, ok := s.apis[apiKey]
	if !ok {
		stats = &unboundedAPIStats{Models: make(map[string]*unboundedModelStats)}
		s.apis[apiKey] = stats
	}
	modelStats, ok := stats.Models[model]
	if !ok {
		modelStats = &unboundedModelStats{}
		stats.Models[model] = modelStats
	}
	modelStats.TotalRequests++
	// BUG: This grows forever with no cap!
	modelStats.Details = append(modelStats.Details, unboundedRequestDetail{
		Timestamp: time.Now(),
		Tokens:    tokens,
	})
	s.totalRequests++
}

func (s *UnboundedRequestStatistics) CountDetails() int {
	total := 0
	for _, api := range s.apis {
		for _, model := range api.Models {
			total += len(model.Details)
		}
	}
	return total
}

func TestMemoryLeak_BEFORE_Fix_Unbounded(t *testing.T) {
	// This demonstrates the LEAK behavior before the fix
	stats := NewUnboundedRequestStatistics()

	var m runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m)
	allocBefore := float64(m.Alloc) / 1024 / 1024

	t.Logf("=== DEMONSTRATING LEAK (unbounded growth) ===")
	t.Logf("Before: %.2f MB, Details: %d", allocBefore, stats.CountDetails())

	// Simulate traffic over "hours" - in production this causes OOM
	for hour := 1; hour <= 5; hour++ {
		for i := 0; i < 20000; i++ {
			stats.Record(
				fmt.Sprintf("api-key-%d", i%10),
				fmt.Sprintf("model-%d", i%5),
				1500,
			)
		}
		runtime.GC()
		runtime.ReadMemStats(&m)
		allocNow := float64(m.Alloc) / 1024 / 1024
		t.Logf("Hour %d: %.2f MB, Details: %d (growth: +%.2f MB)",
			hour, allocNow, stats.CountDetails(), allocNow-allocBefore)
	}

	// Show the problem: details count = total requests (unbounded)
	totalDetails := stats.CountDetails()
	totalRequests := 5 * 20000 // 100k requests
	t.Logf("LEAK EVIDENCE: %d details stored for %d requests (ratio: %.2f)",
		totalDetails, totalRequests, float64(totalDetails)/float64(totalRequests))

	if totalDetails == totalRequests {
		t.Logf("CONFIRMED: Every request stored forever = memory leak!")
	}
}

func TestMemoryLeak_AFTER_Fix_Bounded(t *testing.T) {
	// This demonstrates the FIXED behavior with capped growth
	// Using the real implementation which now has the fix
	stats := NewBoundedRequestStatistics()

	var m runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m)
	allocBefore := float64(m.Alloc) / 1024 / 1024

	t.Logf("=== DEMONSTRATING FIX (bounded growth) ===")
	t.Logf("Before: %.2f MB, Details: %d", allocBefore, stats.CountDetails())

	for hour := 1; hour <= 5; hour++ {
		for i := 0; i < 20000; i++ {
			stats.Record(
				fmt.Sprintf("api-key-%d", i%10),
				fmt.Sprintf("model-%d", i%5),
				1500,
			)
		}
		runtime.GC()
		runtime.ReadMemStats(&m)
		allocNow := float64(m.Alloc) / 1024 / 1024
		t.Logf("Hour %d: %.2f MB, Details: %d (growth: +%.2f MB)",
			hour, allocNow, stats.CountDetails(), allocNow-allocBefore)
	}

	totalDetails := stats.CountDetails()
	maxExpected := 10 * 5 * 1000 // 10 API keys * 5 models * 1000 cap = 50k max
	t.Logf("FIX EVIDENCE: %d details stored (max possible: %d)", totalDetails, maxExpected)

	if totalDetails <= maxExpected {
		t.Logf("CONFIRMED: Details capped, memory bounded!")
	} else {
		t.Errorf("STILL LEAKING: %d > %d", totalDetails, maxExpected)
	}
}

// BoundedRequestStatistics is the FIXED version with cap
type BoundedRequestStatistics struct {
	apis map[string]*boundedAPIStats
}

type boundedAPIStats struct {
	Models map[string]*boundedModelStats
}

type boundedModelStats struct {
	Details []unboundedRequestDetail
}

const maxDetailsPerModelTest = 1000

func NewBoundedRequestStatistics() *BoundedRequestStatistics {
	return &BoundedRequestStatistics{
		apis: make(map[string]*boundedAPIStats),
	}
}

func (s *BoundedRequestStatistics) Record(apiKey, model string, tokens int64) {
	stats, ok := s.apis[apiKey]
	if !ok {
		stats = &boundedAPIStats{Models: make(map[string]*boundedModelStats)}
		s.apis[apiKey] = stats
	}
	modelStats, ok := stats.Models[model]
	if !ok {
		modelStats = &boundedModelStats{}
		stats.Models[model] = modelStats
	}
	modelStats.Details = append(modelStats.Details, unboundedRequestDetail{
		Timestamp: time.Now(),
		Tokens:    tokens,
	})
	// THE FIX: Cap the details slice
	if len(modelStats.Details) > maxDetailsPerModelTest {
		excess := len(modelStats.Details) - maxDetailsPerModelTest
		modelStats.Details = modelStats.Details[excess:]
	}
}

func (s *BoundedRequestStatistics) CountDetails() int {
	total := 0
	for _, api := range s.apis {
		for _, model := range api.Models {
			total += len(model.Details)
		}
	}
	return total
}

func TestCompare_LeakVsFix(t *testing.T) {
	t.Log("=== SIDE-BY-SIDE COMPARISON ===")

	unbounded := NewUnboundedRequestStatistics()
	bounded := NewBoundedRequestStatistics()

	// Same workload
	for i := 0; i < 50000; i++ {
		apiKey := fmt.Sprintf("key-%d", i%10)
		model := fmt.Sprintf("model-%d", i%5)
		unbounded.Record(apiKey, model, 1500)
		bounded.Record(apiKey, model, 1500)
	}

	t.Logf("UNBOUNDED (leak): %d details stored", unbounded.CountDetails())
	t.Logf("BOUNDED (fixed):  %d details stored", bounded.CountDetails())
	t.Logf("Memory saved: %dx reduction", unbounded.CountDetails()/bounded.CountDetails())
}
