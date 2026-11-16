package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	internalcache "github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	internalsignature "github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestGeminiClaudeCarrierExecutorReplay(t *testing.T) {
	for _, backend := range []string{"vertex", "gemini"} {
		for _, mode := range []string{"native", "override", "filter"} {
			for _, stream := range []bool{false, true} {
				name := backend + "/" + mode + "/nonstream"
				if stream {
					name = backend + "/" + mode + "/stream"
				}
				t.Run(name, func(t *testing.T) {
					internalcache.ClearAntigravityReasoningReplayCache()
					t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)
					signature := testVertexBareTinkSignature()
					captured := make(chan []byte, 1)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, errRead := io.ReadAll(r.Body)
						if errRead != nil {
							t.Errorf("read request: %v", errRead)
							w.WriteHeader(http.StatusBadRequest)
							return
						}
						captured <- body
						if strings.Contains(r.URL.Path, "streamGenerateContent") {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = io.WriteString(w, "data: "+vertexBareTinkTestOKResponse+"\n\n")
							return
						}
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, vertexBareTinkTestOKResponse)
					}))
					defer server.Close()

					cfg := &config.Config{}
					models := []config.PayloadModelRule{{Name: "gemini-4-carbon"}}
					signaturePath := "contents.1.parts.0.thoughtSignature"
					if mode == "override" {
						cfg.Payload.Override = []config.PayloadRule{{Models: models, Params: map[string]any{signaturePath: "configured-signature"}}}
					} else if mode == "filter" {
						cfg.Payload.Filter = []config.PayloadFilterRule{{Models: models, Params: []string{signaturePath}}}
					}
					gemini := NewGeminiExecutor(cfg)
					execute, executeStream := gemini.Execute, gemini.ExecuteStream
					if backend == "vertex" {
						vertex := NewGeminiVertexExecutor(cfg)
						execute, executeStream = vertex.Execute, vertex.ExecuteStream
					}
					auth := &cliproxyauth.Auth{ID: t.Name(), Provider: backend, Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL}}
					request := ojson.NewDocString(`{"model":"gemini-4-carbon","max_tokens":1024,"messages":[{"role":"user","content":"lookup"},{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":""},{"type":"tool_use","id":"call-1","name":"lookup","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"ok"}]}]}`)
					_ = request.Set("messages.1.content.0.signature", signature)
					req := cliproxyexecutor.Request{Model: "gemini-4-carbon", Payload: request.Bytes()}
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
					if stream {
						response, errExecute := executeStream(context.Background(), auth, req, opts)
						if errExecute != nil {
							t.Fatal(errExecute)
						}
						for chunk := range response.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
						}
					} else if _, errExecute := execute(context.Background(), auth, req, opts); errExecute != nil {
						t.Fatal(errExecute)
					}
					var body []byte
					select {
					case body = <-captured:
					default:
						t.Fatal("upstream request was not captured")
					}
					part := ojson.GetBytes(body, "contents.1.parts.0")
					if !part.Get("functionCall").Exists() || ojson.GetBytes(body, "contents.1.parts.#").Int() != 1 {
						t.Fatalf("carrier was not consumed by functionCall: %s", body)
					}
					want := signature
					if backend == "gemini" {
						want = internalsignature.GeminiSkipThoughtSignatureValidator
					}
					if mode == "override" {
						want = "configured-signature"
					} else if mode == "filter" {
						want = ""
						if part.Get("thoughtSignature").Exists() {
							t.Fatalf("payload filter was overwritten: %s", body)
						}
					}
					if got := part.Get("thoughtSignature").String(); got != want {
						t.Fatalf("upstream signature = %q, want %q; body=%s", got, want, body)
					}
				})
			}
		}
	}
}
