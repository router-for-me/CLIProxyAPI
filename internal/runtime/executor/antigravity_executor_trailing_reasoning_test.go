package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// issue6530EffectiveContents models the assumed upstream view of
// request.contents: the reporter still gets the 400 although an empty user turn
// is appended, so empty-text-only user turns are treated as absent here.
func issue6530EffectiveContents(body []byte) []gjson.Result {
	var effective []gjson.Result
	for _, content := range gjson.GetBytes(body, "request.contents").Array() {
		if content.Get("role").String() == "user" {
			empty := true
			for _, part := range content.Get("parts").Array() {
				if part.Get("text").String() != "" || len(part.Map()) != 1 {
					empty = false
					break
				}
			}
			if empty {
				continue
			}
		}
		effective = append(effective, content)
	}
	return effective
}

// issue6530AcceptedTail reports whether upstream accepts the turn as the final
// one: a user turn, or the model-role function response Antigravity expects.
func issue6530AcceptedTail(content gjson.Result) bool {
	return content.Get("role").String() == "user" || content.Get("parts.0.functionResponse").Exists()
}

func issue6530Server(t *testing.T, captured chan<- []byte, stream bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read request body: %v", errRead)
			return
		}
		captured <- body

		contents := issue6530EffectiveContents(body)
		if len(contents) == 0 || !issue6530AcceptedTail(contents[len(contents)-1]) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":400,"message":"Requests ending with a model turn are not supported.","status":"INVALID_ARGUMENT"}}`))
			return
		}
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"OK\"}]},\"finishReason\":\"STOP\"}]}}\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"OK"}]},"finishReason":"STOP"}]}}`))
	}))
}

func issue6530Payload(model string, stream bool, prefix, summary, content string) []byte {
	streamValue := "false"
	if stream {
		streamValue = "true"
	}
	return []byte(`{"model":"` + model + `","stream":` + streamValue + `,` +
		`"reasoning":{"effort":"high","summary":"detailed"},"include":["reasoning.encrypted_content"],"input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"Say OK."}]},` + prefix +
		`{"type":"reasoning","id":"rs_repro_0","summary":` + summary + `,"content":` + content + `}` +
		`]}`)
}

func TestAntigravityDropsTrailingStructuredReasoningTurn(t *testing.T) {
	const toolPrefix = `{"type":"function_call","call_id":"call_1","name":"run","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"call_1","output":"done"},`
	const structured = `[{"type":"summary_text","text":"thinking"}]`
	tests := []struct {
		name    string
		model   string
		stream  bool
		prefix  string
		summary string
		content string
	}{
		{name: "stream structured summary", stream: true, summary: structured},
		{name: "non-stream structured summary", stream: false, summary: structured},
		{name: "stream empty content array", stream: true, summary: structured, content: `[]`},
		{name: "stream gemini-pro-agent", model: "gemini-pro-agent", stream: true, summary: structured},
		{name: "stream after tool output", stream: true, prefix: toolPrefix, summary: structured},
		{name: "stream plain summary", stream: true, summary: `["thinking"]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.model == "" {
				tt.model = "gemini-3.8-flash-high"
			}
			if tt.content == "" {
				tt.content = "null"
			}
			captured := make(chan []byte, 1)
			server := issue6530Server(t, captured, tt.stream)
			defer server.Close()

			executor := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
			auth := testAntigravityAuth(server.URL)
			auth.Metadata["project_id"] = "project-1"
			req := cliproxyexecutor.Request{Model: tt.model, Payload: issue6530Payload(tt.model, tt.stream, tt.prefix, tt.summary, tt.content)}
			opts := cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatOpenAIResponse,
				ResponseFormat: sdktranslator.FormatOpenAIResponse,
				Stream:         tt.stream,
			}
			if tt.stream {
				result, errExecute := executor.ExecuteStream(context.Background(), auth, req, opts)
				if errExecute != nil {
					t.Fatalf("ExecuteStream() error = %v", errExecute)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream chunk error = %v", chunk.Err)
					}
				}
			} else if _, errExecute := executor.Execute(context.Background(), auth, req, opts); errExecute != nil {
				t.Fatalf("Execute() error = %v", errExecute)
			}

			body := <-captured
			contents := gjson.GetBytes(body, "request.contents").Array()
			if len(contents) == 0 {
				t.Fatalf("request.contents empty; body=%s", body)
			}
			if last := contents[len(contents)-1]; !issue6530AcceptedTail(last) {
				t.Fatalf("trailing turn = %s, want user or function response; body=%s", last.Raw, body)
			}
			if strings.Contains(gjson.GetBytes(body, "request.contents").Raw, `"thought":true`) {
				t.Fatalf("trailing reasoning-only turn should be dropped; body=%s", body)
			}
		})
	}
}
