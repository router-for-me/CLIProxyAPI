package executor

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestAntigravityGemini38UpstreamFullModelIDs(t *testing.T) {
	for _, modelID := range []string{
		"gemini-3.8-flash-high",
		"gemini-3.8-flash-low",
		"gemini-3.8-flash-medium",
		"gemini-3.8-flash-tiered",
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", modelID, stream), func(t *testing.T) {
				wantPath := antigravityGeneratePath
				if stream {
					wantPath = antigravityStreamPath
				}
				bodies := make(chan map[string]any, 8)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Errorf("decode upstream body: %v", err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					if r.URL.Path != wantPath {
						t.Errorf("path = %q, want %q", r.URL.Path, wantPath)
					}
					select {
					case bodies <- body:
					default:
					}
					const response = `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: "+response+"\n\n")
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, response)
				}))
				defer server.Close()

				exec := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
				auth := &cliproxyauth.Auth{
					ID:         "gemini38-upstream-" + modelID,
					Provider:   "antigravity",
					Attributes: map[string]string{"base_url": server.URL},
					Metadata: map[string]any{
						"access_token": "test-token",
						"project_id":   "test-project",
						"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
					},
				}
				payload := fmt.Appendf(nil, `{"model":%q,"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, modelID)
				req := cliproxyexecutor.Request{Model: modelID, Payload: payload}
				opts := cliproxyexecutor.Options{
					SourceFormat:    sdktranslator.FormatGemini,
					ResponseFormat:  sdktranslator.FormatGemini,
					OriginalRequest: payload,
					Stream:          stream,
				}
				if stream {
					result, errExecute := exec.ExecuteStream(t.Context(), auth, req, opts)
					if errExecute != nil {
						t.Fatal(errExecute)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else if _, errExecute := exec.Execute(t.Context(), auth, req, opts); errExecute != nil {
					t.Fatal(errExecute)
				}

				select {
				case body := <-bodies:
					if got := body["model"]; got != modelID {
						t.Fatalf("upstream model = %v, want exact ID %q", got, modelID)
					}
				default:
					t.Fatal("no upstream request was captured")
				}
			})
		}
	}
}
