package handlers

import "testing"

var benchmarkStreamPayloadSink []byte

func BenchmarkStreamPayloadForHandler_OpenAIResponse(b *testing.B) {
	payload := []byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n")
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		out := streamPayloadForHandler("openai-response", payload)
		if len(out) != len(payload) {
			b.Fatalf("streamPayloadForHandler(): len=%d want %d", len(out), len(payload))
		}
		benchmarkStreamPayloadSink = out
	}
}

func BenchmarkStreamPayloadForHandler_Default(b *testing.B) {
	payload := []byte("event: message\ndata: hello\n\n")
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		out := streamPayloadForHandler("openai", payload)
		if len(out) != len(payload) {
			b.Fatalf("streamPayloadForHandler(): len=%d want %d", len(out), len(payload))
		}
		benchmarkStreamPayloadSink = out
	}
}

func BenchmarkValidateSSEDataJSON(b *testing.B) {
	chunk := []byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\",\"item_id\":\"item_123\"}\n\n")
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if err := validateSSEDataJSON(chunk); err != nil {
			b.Fatalf("validateSSEDataJSON(): %v", err)
		}
	}
}
