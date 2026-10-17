package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestV1CompactionPreservesUsage(t *testing.T) {
	const complete = `{"id":"resp_summary","object":"response","model":"upstream-model","status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"summary"}]}],"usage":{"input_tokens":17,"output_tokens":9,"total_tokens":26}}`
	first := "data: {\"type\":\"response.completed\",\"response\":" + complete + "}\n\n"
	second := "data: {\"type\":\"response.completed\",\"response\":{\"model\":\"duplicate-model\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":99,\"output_tokens\":88,\"total_tokens\":187}}}"
	buffered := `{"type":"response.in_progress","response":{"model":"upstream-model","usage":{"input_tokens":17,"output_tokens":9,"total_tokens":26,"input_tokens_details":{"cached_tokens":2},"output_tokens_details":{"reasoning_tokens":3},"provider_usage":{"kept":true}}}}`
	completedWithoutUsage := `{"type":"response.completed","response":{"id":"resp_summary","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"summary"}]}]}}`
	refusalWithoutUsage := strings.Replace(completedWithoutUsage, `"type":"output_text","text":"summary"`, `"type":"refusal","refusal":"Cannot summarize."`, 1)
	cases := []struct {
		name, contentType, body string
		success                 bool
	}{
		{name: "refusal", contentType: "application/json", body: `{"id":"resp_refusal","object":"response","model":"upstream-model","status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"refusal","refusal":"Cannot summarize."}]}],"usage":{"input_tokens":17,"output_tokens":9,"total_tokens":26}}`},
		{name: "incomplete SSE", contentType: "text/event-stream", body: "data: {\"type\":\"response.incomplete\",\"response\":" + strings.Replace(complete, `"status":"completed"`, `"status":"incomplete"`, 1) + "}\n\n"},
		{name: "incomplete JSON", contentType: "application/json", body: `{"type":"response.incomplete","response":` + complete + `}`},
		{name: "failed JSON", contentType: "application/json", body: `{"model":"upstream-model","status":"failed","error":{"code":"server_error"},"usage":{"input_tokens":17,"output_tokens":9,"total_tokens":26}}`},
		{name: "duplicate SSE", contentType: "text/event-stream", body: first + second + "\n\n"},
		{name: "duplicate at EOF", contentType: "text/event-stream", body: first + second},
		{name: "buffered usage success", contentType: "text/event-stream", body: "data: " + buffered + "\n\ndata: " + completedWithoutUsage + "\n\n", success: true},
		{name: "buffered usage refusal", contentType: "text/event-stream", body: "data: " + buffered + "\n\ndata: " + refusalWithoutUsage + "\n\n"},
		{name: "buffered usage native", contentType: "text/event-stream", body: "data: " + buffered + "\n\ndata: " + `{"type":"response.completed","response":{"id":"resp_native","status":"completed","output":[{"type":"compaction","encrypted_content":"native opaque"}]}}` + "\n\n", success: true},
		{name: "buffered usage null terminal", contentType: "text/event-stream", body: "data: " + buffered + "\n\ndata: " + strings.Replace(completedWithoutUsage, `"status":"completed"`, `"status":"completed","usage":null`, 1) + "\n\n", success: true},
	}
	for _, provider := range []string{"sample", "codex"} {
		for _, stream := range []bool{false, true} {
			for _, tc := range cases {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", provider, stream, tc.name), func(t *testing.T) {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", tc.contentType)
						_, _ = w.Write([]byte(tc.body))
					}))
					defer server.Close()
					capture := &multiProviderUsageCapture{alias: t.Name(), records: make(chan coreusage.Record, 4)}
					coreusage.RegisterNamedPlugin(t.Name(), capture)
					t.Cleanup(func() { coreusage.RegisterNamedPlugin(t.Name(), multiProviderNoopUsagePlugin{}) })
					ctx := coreusage.WithRequestedModelAlias(context.Background(), t.Name())
					var executor cliproxyauth.ProviderExecutor
					auth := &cliproxyauth.Auth{Provider: provider, Attributes: map[string]string{"base_url": server.URL, "api_key": "test-secret"}}
					if provider == "codex" {
						executor = NewCodexExecutor(codexV1CompactionConfig(server.URL, config.CodexModel{Name: "summary-model", Alias: "summary-model", UseV1Compaction: true}))
						auth = codexAPIKeyTestAuth(server.URL)
					} else {
						executor = NewOpenAICompatExecutor(provider, &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{Name: provider, BaseURL: server.URL, Models: []config.OpenAICompatibilityModel{{Name: "summary-model", Alias: "summary-model", UseV1Compaction: true}}}}})
					}
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: stream}
					var err error
					var response cliproxyexecutor.Response
					if stream {
						var result *cliproxyexecutor.StreamResult
						result, err = executor.ExecuteStream(ctx, auth, codexV1CompactionRequest(), opts)
						if err == nil {
							var streamed bytes.Buffer
							for chunk := range result.Chunks {
								if chunk.Err != nil {
									err = chunk.Err
								}
								streamed.Write(chunk.Payload)
							}
							if tc.success && err == nil {
								response.Payload, err = helps.CompletedV1ResponsesBody(streamed.Bytes(), nil)
							}
						}
					} else {
						response, err = executor.Execute(ctx, auth, codexV1CompactionRequest(), opts)
					}
					if tc.success {
						if err != nil {
							t.Fatalf("summary compaction failed: %v", err)
						}
						usage := gjson.GetBytes(response.Payload, "usage")
						if usage.Get("input_tokens").Int() != 17 || usage.Get("output_tokens").Int() != 9 || usage.Get("total_tokens").Int() != 26 || usage.Get("input_tokens_details.cached_tokens").Int() != 2 || usage.Get("output_tokens_details.reasoning_tokens").Int() != 3 || !usage.Get("provider_usage.kept").Bool() {
							t.Fatalf("client response lost upstream usage metadata: %s", response.Payload)
						}
					} else if scoped, ok := err.(interface{ IsRequestScoped() bool }); !ok || !scoped.IsRequestScoped() {
						t.Fatalf("conversion failure = %v, want request-scoped error", err)
					}
					record := capture.await(t)
					if record.Failed != !tc.success || record.Detail.InputTokens != 17 || record.Detail.OutputTokens != 9 || record.Detail.TotalTokens != 26 || record.ResponseModel != "upstream-model" {
						t.Fatalf("compaction usage = %+v, want failed=%t upstream-model with 17/9/26 tokens", record, !tc.success)
					}
				})
			}
		}
	}
}

