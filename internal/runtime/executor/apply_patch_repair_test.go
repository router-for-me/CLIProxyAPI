package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	codexresponses "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/codex/openai/responses"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// task6RepairSource completes tool input without implicitly completing the response.
func task6RepairSource(mode string) [][]byte {
	if mode == "empty" {
		return nil
	}
	item := `{"type":"function_call","id":"a","call_id":"c","name":"apply_patch","arguments":"{\"input\":\"valid patch\"}"}`
	events := [][]byte{[]byte(`{"type":"response.created","response":{"id":"r"}}`)}
	if mode == "args-eof" || mode == "done" {
		events = append(events,
			[]byte(`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"a","call_id":"c","name":"apply_patch","arguments":""}}`),
			[]byte(`{"type":"response.function_call_arguments.done","output_index":0,"item_id":"a","call_id":"c","arguments":"{\"input\":\"valid patch\"}"}`))
	} else {
		events = append(events, []byte(`{"type":"response.output_item.done","output_index":0,"item":`+item+`}`))
	}
	if mode == "done" {
		events = append(events, []byte("[DONE]"))
	} else if strings.HasPrefix(mode, "response.") {
		events = append(events, []byte(fmt.Sprintf(`{"type":%q,"response":{"id":"r","output":[%s],"usage":{"input_tokens":5,"output_tokens":3}}}`, mode, item)))
	}
	return events
}

func task6RepairSSE(events [][]byte) string {
	var body strings.Builder
	for _, event := range events {
		body.WriteString("data: ")
		body.Write(event)
		body.WriteString("\n\n")
	}
	return body.String()
}

func task6RepairExecutor(provider string) cliproxyauth.ProviderExecutor {
	switch provider {
	case "meta":
		return NewMetaExecutor(&config.Config{})
	case "kimi":
		return NewKimiExecutor(&config.Config{})
	default:
		return NewXAIExecutor(&config.Config{})
	}
}

func TestApplyPatchRepairResponsesSourceTerminalHTTP(t *testing.T) {
	for _, provider := range []string{"xai", "meta", "kimi"} {
		for _, mode := range []string{"args-eof", "item-eof", "empty", "done", "response.completed", "response.incomplete", "response.done"} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				body := task6RepairSSE(task6RepairSource(mode))
				if strings.HasPrefix(mode, "response.") {
					body += "data: [DONE]\n\ndata: [DONE]\n\n"
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, body)
				}))
				defer server.Close()
				exec := task6RepairExecutor(provider)
				auth := &cliproxyauth.Auth{ID: t.Name(), Provider: provider, Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
				req := cliproxyexecutor.Request{Model: "grok-4", Payload: []byte(task6PatchRequest)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
				if !strings.HasPrefix(mode, "response.") {
					checkUsage := task6CaptureFailureUsage(t, auth.ID)
					stream, errExecuteStream := exec.ExecuteStream(t.Context(), auth, req, opts)
					if errExecuteStream != nil {
						t.Fatal(errExecuteStream)
					}
					assertTask6FailedStream(t, stream.Chunks)
					checkUsage()
					// Exercise the gateway too, but do not rely on its final HTTP validator.
					auth.ID += "-gateway"
					checkGatewayUsage := task6CaptureFailureUsage(t, auth.ID)
					result := task6Gateway(t, exec, auth, true)
					output := result.Body.String()
					if strings.Count(output, "event: response.failed") != 1 || strings.Contains(output, "event: error") || strings.Contains(output, "response.completed") || strings.Contains(output, "[DONE]") {
						t.Errorf("gateway failure contract: %d %s", result.Code, output)
					}
					checkGatewayUsage()
					return
				}
				stream, errExecuteStream := exec.ExecuteStream(t.Context(), auth, req, opts)
				if errExecuteStream != nil {
					t.Fatal(errExecuteStream)
				}
				var output []byte
				for chunk := range stream.Chunks {
					if chunk.Err != nil {
						t.Error(chunk.Err)
					}
					output = append(output, chunk.Payload...)
				}
				if bytes.Contains(output, []byte("response.failed")) || !bytes.Contains(output, []byte("valid patch")) || bytes.Count(output, []byte("[DONE]")) != 1 {
					t.Fatalf("legal source terminal/first DONE lost: %s", output)
				}
			})
		}
	}
}

