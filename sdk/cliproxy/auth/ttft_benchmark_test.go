package auth

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type ttftBenchmarkStreamExecutor struct {
	id              string
	expectedNeedle  []byte
	minPayloadBytes int
}

func (e ttftBenchmarkStreamExecutor) Identifier() string { return e.id }

func (e ttftBenchmarkStreamExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e ttftBenchmarkStreamExecutor) ExecuteStream(_ context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if len(e.expectedNeedle) > 0 && !bytes.Contains(req.Payload, e.expectedNeedle) {
		return nil, &Error{Code: "invalid_request", Message: "missing expected previous_response_id in payload"}
	}
	if e.minPayloadBytes > 0 && len(req.Payload) < e.minPayloadBytes {
		return nil, &Error{Code: "invalid_request", Message: "payload too small for long-conversation ttft benchmark"}
	}
	ch := make(chan cliproxyexecutor.StreamChunk, 3)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"type":"response.created"}`)}
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"type":"response.output_text.delta","delta":"x"}`)}
	close(ch)
	return &cliproxyexecutor.StreamResult{Headers: http.Header{}, Chunks: ch}, nil
}

func (e ttftBenchmarkStreamExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e ttftBenchmarkStreamExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e ttftBenchmarkStreamExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func BenchmarkManagerExecuteStreamTTFT5000(b *testing.B) {
	manager, _, model := benchmarkManagerSetup(b, 5000, false, false)
	manager.executors["gemini"] = ttftBenchmarkStreamExecutor{id: "gemini"}

	ctx := context.Background()
	req := cliproxyexecutor.Request{Model: model}
	opts := cliproxyexecutor.Options{Stream: true}

	resultWarm, errWarm := manager.ExecuteStream(ctx, []string{"gemini"}, req, opts)
	if errWarm != nil {
		b.Fatalf("warmup ExecuteStream error = %v", errWarm)
	}
	for range resultWarm.Chunks {
	}

	var totalTTFT time.Duration

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		started := time.Now()
		result, errStream := manager.ExecuteStream(ctx, []string{"gemini"}, req, opts)
		if errStream != nil {
			b.Fatalf("ExecuteStream failed: %v", errStream)
		}

		seenDelta := false
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				b.Fatalf("stream chunk error = %v", chunk.Err)
			}
			if bytes.Contains(chunk.Payload, []byte(`"response.output_text.delta"`)) {
				totalTTFT += time.Since(started)
				seenDelta = true
				break
			}
		}
		if !seenDelta {
			b.Fatal("stream returned no delta chunk")
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(totalTTFT.Nanoseconds())/float64(b.N), "ttft-ns/op")
}

func BenchmarkManagerExecuteStreamTTFT5000_LongConversationPreviousResponseID(b *testing.B) {
	manager, _, model := benchmarkManagerSetup(b, 5000, false, false)
	manager.executors["gemini"] = ttftBenchmarkStreamExecutor{
		id:              "gemini",
		expectedNeedle:  []byte(`"previous_response_id":"resp-long-context-bench"`),
		minPayloadBytes: 64 << 10,
	}

	ctx := context.Background()
	req := newTTFTLongConversationRequest(model, "resp-long-context-bench")
	opts := cliproxyexecutor.Options{Stream: true}

	resultWarm, errWarm := manager.ExecuteStream(ctx, []string{"gemini"}, req, opts)
	if errWarm != nil {
		b.Fatalf("warmup ExecuteStream error = %v", errWarm)
	}
	for range resultWarm.Chunks {
	}

	var totalTTFT time.Duration

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		started := time.Now()
		result, errStream := manager.ExecuteStream(ctx, []string{"gemini"}, req, opts)
		if errStream != nil {
			b.Fatalf("ExecuteStream failed: %v", errStream)
		}

		seenDelta := false
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				b.Fatalf("stream chunk error = %v", chunk.Err)
			}
			if bytes.Contains(chunk.Payload, []byte(`"response.output_text.delta"`)) {
				totalTTFT += time.Since(started)
				seenDelta = true
				break
			}
		}
		if !seenDelta {
			b.Fatal("stream returned no delta chunk")
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(totalTTFT.Nanoseconds())/float64(b.N), "ttft-ns/op")
}

func newTTFTLongConversationRequest(model, previousResponseID string) cliproxyexecutor.Request {
	longText := "marker=ttft-long-conversation\n" + strings.Repeat("long conversation segment;", 4096)
	return cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(`{"model":"` + model + `","previous_response_id":"` + previousResponseID + `","input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"keep continuity"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"` + longText + `"}]}],"stream":true}`),
	}
}