func TestOpenAICompatV1ResponsesCompactionPreservesTriggerAndReplaysCapsule(t *testing.T) {
	var mu sync.Mutex
	var requests [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("upstream path = %q, want /responses", r.URL.Path)
		}
		body := new(bytes.Buffer)
		if _, err := body.ReadFrom(r.Body); err != nil {
			t.Errorf("read request body: %v", err)
		}
		mu.Lock()
		requests = append(requests, bytes.Clone(body.Bytes()))
		index := len(requests)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if index == 1 {
			_, _ = w.Write([]byte(`{"id":"resp_summary","object":"response","status":"completed","created_at":1,"model":"mock-model","output":[{"id":"summary_message","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Carry the pending build task forward."}]}],"usage":{"input_tokens":30,"output_tokens":8,"total_tokens":38}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"next step\"}\n\n")
		_, _ = fmt.Fprint(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"answer\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"next step\"}]}}\n\n")
		_, _ = fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_next\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":4,\"output_tokens\":2,\"total_tokens\":6}}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)
	executor := NewOpenAICompatExecutor("sample", &config.Config{})
	auth := &cliproxyauth.Auth{Provider: "sample", Attributes: map[string]string{"base_url": server.URL, "api_key": "test-secret"}}
	triggerPayload := []byte(`{"model":"mock-model","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Finish the build."}]},{"type":"compaction_trigger"}],"tools":[{"type":"function","name":"tool"}],"context_management":{"type":"automatic"}}`)
	req := cliproxyexecutor.Request{Model: "mock-model", Payload: triggerPayload}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: triggerPayload}
	scope := "sample\x00" + server.URL
	compacted, err := executor.executeV1Responses(context.Background(), auth, req, opts, scope, []string{"test-secret"})
	if err != nil {
		t.Fatalf("execute compaction: %v", err)
	}
	if got := gjson.GetBytes(compacted.Payload, "output.0.type").String(); got != "message" {
		t.Fatalf("output.0.type = %q, want preserved summary message", got)
	}
	if got := gjson.GetBytes(compacted.Payload, "output.1.type").String(); got != "compaction" {
		t.Fatalf("output.1.type = %q, want compaction", got)
	}
	capsule := gjson.GetBytes(compacted.Payload, "output.1.encrypted_content").String()
	if capsule == "" {
		t.Fatal("converted compaction item has no encrypted_content")
	}
	mu.Lock()
	firstRequest := bytes.Clone(requests[0])
	mu.Unlock()
	if got := gjson.GetBytes(firstRequest, "input.1.type").String(); got != "message" {
		t.Fatalf("summary prompt position type = %q, want message; request=%s", got, firstRequest)
	}
	if got := gjson.GetBytes(firstRequest, "input.2.type").String(); got != "compaction_trigger" {
		t.Fatalf("trigger was removed or reordered: input.2.type = %q; request=%s", got, firstRequest)
	}
	if gjson.GetBytes(firstRequest, "tools").Exists() || gjson.GetBytes(firstRequest, "context_management").Exists() {
		t.Fatalf("summary request retained unsupported fields: %s", firstRequest)
	}
	followup := []byte(`{"model":"mock-model","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Continue."}]},{"type":"compaction","id":"opaque","encrypted_content":"` + capsule + `"}]}`)
	stream, err := executor.executeV1ResponsesStream(context.Background(), auth, cliproxyexecutor.Request{Model: "mock-model", Payload: followup}, cliproxyexecutor.Options{Stream: true, SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: followup}, scope, []string{"test-secret"})
	if err != nil {
		t.Fatalf("execute replay stream: %v", err)
	}
	var streamed bytes.Buffer
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("replay stream error: %v", chunk.Err)
		}
		streamed.Write(chunk.Payload)
	}
	if !strings.Contains(streamed.String(), `"type":"response.output_text.delta"`) || !strings.Contains(streamed.String(), `"delta":"next step"`) {
		t.Fatalf("stream did not retain Responses delta events: %s", streamed.String())
	}
	if !strings.Contains(streamed.String(), `"type":"response.completed"`) || !strings.Contains(streamed.String(), `"text":"next step"`) {
		t.Fatalf("stream omitted its completed response or reconstructed output: %s", streamed.String())
	}
	mu.Lock()
	secondRequest := bytes.Clone(requests[1])
	mu.Unlock()
	if got := gjson.GetBytes(secondRequest, "input.1.role").String(); got != "developer" {
		t.Fatalf("expanded capsule role = %q, want developer; request=%s", got, secondRequest)
	}
	if !strings.Contains(gjson.GetBytes(secondRequest, "input.1.content.0.text").String(), "Carry the pending build task forward.") {
		t.Fatalf("expanded capsule omitted its summary: %s", secondRequest)
	}
	if strings.Contains(string(secondRequest), capsule) {
		t.Fatalf("proxy-owned capsule was not expanded before forwarding: %s", secondRequest)
	}
}

func TestOpenAICompatV1ResponsesStreamingCompactionUsesDoneItems(t *testing.T) {
	var requestBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("upstream path = %q, want /responses", r.URL.Path)
		}
		requestBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"summary_message\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"Retain indexed summary output.\"}]}}\n\n")
		_, _ = fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_summary\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"mock-model\",\"output\":[],\"usage\":{\"input_tokens\":12,\"output_tokens\":3,\"total_tokens\":15}}}\n\n")
	}))
	t.Cleanup(server.Close)
	executor := NewOpenAICompatExecutor("sample", &config.Config{})
	auth := &cliproxyauth.Auth{Provider: "sample", Attributes: map[string]string{"base_url": server.URL, "api_key": "test-secret"}}
	payload := []byte(`{"model":"mock-model","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Summarize."}]},{"type":"compaction_trigger"}]}`)
	stream, err := executor.executeV1ResponsesStream(context.Background(), auth, cliproxyexecutor.Request{Model: "mock-model", Payload: payload}, cliproxyexecutor.Options{Stream: true, SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: payload}, "sample\x00"+server.URL, []string{"test-secret"})
	if err != nil {
		t.Fatalf("execute compaction stream: %v", err)
	}
	var streamed bytes.Buffer
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("compaction stream error: %v", chunk.Err)
		}
		streamed.Write(chunk.Payload)
	}
	if got := gjson.GetBytes(requestBody, "input.1.type").String(); got != "message" {
		t.Fatalf("summary prompt was not inserted before the trigger: %s", requestBody)
	}
	if got := gjson.GetBytes(requestBody, "input.2.type").String(); got != "compaction_trigger" {
		t.Fatalf("upstream trigger = %q, want preserved at the end; request=%s", got, requestBody)
	}
	if !strings.Contains(streamed.String(), "Retain indexed summary output.") || !strings.Contains(streamed.String(), `"type":"compaction"`) {
		t.Fatalf("streamed compaction omitted restored message output or compaction item: %s", streamed.String())
	}
	var completedEvent []byte
	for _, line := range strings.Split(streamed.String(), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "data:") {
			eventData := bytes.TrimSpace([]byte(strings.TrimPrefix(trimmed, "data:")))
			if gjson.GetBytes(eventData, "type").String() == "response.completed" {
				completedEvent = eventData
			}
		}
	}
	if len(completedEvent) == 0 {
		t.Fatal("streamed compaction omitted response.completed")
	}
	compactionCount := 0
	for _, item := range gjson.GetBytes(completedEvent, "response.output").Array() {
		if item.Get("type").String() == "compaction" {
			compactionCount++
		}
	}
	if compactionCount != 1 {
		t.Fatalf("completed output contains %d compaction items, want one", compactionCount)
	}
}

func TestOpenAICompatV1ResponsesRejectsIncompleteCompactionAndStreamEOF(t *testing.T) {
	for _, terminal := range []string{
		`{"type":"response.completed"}`,
		`{"type":"response.completed","response":{"status":"in_progress","output":[]}}`,
		`{"type":"response.completed","response":{"status":"completed","output":{}}}`,
		`{"type":"response.completed","response":{"status":"completed","output":null}}`,
		`{"type":"response.completed","response":{"status":"completed"}}`,
	} {
		t.Run("malformed replay terminal/"+terminal, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "data: %s\n\n", terminal)
			}))
			t.Cleanup(server.Close)
			executor := NewOpenAICompatExecutor("sample", &config.Config{})
			auth := &cliproxyauth.Auth{Provider: "sample", Attributes: map[string]string{"base_url": server.URL, "api_key": "test-secret"}}
			payload := []byte(`{"model":"mock-model","input":[{"type":"compaction","encrypted_content":"native opaque"}]}`)
			result, err := executor.executeV1ResponsesStream(context.Background(), auth, cliproxyexecutor.Request{Model: "mock-model", Payload: payload}, cliproxyexecutor.Options{Stream: true, SourceFormat: sdktranslator.FormatOpenAIResponse}, "sample\x00"+server.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			var forwarded bytes.Buffer
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					err = chunk.Err
				}
				forwarded.Write(chunk.Payload)
			}
			if scoped, ok := err.(cliproxyexecutor.RequestScopedError); !ok || !scoped.IsRequestScoped() {
				t.Fatalf("malformed replay terminal returned no scoped error: %v", err)
			}
			if strings.Contains(forwarded.String(), `"type":"response.completed"`) {
				t.Fatalf("malformed replay terminal reached client: %s", forwarded.String())
			}
		})
	}
	t.Run("incomplete compaction response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_incomplete","object":"response","status":"incomplete","output":[]}`))
		}))
		t.Cleanup(server.Close)
		executor := NewOpenAICompatExecutor("sample", &config.Config{})
		auth := &cliproxyauth.Auth{Provider: "sample", Attributes: map[string]string{"base_url": server.URL, "api_key": "test-secret"}}
		payload := []byte(`{"model":"mock-model","input":[{"type":"compaction_trigger"}]}`)
		_, err := executor.executeV1Responses(context.Background(), auth, cliproxyexecutor.Request{Model: "mock-model", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: payload}, "sample\x00"+server.URL, nil)
		if err == nil {
			t.Fatal("expected incomplete compaction to fail")
		}
		if scoped, ok := err.(cliproxyexecutor.RequestScopedError); !ok || !scoped.IsRequestScoped() {
			t.Fatalf("error %T is not request-scoped", err)
		}
	})
	t.Run("ordinary replay stream without terminal", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
		}))
		t.Cleanup(server.Close)
		executor := NewOpenAICompatExecutor("sample", &config.Config{})
		auth := &cliproxyauth.Auth{Provider: "sample", Attributes: map[string]string{"base_url": server.URL, "api_key": "test-secret"}}
		payload := []byte(`{"model":"mock-model","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Continue."}]},{"type":"compaction","encrypted_content":"native opaque"}]}`)
		stream, err := executor.executeV1ResponsesStream(context.Background(), auth, cliproxyexecutor.Request{Model: "mock-model", Payload: payload}, cliproxyexecutor.Options{Stream: true, SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: payload}, "sample\x00"+server.URL, nil)
		if err != nil {
			t.Fatalf("execute replay stream: %v", err)
		}
		var streamed bytes.Buffer
		var streamErr error
		for chunk := range stream.Chunks {
			if chunk.Err != nil {
				streamErr = chunk.Err
			}
			streamed.Write(chunk.Payload)
		}
		if !strings.Contains(streamed.String(), `"delta":"partial"`) {
			t.Fatalf("stream did not forward the partial delta: %s", streamed.String())
		}
		if streamErr == nil {
			t.Fatal("expected missing terminal response error")
		}
		if scoped, ok := streamErr.(cliproxyexecutor.RequestScopedError); !ok || !scoped.IsRequestScoped() {
			t.Fatalf("stream error %T is not request-scoped", streamErr)
		}
	})
}

