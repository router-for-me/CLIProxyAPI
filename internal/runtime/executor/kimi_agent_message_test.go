package executor

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestKimiExecutorResponsesNormalizesAgentMessages(t *testing.T) {
	const payload = `{"model":"kimi-k3","input":[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"start"}]},
		{"type":"function_call","call_id":"call_a","name":"lookup","arguments":"{}"},
		{"type":"function_call","call_id":"call_b","name":"lookup","arguments":"{}"},
		{"type":"function_call_output","call_id":"call_a","output":"result A"},
		{"type":"agent_message","id":"amsg_1","author":"/root","recipient":"/root/worker","content":[{"type":"input_text","text":"task text"},{"type":"encrypted_content","encrypted_content":"synthetic handoff"}]},
		{"type":"message","role":"developer","content":[{"type":"input_text","text":"developer note"}]},
		{"type":"function_call_output","call_id":"call_b","output":"result B"}
	]}`
	for _, tc := range []struct {
		name   string
		stream bool
	}{
		{name: "JSON"},
		{name: "SSE", stream: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
					t.Errorf("upstream request = %s %s, want POST /v1/responses", r.Method, r.URL.Path)
					http.Error(w, "unexpected endpoint", http.StatusNotFound)
					return
				}
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					t.Errorf("read upstream request: %v", errRead)
					http.Error(w, "read error", http.StatusBadRequest)
					return
				}
				outbound := ojson.ParseBytes(body)
				items := outbound.Get("input").Array()
				for _, item := range items {
					if item.Get("type").String() == "agent_message" {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusBadRequest)
						_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"unsupported input item: agent_message"}}`)
						return
					}
				}
				if len(items) != 7 {
					t.Errorf("upstream got %d input items, want 7: %s", len(items), body)
					http.Error(w, "unexpected input size", http.StatusBadRequest)
					return
				}
				original := ojson.Get(payload, "input").Array()
				for i, originalIndex := range []int{0, 1, 2, 3, 6, 4, 5} {
					if originalIndex != 4 && items[i].Raw != original[originalIndex].Raw {
						t.Errorf("upstream input[%d] = %s, want %s", i, items[i].Raw, original[originalIndex].Raw)
					}
				}
				item := items[5]
				if item.Get("type").String() != "message" || item.Get("role").String() != "user" || item.Get("id").String() != "amsg_1" {
					t.Errorf("upstream handoff did not become a user message: %s", item.Raw)
				}
				if item.Get("content.0").Raw != original[4].Get("content.0").Raw {
					t.Errorf("existing handoff text changed: %s", item.Raw)
				}
				part := item.Get("content.1")
				if part.Get("type").String() != "input_text" || part.Get("text").String() != "synthetic handoff" || part.Get("encrypted_content").Exists() {
					t.Errorf("upstream handoff content was not converted: %s", part.Raw)
				}
				if outbound.Get("stream").Bool() != tc.stream {
					t.Errorf("wrong upstream stream flag: %s", body)
				}
				if outbound.Get("max_output_tokens").Int() != 123 {
					t.Errorf("payload rule did not match normalized handoff: %s", body)
				}
				const completed = `{"id":"resp_test","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"handoff received"}]}]}`
				if tc.stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":"+completed+"}\n\n")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, completed)
			}))
			t.Cleanup(upstream.Close)

			exec := NewKimiExecutor(&config.Config{Payload: config.PayloadConfig{
				Override: []config.PayloadRule{{
					Models: []config.PayloadModelRule{{Name: "kimi-k3", Match: []map[string]any{{"input.5.role": "user"}}}},
					Params: map[string]any{"max_output_tokens": 123},
				}},
			}})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": upstream.URL}}
			req := cliproxyexecutor.Request{Model: "kimi-k3", Payload: []byte(payload)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: tc.stream}
			if !tc.stream {
				resp, errExecute := exec.Execute(t.Context(), auth, req, opts)
				if errExecute != nil {
					t.Fatalf("Execute() error = %v", errExecute)
				}
				if got := ojson.GetBytes(resp.Payload, "output.0.content.0.text").String(); got != "handoff received" {
					t.Fatalf("unexpected completed response: %s", resp.Payload)
				}
				return
			}

			result, errStream := exec.ExecuteStream(t.Context(), auth, req, opts)
			if errStream != nil {
				t.Fatalf("ExecuteStream() error = %v", errStream)
			}
			var output strings.Builder
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatalf("stream chunk error = %v", chunk.Err)
				}
				output.Write(chunk.Payload)
			}
			if got := output.String(); !strings.Contains(got, "event: response.completed\n") || !strings.Contains(got, "handoff received") {
				t.Fatalf("stream did not complete with expected output: %s", got)
			}
		})
	}
}
