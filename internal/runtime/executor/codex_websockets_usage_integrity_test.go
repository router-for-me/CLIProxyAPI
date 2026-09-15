package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type websocketUsageCapture struct {
	mu      sync.Mutex
	authID  string
	records []usage.Record
}

func (*websocketUsageCapture) Synchronous() bool { return true }
func (p *websocketUsageCapture) HandleUsage(_ context.Context, r usage.Record) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if r.AuthID == p.authID {
		p.records = append(p.records, r)
	}
}
func TestCodexWebsocketImageUsageIntegrity(t *testing.T) {
	for _, mode := range []string{"execute", "stream", "downstream-websocket"} {
		t.Run(mode, func(t *testing.T) {
			capture := &websocketUsageCapture{authID: t.Name()}
			usage.RegisterNamedPlugin(t.Name(), capture)
			defer usage.RegisterNamedPlugin(t.Name(), &websocketUsageCapture{})
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, errUpgrade := upgrader.Upgrade(w, r, nil)
				if errUpgrade != nil {
					t.Error(errUpgrade)
					return
				}
				defer func() {
					if errClose := conn.Close(); errClose != nil {
						t.Log(errClose)
					}
				}()
				if _, _, errRead := conn.ReadMessage(); errRead != nil {
					t.Error(errRead)
					return
				}
				if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"response-one","output":[],"usage":{"input_tokens":100,"output_tokens":10,"total_tokens":110},"tool_usage":{"image_gen":{"input_tokens":40,"output_tokens":60,"total_tokens":100}}}}`)); errWrite != nil {
					t.Error(errWrite)
				}
			}))
			defer server.Close()
			executor := NewCodexWebsocketsExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "codex", Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
			req := cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":"draw","tools":[{"type":"image_generation","model":"gpt-image-1"}]}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")}
			ctx := usage.WithNewGeneration(context.Background())
			if mode == "execute" {
				if _, errExecute := executor.Execute(ctx, auth, req, opts); errExecute != nil {
					t.Fatal(errExecute)
				}
			} else {
				if mode == "downstream-websocket" {
					ctx = cliproxyexecutor.WithDownstreamWebsocket(ctx)
				}
				result, errStream := executor.ExecuteStream(ctx, auth, req, opts)
				if errStream != nil {
					t.Fatal(errStream)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
				}
			}
			capture.mu.Lock()
			defer capture.mu.Unlock()
			if len(capture.records) != 2 {
				t.Fatalf("got %d usage events, want main and image", len(capture.records))
			}
			a, b := capture.records[0], capture.records[1]
			if a.Detail.TotalTokens != 110 || b.Detail.TotalTokens != 100 || b.Model != "gpt-image-1" || b.Kind != "tool" {
				t.Fatalf("incorrect records: %+v / %+v", a, b)
			}
			if a.EventID == b.EventID || a.AttemptID != b.AttemptID || a.GenerationID != b.GenerationID {
				t.Fatal("event identity must differ while attempt/generation stay linked")
			}
		})
	}
}
