package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// Meta documents the same nested input_audio {data, format} part for Chat and
// Responses, with wav and mp3 as the supported inline formats:
// https://dev.meta.ai/docs/api-reference/responses/schemas#input-audio-content
func TestMetaExecutor_ChatAudioReachesUpstream(t *testing.T) {
	for _, format := range []string{"wav", "mp3"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", format, stream), func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					if r.Method != http.MethodPost || r.URL.Path != "/responses" {
						t.Errorf("unexpected upstream: %s %s", r.Method, r.URL.Path)
					}
					if model := ojson.GetBytes(body, "model").String(); model != "muse-ember-1.0" {
						t.Errorf("unexpected model: %s", model)
					}
					parts := ojson.GetBytes(body, "input.0.content").Array()
					if len(parts) != 2 || parts[0].Get("type").String() != "input_text" || parts[0].Get("text").String() != "Transcribe this audio." {
						t.Errorf("text/audio content changed: %s", body)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					audio := parts[1]
					if audio.Get("type").String() != "input_audio" || audio.Get("input_audio.data").String() != "YXVkaW8=" || audio.Get("input_audio.format").String() != format || audio.Get("data").Exists() || audio.Get("format").Exists() {
						t.Errorf("expected nested input_audio per Meta schema, got %s", audio.Raw)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					writeMetaResponsesOK(w, "transcript")
				}))
				defer server.Close()

				exec := NewMetaExecutor(&config.Config{})
				auth := &cliproxyauth.Auth{Provider: "meta", Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL}}
				payload := []byte(fmt.Sprintf(`{"model":"muse-ember-1.0","messages":[{"role":"user","content":[{"type":"text","text":"Transcribe this audio."},{"type":"input_audio","input_audio":{"data":"YXVkaW8=","format":%q}}]}]}`, format))
				req := cliproxyexecutor.Request{Model: "muse-ember-1.0", Payload: payload}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Stream: stream}
				if stream {
					result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
					t.Fatal(err)
				}
				if calls.Load() != 1 {
					t.Fatalf("upstream calls = %d, want 1", calls.Load())
				}
			})
		}
	}
}
