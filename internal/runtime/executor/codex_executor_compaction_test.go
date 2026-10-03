package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

type codexV1CompactionCapturedRequest struct {
	path string
	body []byte
}

type codexV1CompactionCapture struct {
	mu       sync.Mutex
	requests []codexV1CompactionCapturedRequest
}

func (c *codexV1CompactionCapture) add(request codexV1CompactionCapturedRequest) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, request)
	return len(c.requests) - 1
}

func (c *codexV1CompactionCapture) snapshot() []codexV1CompactionCapturedRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]codexV1CompactionCapturedRequest(nil), c.requests...)
}

func newCodexV1CompactionServer(t *testing.T, respond func(int) (string, []byte)) (*httptest.Server, *codexV1CompactionCapture) {
	t.Helper()
	capture := &codexV1CompactionCapture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			http.Error(w, "could not read request", http.StatusBadRequest)
			return
		}
		index := capture.add(codexV1CompactionCapturedRequest{path: r.URL.Path, body: body})
		contentType, response := respond(index)
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(response)
	}))
	t.Cleanup(server.Close)
	return server, capture
}

func codexV1CompactionConfig(baseURL string, models ...config.CodexModel) *config.Config {
	return &config.Config{
		SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll},
		CodexKey: []config.CodexKey{{
			APIKey:  "sk-test",
			BaseURL: baseURL,
			Models:  models,
		}},
	}
}

