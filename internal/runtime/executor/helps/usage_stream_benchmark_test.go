package helps

import "testing"

var (
	benchmarkOpenAIContentChunk = []byte(`data: {"choices":[{"delta":{"content":"hello"}}]}`)
	benchmarkOpenAITierChunk    = []byte(`data: {"service_tier":"default","choices":[]}`)
	benchmarkOpenAIUsageChunk   = []byte(`data: {"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30}}`)
)

func BenchmarkStreamUsageBufferObserveOpenAIStreamContentChunk(b *testing.B) {
	var buffer StreamUsageBuffer
	buffer.ObserveOpenAIStream(benchmarkOpenAITierChunk)
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		buffer.ObserveOpenAIStream(benchmarkOpenAIContentChunk)
	}
}

func BenchmarkStreamUsageBufferObserveOpenAIStream100Chunks(b *testing.B) {
	b.ReportAllocs()
	for index := 0; index < b.N; index++ {
		var buffer StreamUsageBuffer
		buffer.ObserveOpenAIStream(benchmarkOpenAITierChunk)
		for chunk := 0; chunk < 98; chunk++ {
			buffer.ObserveOpenAIStream(benchmarkOpenAIContentChunk)
		}
		buffer.ObserveOpenAIStream(benchmarkOpenAIUsageChunk)
	}
}
