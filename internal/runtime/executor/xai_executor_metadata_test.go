package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestXAIHTTPResponsesMetadata(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, authCase := range []struct {
			name  string
			attrs map[string]string
			strip bool
		}{
			{"subscription", map[string]string{"auth_kind": "oauth"}, true},
			{"oauth_official_api", map[string]string{"auth_kind": "oauth", "using_api": "true"}, false},
			{"api_key", map[string]string{"auth_kind": "api_key"}, false},
		} {
			for _, meta := range []string{"", `,"metadata":{}`, `,"metadata":{"conversation_id":"synthetic"}`} {
				t.Run(fmt.Sprintf("%s/stream=%t/metadata=%s", authCase.name, stream, meta), func(t *testing.T) {
					received := make(chan []byte, 1)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						b, err := io.ReadAll(r.Body)
						if err != nil {
							t.Errorf("read body: %v", err)
							w.WriteHeader(500)
							return
						}
						received <- b
						if authCase.strip && gjson.GetBytes(b, "metadata").Exists() {
							w.WriteHeader(400)
							_, _ = io.WriteString(w, `{"error":{"message":"Argument not supported: metadata"}}`)
							return
						}
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_metadata\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"model\":\"grok-4.6\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
					}))
					defer server.Close()
					attrs := map[string]string{"base_url": server.URL}
					for k, v := range authCase.attrs {
						attrs[k] = v
					}
					auth := &cliproxyauth.Auth{ID: "metadata-test", Provider: "xai", Attributes: attrs, Metadata: map[string]any{"access_token": "synthetic-token"}}
					payload := []byte(`{"model":"grok-4.6","input":[{"role":"user","content":"hello"}],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"metadata":{"type":"string"}}}}]` + meta + `}`)
					before := bytes.Clone(payload)
					req := cliproxyexecutor.Request{Model: "grok-4.6", Payload: payload}
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: stream, Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "synthetic-session"}}
					exec := NewXAIExecutor(&config.Config{})
					if stream {
						result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
						if err != nil {
							t.Fatalf("ExecuteStream: %v", err)
						}
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatalf("stream chunk: %v", chunk.Err)
							}
						}
					} else {
						if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
							t.Fatalf("Execute: %v", err)
						}
					}
					got := <-received
					want := gjson.GetBytes(before, "metadata")
					if authCase.strip {
						if gjson.GetBytes(got, "metadata").Exists() {
							t.Fatal("subscription metadata forwarded")
						}
					} else if gjson.GetBytes(got, "metadata").Raw != want.Raw {
						t.Fatal("official API metadata changed")
					}
					if !bytes.Equal(payload, before) {
						t.Fatal("original request mutated")
					}
					if opts.Metadata[cliproxyexecutor.ExecutionSessionMetadataKey] != "synthetic-session" {
						t.Fatal("local session context changed")
					}
					if gjson.GetBytes(got, "prompt_cache_key").String() != "synthetic-session" {
						t.Fatal("session affinity lost")
					}
					if gjson.GetBytes(got, "tools.0.parameters.properties.metadata.type").String() != "string" {
						t.Fatal("nested metadata schema changed")
					}
					if gjson.GetBytes(got, "input.0.content").String() != "hello" {
						t.Fatal("input changed")
					}
				})
			}
		}
	}
}
