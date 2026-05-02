package logging

import (
	"testing"
	"time"
)

func BenchmarkFileRequestLogger_LogRequest(b *testing.B) {
	dir := b.TempDir()
	logger := NewFileRequestLogger(true, dir, "", 0)

	headers := map[string][]string{
		"Content-Type":  {"application/json"},
		"Authorization": {"Bearer redacted"},
		"User-Agent":    {"benchmark/1.0"},
	}
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}],"max_tokens":1024}`)
	response := []byte(`{"id":"msg_bench","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"model":"claude-sonnet-4-5","stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":2}}`)
	now := time.Now()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = logger.LogRequest(
			"/v1/messages",
			"POST",
			headers,
			body,
			200,
			headers,
			response,
			nil,
			nil,
			nil,
			nil,
			nil,
			"",
			now,
			now,
		)
	}
}

func BenchmarkFileRequestLogger_Disabled(b *testing.B) {
	dir := b.TempDir()
	logger := NewFileRequestLogger(false, dir, "", 0)

	headers := map[string][]string{"Content-Type": {"application/json"}}
	body := []byte(`{"model":"claude-sonnet-4-5"}`)
	response := []byte(`{"id":"msg_bench"}`)
	now := time.Now()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = logger.LogRequest(
			"/v1/messages",
			"POST",
			headers,
			body,
			200,
			headers,
			response,
			nil,
			nil,
			nil,
			nil,
			nil,
			"",
			now,
			now,
		)
	}
}
