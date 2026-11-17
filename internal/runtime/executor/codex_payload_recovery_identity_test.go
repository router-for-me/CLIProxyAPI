package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCodexPayloadRewriteFinalIdentity(t *testing.T) {
	for _, transport := range []string{"http", "websocket"} {
		t.Run(transport, func(t *testing.T) {
			id, model, rewritten := t.Name(), t.Name()+"-route", t.Name()+"-rewritten"
			cfg := &config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}, Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: model, Protocol: "codex", Headers: map[string]string{"X-Rewrite": "yes"}, Match: []map[string]any{{"metadata.rewrite": true}}}}, Params: map[string]any{"model": rewritten}}}}}
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(id, "codex", []*registry.ModelInfo{{ID: rewritten, Config: &registry.ModelConfig{OverrideHeader: map[string]string{"Authorization": "Bearer token-b"}}}})
			t.Cleanup(func() { reg.UnregisterClient(id) })
			type observed struct{ model, token, account string }
			captured := make(chan observed, 1)
			completed := []byte(`{"type":"response.completed","response":{"id":"resp-rewrite","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body []byte
				if transport == "websocket" {
					conn, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
					if err != nil {
						t.Errorf("upgrade: %v", err)
						return
					}
					defer func() { _ = conn.Close() }()
					_, body, err = conn.ReadMessage()
					if err != nil {
						t.Errorf("read: %v", err)
						return
					}
					captured <- observed{ojson.GetBytes(body, "model").String(), r.Header.Get("Authorization"), r.Header.Get("Chatgpt-Account-Id")}
					if err = conn.WriteMessage(websocket.TextMessage, completed); err != nil {
						t.Errorf("write: %v", err)
					}
					return
				}
				var err error
				body, err = io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read: %v", err)
					return
				}
				captured <- observed{ojson.GetBytes(body, "model").String(), r.Header.Get("Authorization"), r.Header.Get("Chatgpt-Account-Id")}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write(append(append([]byte("data: "), completed...), []byte("\n\n")...))
			}))
			defer server.Close()
			auth := &cliproxyauth.Auth{ID: id, Provider: "codex", Attributes: map[string]string{"base_url": server.URL}, Metadata: map[string]any{"access_token": "token-a", "account_id": "account-a"}}
			doc := ojson.NewDocString(`{"input":[],"metadata":{"rewrite":true}}`)
			if err := doc.Set("model", model); err != nil {
				t.Fatal(err)
			}
			req := cliproxyexecutor.Request{Model: model, Payload: doc.Bytes()}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Headers: http.Header{"X-Rewrite": {"yes"}}}
			var err error
			if transport == "websocket" {
				_, err = NewCodexWebsocketsExecutor(cfg).Execute(context.Background(), auth, req, opts)
			} else {
				_, err = NewCodexExecutor(cfg).Execute(context.Background(), auth, req, opts)
			}
			if err != nil {
				t.Fatal(err)
			}
			got := <-captured
			if got.model != rewritten || got.token != "Bearer token-b" || got.account != "account-a" {
				t.Fatalf("finalized wire identity = %+v", got)
			}
		})
	}
}
