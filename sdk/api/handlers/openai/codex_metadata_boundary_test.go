package openai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/codex/openai/responses"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexMetadataBoundaryThroughResponsesHandler(t *testing.T) {
	metadata := `{"metadata":{"openai_chatgpt_moderation_metadata":{"generation":{},"prompt":{},"tool_call":{},"tool_response":{}}},"response_id":"resp_example","sequence_number":30,"type":"response.metadata"}`
	for _, buffering := range []bool{false, true} {
		for _, newline := range []string{"\n", "\r\n"} {
			for _, terminal := range []struct{ name, payload string }{
				{"error", `{"type":"error","error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"The server is overloaded or not ready yet. Please try again later."},"sequence_number":31}`},
				{"response.completed", `{"type":"response.completed","response":{"id":"resp_example","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`},
			} {
				t.Run(fmt.Sprintf("buffering=%v/newline=%q/%s", buffering, newline, terminal.name), func(t *testing.T) {
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						w.Header().Set("Content-Type", "text/event-stream")
						// Exercise a buffered frame boundary before the metadata releases bootstrap.
						_, _ = fmt.Fprint(w, strings.Join([]string{
							`data: {"type":"response.created","response":{"id":"resp_example"}}`, "",
							"event: response.in_progress", `data: {"type":"response.in_progress","response":{"id":"resp_example"}}`, "",
							"data: " + metadata, "", "event: " + terminal.name, "data: " + terminal.payload, "", "",
						}, newline))
					}))
					defer upstream.Close()
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					executor := runtimeexecutor.NewCodexExecutor(&config.Config{Codex: config.CodexConfig{StreamBootstrapBuffering: buffering}})
					result, err := executor.ExecuteStream(ctx,
						&cliproxyauth.Auth{Attributes: map[string]string{"base_url": upstream.URL, "api_key": "test"}},
						cliproxyexecutor.Request{Model: "gpt-5.6-terra", Payload: []byte(`{"model":"gpt-5.6-terra","input":"hello"}`)},
						cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Stream: true})
					if err != nil {
						t.Fatal(err)
					}
					data := make(chan []byte)
					errs := make(chan *interfaces.ErrorMessage)
					forwarded := make(chan struct{})
					go func() {
						defer close(forwarded)
						defer close(data)
						defer close(errs)
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								select {
								case errs <- &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: chunk.Err}:
								case <-ctx.Done():
								}
								return
							}
							select {
							case data <- chunk.Payload:
							case <-ctx.Done():
								return
							}
						}
					}()
					handler, recorder, ginContext, flusher := newResponsesStreamTestHandler(t)
					handler.forwardResponsesStream(ginContext, flusher, func(error) { cancel() }, data, errs, nil)
					<-forwarded
					var payloads []string
					for _, frame := range strings.Split(recorder.Body.String(), "\n\n") {
						if payload, ok := responsesSSEDataPayload([]byte(frame)); ok && gjson.ValidBytes(payload) {
							if event := responsesSSEEventName([]byte(frame)); event != "" && event != gjson.GetBytes(payload, "type").String() {
								t.Fatalf("event field attached to the wrong payload: %s", frame)
							}
							payloads = append(payloads, string(payload))
						}
					}
					if len(payloads) != 4 || payloads[2] != metadata || gjson.Get(payloads[0], "type").String() != "response.created" || gjson.Get(payloads[1], "type").String() != "response.in_progress" {
						t.Fatalf("metadata boundary lost: %s", recorder.Body.String())
					}
					last := gjson.Parse(payloads[3])
					if last.Get("type").String() != terminal.name {
						t.Fatalf("wrong terminal event: %s", payloads[3])
					}
					if terminal.name == "error" && (last.Get("error.type").String() != "service_unavailable_error" || last.Get("error.code").String() != "server_is_overloaded" || last.Get("sequence_number").Int() != 31) {
						t.Fatalf("real upstream error was replaced: %s", payloads[3])
					}
				})
			}
		}
	}
}

func TestCodexBlankPreludeDoesNotStartStream(t *testing.T) {
	for _, buffering := range []bool{false, true} {
		t.Run(fmt.Sprintf("buffering=%v", buffering), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, "\n\n\r\n"+`data: {"type":"error","error":{"type":"invalid_request_error","code":"invalid_value","message":"Invalid input."}}`+"\n\n")
			}))
			defer upstream.Close()
			executor := runtimeexecutor.NewCodexExecutor(&config.Config{Codex: config.CodexConfig{StreamBootstrapBuffering: buffering}})
			result, err := executor.ExecuteStream(t.Context(),
				&cliproxyauth.Auth{Attributes: map[string]string{"base_url": upstream.URL, "api_key": "test"}},
				cliproxyexecutor.Request{Model: "gpt-5.6-terra", Payload: []byte(`{"model":"gpt-5.6-terra","input":"hello"}`)},
				cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Stream: true})
			if err != nil {
				t.Fatal(err)
			}
			var streamErr error
			var payloads []string
			for chunk := range result.Chunks {
				if len(chunk.Payload) > 0 {
					payloads = append(payloads, string(chunk.Payload))
				}
				if chunk.Err != nil {
					streamErr = chunk.Err
				}
			}
			if len(payloads) > 0 {
				t.Fatalf("blank prelude incorrectly started the response stream: %q", payloads)
			}
			if streamErr == nil || !strings.Contains(streamErr.Error(), "invalid_value") {
				t.Fatalf("initial upstream error lost: %v", streamErr)
			}
		})
	}
}
