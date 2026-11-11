package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// contextDiagnosticPayloads carries one reasoning item and the observed calls it
// reaches, so both gate states exercise identical payloads.
type contextDiagnosticPayloads struct {
	Item      map[string]any
	Request   pluginapi.RequestInterceptRequest
	Added     pluginapi.StreamChunkInterceptRequest
	Done      pluginapi.StreamChunkInterceptRequest
	Completed pluginapi.StreamChunkInterceptRequest
}

// newContextDiagnosticPayloads builds the calls one reasoning item reaches,
// using distinguishable content sentinels to prove field-level redaction.
func newContextDiagnosticPayloads(t *testing.T) contextDiagnosticPayloads {
	t.Helper()
	item := map[string]any{
		"id": "private-reasoning-identifier", "type": "reasoning", "status": "completed",
		"encrypted_content": "private-encrypted-sentinel",
		"summary":           []any{map[string]any{"type": "summary_text", "text": "private-summary-sentinel"}},
	}
	request := pluginapi.RequestInterceptRequest{
		RequestID: "diagnostic-request", TraceID: "diagnostic-trace",
		RequestedModel: "Iterative-Model", Model: "terra",
		SourceFormat: "openai-response", ToFormat: "codex",
		Body: marshalBody(t, map[string]any{
			"previous_response_id": "private-response-identifier", "input": []any{item},
		}),
	}
	stream := pluginapi.StreamChunkInterceptRequest{
		RequestID: request.RequestID, RequestedModel: request.RequestedModel,
		Model: request.Model, SourceFormat: request.SourceFormat,
	}
	added := stream
	added.Body = marshalBody(t, map[string]any{"type": "response.output_item.added", "item": item})
	done := stream
	done.Body = append([]byte("data: "), marshalBody(t, map[string]any{"type": "response.output_item.done", "item": item})...)
	completed := stream
	completed.Body = marshalBody(t, map[string]any{
		"type":     "response.completed",
		"response": map[string]any{"id": "private-response-identifier", "output": []any{item}},
	})
	return contextDiagnosticPayloads{Item: item, Request: request, Added: added, Done: done, Completed: completed}
}

// TestContextDiagnosticRPCRedactsContentAndProjectsLane exercises the public
// methods and verifies journal-visible structure without exposing payload
// content, and that the lane projection alone rewrites the outgoing request.
func TestContextDiagnosticRPCRedactsContentAndProjectsLane(t *testing.T) {
	runtime := newTestRuntime(t)
	installTestPlugin(t, runtime)
	if !pluginRegistration(runtime.loadedConfig()).Capabilities.RequestInterceptor {
		t.Fatal("request observation capability is not published")
	}
	if !pluginRegistration(runtime.loadedConfig()).Capabilities.ResponseStreamInterceptor {
		t.Fatal("stream observation capability is not published")
	}

	payloads := newContextDiagnosticPayloads(t)
	before := payloads.Request
	before.ToFormat = ""
	unrelated := payloads.Request
	unrelated.RequestedModel = "unrelated-model"

	cases := []struct {
		name           string
		method         string
		request        any
		collectionKey  string
		stage          string
		wantProjection bool
	}{
		{name: "before credentials", method: pluginabi.MethodRequestInterceptBefore, request: before},
		{name: "unrelated model", method: pluginabi.MethodRequestInterceptAfter, request: unrelated},
		{name: "replay input", method: pluginabi.MethodRequestInterceptAfter, request: payloads.Request, collectionKey: "input", stage: "before_provider_translation", wantProjection: true},
		{name: "initial item", method: pluginabi.MethodResponseInterceptStreamChunk, request: payloads.Added, collectionKey: "output", stage: "response.output_item.added"},
		{name: "finished item", method: pluginabi.MethodResponseInterceptStreamChunk, request: payloads.Done, collectionKey: "output", stage: "response.output_item.done"},
		{name: "completed output", method: pluginabi.MethodResponseInterceptStreamChunk, request: payloads.Completed, collectionKey: "output", stage: "response.completed"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			logs := captureRouteLogs(runtime)
			raw := marshalBody(t, testCase.request)
			original := bytes.Clone(raw)
			result, errHandle := handleMethod(testCase.method, raw)
			if errHandle != nil {
				t.Fatal(errHandle)
			}
			if !bytes.Equal(raw, original) {
				t.Fatal("interceptor modified its input buffer")
			}
			if !bytes.Equal(marshalBody(t, testCase.request), original) {
				t.Fatal("interceptor modified the request payload")
			}
			var reply envelope
			if errDecode := json.Unmarshal(result, &reply); errDecode != nil {
				t.Fatal(errDecode)
			}
			if !reply.OK {
				t.Fatalf("interceptor failed: %s", result)
			}
			var modifications map[string]any
			if errDecode := json.Unmarshal(reply.Result, &modifications); errDecode != nil {
				t.Fatal(errDecode)
			}
			for name, value := range modifications {
				if name == "Body" {
					continue
				}
				if value != nil && !reflect.ValueOf(value).IsZero() {
					t.Fatalf("interceptor returned a modification for %s: %#v", name, value)
				}
			}
			var projection pluginapi.RequestInterceptResponse
			if errDecode := json.Unmarshal(reply.Result, &projection); errDecode != nil {
				t.Fatal(errDecode)
			}
			if (len(projection.Body) != 0) != testCase.wantProjection {
				t.Fatalf("lane projection rewrote %d body bytes, want a projection = %v", len(projection.Body), testCase.wantProjection)
			}
			if testCase.collectionKey == "" {
				if len(*logs) != 0 {
					t.Fatalf("out-of-scope hook emitted %d diagnostics", len(*logs))
				}
				return
			}
			if len(*logs) != 1 {
				t.Fatalf("context diagnostics = %d, want one", len(*logs))
			}
			record := (*logs)[0]
			if record.level != "debug" {
				t.Fatalf("diagnostic level = %s, want debug", record.level)
			}
			for _, secret := range []string{"private-reasoning-identifier", "private-response-identifier", "private-encrypted-sentinel", "private-summary-sentinel"} {
				if strings.Contains(record.message, secret) {
					t.Fatalf("journal message exposed sentinel %s", secret)
				}
			}
			var journal map[string]any
			if errDecode := json.Unmarshal([]byte(strings.TrimPrefix(record.message, "model-sequence-router: context ")), &journal); errDecode != nil {
				t.Fatal(errDecode)
			}
			if journal["request_id"] != payloads.Request.RequestID || journal["stage"] != testCase.stage {
				t.Fatalf("journal correlation = %#v", journal)
			}
			collection := journal[testCase.collectionKey].(map[string]any)
			items := collection["reasoning"].([]any)
			if len(items) != 1 {
				t.Fatalf("reasoning shapes = %d, want one", len(items))
			}
			shape := items[0].(map[string]any)
			if shape["status"] != payloads.Item["status"] || shape["id_hash"] != shortValueHash(payloads.Item["id"].(string)) {
				t.Fatalf("reasoning shape = %#v", shape)
			}
			fields := shape["fields"].(map[string]any)
			if len(fields) != len(payloads.Item) || fields["encrypted_content"] != "string" {
				t.Fatalf("reasoning field types = %#v", fields)
			}
		})
	}
}