func codexV1CompactionRequest() cliproxyexecutor.Request {
	return cliproxyexecutor.Request{
		Model:   "summary-model",
		Payload: []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"keep-original"}]},{"type":"compaction_trigger","id":"trigger-1","opaque":{"keep":true}}],"tools":[{"type":"function","name":"keep-tool"}],"tool_choice":"auto"}`),
	}
}

func codexV1CompactionOptions() cliproxyexecutor.Options {
	return cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
}

func codexV1CompactionSummaryResponse() []byte {
	return []byte(`{"id":"resp-summary","object":"response","created_at":42,"status":"completed","background":false,"error":null,"model":"summary-model","output":[{"type":"message","id":"msg-summary","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Carry the task context forward."}]}],"usage":{"input_tokens":17,"output_tokens":9,"total_tokens":26,"input_tokens_details":{"cached_tokens":2},"provider_usage":{"kept":true}},"metadata":{"keep":"value"}}`)
}

func codexV1CompactionSSE(response []byte) []byte {
	return []byte("data: {\"type\":\"response.completed\",\"response\":" + string(response) + "}\n\n")
}

func codexV1CompactionResponseFor(index int) (string, []byte) {
	if index == 0 {
		return "application/json", codexV1CompactionSummaryResponse()
	}
	return "text/event-stream", codexV1CompactionSSE(codexV1CompactionSummaryResponse())
}

func TestCodexExecutorV1CompactionReplaysDefaultEndpointAcrossCredentials(t *testing.T) {
	executor := NewCodexExecutor(&config.Config{CodexKey: []config.CodexKey{
		{APIKey: "sk-old"},
		{APIKey: "sk-new", BaseURL: "https://chatgpt.com/backend-api/codex/"},
		{APIKey: "sk-foreign", BaseURL: "https://other.example/v1"},
	}})
	auth := codexAPIKeyTestAuth("")
	auth.Attributes["api_key"] = "sk-old"
	scope, secrets := executor.v1CompactionCredentials(auth)
	response, errConvert := helps.ConvertResponsesCompactionResponse(codexV1CompactionSummaryResponse(), "summary-model", scope, secrets)
	if errConvert != nil {
		t.Fatal(errConvert)
	}
	replay := []byte(`{"input":` + gjson.GetBytes(response, "output").Raw + `}`)
	auth.Attributes["api_key"] = "sk-new"
	replayScope, replaySecrets := executor.v1CompactionCredentials(auth)
	if _, errExpand := helps.ExpandResponsesCompactionCapsules(replay, replayScope, replaySecrets); errExpand != nil {
		t.Fatalf("default endpoint credential pool could not replay capsule: %v", errExpand)
	}
	executor.cfg.CodexKey = executor.cfg.CodexKey[1:]
	removedScope, remainingSecrets := executor.v1CompactionCredentials(auth)
	if _, errExpand := helps.ExpandResponsesCompactionCapsules(replay, removedScope, remainingSecrets); errExpand == nil {
		t.Fatal("capsule authenticated after its credential was removed")
	}
}

func TestCodexExecutorV1CompactionUsesResponsesAndReturnsOneCapsule(t *testing.T) {
	cases := []struct {
		name      string
		stream    bool
		jsonReply bool
	}{
		{name: "Execute accepts completed SSE summary"},
		{name: "Execute accepts JSON summary", jsonReply: true},
		{name: "ExecuteStream returns capsule events", stream: true, jsonReply: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, capture := newCodexV1CompactionServer(t, func(int) (string, []byte) {
				if tc.jsonReply {
					return "application/json", codexV1CompactionSummaryResponse()
				}
				return "text/event-stream", codexV1CompactionSSE(codexV1CompactionSummaryResponse())
			})
			executor := NewCodexExecutor(codexV1CompactionConfig(server.URL, config.CodexModel{Name: "summary-model", Alias: "summary-model", UseV1Compaction: true}))
			auth := codexAPIKeyTestAuth(server.URL)
			request := codexV1CompactionRequest()
			options := codexV1CompactionOptions()
			var result cliproxyexecutor.Response
			if tc.stream {
				stream, err := executor.ExecuteStream(context.Background(), auth, request, options)
				if err != nil {
					t.Fatalf("ExecuteStream error: %v", err)
				}
				var chunks strings.Builder
				for chunk := range stream.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream chunk error: %v", chunk.Err)
					}
					chunks.Write(chunk.Payload)
				}
				result.Payload = codexV1CompactionCompletedStreamResponse(t, chunks.String())
			} else {
				var err error
				result, err = executor.Execute(context.Background(), auth, request, options)
				if err != nil {
					t.Fatalf("Execute error: %v", err)
				}
			}

			requests := capture.snapshot()
			if len(requests) != 1 || requests[0].path != "/responses" {
				t.Fatalf("upstream requests = %+v, want one /responses request", requests)
			}
			assertCodexV1CompactionRequest(t, requests[0].body)
			if got := gjson.GetBytes(result.Payload, "status").String(); got != "completed" {
				t.Fatalf("response status = %q, want completed", got)
			}
			if got := gjson.GetBytes(result.Payload, "model").String(); got != "summary-model" {
				t.Fatalf("response model = %q, want summary-model", got)
			}
			if got := gjson.GetBytes(result.Payload, "metadata.keep").String(); got != "value" {
				t.Fatalf("response metadata.keep = %q, want preserved value", got)
			}
			if got := gjson.GetBytes(result.Payload, "usage.input_tokens").Int(); got != 17 {
				t.Fatalf("usage.input_tokens = %d, want 17", got)
			}
			if got := gjson.GetBytes(result.Payload, "usage.provider_usage.kept").Bool(); !got {
				t.Fatalf("provider usage metadata was lost: %s", gjson.GetBytes(result.Payload, "usage").Raw)
			}
			output := gjson.GetBytes(result.Payload, "output").Array()
			if len(output) != 2 || output[0].Get("id").String() != "msg-summary" || output[1].Get("type").String() != "compaction" {
				t.Fatalf("response output = %s, want original message and one appended capsule", gjson.GetBytes(result.Payload, "output").Raw)
			}
			capsule := output[1].Get("encrypted_content").String()
			if capsule == "" || strings.Contains(capsule, "Carry the task context forward") {
				t.Fatalf("capsule is missing or exposes the summary: %q", capsule)
			}
		})
	}
}

func codexV1CompactionCompletedStreamResponse(t *testing.T, stream string) []byte {
	t.Helper()
	var completed []byte
	for _, line := range strings.Split(stream, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		event := []byte(strings.TrimPrefix(line, "data: "))
		if gjson.GetBytes(event, "type").String() == "response.completed" {
			completed = []byte(gjson.GetBytes(event, "response").Raw)
		}
	}
	if len(completed) == 0 {
		t.Fatalf("stream has no response.completed event: %s", stream)
	}
	return completed
}

func assertCodexV1CompactionRequest(t *testing.T, body []byte) {
	t.Helper()
	input := gjson.GetBytes(body, "input").Array()
	if len(input) != 3 || input[0].Get("content.0.text").String() != "keep-original" {
		t.Fatalf("summary request input did not retain conversation: %s", body)
	}
	if input[1].Get("role").String() != "user" || !strings.Contains(input[1].Get("content.0.text").String(), "summary") {
		t.Fatalf("summary request has no summary instruction: %s", body)
	}
	if input[2].Get("type").String() != "compaction_trigger" || input[2].Get("id").String() != "trigger-1" || !input[2].Get("opaque.keep").Bool() {
		t.Fatalf("summary request did not retain its trigger: %s", body)
	}
	if gjson.GetBytes(body, "stream").Bool() != true {
		t.Fatalf("summary request stream = %s, want true", gjson.GetBytes(body, "stream").Raw)
	}
	for _, field := range []string{"tools", "tool_choice", "parallel_tool_calls", "text", "response_format", "context_management"} {
		if gjson.GetBytes(body, field).Exists() {
			t.Errorf("summary request retained incompatible field %q: %s", field, body)
		}
	}
}

func TestCodexExecutorV1CompactionExpandsCapsuleBeforeExecuteAndExecuteStream(t *testing.T) {
	server, capture := newCodexV1CompactionServer(t, codexV1CompactionResponseFor)
	executor := NewCodexExecutor(codexV1CompactionConfig(server.URL, config.CodexModel{Name: "summary-model", Alias: "summary-model", UseV1Compaction: true}))
	auth := codexAPIKeyTestAuth(server.URL)
	first, err := executor.Execute(context.Background(), auth, codexV1CompactionRequest(), codexV1CompactionOptions())
	if err != nil {
		t.Fatalf("initial summary Execute error: %v", err)
	}
	capsule := gjson.GetBytes(first.Payload, "output.1").Raw
	if gjson.GetBytes([]byte(capsule), "type").String() != "compaction" {
		t.Fatalf("initial output has no compaction capsule: %s", first.Payload)
	}
	replayPayload := []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"before-summary"}]},` + capsule + `,{"id":"cmp-native","type":"compaction","encrypted_content":"native-ciphertext","future":{"keep":true}},{"type":"compaction_trigger","id":"trigger-next","opaque":{"keep":true}},{"type":"message","role":"user","content":[{"type":"input_text","text":"after-summary"}]}],"tools":[{"type":"function","name":"remove-for-summary"}]}`)
	replayRequest := cliproxyexecutor.Request{Model: "summary-model", Payload: replayPayload}
	if _, errExecute := executor.Execute(context.Background(), auth, replayRequest, codexV1CompactionOptions()); errExecute != nil {
		t.Fatalf("capsule replay Execute error: %v", errExecute)
	}
	stream, errStream := executor.ExecuteStream(context.Background(), auth, replayRequest, codexV1CompactionOptions())
	if errStream != nil {
		t.Fatalf("capsule replay ExecuteStream error: %v", errStream)
	}
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("capsule replay stream chunk error: %v", chunk.Err)
		}
	}
	requests := capture.snapshot()
	if len(requests) != 3 {
		t.Fatalf("upstream request count = %d, want 3", len(requests))
	}
	for index, request := range requests[1:] {
		if request.path != "/responses" {
			t.Fatalf("request %d path = %q, want /responses", index+2, request.path)
		}
		assertCodexV1CompactionReplayRequest(t, request.body)
	}
}

