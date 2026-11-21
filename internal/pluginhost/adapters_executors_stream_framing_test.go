package pluginhost

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func executorFramingTestRequest(target sdktranslator.Format) preparedExecutorCall {
	request := []byte(`{"model":"cursor-test","stream":true,"max_tokens":256,"messages":[{"role":"user","content":"Reply with pong"}]}`)
	return preparedExecutorCall{
		req:             coreexecutor.Request{Model: "cursor-test", Payload: request},
		opts:            coreexecutor.Options{OriginalRequest: request},
		outputFormat:    sdktranslator.FormatOpenAI,
		requestedFormat: target,
	}
}

func executorFramingTestPayloads() []string {
	return []string{
		`{"id":"chatcmpl-test","created":1700000000,"model":"cursor-test","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`{"id":"chatcmpl-test","created":1700000000,"model":"cursor-test","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"pong"}}]}`,
		`{"id":"chatcmpl-test","created":1700000000,"model":"cursor-test","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":1,"total_tokens":8}}`,
	}
}

func runExecutorFramingTest(t *testing.T, target sdktranslator.Format, payloads []string) string {
	t.Helper()
	input := make(chan pluginapi.ExecutorStreamChunk, len(payloads))
	for _, payload := range payloads {
		input <- pluginapi.ExecutorStreamChunk{Payload: []byte(payload)}
	}
	close(input)
	adapter := &executorAdapter{}
	var output strings.Builder
	for chunk := range adapter.translateExecutorStreamChunks(context.Background(), executorFramingTestRequest(target), input) {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		output.Write(chunk.Payload)
	}
	return output.String()
}

func TestExecutorAdapterOpenAIChunksToClaudePreserveContentAndUsage(t *testing.T) {
	for _, framing := range []string{"json", "sse", "json_done", "sse_done", "whitespace", "usage_only", "usage_without_choices"} {
		t.Run(framing, func(t *testing.T) {
			payloads := executorFramingTestPayloads()
			if framing == "usage_only" || framing == "usage_without_choices" {
				payloads[2] = `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
				trailer := `{"usage":{"prompt_tokens":7,"completion_tokens":1,"total_tokens":8}}`
				if framing == "usage_only" {
					trailer = `{"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":1,"total_tokens":8}}`
				}
				payloads = append(payloads, trailer)
			}
			if strings.HasPrefix(framing, "sse") {
				for i, payload := range payloads {
					payloads[i] = "data: " + payload + "\n\n"
				}
			}
			if framing == "whitespace" {
				for i, payload := range payloads {
					payloads[i] = " \n" + payload + "\t "
				}
			}
			if framing == "json_done" {
				payloads = append(payloads, "[DONE]")
			} else if framing == "sse_done" {
				payloads = append(payloads, "data: [DONE]\n\n")
			}
			body := runExecutorFramingTest(t, sdktranslator.FormatClaude, payloads)
			for _, want := range []string{`"text":"pong"`, `"input_tokens":7`, `"output_tokens":1`, `"stop_reason":"end_turn"`} {
				if !strings.Contains(body, want) {
					t.Errorf("Claude stream missing %s: %s", want, body)
				}
			}
			for _, event := range []string{"message_start", "message_delta", "message_stop"} {
				if got := strings.Count(body, "event: "+event+"\n"); got != 1 {
					t.Errorf("%s count = %d, want 1: %s", event, got, body)
				}
			}
		})
	}
}

func TestExecutorAdapterOpenAIChunksToResponsesMatchFramedInput(t *testing.T) {
	raw := executorFramingTestPayloads()
	framed := make([]string, len(raw))
	for i, payload := range raw {
		framed[i] = "data: " + payload + "\n\n"
	}
	got := runExecutorFramingTest(t, sdktranslator.FormatOpenAIResponse, raw)
	want := runExecutorFramingTest(t, sdktranslator.FormatOpenAIResponse, framed)
	if got != want {
		t.Fatalf("Responses output differs by input framing:\nraw: %s\nframed: %s", got, want)
	}
	if !strings.Contains(got, "response.completed") || !strings.Contains(got, `"pong"`) {
		t.Fatalf("Responses output incomplete: %s", got)
	}
}

func TestExecutorAdapterSameFormatOpenAIChunksRemainUnchanged(t *testing.T) {
	payload := []byte(`{"choices":[{"delta":{"content":"pong"}}]}`)
	adapter := &executorAdapter{}
	prepared := preparedExecutorCall{
		outputFormat:    sdktranslator.FormatOpenAI,
		requestedFormat: sdktranslator.FormatOpenAI,
	}
	var state any
	frames := adapter.translateExecutorStreamPayload(context.Background(), prepared, payload, &state)
	if len(frames) != 1 || !bytes.Equal(frames[0], payload) {
		t.Fatalf("same-format payload changed: %q", frames)
	}
}

func TestExecutorOpenAIStreamTranslationPayload(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"json", `{"choices":[]}`, `data: {"choices":[]}`},
		{"usage", `{"usage":{"prompt_tokens":7}}`, `data: {"usage":{"prompt_tokens":7}}`},
		{"error", `{"error":{"message":"unavailable"}}`, `{"error":{"message":"unavailable"}}`},
		{"null_error", `{"error":null,"choices":[]}`, `data: {"error":null,"choices":[]}`},
		{"done", " \n[DONE]\t", "data: [DONE]"},
		{"sse", "data: {}\n\n", "data: {}\n\n"},
		{"event", "event: chunk\ndata: {}\n\n", "event: chunk\ndata: {}\n\n"},
		{"id", "id: 1\n", "id: 1\n"},
		{"comment", ": heartbeat\n\n", ": heartbeat\n\n"},
		{"malformed", `{"choices":`, `{"choices":`},
		{"multiple_objects", "{}\n{}", "{}\n{}"},
		{"array", `[]`, `[]`},
		{"unknown", "unknown", "unknown"},
		{"empty", " \n", " \n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := []byte(tc.input)
			got := executorOpenAIStreamTranslationPayload(input)
			if string(got) != tc.want {
				t.Fatalf("framed payload = %q, want %q", got, tc.want)
			}
			if string(input) != tc.input {
				t.Fatal("input payload was mutated")
			}
		})
	}
}

func TestExecutorAdapterOpenAIToClaudeHooksReceiveBuiltinCompatibleFraming(t *testing.T) {
	var seen []byte
	host := newHostWithRecords(capabilityRecord{
		id: "response-observer",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ResponseBeforeTranslator: responseNormalizerFunc(func(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
				seen = bytes.Clone(req.Body)
				return pluginapi.PayloadResponse{Body: req.Body}, nil
			}),
		}},
	})
	sdktranslator.SetPluginHooks(host)
	t.Cleanup(func() { sdktranslator.SetPluginHooks(nil) })
	payload := []byte(executorFramingTestPayloads()[1])
	adapter := &executorAdapter{host: host}
	var state any
	adapter.translateExecutorStreamPayload(context.Background(), executorFramingTestRequest(sdktranslator.FormatClaude), payload, &state)
	if !bytes.Equal(seen, append([]byte("data: "), payload...)) {
		t.Fatalf("response hook saw %q, want built-in-compatible SSE data framing", seen)
	}
}

func TestExecutorAdapterOpenAIPluginOnlyTranslationKeepsRawPayload(t *testing.T) {
	target := sdktranslator.Format("raw-plugin-target")
	payload := []byte(executorFramingTestPayloads()[1])
	var seen []byte
	host := newHostWithRecords(capabilityRecord{
		id: "custom-response-translator",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ResponseTranslator: responseTranslatorFunc(func(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
				seen = bytes.Clone(req.Body)
				return pluginapi.PayloadResponse{}, nil
			}),
		}},
	})
	sdktranslator.SetPluginHooks(host)
	t.Cleanup(func() { sdktranslator.SetPluginHooks(nil) })
	adapter := &executorAdapter{host: host}
	var state any
	frames := adapter.translateExecutorStreamPayload(context.Background(), executorFramingTestRequest(target), payload, &state)
	if !bytes.Equal(seen, payload) || len(frames) != 1 || !bytes.Equal(frames[0], payload) {
		t.Fatalf("plugin-only fallback changed: hook %q, frames %q", seen, frames)
	}
}

func TestExecutorAdapterStreamFramingPreservesErrorsAndCancellation(t *testing.T) {
	adapter := &executorAdapter{}
	prepared := executorFramingTestRequest(sdktranslator.FormatClaude)
	upstreamError := errors.New("upstream unavailable")
	input := make(chan pluginapi.ExecutorStreamChunk, 1)
	input <- pluginapi.ExecutorStreamChunk{Err: upstreamError}
	close(input)
	gotError := false
	for chunk := range adapter.translateExecutorStreamChunks(context.Background(), prepared, input) {
		if chunk.Err != nil {
			if chunk.Err != upstreamError {
				t.Fatalf("stream error = %v, want original error", chunk.Err)
			}
			gotError = true
		}
	}
	if !gotError {
		t.Fatal("upstream stream error disappeared")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := <-adapter.translateExecutorStreamChunks(ctx, prepared, make(chan pluginapi.ExecutorStreamChunk)); ok {
		t.Fatal("canceled stream emitted a chunk")
	}
}