func TestOpenAICompatV1ResponsesRejectsDuplicateTerminal(t *testing.T) {
	completed := `{"type":"response.completed","response":{"id":"resp_summary","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"summary"}]}]}}`
	incomplete := `{"type":"response.incomplete","response":{"id":"resp_summary","status":"incomplete","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"partial"}]}]}}`
	for _, tc := range []struct {
		name    string
		trigger bool
		stream  bool
		second  string
	}{
		{name: "summary JSON", trigger: true, second: completed},
		{name: "summary stream", trigger: true, stream: true, second: completed},
		{name: "replay completed", stream: true, second: completed},
		{name: "replay incomplete", stream: true, second: incomplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "data: %s\n\ndata: %s\n\n", completed, tc.second)
			}))
			t.Cleanup(server.Close)
			executor := NewOpenAICompatExecutor("sample", &config.Config{})
			auth := &cliproxyauth.Auth{Provider: "sample", Attributes: map[string]string{"base_url": server.URL, "api_key": "test-secret"}}
			item := `{"type":"compaction","encrypted_content":"native opaque"}`
			if tc.trigger {
				item = `{"type":"compaction_trigger"}`
			}
			payload := []byte(`{"model":"mock-model","input":[` + item + `]}`)
			req := cliproxyexecutor.Request{Model: "mock-model", Payload: payload}
			opts := cliproxyexecutor.Options{Stream: tc.stream, SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: payload}
			scope := "sample\x00" + server.URL
			var observedError error
			if tc.stream {
				stream, err := executor.executeV1ResponsesStream(context.Background(), auth, req, opts, scope, []string{"test-secret"})
				observedError = err
				if err == nil {
					var forwarded bytes.Buffer
					for chunk := range stream.Chunks {
						if chunk.Err != nil {
							observedError = chunk.Err
						}
						forwarded.Write(chunk.Payload)
					}
					if strings.Count(forwarded.String(), `"type":"response.completed"`) > 1 || strings.Contains(forwarded.String(), `"type":"response.incomplete"`) || strings.Contains(forwarded.String(), `"type":"compaction"`) {
						t.Fatalf("duplicate terminal produced conflicting output: %s", forwarded.String())
					}
				}
			} else {
				response, err := executor.executeV1Responses(context.Background(), auth, req, opts, scope, []string{"test-secret"})
				observedError = err
				if len(response.Payload) != 0 {
					t.Fatalf("duplicate terminal produced a compaction result: %s", response.Payload)
				}
			}
			if scoped, ok := observedError.(cliproxyexecutor.RequestScopedError); !ok || !scoped.IsRequestScoped() {
				t.Fatalf("duplicate terminal did not return a request-scoped error: %v", observedError)
			}
		})
	}
}

