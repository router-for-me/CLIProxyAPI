package executor

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorExecute_EmptyStreamCompletionOutputUsesOutputItemDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]},\"output_index\":0}\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":1775555723,\"status\":\"completed\",\"model\":\"gpt-5.4-mini-2026-03-17\",\"output\":[],\"usage\":{\"input_tokens\":8,\"output_tokens\":28,\"total_tokens\":36}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4-mini",
		Payload: []byte(`{"model":"gpt-5.4-mini","messages":[{"role":"user","content":"Say ok"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	gotContent := gjson.GetBytes(resp.Payload, "choices.0.message.content").String()
	if gotContent != "ok" {
		t.Fatalf("choices.0.message.content = %q, want %q; payload=%s", gotContent, "ok", string(resp.Payload))
	}
}

func TestCodexExecutorExecuteSurfacesTerminalStreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.created\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: error\n"))
		_, _ = w.Write([]byte(`data: {"type":"error","error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again.","param":"input"},"sequence_number":2}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.failed\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again."}}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err == nil {
		t.Fatal("expected terminal stream error, got nil")
	}
	if got := statusCodeFromTestError(t, err); got != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusBadRequest, err)
	}
	assertCodexErrorCode(t, err.Error(), "invalid_request_error", "context_too_large")
	if !strings.Contains(err.Error(), "Your input exceeds the context window") {
		t.Fatalf("error message missing upstream context text: %v", err)
	}
}

func TestCodexExecutorExecuteStreamSurfacesTerminalStreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.created\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: error\n"))
		_, _ = w.Write([]byte(`data: {"type":"error","error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again.","param":"input"},"sequence_number":2}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
			break
		}
	}
	if streamErr == nil {
		t.Fatal("missing stream terminal error")
	}
	if got := statusCodeFromTestError(t, streamErr); got != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusBadRequest, streamErr)
	}
	assertCodexErrorCode(t, streamErr.Error(), "invalid_request_error", "context_too_large")
}

func TestCodexTerminalStreamContextLengthErrFromResponseFailed(t *testing.T) {
	err, ok := codexTerminalStreamContextLengthErr([]byte(`{"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again."}}}`))
	if !ok {
		t.Fatal("expected context length terminal error")
	}
	if got := statusCodeFromTestError(t, err); got != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusBadRequest, err)
	}
	assertCodexErrorCode(t, err.Error(), "invalid_request_error", "context_too_large")
}

func TestCodexTerminalStreamContextLengthErrFromTopLevelError(t *testing.T) {
	err, ok := codexTerminalStreamContextLengthErr([]byte(`{"type":"error","code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again.","sequence_number":2}`))
	if !ok {
		t.Fatal("expected top-level context length terminal error")
	}
	if got := statusCodeFromTestError(t, err); got != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusBadRequest, err)
	}
	assertCodexErrorCode(t, err.Error(), "invalid_request_error", "context_too_large")
	if !strings.Contains(err.Error(), "Your input exceeds the context window") {
		t.Fatalf("error message missing upstream context text: %v", err)
	}
}

func TestCodexTerminalStreamContextLengthErrIgnoresOtherTerminalErrors(t *testing.T) {
	_, ok := codexTerminalStreamContextLengthErr([]byte(`{"type":"error","error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"Rate limit reached."}}`))
	if ok {
		t.Fatal("rate limit terminal error should not be handled by context length fix")
	}
}

func TestCodexTerminalStreamErrIgnoresRateLimitTerminalErrors(t *testing.T) {
	_, _, ok := codexTerminalStreamErr([]byte(`{"type":"error","error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"Rate limit reached."}}`))
	if ok {
		t.Fatal("rate limit terminal error should not be handled by replay terminal error path")
	}
}

