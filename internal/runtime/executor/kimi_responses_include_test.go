package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestKimiResponsesIncludes(t *testing.T) {
	cases := []struct {
		name     string
		include  string
		override bool
		want     string
	}{
		{name: "missing include"},
		{name: "empty include", include: `[]`, want: `[]`},
		{name: "supported values", include: `["reasoning.encrypted_content","file_search_call.results"]`, want: `["reasoning.encrypted_content","file_search_call.results"]`},
		{name: "unsupported value only", include: `["output.encrypted_content"]`, want: `[]`},
		{name: "mixed and repeated values", include: `["output.encrypted_content","reasoning.encrypted_content","output.encrypted_content","file_search_call.results","output.encrypted_content"]`, want: `["reasoning.encrypted_content","file_search_call.results"]`},
		{name: "payload override runs last", include: `["output.encrypted_content"]`, override: true, want: `["output.encrypted_content","reasoning.encrypted_content"]`},
	}
	for _, stream := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("stream=%t/%s", stream, tc.name), func(t *testing.T) {
				var upstreamBody []byte
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var errRead error
					upstreamBody, errRead = io.ReadAll(r.Body)
					if errRead != nil {
						t.Error(errRead)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					if !tc.override {
						for _, include := range ojson.GetBytes(upstreamBody, "include").Array() {
							if include.String() == "output.encrypted_content" {
								w.WriteHeader(http.StatusBadRequest)
								_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"include value output.encrypted_content is not supported"}}`)
								return
							}
						}
					}
					response := `{"id":"resp_include","object":"response","model":"k3.1-pro","status":"completed","output":[]}`
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", response)
					} else {
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, response)
					}
				}))
				defer server.Close()

				cfg := &config.Config{}
				if tc.override {
					cfg.Payload.Override = []config.PayloadRule{{
						Models: []config.PayloadModelRule{{Name: "kimi-k3.1-pro"}},
						Params: map[string]any{"include": []string{"output.encrypted_content", "reasoning.encrypted_content"}},
					}}
				}
				executor := NewKimiExecutor(cfg)
				auth := &cliproxyauth.Auth{Provider: "kimi", Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
				input := `[{"type":"message","role":"user","content":[{"type":"input_text","text":"Continue"}]}]`
				payload := `{"model":"kimi-k3.1-pro","input":` + input
				if tc.include != "" {
					payload += `,"include":` + tc.include
				}
				payload += `}`
				req := cliproxyexecutor.Request{Model: "kimi-k3.1-pro", Payload: []byte(payload)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: []byte(payload), Stream: stream}
				if stream {
					result, errStream := executor.ExecuteStream(context.Background(), auth, req, opts)
					if errStream != nil {
						t.Fatal(errStream)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else if _, errExecute := executor.Execute(context.Background(), auth, req, opts); errExecute != nil {
					t.Fatal(errExecute)
				}
				if got := ojson.GetBytes(upstreamBody, "include").Raw; got != tc.want {
					t.Fatalf("include = %s, want %s", got, tc.want)
				}
				if got := ojson.GetBytes(upstreamBody, "input").Raw; got != input {
					t.Fatalf("unrelated input changed: %s", got)
				}
			})
		}
	}
}