func TestOpenAICompatV1ResponsesPreviousResponseID(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, kind := range []string{"native", "foreign", "owned", "trigger", "required websocket"} {
			t.Run(fmt.Sprintf("%s/stream=%t", kind, stream), func(t *testing.T) {
				requests := make(chan []byte, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					requests <- body
					if r.URL.Path != "/responses" {
						t.Errorf("upstream path = %q, want /responses", r.URL.Path)
					}
					response := `{"id":"resp_next","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"next"}]}]}`
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", response)
					} else {
						w.Header().Set("Content-Type", "application/json")
						_, _ = fmt.Fprint(w, response)
					}
				}))
				t.Cleanup(server.Close)
				scope := "sample\x00" + server.URL
				item := `{"type":"compaction","encrypted_content":"native opaque"}`
				switch kind {
				case "foreign":
					item = `{"type":"compaction","encrypted_content":"other-format:opaque"}`
				case "owned":
					converted, err := helps.ConvertResponsesCompactionResponse(codexV1CompactionSummaryResponse(), "mock-model", scope, []string{"test-secret"})
					if err != nil {
						t.Fatalf("create capsule: %v", err)
					}
					item = gjson.GetBytes(converted, "output.1").Raw
				case "trigger":
					item = `{"type":"compaction_trigger"}`
				}
				payload := []byte(`{"model":"mock-model","previous_response_id":"resp_previous","input":[` + item + `]}`)
				executor := NewOpenAICompatExecutor("sample", &config.Config{})
				auth := &cliproxyauth.Auth{Provider: "sample", Attributes: map[string]string{"base_url": server.URL, "api_key": "test-secret"}}
				ctx := context.Background()
				if kind == "required websocket" {
					ctx = cliproxyexecutor.WithRequiredUpstreamWebsocket(ctx)
				}
				req := cliproxyexecutor.Request{Model: "mock-model", Payload: payload}
				opts := cliproxyexecutor.Options{Stream: stream, SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: payload}
				var executionError error
				if stream {
					result, err := executor.executeV1ResponsesStream(ctx, auth, req, opts, scope, []string{"test-secret"})
					executionError = err
					if err == nil {
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								executionError = chunk.Err
							}
						}
					}
				} else {
					_, executionError = executor.executeV1Responses(ctx, auth, req, opts, scope, []string{"test-secret"})
				}
				if kind == "trigger" || kind == "required websocket" {
					if executionError == nil {
						t.Fatal("expected unsafe summary or WebSocket fallback to fail")
					}
					select {
					case body := <-requests:
						t.Fatalf("rejected request reached upstream: %s", body)
					default:
					}
				} else {
					if executionError != nil {
						t.Fatalf("replay with previous_response_id failed: %v", executionError)
					}
					body := <-requests
					if gjson.GetBytes(body, "previous_response_id").String() != "resp_previous" {
						t.Fatalf("server-owned history was dropped: %s", body)
					}
					if kind == "owned" {
						if gjson.GetBytes(body, "input.0.role").String() != "developer" || !strings.Contains(gjson.GetBytes(body, "input.0.content.0.text").String(), "Carry the task context forward.") {
							t.Fatalf("capsule was not expanded: %s", body)
						}
					} else if gjson.GetBytes(body, "input.0").Raw != item {
						t.Fatalf("native or foreign item changed: %s", body)
					}
				}
			})
		}
	}
}