func TestCodexTerminalStreamErrHandlesUsageLimitErrorEvent(t *testing.T) {
	streamErr, _, ok := codexTerminalStreamErr([]byte(`{"type":"error","error":{"type":"usage_limit_reached","message":"You've hit your usage limit.","resets_in_seconds":300}}`))
	if !ok {
		t.Fatal("expected usage_limit_reached terminal error to be handled")
	}
	if got := statusCodeFromTestError(t, streamErr); got != http.StatusTooManyRequests {
		t.Fatalf("status code = %d, want %d", got, http.StatusTooManyRequests)
	}
	retryAfter := streamErr.RetryAfter()
	if retryAfter == nil {
		t.Fatal("expected retryAfter from usage_limit_reached terminal error")
	}
	if *retryAfter != 300*time.Second {
		t.Fatalf("retryAfter = %v, want %v", *retryAfter, 300*time.Second)
	}
}

func TestCodexTerminalStreamErrHandlesUsageLimitResponseFailed(t *testing.T) {
	streamErr, _, ok := codexTerminalStreamErr([]byte(`{"type":"response.failed","response":{"error":{"type":"usage_limit_reached","message":"usage limit reached","resets_in_seconds":60}}}`))
	if !ok {
		t.Fatal("expected usage_limit_reached response.failed terminal error to be handled")
	}
	if got := statusCodeFromTestError(t, streamErr); got != http.StatusTooManyRequests {
		t.Fatalf("status code = %d, want %d", got, http.StatusTooManyRequests)
	}
	if streamErr.RetryAfter() == nil {
		t.Fatal("expected retryAfter from usage_limit_reached response.failed terminal error")
	}
}

func statusCodeFromTestError(t *testing.T, err error) int {
	t.Helper()

	statusErr, ok := err.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("error %T does not expose StatusCode(): %v", err, err)
	}
	return statusErr.StatusCode()
}

