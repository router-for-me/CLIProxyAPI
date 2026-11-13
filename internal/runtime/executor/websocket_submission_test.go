package executor

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/wsrelay"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestWebsocketSubmissionDisconnectAndPrewriteFailover(t *testing.T) {
	for _, provider := range []string{"aistudio", "xai", "codex"} {
		for _, mode := range []string{"close", "prewrite", "refusal", "malformed"} {
			if provider == "aistudio" && mode == "malformed" {
				continue
			}
			prewrite := mode == "prewrite"
			refusal := mode == "refusal"
			t.Run(fmt.Sprintf("%s/%s", provider, mode), func(t *testing.T) {
				var frames atomic.Int32
				authIDs := []string{t.Name() + "a", t.Name() + "b"}
				manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
				manager.SetRetryConfig(0, 0, 0)
				model := "grok-4"
				if provider == "codex" {
					model = "gpt-5"
				}
				baseURL := ""
				firstURL := ""
				if provider == "aistudio" {
					model = "gemini-2.5-flash"
					connected := make(chan string, 2)
					relay := wsrelay.NewManager(wsrelay.Options{ProviderFactory: func(r *http.Request) (string, error) { return r.URL.Query().Get("id"), nil }, OnConnected: func(id string) { connected <- id }})
					server := httptest.NewServer(relay.Handler())
					t.Cleanup(server.Close)
					t.Cleanup(func() { _ = relay.Stop(context.Background()) })
					for index, id := range authIDs {
						if prewrite && index == 0 {
							continue
						}
						conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+relay.Path()+"?id="+id, nil)
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = conn.Close() })
						<-connected
						go func() {
							var msg wsrelay.Message
							if err := conn.ReadJSON(&msg); err != nil {
								return
							}
							frames.Add(1)
							if refusal && index == 0 {
								_ = conn.WriteJSON(wsrelay.Message{ID: msg.ID, Type: wsrelay.MessageTypeHTTPResp, Payload: map[string]any{"status": 503, "body": `{"error":{"code":503,"message":"provider refused generation"}}`}})
								return
							}
							if !prewrite && !refusal {
								_ = conn.Close()
								return
							}
							body := `{"candidates":[{"content":{"role":"model","parts":[{"text":"safe-fallback"}]},"finishReason":"STOP"}]}`
							if err := conn.WriteJSON(wsrelay.Message{ID: msg.ID, Type: wsrelay.MessageTypeHTTPResp, Payload: map[string]any{"status": 200, "body": body}}); err != nil {
								t.Error(err)
							}
						}()
					}
					manager.RegisterExecutor(NewAIStudioExecutor(&config.Config{}, provider, relay))
				} else {
					upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						conn, err := upgrader.Upgrade(w, r, nil)
						if err != nil {
							t.Error(err)
							return
						}
						defer func() { _ = conn.Close() }()
						if _, _, err := conn.ReadMessage(); err != nil {
							t.Error(err)
							return
						}
						count := frames.Add(1)
						if mode == "malformed" {
							_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"error","status":503,"error":{}}`))
							return
						}
						if refusal && count == 1 {
							_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"error","status":503,"error":{"code":"server_error","message":"provider refused generation"}}`))
							return
						}
						if !prewrite && !refusal {
							return
						}
						if r.Header.Get("Authorization") != "Bearer synthetic-b" {
							t.Error("safe failover did not reach second credential")
						}
						_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"safe-fallback","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`))
					}))
					t.Cleanup(server.Close)
					baseURL = server.URL
					if prewrite {
						listener, err := net.Listen("tcp", "127.0.0.1:0")
						if err != nil {
							t.Fatal(err)
						}
						firstURL = "http://" + listener.Addr().String()
						if err := listener.Close(); err != nil {
							t.Fatal(err)
						}
					}
					if provider == "codex" {
						manager.RegisterExecutor(NewCodexWebsocketsExecutor(&config.Config{}))
					} else {
						manager.RegisterExecutor(NewXAIWebsocketsExecutor(&config.Config{}))
					}
				}
				for index, id := range authIDs {
					attrs := map[string]string{"base_url": baseURL, "websockets": "true"}
					if index == 0 && firstURL != "" {
						attrs["base_url"] = firstURL
					}
					token := "synthetic-a"
					if index == 1 {
						token = "synthetic-b"
					}
					auth := &coreauth.Auth{ID: id, Provider: provider, Status: coreauth.StatusActive, ProxyURL: "direct", Attributes: attrs, Metadata: map[string]any{"access_token": token}}
					if _, err := manager.Register(context.Background(), auth); err != nil {
						t.Fatal(err)
					}
					registry.GetGlobalRegistry().RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
				}
				payload := []byte(`{"input":"hello","stream":true}`)
				result, err := manager.ExecuteStream(context.Background(), []string{provider}, coreexecutor.Request{Model: model, Payload: payload}, coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, Stream: true})
				if result != nil {
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							err = chunk.Err
						}
					}
				}
				t.Logf("generation frames=%d error=%v", frames.Load(), err)
				if prewrite || refusal {
					want := int32(1)
					if refusal {
						want = 2
					}
					if err != nil || frames.Load() != want {
						t.Fatalf("safe failover failed: frames=%d err=%v", frames.Load(), err)
					}
				} else if frames.Load() != 1 || err == nil || !strings.Contains(err.Error(), "upstream_outcome_unknown") {
					t.Fatalf("post-write close replayed or lost unknown outcome: frames=%d err=%v", frames.Load(), err)
				}
			})
		}
	}
}

func TestWebsocketSubmissionIgnoresHandshakeAndControlFrames(t *testing.T) {
	received := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.Close() }()
		for range 2 {
			if _, _, err := conn.ReadMessage(); err != nil {
				t.Error(err)
				return
			}
			received <- struct{}{}
		}
	}))
	defer server.Close()
	ctx := httptransport.WithSubmissionTracking(context.Background())
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := httptransport.ClassifySubmissionError(ctx, io.EOF); httptransport.IsOutcomeUnknown(err) {
		t.Fatal("handshake marked accepted")
	}
	if err := writeWebsocketPayloadMessage(ctx, "codex", nil, conn, []byte(`{"type":"session.update"}`)); err != nil {
		t.Fatal(err)
	}
	<-received
	if err := httptransport.ClassifySubmissionError(ctx, io.EOF); httptransport.IsOutcomeUnknown(err) {
		t.Fatal("control frame marked accepted")
	}
	if err := writeWebsocketPayloadMessage(ctx, "codex", nil, conn, []byte(`{"type":"response.create"}`)); err != nil {
		t.Fatal(err)
	}
	<-received
	if err := httptransport.ClassifySubmissionError(ctx, io.EOF); !httptransport.IsOutcomeUnknown(err) {
		t.Fatalf("generation frame was not marked accepted: %v", err)
	}
}