func TestOpenAICompatV1ResponsesDuplicateTerminalPreservesUsage(t *testing.T) {
	for _, usagePath := range []string{"usage", "response.usage"} {
		for _, terminal := range []string{"response.completed", "response.incomplete", "response.failed"} {
			t.Run(usagePath+"/"+terminal, func(t *testing.T) {
				capture := &multiProviderUsageCapture{alias: t.Name(), records: make(chan coreusage.Record, 4)}
				coreusage.RegisterNamedPlugin(t.Name(), capture)
				t.Cleanup(func() { coreusage.RegisterNamedPlugin(t.Name(), multiProviderNoopUsagePlugin{}) })
				first, err := sjson.SetRawBytes([]byte(`{"type":"response.completed","response":{"id":"resp_first","status":"completed","output":[]}}`), usagePath, []byte(`{"input_tokens":3,"output_tokens":5,"total_tokens":8}`))
				if err != nil {
					t.Fatal(err)
				}
				second, err := sjson.SetRawBytes(first, usagePath, []byte(`{"input_tokens":900,"output_tokens":99,"total_tokens":999}`))
				if err != nil {
					t.Fatal(err)
				}
				second, err = sjson.SetBytes(second, "type", terminal)
				if err != nil {
					t.Fatal(err)
				}
				second, err = sjson.SetBytes(second, "response.status", strings.TrimPrefix(terminal, "response."))
				if err != nil {
					t.Fatal(err)
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: %s\n\ndata: %s\n\n", first, second)
				}))
				t.Cleanup(server.Close)
				ctx := coreusage.WithRequestedModelAlias(context.Background(), t.Name())
				executor := NewOpenAICompatExecutor("sample", &config.Config{})
				auth := &cliproxyauth.Auth{Provider: "sample", Attributes: map[string]string{"base_url": server.URL, "api_key": "test-secret"}}
				payload := []byte(`{"model":"mock-model","input":[{"type":"compaction","encrypted_content":"native opaque"}]}`)
				stream, err := executor.executeV1ResponsesStream(ctx, auth, cliproxyexecutor.Request{Model: "mock-model", Payload: payload}, cliproxyexecutor.Options{Stream: true, SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: payload}, "sample\x00"+server.URL, []string{"test-secret"})
				if err != nil {
					t.Fatalf("open replay stream: %v", err)
				}
				var streamError error
				for chunk := range stream.Chunks {
					if chunk.Err != nil {
						streamError = chunk.Err
					}
				}
				if streamError == nil {
					t.Fatal("expected duplicate-terminal error")
				}
				record := capture.await(t)
				if !record.Failed || record.Detail.InputTokens != 3 || record.Detail.OutputTokens != 5 || record.Detail.TotalTokens != 8 {
					t.Fatalf("rejected terminal changed the first response usage: %+v", record)
				}
			})
		}
	}
}