func assertCodexV1CompactionReplayRequest(t *testing.T, body []byte) {
	t.Helper()
	input := gjson.GetBytes(body, "input").Array()
	if len(input) != 6 {
		t.Fatalf("replay input has %d items, want 6: %s", len(input), body)
	}
	if input[0].Get("content.0.text").String() != "before-summary" || input[5].Get("content.0.text").String() != "after-summary" {
		t.Fatalf("replay lost surrounding input: %s", body)
	}
	if input[1].Get("type").String() != "message" || input[1].Get("role").String() != "developer" || !strings.Contains(input[1].Get("content.0.text").String(), "Carry the task context forward") {
		t.Fatalf("capsule was not expanded into developer summary context: %s", body)
	}
	if input[2].Raw != `{"id":"cmp-native","type":"compaction","encrypted_content":"native-ciphertext","future":{"keep":true}}` {
		t.Fatalf("native compaction input changed: %s", input[2].Raw)
	}
	if input[3].Get("role").String() != "user" || !strings.Contains(input[3].Get("content.0.text").String(), "summary") {
		t.Fatalf("next summary instruction is missing: %s", body)
	}
	if input[4].Get("type").String() != "compaction_trigger" || input[4].Get("id").String() != "trigger-next" || !input[4].Get("opaque.keep").Bool() {
		t.Fatalf("next compaction trigger changed: %s", body)
	}
	if strings.Contains(string(body), "cpa-responses-v1-compaction-v1.") {
		t.Fatalf("bridge capsule was not removed from the replay request: %s", body)
	}
	if gjson.GetBytes(body, "tools").Exists() {
		t.Fatalf("tools were retained in the summary request: %s", body)
	}
}

