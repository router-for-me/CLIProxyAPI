package amp

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// These benchmarks exercise the read-path RWMutex hot spots flagged for the
// Phase C atomic.Pointer[routingTable] refactor. The current implementation
// holds three RWMutexes (proxyMu, restrictMu, configMu); after Phase C the
// reads should be lock-free atomic loads.

func BenchmarkAmpModule_ForceModelMappings_Read(b *testing.B) {
	m := New()
	m.lastConfig = &config.AmpCode{ForceModelMappings: true}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.forceModelMappings()
	}
}

func BenchmarkAmpModule_ForceModelMappings_Read_Parallel(b *testing.B) {
	m := New()
	m.lastConfig = &config.AmpCode{ForceModelMappings: true}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = m.forceModelMappings()
		}
	})
}

func BenchmarkAmpModule_RestrictRead_Parallel(b *testing.B) {
	m := New()
	m.restrictMu.Lock()
	m.restrictToLocalhost = true
	m.restrictMu.Unlock()

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			m.restrictMu.RLock()
			_ = m.restrictToLocalhost
			m.restrictMu.RUnlock()
		}
	})
}