func TestOpenAICompatV1ResponsesReplayStreamPreservesBufferedUsage(t *testing.T) {
	const earlierUsage = `{"input_tokens":17,"output_tokens":9,"total_tokens":26,"provider_usage":{"kept":true}}`
	const terminalUsage = `{"input_tokens":3,"output_tokens":2,"total_tokens":5,"provider_usage":{"terminal":true}}`
	for _, tc := range []struct{ name, field, wantUsage string }{
		{name: "omitted terminal usage", wantUsage: earlierUsage},
		{name: "null terminal usage", field: `,"usage":null`, wantUsage: earlierUsage},
		{name: "terminal usage takes precedence", field: `,"usage":` + terminalUsage, wantUsage: terminalUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture := &multiProviderUsageCapture{alias: t.Name(), records: make(chan coreusage.Record, 4)}
			coreusage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() { coreusage.RegisterNamedPlugin(t.Name(), multiProviderNoopUsagePlugin{}) })
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, "data: "+`{"type":"response.in_progress","response":{"model":"upstream-model","usage":`+earlierUsage+`}}`+"\n\ndata: "+`{"type":"response.completed","response":{"id":"resp_next","status":"completed","output":[]`+tc.field+`}}`+"\n\n")
			}))
			t.Cleanup(server.Close)
			ctx := coreusage.WithRequestedModelAlias(context.Background(), t.Name())
			executor := NewOpenAICompatExecutor("sample", &config.Config{})
			auth := &cliproxyauth.Auth{Provider: "sample", Attributes: map[string]string{"base_url": server.URL, "api_key": "test-secret"}}
			payload := []byte(`{"model":"mock-model","input":[{"type":"compaction","encrypted_content":"native opaque"}]}`)
			stream, err := executor.executeV1ResponsesStream(ctx, auth, cliproxyexecutor.Request{Model: "mock-model", Payload: payload}, cliproxyexecutor.Options{Stream: true, SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: payload}, "sample\x00"+server.URL, []string{"test-secret"})
			if err != nil {
				t.Fatal(err)
			}
			var terminal []byte
			for chunk := range stream.Chunks {
				if chunk.Err != nil {
					t.Fatal(chunk.Err)
				}
				for _, line := range strings.Split(string(chunk.Payload), "\n") {
					if strings.HasPrefix(line, "data:") {
						data := []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
						if gjson.GetBytes(data, "type").String() == "response.completed" {
							terminal = bytes.Clone(data)
						}
					}
				}
			}
			if got := gjson.GetBytes(terminal, "response.usage").Raw; got != tc.wantUsage {
				t.Fatalf("terminal usage = %s, want original upstream usage %s", got, tc.wantUsage)
			}
			record := capture.await(t)
			want := gjson.Parse(tc.wantUsage)
			if record.Failed || record.Detail.InputTokens != want.Get("input_tokens").Int() || record.Detail.OutputTokens != want.Get("output_tokens").Int() || record.Detail.TotalTokens != want.Get("total_tokens").Int() || record.ResponseModel != "upstream-model" {
				t.Fatalf("replay usage or response model changed: %+v", record)
			}
		})
	}
}

