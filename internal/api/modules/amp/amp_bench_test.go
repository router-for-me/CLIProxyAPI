package amp

import (
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// These benchmarks exercise the read-path hot spots flagged for the Phase C
// atomic.Pointer[routingTable] refactor. After the refactor the reads are
// lock-free atomic loads — compare against the Phase A baseline that captured
// RWMutex contention numbers.
//
// We use a package-level sink to defeat the compiler's tendency to hoist
// invariant work out of the bench loop. Without it, the inlined post-refactor
// read path collapses to ~0 ns/op, which understates the production cost.

var benchBoolSink atomic.Uint32 //nolint:unused // populated below; placed in unused init to silence linter

func init() {
	// Use the variable in a way the compiler can't elide; the value is
	// irrelevant.
	_ = benchBoolSink.Load()
}

func BenchmarkAmpModule_ForceModelMappings_Read(b *testing.B) {
	m := New()
	m.updateState(func(rt *routingTable) {
		rt.lastConfig = &config.AmpCode{ForceModelMappings: true}
	})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if m.forceModelMappings() {
			benchBoolSink.Add(1)
		}
	}
}

func BenchmarkAmpModule_ForceModelMappings_Read_Parallel(b *testing.B) {
	m := New()
	m.updateState(func(rt *routingTable) {
		rt.lastConfig = &config.AmpCode{ForceModelMappings: true}
	})

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		var local uint32
		for pb.Next() {
			if m.forceModelMappings() {
				local++
			}
		}
		benchBoolSink.Add(local)
	})
}

func BenchmarkAmpModule_RestrictRead_Parallel(b *testing.B) {
	m := New()
	m.setRestrictToLocalhost(true)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		var local uint32
		for pb.Next() {
			if m.IsRestrictedToLocalhost() {
				local++
			}
		}
		benchBoolSink.Add(local)
	})
}
