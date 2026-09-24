package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatExecutor_VideoInput(t *testing.T) {
	for _, source := range []string{"openai", "openai-response"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", source, stream), func(t *testing.T) {
				type capturedRequest struct {
					path string
					body []byte
				}
				requests := make(chan capturedRequest, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						http.Error(w, errRead.Error(), http.StatusBadRequest)
						return
					}
					requests <- capturedRequest{path: r.URL.Path, body: body}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-video\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"video-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"blue, red, green\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"chatcmpl-video\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"video-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"id":"chatcmpl-video","object":"chat.completion","created":1,"model":"video-model","choices":[{"index":0,"message":{"role":"assistant","content":"blue, red, green"},"finish_reason":"stop"}]}`)
				}))
				defer server.Close()

				executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
				auth := &cliproxyauth.Auth{
					Provider: "openai-compatibility",
					Attributes: map[string]string{
						"base_url": server.URL + "/v1",
						"api_key":  "test-key",
					},
				}
				payload := `{"model":"client-alias","messages":[{"role":"user","content":[{"type":"text","text":"Describe the videos."},{"type":"video_url","video_url":{"url":"https://example.com/clip.mp4?part=1&name=a%20b","processing":"agentic"}},{"type":"video_url","video_url":{"url":"data:video/mp4;base64,AAECAwQ="}}]}]}`
				if source == "openai-response" {
					payload = `{"model":"client-alias","input":[{"role":"user","content":[{"type":"input_text","text":"Describe the videos."},{"type":"input_video","video_url":"https://example.com/clip.mp4?part=1&name=a%20b","processing":"agentic"},{"type":"input_video","video_url":"data:video/mp4;base64,AAECAwQ="}]}]}`
				}
				req := cliproxyexecutor.Request{Model: "video-model", Payload: []byte(payload)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString(source), Stream: stream}
				var response strings.Builder
				if stream {
					result, errExecute := executor.ExecuteStream(context.Background(), auth, req, opts)
					if errExecute != nil {
						t.Fatalf("ExecuteStream: %v", errExecute)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatalf("stream chunk: %v", chunk.Err)
						}
						response.Write(chunk.Payload)
					}
				} else {
					result, errExecute := executor.Execute(context.Background(), auth, req, opts)
					if errExecute != nil {
						t.Fatalf("Execute: %v", errExecute)
					}
					response.Write(result.Payload)
				}
				if !strings.Contains(response.String(), "blue, red, green") {
					t.Fatalf("upstream response was lost: %s", response.String())
				}

				var captured capturedRequest
				select {
				case captured = <-requests:
				default:
					t.Fatal("no upstream request")
				}
				if captured.path != "/v1/chat/completions" {
					t.Fatalf("upstream path = %q, want /v1/chat/completions", captured.path)
				}
				if got := gjson.GetBytes(captured.body, "model").String(); got != "video-model" {
					t.Fatalf("upstream model = %q, want video-model", got)
				}
				content := gjson.GetBytes(captured.body, "messages.0.content").Array()
				if len(content) != 3 {
					t.Fatalf("upstream content has %d parts, want text and two videos: %s", len(content), captured.body)
				}
				if content[0].Get("text").String() != "Describe the videos." {
					t.Fatalf("text changed: %s", content[0].Raw)
				}
				for i, wantURL := range []string{"https://example.com/clip.mp4?part=1&name=a%20b", "data:video/mp4;base64,AAECAwQ="} {
					part := content[i+1]
					if part.Get("type").String() != "video_url" || part.Get("video_url.url").String() != wantURL {
						t.Fatalf("video %d changed: %s", i, part.Raw)
					}
				}
				if got := content[1].Get("video_url.processing").String(); got != "agentic" {
					t.Fatalf("video processing = %q, want agentic", got)
				}
			})
		}
	}
}