func TestOpenAICompatV1ResponsesReplayRejectsInvalidJSONResults(t *testing.T) {
	const metadata = `"model":"upstream-model","usage":{"input_tokens":17,"output_tokens":9,"total_tokens":26}`
	for _, tc := range []struct {
		name, body string
		knownUsage bool
	}{
		{name: "malformed JSON", body: `{broken`},
		{name: "missing output", body: `{` + metadata + `,"status":"completed"}`, knownUsage: true},
		{name: "object output", body: `{` + metadata + `,"status":"completed","output":{}}`, knownUsage: true},
		{name: "null output", body: `{` + metadata + `,"status":"completed","output":null}`, knownUsage: true},
		{name: "missing status", body: `{` + metadata + `,"output":[]}`, knownUsage: true},
		{name: "in progress", body: `{` + metadata + `,"status":"in_progress","output":[]}`, knownUsage: true},
		{name: "incomplete", body: `{` + metadata + `,"status":"incomplete","output":[]}`, knownUsage: true},
		{name: "failed", body: `{` + metadata + `,"status":"failed","error":{"code":"server_error"},"output":[]}`, knownUsage: true},
		{name: "completed with error", body: `{` + metadata + `,"status":"completed","error":{"code":"server_error"},"output":[]}`, knownUsage: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture := &multiProviderUsageCapture{alias: t.Name(), records: make(chan coreusage.Record, 4)}
			coreusage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() { coreusage.RegisterNamedPlugin(t.Name(), multiProviderNoopUsagePlugin{}) })
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, tc.body)
			}))
			t.Cleanup(server.Close)
			ctx := coreusage.WithRequestedModelAlias(context.Background(), t.Name())
			executor := NewOpenAICompatExecutor("sample", &config.Config{})
			auth := &cliproxyauth.Auth{Provider: "sample", Attributes: map[string]string{"base_url": server.URL, "api_key": "test-secret"}}
			payload := []byte(`{"model":"mock-model","input":[{"type":"compaction","encrypted_content":"native opaque"}]}`)
			response, err := executor.executeV1Responses(ctx, auth, cliproxyexecutor.Request{Model: "mock-model", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: payload}, "sample\x00"+server.URL, []string{"test-secret"})
			if scoped, ok := err.(cliproxyexecutor.RequestScopedError); !ok || !scoped.IsRequestScoped() || len(response.Payload) != 0 {
				t.Fatalf("invalid JSON result returned response %s and error %v", response.Payload, err)
			}
			record := capture.await(t)
			if !record.Failed {
				t.Fatalf("invalid JSON result was not recorded as a failure: %+v", record)
			}
			if tc.knownUsage && (record.Detail.InputTokens != 17 || record.Detail.OutputTokens != 9 || record.Detail.TotalTokens != 26 || record.ResponseModel != "upstream-model") {
				t.Fatalf("JSON replay failure lost known usage or response model: %+v", record)
			}
		})
	}
}

func TestOpenAICompatV1ResponsesNonStreamReplayPreservesSSEUsageAndModel(t *testing.T) {
	const usage = `{"input_tokens":17,"output_tokens":9,"total_tokens":26,"provider_usage":{"kept":true}}`
	const earlier = `{"type":"response.in_progress","response":{"model":"upstream-model","usage":` + usage + `}}`
	const completed = `{"type":"response.completed","response":{"id":"resp_next","model":"upstream-model","status":"completed","output":[],"usage":` + usage + `}}`
	const completedWithoutMetadata = `{"type":"response.completed","response":{"id":"resp_next","status":"completed","output":[]}}`
	const duplicate = `{"type":"response.completed","response":{"id":"resp_duplicate","model":"duplicate-model","status":"completed","output":[],"usage":{"input_tokens":900,"output_tokens":99,"total_tokens":999}}}`
	for _, tc := range []struct {
		name, body string
		failed     bool
	}{
		{name: "terminal metadata", body: "data: " + completed + "\n\n"},
		{name: "earlier metadata", body: "data: " + earlier + "\n\ndata: " + completedWithoutMetadata + "\n\n"},
		{name: "duplicate terminal", body: "data: " + completed + "\n\ndata: " + duplicate + "\n\n", failed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture := &multiProviderUsageCapture{alias: t.Name(), records: make(chan coreusage.Record, 4)}
			coreusage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() { coreusage.RegisterNamedPlugin(t.Name(), multiProviderNoopUsagePlugin{}) })
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, tc.body)
			}))
			t.Cleanup(server.Close)
			ctx := coreusage.WithRequestedModelAlias(context.Background(), t.Name())
			executor := NewOpenAICompatExecutor("sample", &config.Config{})
			auth := &cliproxyauth.Auth{Provider: "sample", Attributes: map[string]string{"base_url": server.URL, "api_key": "test-secret"}}
			payload := []byte(`{"model":"mock-model","input":[{"type":"compaction","encrypted_content":"native opaque"}]}`)
			response, err := executor.executeV1Responses(ctx, auth, cliproxyexecutor.Request{Model: "mock-model", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: payload}, "sample\x00"+server.URL, []string{"test-secret"})
			if tc.failed {
				if scoped, ok := err.(cliproxyexecutor.RequestScopedError); !ok || !scoped.IsRequestScoped() || len(response.Payload) != 0 {
					t.Fatalf("duplicate returned response %s and error %v", response.Payload, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if !gjson.GetBytes(response.Payload, "usage.provider_usage.kept").Bool() || gjson.GetBytes(response.Payload, "usage.total_tokens").Int() != 26 {
					t.Fatalf("successful SSE replay lost upstream usage metadata: %s", response.Payload)
				}
			}
			record := capture.await(t)
			if record.Failed != tc.failed || record.Detail.InputTokens != 17 || record.Detail.OutputTokens != 9 || record.Detail.TotalTokens != 26 || record.ResponseModel != "upstream-model" {
				t.Fatalf("SSE replay lost authoritative usage or response model: %+v", record)
			}
		})
	}
}