func task6RepairChunkType(payload []byte) string {
	payload = bytes.TrimSpace(payload)
	if bytes.HasPrefix(payload, []byte("data:")) {
		payload = bytes.TrimSpace(payload[5:])
	}
	return gjson.GetBytes(payload, "type").String()
}

func task6RepairReadStream(t *testing.T, stream *cliproxyexecutor.StreamResult, failure bool) {
	t.Helper()
	failed, cleanErrors, completed := 0, 0, 0
	validPatch := false
	for chunk := range stream.Chunks {
		validPatch = validPatch || bytes.Contains(chunk.Payload, []byte("valid patch"))
		if chunk.Err != nil {
			cleanErrors++
			if failure {
				assertTask6PatchError(t, chunk.Err)
			} else {
				t.Error(chunk.Err)
			}
		}
		switch task6RepairChunkType(chunk.Payload) {
		case "response.failed":
			failed++
		case "response.completed", "response.done", "response.incomplete":
			completed++
		}
		if bytes.Contains(chunk.Payload, []byte("RAW_SECRET")) || (failure && bytes.Contains(chunk.Payload, []byte("[DONE]"))) {
			t.Errorf("invalid source leaked: %s", chunk.Payload)
		}
	}
	if failure && (failed != 1 || cleanErrors != 1 || completed != 0) {
		t.Fatalf("failure counts: failed=%d errors=%d completed=%d", failed, cleanErrors, completed)
	}
	if !failure && (failed != 0 || cleanErrors != 0 || completed != 1 || !validPatch) {
		t.Fatalf("success counts: failed=%d errors=%d completed=%d", failed, cleanErrors, completed)
	}
}

func TestApplyPatchRepairXAIWebsocketSourceTerminal(t *testing.T) {
	for _, raw := range []bool{false, true} {
		for _, mode := range []string{"args-eof", "item-eof", "empty", "done", "response.completed", "response.done", "response.incomplete"} {
			t.Run(fmt.Sprintf("%s/raw=%v", mode, raw), func(t *testing.T) {
				upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					conn, errUpgrade := upgrader.Upgrade(w, r, nil)
					if errUpgrade != nil {
						return
					}
					defer func() {
						if errClose := conn.Close(); errClose != nil {
							t.Log(errClose)
						}
					}()
					if _, _, errReadMessage := conn.ReadMessage(); errReadMessage != nil {
						return
					}
					for _, event := range task6RepairSource(mode) {
						if errWriteMessage := conn.WriteMessage(websocket.TextMessage, event); errWriteMessage != nil {
							return
						}
					}
				}))
				defer server.Close()
				exec := NewXAIWebsocketsExecutor(&config.Config{})
				exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
				auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "xai", Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
				failure := !strings.HasPrefix(mode, "response.")
				if failure {
					checkUsage := task6CaptureFailureUsage(t, auth.ID)
					defer checkUsage()
				}
				ctx := t.Context()
				if raw {
					ctx = cliproxyexecutor.WithDownstreamWebsocket(ctx)
				}
				stream, errExecuteStream := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "grok-4", Payload: []byte(task6PatchRequest)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
				if errExecuteStream != nil {
					t.Fatal(errExecuteStream)
				}
				task6RepairReadStream(t, stream, failure)
			})
		}
	}
}

type task6RepairLifecycle struct {
	mu    sync.Mutex
	close func() error
	ended chan string
	once  sync.Once
}

func (l *task6RepairLifecycle) Bind(closeResource func() error) error {
	l.mu.Lock()
	l.close = closeResource
	l.mu.Unlock()
	return nil
}
func (l *task6RepairLifecycle) End(reason string) { l.once.Do(func() { l.ended <- reason }) }
func (*task6RepairLifecycle) Retain()             {}
func (l *task6RepairLifecycle) detach() error {
	l.mu.Lock()
	closeResource := l.close
	l.mu.Unlock()
	if closeResource == nil {
		return errors.New("lifecycle was not bound")
	}
	return closeResource()
}

