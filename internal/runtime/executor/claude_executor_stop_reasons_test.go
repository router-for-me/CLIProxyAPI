package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// pause_turn end to end through the executor with the web search result cache:
// the first response pauses after one finished and one unanswered search; the
// client history keeps no results. The next upstream request must carry the
// finished search with its cached results and end with the paused search sent
// back without a result.
func TestClaudeExecutorPauseTurnContinuationRestoresFinishedSearchAndKeepsPausedOne(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "non-stream", true: "stream"}[stream], func(t *testing.T) {
			var (
				mu     sync.Mutex
				bodies [][]byte
			)
			paused := strings.Join([]string{
				`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg_p","type":"message","model":"claude-opus-5-5","role":"assistant","content":[],"usage":{"input_tokens":5,"output_tokens":0}}}`,
				`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_done","name":"web_search","input":{}}}`,
				`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"done query\"}"}}`,
				`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
				`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_done","content":[{"type":"web_search_result","url":"https://example.com/d","title":"D","encrypted_content":"ENC_DONE"}]}}`,
				`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":1}`,
				`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":2,"content_block":{"type":"server_tool_use","id":"srvtoolu_paused","name":"web_search","input":{}}}`,
				`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"paused query\"}"}}`,
				`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":2}`,
				`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"pause_turn"},"usage":{"output_tokens":3}}`,
				`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
			}, "\n\n") + "\n\n"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				bodies = append(bodies, body)
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(paused))
			}))
			defer server.Close()

			executor := NewClaudeExecutor(enabledClaudeWebSearchCacheConfig())
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-pause", "base_url": server.URL}}
			run := func(input string) ojson.Result {
				payload := []byte(`{"model":"claude-opus-5-5","stream":` + map[bool]string{false: "false", true: "true"}[stream] + `,"tools":[{"type":"web_search"}],"input":[` + input + `]}`)
				req := cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}
				opts := cliproxyexecutor.Options{
					SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse,
					Stream: stream, OriginalRequest: payload,
					Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "session-pause"},
				}
				if !stream {
					resp, errExecute := executor.Execute(context.Background(), auth, req, opts)
					if errExecute != nil {
						t.Fatal(errExecute)
					}
					return ojson.ParseBytes(resp.Payload)
				}
				result, errStream := executor.ExecuteStream(context.Background(), auth, req, opts)
				if errStream != nil {
					t.Fatal(errStream)
				}
				var terminal ojson.Result
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
					for _, line := range strings.Split(string(chunk.Payload), "\n") {
						if data, ok := strings.CutPrefix(line, "data: "); ok && ojson.Get(data, "type").String() == "response.completed" {
							terminal = ojson.Get(data, "response")
						}
					}
				}
				return terminal
			}

			first := run(`{"type":"message","role":"user","content":[{"type":"input_text","text":"research"}]}`)
			if first.Get("end_turn").Raw != "false" {
				t.Fatalf("first response end_turn = %q: %s", first.Get("end_turn").Raw, first.Raw)
			}
			// Codex history: output items without web_search_call results.
			history := []string{`{"type":"message","role":"user","content":[{"type":"input_text","text":"research"}]}`}
			for _, item := range first.Get("output").Array() {
				raw := item.Raw
				if item.Get("type").String() == "web_search_call" {
					raw = strings.Replace(raw, `,"results":`+item.Get("results").Raw, "", 1)
				}
				history = append(history, raw)
			}
			run(strings.Join(history, ","))

			mu.Lock()
			defer mu.Unlock()
			if len(bodies) != 2 {
				t.Fatalf("upstream requests = %d", len(bodies))
			}
			messages := ojson.GetBytes(bodies[1], "messages").Array()
			last := messages[len(messages)-1]
			blocks := last.Get("content").Array()
			var types []string
			for _, block := range blocks {
				types = append(types, block.Get("type").String())
			}
			if last.Get("role").String() != "assistant" || strings.Join(types, ",") != "server_tool_use,web_search_tool_result,server_tool_use" {
				t.Fatalf("continuation assistant blocks = %v: %s", types, bodies[1])
			}
			if got := blocks[1].Get("content.0.encrypted_content").String(); got != "ENC_DONE" {
				t.Fatalf("finished search results not restored: %s", blocks[1].Raw)
			}
			if blocks[2].Get("id").String() != "srvtoolu_paused" || blocks[2].Get("input.query").String() != "paused query" {
				t.Fatalf("paused search = %s", blocks[2].Raw)
			}
		})
	}
}

func TestClaudeExecutorChatContextWindowExceededMapsToLength(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"model\":\"claude-opus-5-5\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":5,\"output_tokens\":0}}}\n\n" +
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"cut\"}}\n\n" +
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"model_context_window_exceeded\"},\"usage\":{\"output_tokens\":2}}\n\n" +
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{ID: "ctx-window", Attributes: map[string]string{"api_key": "sk-ant-test", "base_url": server.URL}}
	payload := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hello"}]}`)
	resp, errExecute := NewClaudeExecutor(&config.Config{}).Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "claude-opus-5-5", Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: payload})
	if errExecute != nil {
		t.Fatalf("Execute err = %v, want nil", errExecute)
	}
	if got := ojson.GetBytes(resp.Payload, "choices.0.finish_reason").String(); got != "length" {
		t.Fatalf("finish_reason = %q, want length: %s", got, resp.Payload)
	}
	if got := ojson.GetBytes(resp.Payload, "choices.0.message.content").String(); got != "cut" {
		t.Fatalf("content = %q, want cut: %s", got, resp.Payload)
	}
	if got := ojson.GetBytes(resp.Payload, "usage.completion_tokens").Int(); got != 2 {
		t.Fatalf("completion_tokens = %d, want 2: %s", got, resp.Payload)
	}
}

