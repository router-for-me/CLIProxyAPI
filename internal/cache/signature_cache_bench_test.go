package cache

import (
	"fmt"
	"testing"
)

const benchSignature = "validBenchmarkSignature_ABCDEFGHIJKLMNOPQRSTUVWXYZ_0123456789_padded_to_meet_min_length"

func BenchmarkSignatureCache_Get_Hit(b *testing.B) {
	ClearSignatureCache("")
	text := "thinking text for benchmark"
	CacheSignature("claude-sonnet-4-5", text, benchSignature)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = GetCachedSignature("claude-sonnet-4-5", text)
	}
}

func BenchmarkSignatureCache_Get_Miss(b *testing.B) {
	ClearSignatureCache("")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = GetCachedSignature("claude-sonnet-4-5", "no-such-text")
	}
}

func BenchmarkSignatureCache_Get_GeminiSentinelMiss(b *testing.B) {
	ClearSignatureCache("")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = GetCachedSignature("gemini-3-pro-preview", "no-such-text")
	}
}

func BenchmarkSignatureCache_Set(b *testing.B) {
	ClearSignatureCache("")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		text := fmt.Sprintf("text-%d", i)
		CacheSignature("claude-sonnet-4-5", text, benchSignature)
	}
}

func BenchmarkSignatureCache_Get_Hit_Parallel(b *testing.B) {
	ClearSignatureCache("")
	text := "thinking text for benchmark"
	CacheSignature("claude-sonnet-4-5", text, benchSignature)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = GetCachedSignature("claude-sonnet-4-5", text)
		}
	})
}

func BenchmarkSignatureCache_Mixed_ReadHeavy_Parallel(b *testing.B) {
	ClearSignatureCache("")
	for i := 0; i < 32; i++ {
		CacheSignature("claude-sonnet-4-5", fmt.Sprintf("seed-%d", i), benchSignature)
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		var i int
		for pb.Next() {
			i++
			if i%32 == 0 {
				CacheSignature("claude-sonnet-4-5", fmt.Sprintf("write-%d", i), benchSignature)
			} else {
				_ = GetCachedSignature("claude-sonnet-4-5", fmt.Sprintf("seed-%d", i%32))
			}
		}
	})
}