func task6RepairAwait(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("watchdog: %s", label)
	}
}

func TestApplyPatchRepairXAIWebsocketPersistentFailureRetry(t *testing.T) {
	for _, branch := range []string{"bridge", "translator", "translator-terminal", "translator-warmup"} {
		for _, raw := range []bool{false, true} {
			if strings.HasPrefix(branch, "translator") && raw {
				// Raw downstream WebSockets deliberately bypass the SDK translator.
				continue
			}
			for _, detach := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/raw=%v/detach=%v", branch, raw, detach), func(t *testing.T) {
					if strings.HasPrefix(branch, "translator") {
						// Inject only the optional translator failure; retain the real executor/socket.
						sdktranslator.Register(sdktranslator.FormatOpenAIResponse, sdktranslator.FormatCodex, nil, sdktranslator.ResponseTransform{Stream: func(ctx context.Context, model string, original, effective, event []byte, param *any) [][]byte {
							payload := bytes.TrimSpace(bytes.TrimPrefix(event, []byte("data: ")))
							if gjson.GetBytes(payload, "type").String() == "repair.invalid" || (gjson.GetBytes(payload, "type").String() == "response.completed" && gjson.GetBytes(payload, "response.id").String() == "repair-invalid") {
								state := &translatorcommon.ApplyPatchErrorState{}
								state.SetToolInputError(errors.New("RAW_SECRET"))
								*param = state
								return [][]byte{[]byte("data: " + string(translatorcommon.ApplyPatchFailure("r", 1)) + "\n\n")}
							}
							return codexresponses.ConvertCodexResponseToOpenAIResponses(ctx, model, original, effective, event, param)
						}})
						defer sdktranslator.Register(sdktranslator.FormatOpenAIResponse, sdktranslator.FormatCodex, nil, sdktranslator.ResponseTransform{Stream: codexresponses.ConvertCodexResponseToOpenAIResponses, NonStream: codexresponses.ConvertCodexResponseToOpenAIResponsesNonStream})
					}
					oldClosed := make(chan struct{})
					freshRequest := make(chan struct{})
					releaseSuccess := make(chan struct{})
					var connections atomic.Int32
					upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						conn, errUpgrade := upgrader.Upgrade(w, r, nil)
						if errUpgrade != nil {
							return
						}
						n := connections.Add(1)
						defer func() {
							if errClose := conn.Close(); errClose != nil {
								t.Log(errClose)
							}
						}()
						if _, _, errReadMessage := conn.ReadMessage(); errReadMessage != nil {
							return
						}
						if n == 1 {
							events := [][]byte{[]byte(`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"a","call_id":"c","name":"apply_patch","arguments":""}}`), []byte(`{"type":"response.function_call_arguments.delta","item_id":"a","output_index":0,"delta":"{\"input\":7,\"secret\":\"RAW_SECRET\"}"}`)}
							if branch == "translator" {
								events = [][]byte{[]byte(`{"type":"repair.invalid"}`)}
							} else if branch == "translator-terminal" {
								events = [][]byte{[]byte(`{"type":"response.completed","response":{"id":"repair-invalid","output":[],"usage":{"input_tokens":5,"output_tokens":3}}}`)}
							} else if branch == "translator-warmup" {
								events = [][]byte{[]byte(`{"type":"response.created","response":{"id":"repair-invalid"}}`)}
							}
							for _, event := range events {
								if errWriteMessage := conn.WriteMessage(websocket.TextMessage, event); errWriteMessage != nil {
									return
								}
							}
							// Leave the attempt open. A same-socket retry would get stale output.
							if _, _, errReadMessage := conn.ReadMessage(); errReadMessage == nil {
								_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"stale","output":[]}}`))
								t.Error("failed socket accepted a second request")
							} else {
								close(oldClosed)
							}
							return
						}
						close(freshRequest)
						<-releaseSuccess
						for _, event := range task6RepairSource("response.completed") {
							if errWriteMessage := conn.WriteMessage(websocket.TextMessage, event); errWriteMessage != nil {
								return
							}
						}
						_, _, _ = conn.ReadMessage()
					}))
					defer server.Close()
					var releaseOnce sync.Once
					release := func() { releaseOnce.Do(func() { close(releaseSuccess) }) }
					defer release()
					exec := NewXAIWebsocketsExecutor(&config.Config{})
					exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
					exec.idStore = &xaiWebsocketIDStateStore{sessions: make(map[string]*xaiWebsocketIDState)}
					sessionID := t.Name()
					defer exec.CloseExecutionSession(sessionID)
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					if raw {
						ctx = cliproxyexecutor.WithDownstreamWebsocket(ctx)
					}
					auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "xai", Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
					req := cliproxyexecutor.Request{Model: "grok-4", Payload: []byte(task6PatchRequest)}
					if branch == "translator-warmup" {
						req.Payload = []byte(`{"generate":false,"input":"patch","tools":[{"type":"custom","name":"apply_patch"}]}`)
					}
					oldLifecycle := &task6RepairLifecycle{ended: make(chan string, 1)}
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: sessionID}, ExecutionLifecycle: oldLifecycle}
					checkUsage := task6CaptureFailureUsage(t, auth.ID)
					first, errExecuteStream := exec.ExecuteStream(ctx, auth, req, opts)
					if errExecuteStream != nil {
						t.Fatal(errExecuteStream)
					}
					// The failure frame is a synchronization point before the request lock releases.
					failed := 0
					for chunk := range first.Chunks {
						if bytes.Contains(chunk.Payload, []byte("RAW_SECRET")) || task6RepairChunkType(chunk.Payload) == "response.completed" {
							t.Fatalf("failed attempt leaked/completed: %s", chunk.Payload)
						}
						if task6RepairChunkType(chunk.Payload) != "response.failed" {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
							continue
						}
						failed++
						sess := exec.getOrCreateSession(sessionID)
						sess.connMu.Lock()
						live := sess.conn != nil
						sess.connMu.Unlock()
						if live {
							t.Fatal("validation failure delivered before invalidating persistent connection")
						}
						break
					}
					if failed != 1 {
						t.Fatal("missing clean failure frame")
					}
					// Retry starts while the first attempt is blocked delivering its clean error.
					type attempt struct {
						stream *cliproxyexecutor.StreamResult
						err    error
					}
					secondResult := make(chan attempt, 1)
					req.Payload = []byte(task6PatchRequest)
					opts.ExecutionLifecycle = &task6RepairLifecycle{ended: make(chan string, 1)}
					go func() {
						stream, errRetry := exec.ExecuteStream(ctx, auth, req, opts)
						secondResult <- attempt{stream: stream, err: errRetry}
					}()
					errorCount := 0
					for chunk := range first.Chunks {
						if chunk.Err != nil {
							errorCount++
							assertTask6PatchError(t, chunk.Err)
						} else {
							t.Errorf("extra failure payload: %s", chunk.Payload)
						}
					}
					if errorCount != 1 {
						t.Errorf("clean errors=%d, want one", errorCount)
					}
					checkUsage()
					task6RepairAwait(t, oldClosed, "first attempt must close open upstream")
					task6RepairAwait(t, freshRequest, "retry must use a fresh connection")
					select {
					case reason := <-oldLifecycle.ended:
						if reason != "invalid_tool_arguments" {
							t.Errorf("lifecycle ended as %q", reason)
						}
					default:
						t.Error("failed lifecycle was not ended")
					}
					if detach {
						// A stale bound-resource callback races with the new active connection.
						detached := make(chan struct{})
						go func() {
							if errDetach := oldLifecycle.detach(); errDetach != nil {
								t.Error(errDetach)
							}
							close(detached)
						}()
						task6RepairAwait(t, detached, "stale lifecycle detach")
					}
					release()
					second := <-secondResult
					if second.err != nil {
						t.Fatal(second.err)
					}
					task6RepairReadStream(t, second.stream, false)
					task6DrainUsage(t)
					if connections.Load() != 2 {
						t.Errorf("connections=%d, want two", connections.Load())
					}
				})
			}
		}
	}
}

func TestApplyPatchRepairGatewayInitializesGinTestMode(t *testing.T) {
	// Simulate an isolated fixture starting in Gin's default mode, not suite order.
	gin.SetMode(gin.DebugMode)
	defer gin.SetMode(gin.TestMode)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, task6ProviderFixture("custom-compat", "nonstream", "apply_patch"))
	}))
	defer server.Close()
	exec := NewOpenAICompatExecutor("custom-compat", &config.Config{})
	auth := &cliproxyauth.Auth{ID: t.Name(), Provider: exec.Identifier(), Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
	result := task6Gateway(t, exec, auth, false)
	if result.Code != http.StatusBadGateway {
		t.Fatalf("expected fixture error path, got %d", result.Code)
	}
	if gin.Mode() != gin.TestMode {
		t.Fatalf("shared gateway fixture left Gin in %q", gin.Mode())
	}
}

func TestApplyPatchRepairResponsesSourceTerminalNonStream(t *testing.T) {
	for _, provider := range []string{"xai", "meta", "kimi"} {
		for _, mode := range []string{"args-eof", "item-eof", "empty", "done"} {
			if provider == "kimi" && mode != "empty" {
				// Kimi's non-stream endpoint returns JSON, not a buffered SSE response.
				continue
			}
			t.Run(provider+"/"+mode, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_, _ = io.WriteString(w, task6RepairSSE(task6RepairSource(mode)))
				}))
				defer server.Close()
				exec := task6RepairExecutor(provider)
				auth := &cliproxyauth.Auth{ID: t.Name(), Provider: provider, Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
				checkUsage := task6CaptureFailureUsage(t, auth.ID)
				response, errExecute := exec.Execute(t.Context(), auth, cliproxyexecutor.Request{Model: "grok-4", Payload: []byte(task6PatchRequest)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
				if len(response.Payload) != 0 {
					t.Errorf("failed source returned payload: %s", response.Payload)
				}
				assertTask6PatchError(t, errExecute)
				checkUsage()
				auth.ID += "-gateway"
				checkGatewayUsage := task6CaptureFailureUsage(t, auth.ID)
				result := task6Gateway(t, exec, auth, false)
				if result.Code != http.StatusBadGateway || !strings.Contains(result.Body.String(), "Invalid apply_patch tool arguments received from upstream.") {
					t.Errorf("non-stream gateway did not return clean 502: %d %s", result.Code, result.Body)
				}
				checkGatewayUsage()
			})
		}
	}
}

func TestApplyPatchRepairXAIWebsocketCompactionUsage(t *testing.T) {
	const invalidState = `{"id":"resp_empty","output":[]}`
	const validState = `{"id":"resp_compact","output":[{"type":"compaction","encrypted_content":"opaque-state"}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`
	for _, tc := range []struct {
		name        string
		body        string
		httpStatus  int
		wantStatus  int
		wantRecords int
		emptyInput  bool
		passthrough bool
	}{
		{name: "invalid-state", body: invalidState, httpStatus: http.StatusOK, wantStatus: http.StatusBadGateway, wantRecords: 1},
		{name: "valid-state", body: validState, httpStatus: http.StatusOK, wantRecords: 1},
		{name: "http-error", body: `{"error":{"message":"compact rejected"}}`, httpStatus: http.StatusBadRequest, wantStatus: http.StatusBadRequest, wantRecords: 1},
		{name: "missing-reporter-empty-context", body: invalidState, httpStatus: http.StatusOK, wantStatus: http.StatusBadRequest, emptyInput: true},
		{name: "http-compact-passthrough", body: invalidState, httpStatus: http.StatusOK, wantRecords: 1, passthrough: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := make(chan []byte, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/responses/compact" {
					t.Errorf("unexpected compact request: %s %s", r.Method, r.URL.Path)
				}
				body, errReadAll := io.ReadAll(r.Body)
				if errReadAll != nil {
					t.Errorf("read compact request: %v", errReadAll)
				}
				requests <- body
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.httpStatus)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			exec := NewXAIWebsocketsExecutor(&config.Config{})
			exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
			exec.idStore = &xaiWebsocketIDStateStore{sessions: make(map[string]*xaiWebsocketIDState)}
			auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "xai", Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
			capture := &task6UsageCapture{id: auth.ID, records: make(chan usage.Record, 32)}
			usage.RegisterNamedPlugin("task6-patch-failure", capture)
			req := cliproxyexecutor.Request{Model: "grok-4.3", Payload: []byte(`{"model":"grok-4.3","input":[{"type":"message","role":"user","content":"history"},{"type":"compaction_trigger"}]}`)}
			if tc.emptyInput {
				req.Payload = []byte(`{"model":"grok-4.3","input":[{"type":"compaction_trigger"}]}`)
			}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, Stream: true, Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: t.Name()}}
			var errExecute error
			if tc.passthrough {
				opts.Alt = "responses/compact"
				opts.Stream = false
				var response cliproxyexecutor.Response
				response, errExecute = exec.XAIExecutor.Execute(t.Context(), auth, req, opts)
				if output := gjson.GetBytes(response.Payload, "output"); !output.IsArray() || len(output.Array()) != 0 {
					t.Errorf("ordinary HTTP compact output changed: %s", response.Payload)
				}
			} else {
				result, errExecuteStream := exec.ExecuteStream(cliproxyexecutor.WithDownstreamWebsocket(t.Context()), auth, req, opts)
				errExecute = errExecuteStream
				if result == nil && tc.wantStatus == 0 {
					t.Error("valid compact response returned no stream")
				}
				if result != nil {
					var completed []byte
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Errorf("compact stream chunk error: %v", chunk.Err)
						}
						if gjson.GetBytes(chunk.Payload, "type").String() == "response.completed" {
							completed = chunk.Payload
						}
					}
					if tc.wantStatus != 0 || gjson.GetBytes(completed, "response.output.0.encrypted_content").String() != "opaque-state" {
						t.Errorf("unexpected compact success: %s", completed)
					}
				}
			}
			if tc.wantStatus == 0 {
				if errExecute != nil {
					t.Errorf("compact execution error: %v", errExecute)
				}
			} else if status, okStatus := errExecute.(interface{ StatusCode() int }); !okStatus || status.StatusCode() != tc.wantStatus {
				t.Errorf("compact error = %v, want status %d", errExecute, tc.wantStatus)
			}
			select {
			case body := <-requests:
				if tc.emptyInput || xaiInputHasItemType(body, "compaction_trigger") {
					t.Errorf("unexpected upstream compact input: %s", body)
				}
			default:
				if !tc.emptyInput {
					t.Error("compact execution made no HTTP request")
				}
			}
			// A queued barrier observes every attempt record without timing-based sleeps.
			usage.PublishRecord(t.Context(), usage.Record{RequestID: auth.ID + "-barrier"})
			count := 0
			for {
				select {
				case record := <-capture.records:
					if record.RequestID == auth.ID+"-barrier" {
						if count != tc.wantRecords {
							t.Errorf("usage records=%d, want %d", count, tc.wantRecords)
						}
						return
					}
					count++
					if record.Failed != (tc.wantStatus != 0) || record.Fail.StatusCode != tc.wantStatus {
						t.Errorf("unexpected compact usage outcome: %+v", record)
					}
					if tc.body == validState && record.Detail.TotalTokens != 3 {
						t.Errorf("valid compact usage total=%d, want 3", record.Detail.TotalTokens)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("compact usage barrier did not arrive")
				}
			}
		})
	}
}

func TestApplyPatchRepairOrdinaryEmptyHTTPPassthrough(t *testing.T) {
	for _, provider := range []string{"xai", "meta", "kimi"} {
		t.Run(provider, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			defer server.Close()
			exec := task6RepairExecutor(provider)
			auth := &cliproxyauth.Auth{Provider: provider, Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
			response, errExecute := exec.Execute(t.Context(), auth, cliproxyexecutor.Request{Model: "grok-4", Payload: []byte(`{"input":"ordinary","tools":[{"type":"function","name":"apply_patch"}]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
			if provider == "kimi" {
				if errExecute != nil || len(response.Payload) != 0 {
					t.Fatalf("ordinary Kimi response changed: %s %v", response.Payload, errExecute)
				}
			} else if status, okStatus := errExecute.(interface{ StatusCode() int }); !okStatus || status.StatusCode() != http.StatusRequestTimeout {
				t.Fatalf("ordinary disconnect changed: %v", errExecute)
			}
		})
	}
}
