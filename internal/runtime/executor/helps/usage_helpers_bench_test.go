package helps

import (
	"bytes"
	"strings"
	"testing"
)

func BenchmarkObserveOpenAIStream_TextDelta(b *testing.B) {
	deltaChunk := []byte(`data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1700000000,"model":"gpt-5.4","choices":[{"index":0,"delta":{"content":"Hello world"},"finish_reason":null}]}`)
	finalChunk := []byte(`data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1700000000,"model":"gpt-5.4","service_tier":"default","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":120,"completion_tokens":45,"total_tokens":165,"prompt_tokens_details":{"cached_tokens":64,"text_tokens":120},"completion_tokens_details":{"reasoning_tokens":10,"text_tokens":35}}}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var buf StreamUsageBuffer
		for j := 0; j < 50; j++ {
			buf.ObserveOpenAIStream(deltaChunk)
		}
		buf.ObserveOpenAIStream(finalChunk)
	}
}

func BenchmarkObserveClaudeStream_TextDelta(b *testing.B) {
	msgStart := []byte(`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-4-6","usage":{"input_tokens":200,"cache_creation_input_tokens":100,"cache_read_input_tokens":50,"cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":0},"output_tokens":1,"service_tier":"standard"}}}`)
	deltaChunk := []byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello world"}}`)
	msgDelta := []byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":85,"server_tool_use":{"web_search_requests":1}}}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var buf StreamUsageBuffer
		buf.ObserveClaudeStream(msgStart)
		for j := 0; j < 50; j++ {
			buf.ObserveClaudeStream(deltaChunk)
		}
		buf.ObserveClaudeStream(msgDelta)
	}
}

func BenchmarkObserveGeminiStream_TextAndUsage(b *testing.B) {
	deltaChunk := []byte(`data: {"candidates":[{"content":{"parts":[{"text":"Go 1.24 was released in "}],"role":"model"},"index":0}],"usageMetadata":{"promptTokenCount":22,"candidatesTokenCount":12,"totalTokenCount":114,"promptTokensDetails":[{"modality":"TEXT","tokenCount":22}],"thoughtsTokenCount":80,"serviceTier":"standard"},"modelVersion":"gemini-3.8-flash"}`)
	finalChunk := []byte(`data: {"candidates":[{"content":{"parts":[{"text":"2025."}],"role":"model"},"finishReason":"STOP","index":0,"groundingMetadata":{"webSearchQueries":["Go 1.24 release year"],"groundingChunks":[{"web":{"uri":"https://go.dev/doc/devel/release","title":"Release History"}}]}}],"usageMetadata":{"promptTokenCount":103,"candidatesTokenCount":22,"totalTokenCount":286,"promptTokensDetails":[{"modality":"TEXT","tokenCount":103}],"thoughtsTokenCount":161,"serviceTier":"standard"},"modelVersion":"gemini-3.8-flash"}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var buf StreamUsageBuffer
		for j := 0; j < 10; j++ {
			buf.ObserveGeminiStream(deltaChunk)
		}
		buf.ObserveGeminiStream(finalChunk)
	}
}

func BenchmarkParseOpenAIImageUsage_LargeBase64(b *testing.B) {
	reqPayload := []byte(`{"model":"grok-imagine-image","prompt":"A minimal orange square","response_format":"b64_json","aspect_ratio":"16:9","resolution":"1k"}`)
	largeB64 := strings.Repeat("QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVowMTIzNDU2Nzg5", 30000) // ~1.44MB
	respPayload := []byte(`{"data":[{"b64_json":"` + largeB64 + `","mime_type":"image/jpeg"}],"usage":{"cost_in_usd_ticks":200000000}}`)
	b.SetBytes(int64(len(respPayload)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		detail := ParseOpenAIImageUsage(reqPayload, respPayload)
		if detail.ImageCount == nil || *detail.ImageCount != 1 {
			b.Fatal("expected image count 1")
		}
	}
}

func BenchmarkParseGeminiUsage_LargeBase64(b *testing.B) {
	reqPayload := []byte(`{"contents":[{"role":"user","parts":[{"text":"draw"}]}],"generationConfig":{"responseModalities":["TEXT","IMAGE"],"imageConfig":{"aspectRatio":"16:9","imageSize":"1K"}}}`)
	largeB64 := strings.Repeat("QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVowMTIzNDU2Nzg5", 30000) // ~1.44MB
	var buf bytes.Buffer
	buf.WriteString(`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/jpeg","data":"`)
	buf.WriteString(largeB64)
	buf.WriteString(`"}}],"role":"model"},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":1343,"totalTokenCount":1354,"promptTokensDetails":[{"modality":"TEXT","tokenCount":11}],"candidatesTokensDetails":[{"modality":"IMAGE","tokenCount":1120}],"serviceTier":"standard"}}`)
	respPayload := buf.Bytes()
	b.SetBytes(int64(len(respPayload)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		detail := ParseGeminiUsage(respPayload)
		ApplyGeminiRequestImageConfig(&detail, reqPayload)
		if detail.ImageCount == nil || *detail.ImageCount != 1 {
			b.Fatal("expected image count 1")
		}
	}
}
