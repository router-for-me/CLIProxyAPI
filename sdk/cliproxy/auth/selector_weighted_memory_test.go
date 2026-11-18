package auth

import (
	"fmt"
	"runtime"
	"testing"
)

func BenchmarkWeightedPoolReserve1000(b *testing.B) {
	for _, bound := range []bool{false, true} {
		b.Run(fmt.Sprintf("bound=%t", bound), func(b *testing.B) {
			auths := make([]*Auth, 1000)
			for i := range auths {
				auths[i] = &Auth{ID: fmt.Sprintf("%036d", i)}
			}
			selector := &WeightedRoundRobinSelector{}
			_, warmup := selector.reserve("codex:model", auths, "")
			warmup.finish(true)
			b.ReportAllocs()
			b.ResetTimer()
			for i := range b.N {
				boundID := ""
				if bound {
					boundID = auths[i%len(auths)].ID
				}
				_, reservation := selector.reserve("codex:model", auths, boundID)
				reservation.finish(true)
			}
		})
	}
}

// BenchmarkWeightedPoolRetainedMemory measures live cache growth separately from
// total heap and allocation churn. Each retry subset omits a different credential.
func BenchmarkWeightedPoolRetainedMemory(b *testing.B) {
	const members = 1000
	auths := make([]*Auth, members)
	for i := range auths {
		auths[i] = &Auth{ID: fmt.Sprintf("%036d", i)}
	}
	subset := make([]*Auth, 0, members-1)
	var retained, heap, allocated int64
	b.ResetTimer()
	for range b.N {
		selector := &WeightedRoundRobinSelector{}
		_, warmup := selector.reserve("codex:model", auths, "")
		warmup.finish(true)
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		for excluded := range auths {
			subset = append(subset[:0], auths[:excluded]...)
			subset = append(subset, auths[excluded+1:]...)
			_, reservation := selector.reserve("codex:model", subset, "")
			reservation.finish(true)
		}
		runtime.GC()
		runtime.ReadMemStats(&after)
		if len(selector.states) != members+1 {
			b.Fatalf("cached pools = %d, want %d", len(selector.states), members+1)
		}
		runtime.KeepAlive(selector)
		runtime.KeepAlive(auths)
		retained += int64(after.HeapAlloc) - int64(before.HeapAlloc)
		heap += int64(after.HeapAlloc)
		allocated += int64(after.TotalAlloc - before.TotalAlloc)
	}
	b.ReportMetric(float64(retained)/float64(b.N), "retained-B/op")
	b.ReportMetric(float64(heap)/float64(b.N), "heap-total-B/op")
	b.ReportMetric(float64(allocated)/float64(b.N), "alloc-total-B/op")
}
