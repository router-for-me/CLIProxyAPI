// Package internal contains memory leak reproduction tests.
// Run with: go test -v -run TestMemoryLeak -memprofile=mem.prof ./internal/
package internal

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func getMemStats() (allocMB, heapMB float64) {
	var m runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m)
	return float64(m.Alloc) / 1024 / 1024, float64(m.HeapAlloc) / 1024 / 1024
}

func TestMemoryLeak_UsageStats(t *testing.T) {
	// This test simulates the usage statistics leak where Details grows unbounded
	stats := usage.NewRequestStatistics()

	allocBefore, heapBefore := getMemStats()
	t.Logf("Before: Alloc=%.2f MB, Heap=%.2f MB", allocBefore, heapBefore)

	// Simulate 10k requests (would happen over hours/days in production)
	numRequests := 10000
	for i := 0; i < numRequests; i++ {
		stats.Record(context.Background(), coreusage.Record{
			Provider:    "test-provider",
			Model:       fmt.Sprintf("model-%d", i%10), // 10 different models
			APIKey:      fmt.Sprintf("api-key-%d", i%5),
			RequestedAt: time.Now(),
			Detail: coreusage.Detail{
				InputTokens:  1000,
				OutputTokens: 500,
				TotalTokens:  1500,
			},
		})
	}

	allocAfter, heapAfter := getMemStats()
	t.Logf("After %d requests: Alloc=%.2f MB, Heap=%.2f MB", numRequests, allocAfter, heapAfter)
	t.Logf("Growth: Alloc=+%.2f MB, Heap=+%.2f MB", allocAfter-allocBefore, heapAfter-heapBefore)

	// Verify the cap is working - check snapshot
	snapshot := stats.Snapshot()
	for apiName, apiSnap := range snapshot.APIs {
		for modelName, modelSnap := range apiSnap.Models {
			if len(modelSnap.Details) > 1000 {
				t.Errorf("LEAK: API %s Model %s has %d details (should be <= 1000)",
					apiName, modelName, len(modelSnap.Details))
			} else {
				t.Logf("OK: API %s Model %s has %d details (capped at 1000)",
					apiName, modelName, len(modelSnap.Details))
			}
		}
	}
}

func TestMemoryLeak_SignatureCache(t *testing.T) {
	// This test simulates the signature cache leak where sessions accumulate
	allocBefore, heapBefore := getMemStats()
	t.Logf("Before: Alloc=%.2f MB, Heap=%.2f MB", allocBefore, heapBefore)

	// Simulate 1000 unique sessions (each with signatures)
	numSessions := 1000
	sigText := string(make([]byte, 100)) // 100 byte signature text
	sig := string(make([]byte, 200))     // 200 byte signature (> MinValidSignatureLen)

	for i := 0; i < numSessions; i++ {
		sessionID := fmt.Sprintf("session-%d", i)
		// Each session caches 50 signatures
		for j := 0; j < 50; j++ {
			text := fmt.Sprintf("%s-text-%d", sigText, j)
			signature := fmt.Sprintf("%s-sig-%d", sig, j)
			cache.CacheSignature(sessionID, text, signature)
		}
	}

	allocAfter, heapAfter := getMemStats()
	t.Logf("After %d sessions x 50 sigs: Alloc=%.2f MB, Heap=%.2f MB",
		numSessions, allocAfter, heapAfter)
	t.Logf("Growth: Alloc=+%.2f MB, Heap=+%.2f MB", allocAfter-allocBefore, heapAfter-heapBefore)

	// Clear all and check memory drops
	cache.ClearSignatureCache("")
	runtime.GC()

	allocCleared, heapCleared := getMemStats()
	t.Logf("After clear: Alloc=%.2f MB, Heap=%.2f MB", allocCleared, heapCleared)
	t.Logf("Recovered: Alloc=%.2f MB, Heap=%.2f MB",
		allocAfter-allocCleared, heapAfter-heapCleared)

	if allocCleared > allocBefore*1.5 {
		t.Logf("WARNING: Memory not fully recovered after clear (may indicate leak)")
	}
}

func TestMemoryLeak_SimulateProductionLoad(t *testing.T) {
	// Simulate realistic production load pattern over time
	stats := usage.NewRequestStatistics()

	t.Log("=== Simulating production load pattern ===")

	// Phase 1: Ramp up
	allocStart, _ := getMemStats()
	t.Logf("Start: %.2f MB", allocStart)

	// Simulate 1 hour of traffic (compressed into fast iterations)
	// Real: ~1000 req/min = 60k/hour
	// Test: 60k requests
	for hour := 0; hour < 3; hour++ {
		for i := 0; i < 20000; i++ {
			stats.Record(context.Background(), coreusage.Record{
				Provider:    "antigravity",
				Model:       fmt.Sprintf("gemini-2.5-pro-%d", i%5),
				APIKey:      fmt.Sprintf("user-%d", i%100),
				RequestedAt: time.Now(),
				Detail: coreusage.Detail{
					InputTokens:  int64(1000 + i%500),
					OutputTokens: int64(200 + i%100),
					TotalTokens:  int64(1200 + i%600),
				},
			})
		}
		allocNow, _ := getMemStats()
		t.Logf("Hour %d: %.2f MB (growth: +%.2f MB)", hour+1, allocNow, allocNow-allocStart)
	}

	allocEnd, _ := getMemStats()
	totalGrowth := allocEnd - allocStart

	// With the fix, growth should be bounded
	// Without fix: would grow linearly with requests
	// With fix: should plateau around 1000 details * num_models * detail_size
	t.Logf("Total growth over 60k requests: %.2f MB", totalGrowth)

	// Rough estimate: 1000 details * 5 models * 100 APIs * ~200 bytes = ~100MB max
	// Should be well under 50MB for this test
	if totalGrowth > 100 {
		t.Errorf("POTENTIAL LEAK: Growth of %.2f MB is too high for bounded storage", totalGrowth)
	} else {
		t.Logf("OK: Memory growth is bounded at %.2f MB", totalGrowth)
	}
}
