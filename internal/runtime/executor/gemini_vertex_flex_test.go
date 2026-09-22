package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestVertexFlexRequestAndUsage(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, flex := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%v/flex=%v", stream, flex), func(t *testing.T) {
				seen := make(chan http.Header, 1)
				tier := "ON_DEMAND"
				if flex {
					tier = "ON_DEMAND_FLEX"
				}
				raw := `{"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":1,"cachedContentTokenCount":0,"trafficType":"` + tier + `"}}`
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					seen <- r.Header.Clone()
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
					} else {
						w.Header().Set("Content-Type", "application/json")
						_, _ = fmt.Fprint(w, raw)
					}
				}))
				defer server.Close()
				exec := NewGeminiVertexExecutor(&config.Config{})
				auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "fake-upstream-key", "base_url": server.URL}}
				incoming := make(http.Header)
				incoming.Set("Authorization", "Bearer fake-client-key")
				if flex {
					incoming.Set("X-Vertex-AI-LLM-Request-Type", "shared")
					incoming.Set("X-Vertex-AI-LLM-Shared-Request-Type", "flex")
				}
				req := cliproxyexecutor.Request{Model: "gemini-3.1-pro-preview", Payload: []byte(`{"model":"gemini-3.1-pro-preview","messages":[{"role":"user","content":"hi"}]}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Headers: incoming}
				var payload []byte
				if stream {
					result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
						if strings.Contains(string(chunk.Payload), "usageMetadata") {
							payload = chunk.Payload
						}
					}
				} else {
					result, err := exec.Execute(context.Background(), auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					payload = result.Payload
				}
				header := <-seen
				if header.Get("X-Vertex-AI-LLM-Request-Type") != incoming.Get("X-Vertex-AI-LLM-Request-Type") || header.Get("X-Vertex-AI-LLM-Shared-Request-Type") != incoming.Get("X-Vertex-AI-LLM-Shared-Request-Type") {
					t.Fatalf("wrong outbound tier headers: %v", header)
				}
				if header.Get("Authorization") != "" || header.Get("X-Goog-Api-Key") != "fake-upstream-key" {
					t.Fatal("wrong outbound credentials")
				}
				payload = []byte(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(payload)), "data:")))
				if gjson.GetBytes(payload, "usageMetadata.trafficType").String() != tier || gjson.GetBytes(payload, "usage.prompt_tokens_details.cached_tokens").Raw != "0" {
					t.Fatalf("lost upstream usage evidence: %s", payload)
				}
			})
		}
	}
}
