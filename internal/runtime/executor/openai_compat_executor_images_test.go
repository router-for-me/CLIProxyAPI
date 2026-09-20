package executor

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestOpenAICompatExecutor_ImageStreamChunkBoundaryObservability(t *testing.T) {
	tests := []struct {
		name      string
		chunks    []string
		wantModel string
	}{
		{
			name: "chunk split across json boundaries (拆包)",
			chunks: []string{
				`data: {"id":"img_split","cre`,
				`ated":123,"mo`,
				`del":"dall-e-3","data":[{"url":"http://example.com/1.png"}]}` + "\n\n",
			},
			wantModel: "dall-e-3",
		},
		{
			name: "multiple events in single network chunk (合包)",
			chunks: []string{
				"event: ping\ndata: {}\n\nevent: completion\ndata: {\"model\":\"dall-e-3\",\"status\":\"done\"}\n\n",
			},
			wantModel: "dall-e-3",
		},
		{
			name: "chunk starting with event: prefix",
			chunks: []string{
				"event: image_event\n",
				"data: {\"model\":\"dall-e-3\",\"data\":[]}\n\n",
			},
			wantModel: "dall-e-3",
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				flusher, ok := w.(http.Flusher)
				for _, c := range tt.chunks {
					_, _ = w.Write([]byte(c))
					if ok {
						flusher.Flush()
					}
				}
			}))
			defer server.Close()

			alias := fmt.Sprintf("openai-compat-image-stream-test-%d", i)
			capture := &multiProviderUsageCapture{alias: alias, records: make(chan coreusage.Record, 4)}
			coreusage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() {
				coreusage.RegisterNamedPlugin(t.Name(), multiProviderNoopUsagePlugin{})
			})

			executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
				OpenAICompatibility: []config.OpenAICompatibility{{
					Name: "compat",
				}},
			})
			auth := &cliproxyauth.Auth{
				Provider: "openai-compatibility",
				Attributes: map[string]string{
					"base_url":     server.URL,
					"api_key":      "test-key",
					"compat_name":  "compat",
					"provider_key": "compat",
				},
			}

			ctx := coreusage.WithRequestedModelAlias(context.Background(), alias)
			payload := []byte(`{"model":"dall-e-3","prompt":"a sunset over mountains"}`)
			streamRes, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
				Model:   "dall-e-3",
				Payload: payload,
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("openai-image"),
				Stream:       true,
			})
			if err != nil {
				t.Fatalf("ExecuteStream error: %v", err)
			}

			// Verify verbatim forwarding: collect all streamed bytes
			var received bytes.Buffer
			for chunk := range streamRes.Chunks {
				if chunk.Err != nil {
					t.Fatalf("unexpected chunk error: %v", chunk.Err)
				}
				received.Write(chunk.Payload)
			}

			expectedForwarded := strings.Join(tt.chunks, "")
			if received.String() != expectedForwarded {
				t.Fatalf("forwarded payload mismatch: got %q, want %q", received.String(), expectedForwarded)
			}

			// Verify response model in recorded usage
			record := capture.await(t)
			if record.ResponseModel != tt.wantModel {
				t.Fatalf("recorded ResponseModel = %q, want %q", record.ResponseModel, tt.wantModel)
			}
		})
	}
}