// TestContextDiagnosticsStaySilentWhenUnrequested verifies a configuration that
// leaves diagnostics.context unset emits no context record at any observed stage.
func TestContextDiagnosticsStaySilentWhenUnrequested(t *testing.T) {
	cfg, errCompile := decodeAndCompileConfig([]byte(`
aliases:
  - alias: Iterative-Model
    targets: [{provider: codex, model: terra}]
`), 1)
	if errCompile != nil {
		t.Fatal(errCompile)
	}
	runtime := newRuntimeState(func() time.Time { return time.Unix(100, 0) })
	runtime.config.Store(cfg)
	installTestPlugin(t, runtime)
	logs := captureRouteLogs(runtime)
	payloads := newContextDiagnosticPayloads(t)

	calls := []struct {
		method  string
		request any
	}{
		{method: pluginabi.MethodRequestInterceptAfter, request: payloads.Request},
		{method: pluginabi.MethodResponseInterceptStreamChunk, request: payloads.Added},
		{method: pluginabi.MethodResponseInterceptStreamChunk, request: payloads.Done},
		{method: pluginabi.MethodResponseInterceptStreamChunk, request: payloads.Completed},
	}
	for _, call := range calls {
		if _, errHandle := handleMethod(call.method, marshalBody(t, call.request)); errHandle != nil {
			t.Fatal(errHandle)
		}
	}
	if len(*logs) != 0 {
		t.Fatalf("unrequested context diagnostics = %d, want none: %#v", len(*logs), *logs)
	}
}

// TestDiagnosticStatusDistinguishesPresenceWithoutLoggingArbitraryText verifies
// that null, absence, known statuses, and unsupported values remain distinct.
func TestDiagnosticStatusDistinguishesPresenceWithoutLoggingArbitraryText(t *testing.T) {
	cases := []struct {
		name string
		item map[string]any
		want string
	}{
		{name: "absent", item: map[string]any{}, want: "absent"},
		{name: "null", item: map[string]any{"status": nil}, want: "null"},
		{name: "in progress", item: map[string]any{"status": "in_progress"}, want: "in_progress"},
		{name: "completed", item: map[string]any{"status": "completed"}, want: "completed"},
		{name: "incomplete", item: map[string]any{"status": "incomplete"}, want: "incomplete"},
		{name: "arbitrary text", item: map[string]any{"status": "private-status-sentinel"}, want: "other"},
		{name: "empty", item: map[string]any{"status": ""}, want: "other"},
		{name: "object", item: map[string]any{"status": map[string]any{"secret": "private-status-sentinel"}}, want: "non_string"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := diagnosticStatus(testCase.item); got != testCase.want {
				t.Fatalf("status class = %s, want %s", got, testCase.want)
			}
		})
	}
}
