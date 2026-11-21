package executor

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type submissionFaultConn struct {
	net.Conn
	armed    atomic.Bool
	partial  bool
	received <-chan struct{}
	wrote    atomic.Int32
}

func (c *submissionFaultConn) Write(payload []byte) (int, error) {
	if !c.armed.CompareAndSwap(true, false) {
		return c.Conn.Write(payload)
	}
	if c.partial && len(payload) > 8 {
		payload = payload[:8]
	}
	n, errWrite := c.Conn.Write(payload)
	c.wrote.Add(int32(n))
	if errWrite != nil {
		return n, errWrite
	}
	if !c.partial {
		<-c.received
	}
	return n, errors.New("synthetic write error after delivery")
}

func TestWebsocketSubmissionWriteErrorDoesNotResend(t *testing.T) {
	for _, provider := range []string{"codex", "xai"} {
		for _, mode := range []string{"delivered", "partial", "prewrite"} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				var frames, dials atomic.Int32
				received := make(chan struct{})
				var receivedOnce sync.Once
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					dials.Add(1)
					conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
					if errUpgrade != nil {
						t.Error(errUpgrade)
						return
					}
					defer func() {
						if errClose := conn.Close(); errClose != nil {
							t.Logf("close peer: %v", errClose)
						}
					}()
					if _, _, errRead := conn.ReadMessage(); errRead != nil {
						return
					}
					count := frames.Add(1)
					if mode == "delivered" && count == 1 {
						receivedOnce.Do(func() { close(received) })
						_, _, _ = conn.ReadMessage()
						return
					}
					if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"safe-fallback","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)); errWrite != nil {
						t.Error(errWrite)
					}
				}))
				defer server.Close()
				wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/responses"
				sessionID := t.Name()
				store := &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
				var executor coreauth.ProviderExecutor
				var sess *codexWebsocketSession
				if provider == "codex" {
					exec := NewCodexWebsocketsExecutor(&config.Config{})
					exec.store = store
					executor = exec
					sess = exec.getOrCreateSession(sessionID)
					t.Cleanup(func() { exec.CloseExecutionSession(sessionID) })
				} else {
					exec := NewXAIWebsocketsExecutor(&config.Config{})
					exec.store = store
					exec.idStore = &xaiWebsocketIDStateStore{sessions: make(map[string]*xaiWebsocketIDState)}
					executor = exec
					sess = exec.getOrCreateSession(sessionID)
					t.Cleanup(func() { exec.CloseExecutionSession(sessionID) })
				}
				firstURL := server.URL
				var fault *submissionFaultConn
				if mode == "prewrite" {
					listener, errListen := net.Listen("tcp", "127.0.0.1:0")
					if errListen != nil {
						t.Fatal(errListen)
					}
					firstURL = "http://" + listener.Addr().String()
					if errClose := listener.Close(); errClose != nil {
						t.Fatal(errClose)
					}
				} else {
					dialer := websocket.Dialer{NetDialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
						conn, errDial := (&net.Dialer{}).DialContext(ctx, network, address)
						if errDial != nil {
							return nil, errDial
						}
						fault = &submissionFaultConn{Conn: conn, partial: mode == "partial", received: received}
						return fault, nil
					}}
					conn, _, errDial := dialer.Dial(wsURL, nil)
					if errDial != nil {
						t.Fatal(errDial)
					}
					fault.armed.Store(true)
					sess.conn = conn
					sess.connCloser = newWebsocketConnectionCloser(conn)
					sess.authID = sessionID + "a"
					sess.wsURL = wsURL
					sess.proxyURL = "direct"
				}
				manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
				manager.SetRetryConfig(0, 0, 0)
				manager.RegisterExecutor(executor)
				model := "gpt-5"
				if provider == "xai" {
					model = "grok-4"
				}
				for index, suffix := range []string{"a", "b"} {
					baseURL := server.URL
					if index == 0 {
						baseURL = firstURL
					}
					auth := &coreauth.Auth{ID: sessionID + suffix, Provider: provider, Status: coreauth.StatusActive, ProxyURL: "direct", Attributes: map[string]string{"base_url": baseURL, "api_key": "synthetic", "websockets": "true"}, Metadata: map[string]any{"access_token": "synthetic"}}
					if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
						t.Fatal(errRegister)
					}
					registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
				}
				result, errExecute := manager.ExecuteStream(context.Background(), []string{provider}, coreexecutor.Request{Model: model, Payload: []byte(`{"input":"hello","stream":true}`)}, coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, Stream: true, Metadata: map[string]any{coreexecutor.ExecutionSessionMetadataKey: sessionID}})
				if result != nil {
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							errExecute = chunk.Err
						}
					}
				}
				bytesWritten := int32(0)
				if fault != nil {
					bytesWritten = fault.wrote.Load()
				}
				t.Logf("dials=%d received_frames=%d fault_bytes_written=%d error=%v", dials.Load(), frames.Load(), bytesWritten, errExecute)
				if mode == "prewrite" {
					if errExecute != nil || frames.Load() != 1 || dials.Load() != 1 {
						t.Fatalf("provably pre-write failure lost safe failover: %v", errExecute)
					}
					return
				}
				wantFrames := int32(1)
				if mode == "partial" {
					wantFrames = 0
				}
				if bytesWritten == 0 || dials.Load() != 1 || frames.Load() != wantFrames || errExecute == nil || !strings.Contains(errExecute.Error(), "upstream_outcome_unknown") {
					t.Fatalf("ambiguous write replayed or lost unknown outcome: dials=%d frames=%d bytes=%d error=%v", dials.Load(), frames.Load(), bytesWritten, errExecute)
				}
			})
		}
	}
}

func TestWebsocketSubmissionMissingConnectionIsPrewrite(t *testing.T) {
	for _, sess := range []*codexWebsocketSession{nil, {}} {
		ctx := httptransport.WithSubmissionTracking(context.Background())
		errWrite := writeWebsocketPayloadMessage(ctx, "codex", sess, nil, []byte(`{"type":"response.create"}`))
		if errWrite == nil || httptransport.IsOutcomeUnknown(httptransport.ClassifySubmissionError(ctx, errWrite)) {
			t.Fatalf("missing connection was submitted: %v", errWrite)
		}
	}
}