func TestCodexExecutorExecuteStream_EmptyStreamCompletionOutputUsesOutputItemDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]},\"output_index\":0}\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":1775555723,\"status\":\"completed\",\"model\":\"gpt-5.4-mini-2026-03-17\",\"output\":[],\"usage\":{\"input_tokens\":8,\"output_tokens\":28,\"total_tokens\":36}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4-mini",
		Payload: []byte(`{"model":"gpt-5.4-mini","input":"Say ok"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var completed []byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		payload := bytes.TrimSpace(chunk.Payload)
		if !bytes.HasPrefix(payload, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(payload[5:])
		if gjson.GetBytes(data, "type").String() == "response.completed" {
			completed = append([]byte(nil), data...)
		}
	}

	if len(completed) == 0 {
		t.Fatal("missing response.completed chunk")
	}

	gotContent := gjson.GetBytes(completed, "response.output.0.content.0.text").String()
	if gotContent != "ok" {
		t.Fatalf("response.output[0].content[0].text = %q, want %q; completed=%s", gotContent, "ok", string(completed))
	}
}

func TestPatchClaudeMessageStartInputUsage(t *testing.T) {
	chunk := []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":0,\"output_tokens\":0}}}\n\n")
	patched := patchClaudeMessageStartInputUsage(chunk, 42)
	if got := gjson.Get(firstClaudeSSEData(patched), "message.usage.input_tokens").Int(); got != 42 {
		t.Fatalf("input_tokens = %d, want 42; chunk=%s", got, patched)
	}
	preserved := patchClaudeMessageStartInputUsage(patched, 99)
	if got := gjson.Get(firstClaudeSSEData(preserved), "message.usage.input_tokens").Int(); got != 42 {
		t.Fatalf("existing positive input_tokens = %d, want 42", got)
	}
}

func TestCodexTerminalUsageAvailableRequiresInputTokens(t *testing.T) {
	if codexTerminalUsageAvailable([]byte(`{"response":{"usage":{}}}`)) {
		t.Fatal("empty usage object must not be authoritative")
	}
	if codexTerminalUsageAvailable([]byte(`{"response":{"usage":{"output_tokens":2}}}`)) {
		t.Fatal("output-only usage must not be authoritative for context accounting")
	}
	if !codexTerminalUsageAvailable([]byte(`{"response":{"usage":{"input_tokens":0,"output_tokens":0}}}`)) {
		t.Fatal("explicit zero input_tokens must be authoritative")
	}
}

func TestCodexClaudeTerminalUsageAndOrdering(t *testing.T) {
	tests := []struct {
		name           string
		event          string
		usage          string
		wantInput      int64
		wantOutput     int64
		wantCacheRead  int64
		wantCacheWrite int64
		wantStopReason string
	}{
		{name: "uncached", event: "response.completed", usage: `{"input_tokens":1666,"output_tokens":42}`, wantInput: 1666, wantOutput: 42, wantStopReason: "end_turn"},
		{name: "cached", event: "response.completed", usage: `{"input_tokens":210050,"output_tokens":42,"input_tokens_details":{"cached_tokens":208384}}`, wantInput: 1666, wantOutput: 42, wantCacheRead: 208384, wantStopReason: "end_turn"},
		{name: "cache creation", event: "response.completed", usage: `{"input_tokens":100,"output_tokens":7,"input_tokens_details":{"cached_tokens":30,"cache_write_tokens":40}}`, wantInput: 30, wantOutput: 7, wantCacheRead: 30, wantCacheWrite: 40, wantStopReason: "end_turn"},
		{name: "zero", event: "response.completed", usage: `{"input_tokens":0,"output_tokens":0}`, wantStopReason: "end_turn"},
		{name: "missing", event: "response.completed", usage: `null`, wantStopReason: "end_turn"},
		{name: "incomplete", event: "response.incomplete", usage: `{"input_tokens":11,"output_tokens":5}`, wantInput: 11, wantOutput: 5, wantStopReason: "max_tokens"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			responseFields := `"usage":` + tt.usage
			if tt.event == "response.incomplete" {
				responseFields += `,"incomplete_details":{"reason":"max_output_tokens"}`
			}
			var param any
			outputs := sdktranslator.TranslateStream(context.Background(), sdktranslator.FormatCodex, sdktranslator.FormatClaude, "gpt-5.6-sol", []byte(`{"messages":[]}`), nil, []byte(`data: {"type":"`+tt.event+`","response":{`+responseFields+`}}`), &param)
			streamBytes := bytes.Join(outputs, nil)
			streamBytes = patchClaudeTerminalUsage(streamBytes, []byte(`{"response":{"usage":`+tt.usage+`}}`))
			stream := string(streamBytes)
			delta, ok := firstClaudeEventData(stream, "message_delta")
			if !ok {
				t.Fatalf("missing message_delta: %s", stream)
			}
			if got := delta.Get("usage.input_tokens").Int(); got != tt.wantInput {
				t.Fatalf("input_tokens = %d, want %d", got, tt.wantInput)
			}
			if got := delta.Get("usage.output_tokens").Int(); got != tt.wantOutput {
				t.Fatalf("output_tokens = %d, want %d", got, tt.wantOutput)
			}
			if got := delta.Get("usage.cache_read_input_tokens").Int(); got != tt.wantCacheRead {
				t.Fatalf("cache_read_input_tokens = %d, want %d", got, tt.wantCacheRead)
			}
			if got := delta.Get("usage.cache_creation_input_tokens").Int(); got != tt.wantCacheWrite {
				t.Fatalf("cache_creation_input_tokens = %d, want %d", got, tt.wantCacheWrite)
			}
			if got := tt.wantInput + tt.wantCacheRead + tt.wantCacheWrite; got != gjson.Parse(tt.usage).Get("input_tokens").Int() {
				t.Fatalf("Claude input usage sum = %d, upstream input_tokens = %d", got, gjson.Parse(tt.usage).Get("input_tokens").Int())
			}
			if got := delta.Get("delta.stop_reason").String(); got != tt.wantStopReason {
				t.Fatalf("stop_reason = %q, want %q", got, tt.wantStopReason)
			}
			if strings.Index(stream, `"type":"message_delta"`) >= strings.Index(stream, `"type":"message_stop"`) {
				t.Fatalf("message_delta must precede message_stop: %s", stream)
			}
		})
	}
}

func TestPatchClaudeTerminalUsageAfterContentBlockStop(t *testing.T) {
	var param any
	_ = sdktranslator.TranslateStream(context.Background(), sdktranslator.FormatCodex, sdktranslator.FormatClaude, "gpt-5.6-sol", []byte(`{"messages":[]}`), nil, []byte(`data: {"type":"response.output_text.delta","delta":"ok"}`), &param)
	usage := `{"input_tokens":100,"output_tokens":7,"input_tokens_details":{"cached_tokens":30,"cache_write_tokens":40}}`
	outputs := sdktranslator.TranslateStream(context.Background(), sdktranslator.FormatCodex, sdktranslator.FormatClaude, "gpt-5.6-sol", []byte(`{"messages":[]}`), nil, []byte(`data: {"type":"response.completed","response":{"usage":`+usage+`}}`), &param)
	stream := patchClaudeTerminalUsage(bytes.Join(outputs, nil), []byte(`{"response":{"usage":`+usage+`}}`))
	if !bytes.Contains(stream, []byte(`"type":"content_block_stop"`)) {
		t.Fatalf("fixture did not emit content_block_stop before terminal usage: %s", stream)
	}
	delta, ok := firstClaudeEventData(string(stream), "message_delta")
	if !ok {
		t.Fatalf("missing message_delta: %s", stream)
	}
	if got := delta.Get("usage.input_tokens").Int(); got != 30 {
		t.Fatalf("input_tokens = %d, want 30", got)
	}
	if got := delta.Get("usage.cache_read_input_tokens").Int(); got != 30 {
		t.Fatalf("cache_read_input_tokens = %d, want 30", got)
	}
	if got := delta.Get("usage.cache_creation_input_tokens").Int(); got != 40 {
		t.Fatalf("cache_creation_input_tokens = %d, want 40", got)
	}
}

func TestCodexExecutorExecuteStream_CanceledBeforeTerminalUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.6-sol","usage":{"input_tokens":0}}}` + "\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	previousLevel := log.GetLevel()
	log.SetLevel(log.DebugLevel)
	hook := logrustest.NewLocal(log.StandardLogger())
	t.Cleanup(func() {
		hook.Reset()
		log.SetLevel(previousLevel)
	})

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL, "api_key": "test"}}
	ctx, cancel := context.WithCancel(context.Background())
	result, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: []byte(`{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Stream: true})
	if err != nil {
		cancel()
		t.Fatalf("ExecuteStream error: %v", err)
	}

	first, ok := <-result.Chunks
	if !ok {
		cancel()
		t.Fatal("stream closed before message_start")
	}
	if got := gjson.Get(firstClaudeSSEData(first.Payload), "message.usage.input_tokens").Int(); got <= 0 {
		cancel()
		t.Fatalf("message_start input token estimate = %d, want positive; payload=%s", got, first.Payload)
	}
	cancel()

	var stream bytes.Buffer
	stream.Write(first.Payload)
	for chunk := range result.Chunks {
		stream.Write(chunk.Payload)
	}
	if strings.Contains(stream.String(), `"type":"message_delta"`) || strings.Contains(stream.String(), `"type":"message_stop"`) {
		t.Fatalf("canceled stream fabricated terminal events: %s", stream.String())
	}

	foundDiagnostic := false
	for _, entry := range hook.AllEntries() {
		if entry.Message == "codex stream ended without authoritative terminal usage" && entry.Data["reason"] == "context_done" {
			foundDiagnostic = true
			break
		}
	}
	if !foundDiagnostic {
		t.Fatalf("missing cancellation diagnostic; entries=%v", hook.AllEntries())
	}
}

func firstClaudeSSEData(payload []byte) string {
	for _, line := range strings.Split(string(payload), "\n") {
		if strings.HasPrefix(line, "data: ") {
			return strings.TrimPrefix(line, "data: ")
		}
	}
	return ""
}

func firstClaudeEventData(stream, event string) (gjson.Result, bool) {
	currentEvent := ""
	for _, line := range strings.Split(stream, "\n") {
		if strings.HasPrefix(line, "event: ") {
			currentEvent = strings.TrimPrefix(line, "event: ")
			continue
		}
		if currentEvent == event && strings.HasPrefix(line, "data: ") {
			return gjson.Parse(strings.TrimPrefix(line, "data: ")), true
		}
	}
	return gjson.Result{}, false
}