func TestOpenAICompatV1ResponsesPayloadRulesAreFinal(t *testing.T) {
	for _, summary := range []bool{true, false} {
		for _, stream := range []bool{false, true} {
			for _, rules := range []bool{false, true} {
				t.Run(fmt.Sprintf("summary=%t/stream=%t/rules=%t", summary, stream, rules), func(t *testing.T) {
					server, capture := newCodexV1CompactionServer(t, func(int) (string, []byte) {
						return "text/event-stream", codexV1CompactionSSE(codexV1CompactionSummaryResponse())
					})
					cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
						Name: "sample", BaseURL: server.URL,
						APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "test-secret"}},
						Models:        []config.OpenAICompatibilityModel{{Name: "summary-model", Alias: "summary-model", UseV1Compaction: true}},
					}}}
					if rules {
						cfg.Payload = config.PayloadConfig{
							Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "summary-model"}}, Params: map[string]any{
								"model": "configured-model", "stream": false,
								"tools":       []map[string]string{{"type": "function", "name": "configured-tool"}},
								"tool_choice": "required",
							}}},
							Filter: []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "summary-model"}}, Params: []string{"input.0", "prompt_cache_key"}}},
						}
					}
					item := `{"type":"compaction_trigger","id":"trigger-final"}`
					if !summary {
						response, err := helps.ConvertResponsesCompactionResponse(codexV1CompactionSummaryResponse(), "summary-model", "sample\x00"+server.URL, []string{"test-secret"})
						if err != nil {
							t.Fatal(err)
						}
						item = gjson.GetBytes(response, "output.1").Raw
					}
					payload := []byte(`{"model":"summary-model","prompt_cache_key":"caller-cache","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"remove-first"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"keep-second"}]},` + item + `]}`)
					executor := NewOpenAICompatExecutor("sample", cfg)
					auth := &cliproxyauth.Auth{Provider: "sample", Attributes: map[string]string{"base_url": server.URL, "api_key": "test-secret"}}
					req := cliproxyexecutor.Request{Model: "summary-model", Payload: payload}
					opts := cliproxyexecutor.Options{Stream: stream, SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: payload}
					if stream {
						result, err := executor.ExecuteStream(context.Background(), auth, req, opts)
						if err != nil {
							t.Fatal(err)
						}
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
						}
					} else if _, err := executor.Execute(context.Background(), auth, req, opts); err != nil {
						t.Fatal(err)
					}
					requests := capture.snapshot()
					if len(requests) != 1 || requests[0].path != "/responses" {
						t.Fatalf("upstream requests = %+v, want one /responses request", requests)
					}
					body := requests[0].body
					input := gjson.GetBytes(body, "input").Array()
					wantCount := 3
					if summary {
						wantCount++
					}
					if rules {
						wantCount--
						if gjson.GetBytes(body, "model").String() != "configured-model" || gjson.GetBytes(body, "stream").Bool() || gjson.GetBytes(body, "tools.0.name").String() != "configured-tool" || gjson.GetBytes(body, "tool_choice").String() != "required" || gjson.GetBytes(body, "prompt_cache_key").Exists() {
							t.Fatalf("built-in processing changed final user rules: %s", body)
						}
						if len(input) != wantCount || input[0].Get("content.0.text").String() != "keep-second" {
							t.Fatalf("input filter was not applied exactly once: %s", body)
						}
					} else if len(input) != wantCount || gjson.GetBytes(body, "model").String() != "summary-model" || gjson.GetBytes(body, "stream").Bool() != (summary || stream) {
						t.Fatalf("default request preparation changed: %s", body)
					}
					if summary {
						if input[len(input)-1].Get("type").String() != "compaction_trigger" || input[len(input)-1].Get("id").String() != "trigger-final" || strings.Count(gjson.GetBytes(body, "input").Raw, "Summarize the conversation so far") != 1 {
							t.Fatalf("summary request lost its trigger or instruction: %s", body)
						}
					} else if input[len(input)-1].Get("role").String() != "developer" || !strings.Contains(input[len(input)-1].Get("content.0.text").String(), "Carry the task context forward.") || strings.Contains(string(body), "cpa-responses-v1-compaction-v1.") {
						t.Fatalf("capsule replay was not expanded before final user rules: %s", body)
					}
				})
			}
		}
	}
}

func TestOpenAICompatV1ResponsesPayloadRulesRejectUnsafeSummaryHistory(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			server, capture := newCodexV1CompactionServer(t, func(int) (string, []byte) {
				return "text/event-stream", codexV1CompactionSSE(codexV1CompactionSummaryResponse())
			})
			cfg := &config.Config{
				OpenAICompatibility: []config.OpenAICompatibility{{Name: "sample", BaseURL: server.URL, Models: []config.OpenAICompatibilityModel{{Name: "summary-model", Alias: "summary-model", UseV1Compaction: true}}}},
				Payload:             config.PayloadConfig{Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "summary-model"}}, Params: map[string]any{"previous_response_id": "resp_server_owned"}}}},
			}
			executor := NewOpenAICompatExecutor("sample", cfg)
			auth := &cliproxyauth.Auth{Provider: "sample", Attributes: map[string]string{"base_url": server.URL, "api_key": "test-secret"}}
			req := codexV1CompactionRequest()
			opts := cliproxyexecutor.Options{Stream: stream, SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: req.Payload}
			var executionError error
			if stream {
				_, executionError = executor.ExecuteStream(context.Background(), auth, req, opts)
			} else {
				_, executionError = executor.Execute(context.Background(), auth, req, opts)
			}
			if scoped, ok := executionError.(cliproxyexecutor.RequestScopedError); !ok || !scoped.IsRequestScoped() {
				t.Fatalf("unsafe configured summary history returned no scoped error: %v", executionError)
			}
			if requests := capture.snapshot(); len(requests) != 0 {
				t.Fatalf("unsafe configured summary history reached upstream: %+v", requests)
			}
		})
	}
}