func TestClaudeExecutorChatRefusalStaysSuccessful(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"model\":\"claude-opus-5-5\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":5,\"output_tokens\":0}}}\n\n" +
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"no\"}}\n\n" +
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"refusal\"},\"usage\":{\"output_tokens\":2}}\n\n" +
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{ID: "refusal", Attributes: map[string]string{"api_key": "sk-ant-test", "base_url": server.URL}}
	payload := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hello"}]}`)
	resp, errExecute := NewClaudeExecutor(&config.Config{}).Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "claude-opus-5-5", Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: payload})
	if errExecute != nil {
		t.Fatal(errExecute)
	}
	if got := ojson.GetBytes(resp.Payload, "choices.0.finish_reason").String(); got != "content_filter" {
		t.Fatalf("finish_reason = %q: %s", got, resp.Payload)
	}
}

func TestWrapClaudeFastRequestErrorPreservesClassifiedStatusCode(t *testing.T) {
	inner := statusErr{code: http.StatusBadRequest, msg: "claude refusal (cyber): blocked"}
	wrapped := wrapClaudeFastRequestError(true, http.StatusOK, inner)
	var statusCoder interface{ StatusCode() int }
	if !errors.As(wrapped, &statusCoder) || statusCoder == nil {
		t.Fatalf("expected wrapped error to implement StatusCode()")
	}
	if got := statusCoder.StatusCode(); got != http.StatusBadRequest {
		t.Fatalf("statusCoder.StatusCode() = %d, want %d", got, http.StatusBadRequest)
	}
}

func TestClaudeExecutorExecuteReturnsBadRequestOnPolicyRefusal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"claude-sonnet-5-5","id":"msg_cyber","type":"message","role":"assistant","content":[],"stop_reason":"refusal","stop_sequence":null,"stop_details":{"type":"refusal","category":"cyber","explanation":"This request triggered restrictions on violative cyber content and was blocked under Anthropic's Usage Policy. To learn more, see https://platform.claude.com/docs/en/build-with-claude/refusals-and-fallback."},"usage":{"input_tokens":4,"output_tokens":0}}`))
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{ID: "cyber-refusal", Attributes: map[string]string{"api_key": "sk-ant-test", "base_url": server.URL}}
	payload := []byte(`{"model":"claude-sonnet-5-5","stream":false,"messages":[{"role":"user","content":"test"}]}`)
	_, errExecute := NewClaudeExecutor(&config.Config{}).Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "claude-sonnet-5-5", Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: payload})
	if errExecute == nil {
		t.Fatal("expected policy refusal error, got nil")
	}
	var statusCoder interface{ StatusCode() int }
	if !errors.As(errExecute, &statusCoder) || statusCoder == nil || statusCoder.StatusCode() != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400 status error, got %v", errExecute)
	}
}

func TestClaudeExecutorStreamSparseStopDetailsPreservesMessageStop(t *testing.T) {
	sseBody := strings.Join([]string{
		`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg_sparse","type":"message","model":"claude-sonnet-5-5","role":"assistant","content":[],"usage":{"input_tokens":4,"output_tokens":0}}}`,
		`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"refusal","stop_details":{"type":"refusal"}},"usage":{"output_tokens":0}}`,
		`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
	}, "\n\n") + "\n\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sseBody))
	}))
	defer server.Close()

	for _, tc := range []struct {
		name           string
		sourceFormat   sdktranslator.Format
		responseFormat sdktranslator.Format
		payload        string
		wantStopMarker string
	}{
		{
			name:           "passthrough_claude",
			sourceFormat:   sdktranslator.FormatClaude,
			responseFormat: sdktranslator.FormatClaude,
			payload:        `{"model":"claude-sonnet-5-5","stream":true,"messages":[{"role":"user","content":"test"}]}`,
			wantStopMarker: "message_stop",
		},
		{
			name:           "translated_openai",
			sourceFormat:   sdktranslator.FormatOpenAI,
			responseFormat: sdktranslator.FormatOpenAI,
			payload:        `{"model":"claude-sonnet-5-5","stream":true,"messages":[{"role":"user","content":"test"}]}`,
			wantStopMarker: "content_filter",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := &cliproxyauth.Auth{ID: "sparse-refusal-" + tc.name, Attributes: map[string]string{"api_key": "sk-ant-test", "base_url": server.URL}}
			result, errStream := NewClaudeExecutor(&config.Config{}).ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "claude-sonnet-5-5",
				Payload: []byte(tc.payload),
			}, cliproxyexecutor.Options{
				SourceFormat:    tc.sourceFormat,
				ResponseFormat:  tc.responseFormat,
				Stream:          true,
				OriginalRequest: []byte(tc.payload),
			})
			if errStream != nil {
				t.Fatalf("ExecuteStream() error = %v", errStream)
			}

			var output strings.Builder
			var streamErr error
			for chunk := range result.Chunks {
				if len(chunk.Payload) > 0 {
					output.Write(chunk.Payload)
				}
				if chunk.Err != nil {
					streamErr = chunk.Err
				}
			}
			if !strings.Contains(output.String(), tc.wantStopMarker) {
				t.Fatalf("expected %q in stream output before refusal error, got: %s", tc.wantStopMarker, output.String())
			}
			var statusCoder interface{ StatusCode() int }
			if !errors.As(streamErr, &statusCoder) || statusCoder == nil || statusCoder.StatusCode() != http.StatusBadRequest {
				t.Fatalf("expected HTTP 400 refusal error, got %v", streamErr)
			}
		})
	}
}