func TestCodexExecutorV1CompactionRejectsInvalidCapsuleBeforeUpstream(t *testing.T) {
	server, capture := newCodexV1CompactionServer(t, func(int) (string, []byte) {
		return "text/event-stream", codexV1CompactionSSE(codexV1CompactionSummaryResponse())
	})
	executor := NewCodexExecutor(codexV1CompactionConfig(server.URL, config.CodexModel{Name: "summary-model", Alias: "summary-model", UseV1Compaction: true}))
	request := codexV1CompactionRequest()
	request.Payload = []byte(`{"input":[{"type":"compaction","encrypted_content":"cpa-responses-v1-compaction-v1.invalid"},{"type":"compaction_trigger","id":"trigger-1"}]}`)
	if _, err := executor.Execute(context.Background(), codexAPIKeyTestAuth(server.URL), request, codexV1CompactionOptions()); err == nil {
		t.Fatal("Execute accepted an invalid bridge capsule")
	}
	if requests := capture.snapshot(); len(requests) != 0 {
		t.Fatalf("invalid capsule reached upstream: %+v", requests)
	}
}

func TestCodexExecutorV1CompactionRejectsUnfinishedResponses(t *testing.T) {
	cases := []struct {
		name     string
		response []byte
	}{
		{name: "incomplete", response: []byte("data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"status\":\"incomplete\",\"content\":[{\"type\":\"output_text\",\"text\":\"partial summary\"}]}]}}\n\n")},
		{name: "failed", response: []byte("data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"server_error\",\"message\":\"failed\"}}}\n\n")},
		{name: "missing terminal", response: []byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial summary\"}\n\n")},
		{name: "duplicate terminal", response: append(codexV1CompactionSSE(codexV1CompactionSummaryResponse()), codexV1CompactionSSE(codexV1CompactionSummaryResponse())...)},
		{name: "invalid output before reconstruction", response: []byte("data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":" + gjson.GetBytes(codexV1CompactionSummaryResponse(), "output.0").Raw + "}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":{\"bad\":\"field\"}}}\n\n")},
		{name: "malformed outer terminal", response: []byte("data: {\"type\":\"response.completed\",\"response\":" + string(codexV1CompactionSummaryResponse()) + "\n\n")},
		{name: "trailing malformed terminal field", response: []byte("data: {\"type\":\"response.completed\",\"response\":" + string(codexV1CompactionSummaryResponse()) + ",\"broken\":\n\n")},
		{name: "JSON failed", response: []byte(`{"status":"failed","error":{"code":"server_error"},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"partial summary"}]}]}`)},
		{name: "JSON incomplete event", response: []byte(`{"type":"response.incomplete","response":` + string(codexV1CompactionSummaryResponse()) + `}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, capture := newCodexV1CompactionServer(t, func(int) (string, []byte) {
				if strings.HasPrefix(string(tc.response), "{") {
					return "application/json", tc.response
				}
				return "text/event-stream", tc.response
			})
			executor := NewCodexExecutor(codexV1CompactionConfig(server.URL, config.CodexModel{Name: "summary-model", Alias: "summary-model", UseV1Compaction: true}))
			if _, err := executor.Execute(context.Background(), codexAPIKeyTestAuth(server.URL), codexV1CompactionRequest(), codexV1CompactionOptions()); err == nil {
				t.Fatal("Execute returned a compaction success for an unfinished response")
			}
			if requests := capture.snapshot(); len(requests) != 1 || requests[0].path != "/responses" {
				t.Fatalf("upstream requests = %+v, want one /responses attempt", requests)
			}
		})
	}
}

func TestCodexExecutorV1CompactionAcceptsMultilineSSE(t *testing.T) {
	item := gjson.GetBytes(codexV1CompactionSummaryResponse(), "output.0").Raw
	response := []byte("event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":" + item + "}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\ndata: \"response\":{\"id\":\"resp-multiline\",\"status\":\"completed\",\"output\":[],\"usage\":{\"total_tokens\":26}}}\n\n")
	server, _ := newCodexV1CompactionServer(t, func(int) (string, []byte) {
		return "text/event-stream", response
	})
	executor := NewCodexExecutor(codexV1CompactionConfig(server.URL, config.CodexModel{Name: "summary-model", Alias: "summary-model", UseV1Compaction: true}))
	result, err := executor.Execute(context.Background(), codexAPIKeyTestAuth(server.URL), codexV1CompactionRequest(), codexV1CompactionOptions())
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(result.Payload, "id").String() != "resp-multiline" || gjson.GetBytes(result.Payload, "output.0").Raw != item || gjson.GetBytes(result.Payload, "output.1.type").String() != "compaction" || gjson.GetBytes(result.Payload, "usage.total_tokens").Int() != 26 {
		t.Fatalf("multiline completed response lost output or metadata: %s", result.Payload)
	}
}

func TestCodexExecutorV1CompactionHonorsDisabledAndAuthoritativeSiblingSettings(t *testing.T) {
	cases := []struct {
		name       string
		models     []config.CodexModel
		metadata   map[string]any
		wantPrompt bool
	}{
		{name: "disabled configuration", models: []config.CodexModel{{Name: "summary-model", Alias: "summary-model"}}},
		{name: "authoritative disabled sibling", models: []config.CodexModel{{Name: "summary-model", Alias: "enabled", UseV1Compaction: true}, {Name: "summary-model", Alias: "disabled"}}, metadata: map[string]any{"cliproxy.resolved_api_key_model_info": &registry.ModelInfo{ID: "summary-model", UseV1Compaction: false}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, capture := newCodexV1CompactionServer(t, func(int) (string, []byte) {
				return "text/event-stream", codexV1CompactionSSE(codexV1CompactionSummaryResponse())
			})
			executor := NewCodexExecutor(codexV1CompactionConfig(server.URL, tc.models...))
			request := codexV1CompactionRequest()
			request.Metadata = tc.metadata
			response, err := executor.Execute(context.Background(), codexAPIKeyTestAuth(server.URL), request, codexV1CompactionOptions())
			if err != nil {
				t.Fatalf("Execute error: %v", err)
			}
			requests := capture.snapshot()
			if len(requests) != 1 || requests[0].path != "/responses" {
				t.Fatalf("upstream requests = %+v, want one /responses request", requests)
			}
			body := requests[0].body
			hasPrompt := strings.Contains(gjson.GetBytes(body, "input").Raw, "Summarize the conversation")
			if hasPrompt != tc.wantPrompt {
				t.Fatalf("summary instruction presence = %t, want %t: %s", hasPrompt, tc.wantPrompt, body)
			}
			if gjson.GetBytes(body, "input.1.type").String() != "compaction_trigger" {
				t.Fatalf("disabled compaction changed the original trigger: %s", body)
			}
			if output := gjson.GetBytes(response.Payload, "output").Array(); len(output) != 1 || output[0].Get("type").String() == "compaction" {
				t.Fatalf("disabled compaction synthesized a capsule: %s", response.Payload)
			}
		})
	}
}

func TestCodexExecutorV1CompactionPreservesNativeCompactionOutput(t *testing.T) {
	native := `{"id":"cmp-native","type":"compaction","encrypted_content":"upstream-native","future":{"keep":true}}`
	response := []byte(`{"id":"resp-native","object":"response","status":"completed","model":"summary-model","output":[` + native + `],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}`)
	server, capture := newCodexV1CompactionServer(t, func(int) (string, []byte) { return "application/json", response })
	executor := NewCodexExecutor(codexV1CompactionConfig(server.URL, config.CodexModel{Name: "summary-model", Alias: "summary-model", UseV1Compaction: true}))
	result, err := executor.Execute(context.Background(), codexAPIKeyTestAuth(server.URL), codexV1CompactionRequest(), codexV1CompactionOptions())
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if requests := capture.snapshot(); len(requests) != 1 || requests[0].path != "/responses" {
		t.Fatalf("upstream requests = %+v, want one /responses request", requests)
	}
	output := gjson.GetBytes(result.Payload, "output").Array()
	if len(output) != 1 || output[0].Raw != native {
		t.Fatalf("native compaction output changed or was duplicated: %s", result.Payload)
	}
	if got := gjson.GetBytes(result.Payload, "usage.total_tokens").Int(); got != 4 {
		t.Fatalf("usage.total_tokens = %d, want 4", got)
	}
}

func TestCodexAutoRequiredUpstreamWebsocketDoesNotFallbackToHTTP(t *testing.T) {
	server, capture := newCodexV1CompactionServer(t, func(int) (string, []byte) {
		return "text/event-stream", codexV1CompactionSSE(codexV1CompactionSummaryResponse())
	})
	executor := NewCodexAutoExecutor(codexV1CompactionConfig(server.URL, config.CodexModel{Name: "summary-model", Alias: "summary-model", UseV1Compaction: true}))
	auth := codexAPIKeyTestAuth(server.URL)
	ctx := cliproxyexecutor.WithRequiredUpstreamWebsocket(context.Background())
	if _, err := executor.Execute(ctx, auth, codexV1CompactionRequest(), codexV1CompactionOptions()); !cliproxyexecutor.IsUpstreamWebsocketReplayRequired(err) {
		t.Fatalf("Execute error = %v, want required-WebSocket replay error", err)
	}
	if _, err := executor.ExecuteStream(ctx, auth, codexV1CompactionRequest(), codexV1CompactionOptions()); !cliproxyexecutor.IsUpstreamWebsocketReplayRequired(err) {
		t.Fatalf("ExecuteStream error = %v, want required-WebSocket replay error", err)
	}
	if requests := capture.snapshot(); len(requests) != 0 {
		t.Fatalf("required WebSocket request fell back to HTTP: %+v", requests)
	}
}
