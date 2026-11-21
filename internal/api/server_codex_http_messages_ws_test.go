package api

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestCodexHTTPMessagesWebsocketEndpoint(t *testing.T) {
	for _, test := range []struct {
		name            string
		stopAfterCreate bool
		wantHTTP        bool
		editPrefix      bool
		noSession       bool
		disabled        bool
		rejectUpgrade   bool
		acceptedError   bool
		changeAgent     bool
		changeHeader    bool
		tooBig          bool
		payloadRules    bool
		terminalFrame   string
		terminalStatus  int
		acknowledge     bool
		noBuffer        bool
		internalPath    string
		contextLimit    bool
		rejectionFrame  string
		rejectionDelay  int
		recoveryEOF     bool
		emptyRecovery   bool
		retryAfter      string
		quotaExceeded   bool
		nonUpgradeOK    bool
	}{
		{name: "reuse"},
		{name: "nested response ID in first failed event", terminalFrame: `{"type":"response.failed","status":503,"response":{"id":"resp_1","status":"failed","error":{"type":"server_error","code":"server_is_overloaded","message":"accepted terminal failure"}}}`},
		{name: "top-level response ID in first failed event", terminalFrame: `{"type":"response.failed","status":503,"response_id":"resp_1","response":{"status":"failed","error":{"type":"server_error","code":"server_is_overloaded","message":"accepted terminal failure"}}}`},
		{name: "top-level response ID in first error event", terminalFrame: `{"type":"error","response_id":"resp_1","status":503,"error":{"type":"server_error","message":"accepted terminal failure"}}`},
		{name: "untyped first error with response ID", terminalFrame: `{"response_id":"resp_1","status":503,"error":{"type":"server_error","message":"accepted terminal failure"}}`},
		{name: "accepted context length failure keeps 413", terminalStatus: 413, terminalFrame: `{"type":"response.failed","response":{"id":"resp_1","error":{"status":413,"code":"context_length_exceeded","message":"accepted terminal failure"}}}`},
		{name: "accepted capacity failure keeps 503", terminalFrame: `{"type":"response.failed","status":503,"response":{"id":"resp_1","error":{"type":"server_error","code":"model_at_capacity","message":"accepted terminal failure: model is at capacity"}}}`},
		{name: "only the public Messages route opts in", internalPath: "/internal/claude"},
		{name: "internal callback stays HTTP even with the Messages path", internalPath: "/v1/messages"},
		{name: "acknowledged failed event before SSE keeps 413", acknowledge: true, terminalStatus: 413, terminalFrame: `{"type":"response.failed","status":413,"response":{"id":"resp_1","error":{"code":"context_length_exceeded","message":"accepted terminal failure"}}}`},
		{name: "acknowledged socket limit is not recoverable", acknowledge: true, terminalStatus: 400, terminalFrame: `{"type":"error","status":400,"error":{"code":"websocket_connection_limit_reached","message":"accepted terminal failure"}}`},
		{name: "acknowledged failed event before SSE keeps 503", acknowledge: true, terminalFrame: `{"type":"response.failed","status":503,"response":{"id":"resp_1","error":{"code":"model_at_capacity","message":"accepted terminal failure"}}}`},
		{name: "acknowledged error event before SSE keeps 503", acknowledge: true, terminalFrame: `{"type":"error","status":503,"error":{"code":"model_at_capacity","message":"accepted terminal failure"}}`},
		{name: "unaccepted terminal failure keeps 413", terminalStatus: 413, terminalFrame: `{"type":"response.failed","status":413,"response":{"error":{"code":"context_length_exceeded","message":"accepted terminal failure"}}}`},
		{name: "unbuffered unaccepted terminal failure keeps 413", terminalStatus: 413, noBuffer: true, terminalFrame: `{"type":"response.failed","status":413,"response":{"error":{"code":"context_length_exceeded","message":"accepted terminal failure"}}}`},
		{name: "accepted request preserves known status", contextLimit: true},
		{name: "expired upstream parent falls back with full input", rejectionFrame: `{"type":"error","status":400,"error":{"type":"invalid_request_error","code":"previous_response_not_found","message":"unknown previous response"}}`},
		{name: "statusless missing parent falls back with full input", rejectionFrame: `{"type":"error","error":{"type":"invalid_request_error","code":"previous_response_not_found","message":"unknown previous response"}}`},
		{name: "untyped statusless missing parent falls back", rejectionFrame: `{"error":{"type":"invalid_request_error","code":"previous_response_not_found","message":"unknown previous response"}}`},
		{name: "expired upstream socket falls back with full history", rejectionFrame: `{"type":"error","status":400,"error":{"code":"websocket_connection_limit_reached","message":"connection expired"}}`},
		{name: "late continuation rejection still recovers", rejectionDelay: 49, rejectionFrame: `{"type":"error","status":400,"error":{"code":"previous_response_not_found","message":"parent expired"}}`},
		{name: "ambiguous HTTP recovery is not replayed", recoveryEOF: true, rejectionFrame: `{"type":"error","status":400,"error":{"code":"previous_response_not_found","message":"parent expired"}}`},
		{name: "empty HTTP recovery SSE is not replayed", recoveryEOF: true, emptyRecovery: true, rejectionFrame: `{"type":"error","status":400,"error":{"code":"previous_response_not_found","message":"parent expired"}}`},
		{name: "failed terminal event preserves capacity retry", terminalStatus: 429, retryAfter: "17", terminalFrame: `{"type":"response.failed","status":429,"headers":{"Retry-After":"17"},"response":{"error":{"code":"turn_rate_limit_exceeded","message":"accepted terminal failure"}}}`},
		{name: "error event preserves capacity retry", terminalStatus: 429, retryAfter: "17", terminalFrame: `{"type":"error","status":429,"headers":{"Retry-After":"17"},"error":{"code":"turn_rate_limit_exceeded","message":"accepted terminal failure"}}`},
		{name: "failed terminal event preserves quota reset", terminalStatus: 429, retryAfter: "120", quotaExceeded: true, terminalFrame: `{"type":"response.failed","status":429,"headers":{"Retry-After":"17"},"response":{"error":{"type":"usage_limit_reached","resets_in_seconds":120,"message":"accepted terminal failure"}}}`},
		{name: "error event preserves quota reset", terminalStatus: 429, retryAfter: "120", quotaExceeded: true, terminalFrame: `{"type":"error","status":429,"headers":{"Retry-After":"17"},"error":{"type":"usage_limit_reached","resets_in_seconds":120,"message":"accepted terminal failure"}}`},
		{name: "ordinary 200 cannot masquerade as an upgrade", nonUpgradeOK: true, wantHTTP: true},
		{name: "no replay after an ambiguous disconnect", stopAfterCreate: true},
		{name: "no replay after a buffered accepted error", acceptedError: true},
		{name: "body agent is isolated", changeAgent: true},
		{name: "changed handshake header reconnects", changeHeader: true},
		{name: "oversized request keeps status", tooBig: true},
		{name: "edited history sends full input", editPrefix: true},
		{name: "payload rules are final on full history", payloadRules: true},
		{name: "no explicit session stays on HTTP", noSession: true, wantHTTP: true},
		{name: "off by default stays on HTTP", disabled: true, wantHTTP: true},
		{name: "upgrade rejection falls back before sending", rejectUpgrade: true, wantHTTP: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var connections, creates, httpCalls atomic.Int32
			fullRetry := make(chan []byte, 1)
			frames := make(chan []byte, 8)
			origins := make(chan string, 8)
			upgrader := websocket.Upgrader{}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !websocket.IsWebSocketUpgrade(r) {
					httpCalls.Add(1)
					if test.rejectionFrame != "" {
						body, _ := io.ReadAll(r.Body)
						fullRetry <- body
						if test.emptyRecovery {
							w.Header().Set("Content-Type", "text/event-stream")
							w.WriteHeader(http.StatusOK)
							return
						}
						if test.recoveryEOF {
							conn, _, errHijack := w.(http.Hijacker).Hijack()
							if errHijack == nil {
								_ = conn.Close()
							}
							return
						}
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprint(w, `data: {"type":"response.completed","response":{"id":"resp_http","model":"gpt-5.5","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`+"\n\n")
					return
				}
				if test.nonUpgradeOK {
					_, _ = fmt.Fprint(w, "websocket not supported")
					return
				}
				if test.rejectUpgrade {
					http.Error(w, "upgrade refused", http.StatusUpgradeRequired)
					return
				}
				conn, errUpgrade := upgrader.Upgrade(w, r, nil)
				if errUpgrade != nil {
					return
				}
				defer func() { _ = conn.Close() }()
				connections.Add(1)
				origins <- r.Header.Get("Originator")
				for {
					_, frame, errRead := conn.ReadMessage()
					if errRead != nil {
						return
					}
					turn := creates.Add(1)
					if test.stopAfterCreate {
						return
					}
					if test.terminalFrame != "" {
						if test.acknowledge {
							_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5","output":[]}}`))
						}
						_ = conn.WriteMessage(websocket.TextMessage, []byte(test.terminalFrame))
						return
					}
					if test.rejectionFrame != "" && turn == 2 {
						frames <- frame
						for range test.rejectionDelay {
							if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"keepalive"}`)); errWrite != nil {
								return
							}
						}
						_ = conn.WriteMessage(websocket.TextMessage, []byte(test.rejectionFrame))
						return
					}
					if test.acceptedError || test.contextLimit {
						_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5","output":[]}}`))
						text := `{"type":"error","status":500,"error":{"type":"server_error","message":"accepted turn failed"}}`
						if test.contextLimit {
							text = `{"type":"error","status":400,"error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"input exceeds context"}}`
						}
						_ = conn.WriteMessage(websocket.TextMessage, []byte(text))
						return
					}
					if test.tooBig {
						_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseMessageTooBig, "oversized"))
						return
					}
					frames <- frame
					id := fmt.Sprintf("resp_%d", turn)
					item := fmt.Sprintf(`{"type":"function_call","id":"fc_%d","call_id":"call_%d","name":"probe","arguments":"{}","status":"completed"}`, turn, turn)
					for _, event := range []string{
						fmt.Sprintf(`{"type":"response.created","response":{"id":%q,"model":"gpt-5.5","output":[]}}`, id),
						`{"type":"response.output_item.added","output_index":0,"item":` + item + `}`,
						`{"type":"response.output_item.done","output_index":0,"item":` + item + `}`,
						fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"model":"gpt-5.5","status":"completed","output":[%s],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`, id, item),
					} {
						if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(event)); errWrite != nil {
							return
						}
					}
				}
			}))
			t.Cleanup(upstream.Close)
			cfg := &proxyconfig.Config{Codex: proxyconfig.CodexConfig{ServeHTTPMessagesOverWebsockets: !test.disabled}}
			cfg.PassthroughHeaders = true
			cfg.Streaming.BootstrapRetries = 2
			cfg.Codex.StreamBootstrapBuffering = !test.noBuffer && (test.acceptedError || test.contextLimit || test.terminalFrame != "")
			cfg.Codex.DisableCodexCloaking = test.changeHeader
			if test.payloadRules {
				models := []proxyconfig.PayloadModelRule{{Name: "*"}}
				cfg.Payload.Override = []proxyconfig.PayloadRule{{Models: models, Params: map[string]any{"instructions": "operator override"}}}
				cfg.Payload.Filter = []proxyconfig.PayloadFilterRule{{Models: models, Params: []string{"parallel_tool_calls"}}}
			}
			server := newTestServerWithConfig(t, cfg)
			server.accessManager.SetProviders([]sdkaccess.Provider{&revocableServerAccessProvider{}})
			exec := executor.NewCodexAutoExecutor(cfg)
			server.handlers.AuthManager.RegisterExecutor(exec)
			server.handlers.AuthManager.SetRetryConfig(1, 0, 2)
			if test.recoveryEOF {
				affinity := auth.NewSessionAffinitySelector(&auth.RoundRobinSelector{})
				server.handlers.AuthManager.SetSelector(affinity)
				t.Cleanup(affinity.Stop)
			}
			entry := &auth.Auth{ID: t.Name(), Provider: "codex", Status: auth.StatusActive,
				Attributes: map[string]string{"base_url": upstream.URL, "websockets": "true"},
				Metadata:   map[string]any{"type": "codex", "access_token": "test-only-token", "account_id": "test-account"}}
			if _, err := server.handlers.AuthManager.Register(t.Context(), entry); err != nil {
				t.Fatal(err)
			}
			registry.GetGlobalRegistry().RegisterClient(entry.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
			if test.recoveryEOF || test.acceptedError || test.terminalFrame != "" && test.retryAfter == "" || test.contextLimit {
				backup := entry.Clone()
				backup.ID += "-backup"
				if _, err := server.handlers.AuthManager.Register(t.Context(), backup); err != nil {
					t.Fatal(err)
				}
				registry.GetGlobalRegistry().RegisterClient(backup.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
				t.Cleanup(func() {
					exec.CloseAuthExecutionSessions(backup.ID)
					registry.GetGlobalRegistry().UnregisterClient(backup.ID)
				})
			}
			t.Cleanup(func() {
				exec.CloseAuthExecutionSessions(entry.ID)
				registry.GetGlobalRegistry().UnregisterClient(entry.ID)
			})
			if test.internalPath != "" {
				stream, err := server.handlers.ExecuteModelStream(t.Context(), handlers.ModelExecutionRequest{
					EntryProtocol: "claude", ExitProtocol: "claude", Model: "gpt-5.5", Stream: true, Path: test.internalPath, CallerScope: "test-internal",
					Headers: http.Header{"X-Session-Id": {"internal-session"}},
					Body:    []byte(`{"model":"gpt-5.5","max_tokens":128,"stream":true,"messages":[{"role":"user","content":"hi"}]}`),
				})
				if err != nil {
					t.Fatal(err)
				}
				for range stream.Chunks {
				}
				if httpCalls.Load() != 1 || connections.Load() != 0 {
					t.Fatalf("internal request used WebSocket rather than HTTP: HTTP=%d WS=%d", httpCalls.Load(), connections.Load())
				}
				return
			}
			request := func(messages, turn string) *httptest.ResponseRecorder {
				t.Helper()
				body := `{"model":"gpt-5.5","max_tokens":128,"stream":true,"tools":[{"name":"probe","input_schema":{"type":"object"}}],"messages":` + messages
				if test.changeAgent {
					body += fmt.Sprintf(`,"metadata":{"agent_id":%q}`, turn)
				}
				body += `}`
				req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)).WithContext(t.Context())
				req.Header.Set("Authorization", "Bearer key-1")
				req.Header.Set("Content-Type", "application/json")
				if !test.noSession {
					req.Header.Set("X-Claude-Code-Session-Id", t.Name())
				}
				if test.changeHeader {
					req.Header.Set("Originator", turn)
				}
				res := httptest.NewRecorder()
				server.engine.ServeHTTP(res, req)
				return res
			}
			const user = `{"role":"user","content":"run probe"}`
			first := request(`[`+user+`]`, "a")
			if test.wantHTTP {
				if first.Code != http.StatusOK || httpCalls.Load() != 1 || connections.Load() != 0 {
					t.Fatalf("expected one HTTP request; status=%d HTTP=%d WS=%d", first.Code, httpCalls.Load(), connections.Load())
				}
				return
			}
			if test.tooBig {
				if first.Code != http.StatusRequestEntityTooLarge || creates.Load() != 1 {
					t.Fatalf("expected 413 without replay: status=%d creates=%d body=%s", first.Code, creates.Load(), first.Body)
				}
				return
			}
			if test.contextLimit {
				if first.Code != http.StatusBadRequest || creates.Load() != 1 || !strings.Contains(first.Body.String(), "input exceeds context") {
					t.Fatalf("expected original 400 without replay: status=%d creates=%d body=%s", first.Code, creates.Load(), first.Body)
				}
				return
			}
			if test.terminalFrame != "" {
				want := test.terminalStatus
				if want == 0 {
					want = http.StatusServiceUnavailable
				}
				if test.retryAfter != "" {
					got, ok := server.handlers.AuthManager.GetByID(entry.ID)
					if !ok {
						t.Fatal("missing test credential")
					}
					state := got.ModelStates["gpt-5.5"]
					if got.Quota.Exceeded != test.quotaExceeded || (!test.quotaExceeded && state != nil && state.Quota.Exceeded) || first.Header().Get("Retry-After") != test.retryAfter {
						t.Fatalf("expected auth quota=%v and Retry-After %s, got %q; auth quota=%v model quota=%v", test.quotaExceeded, test.retryAfter, first.Header().Get("Retry-After"), got.Quota.Exceeded, state != nil && state.Quota.Exceeded)
					}
				}
				if first.Code != want || creates.Load() != 1 || !strings.Contains(first.Body.String(), "accepted terminal failure") {
					t.Fatalf("expected original %d without replay: status=%d creates=%d body=%s", want, first.Code, creates.Load(), first.Body)
				}
				return
			}
			if test.stopAfterCreate || test.acceptedError {
				if first.Code < 400 || creates.Load() != 1 {
					t.Fatalf("ambiguous lost turn must fail without replay: status=%d creates=%d body=%s", first.Code, creates.Load(), first.Body)
				}
				return
			}
			if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"type":"tool_use"`) {
				t.Fatalf("expected tool call, got %d: %s", first.Code, first.Body)
			}
			if got := first.Header().Get("Sec-WebSocket-Accept"); got != "" {
				t.Fatal("HTTP Messages response exposed a WebSocket handshake header")
			}
			<-frames
			const assistant = `{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"probe","input":{}}]}`
			const result = `{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"ok"}]}`
			history := user
			if test.editPrefix {
				history = strings.Replace(user, "run probe", "edited prompt", 1)
			}
			second := request(`[`+history+`,`+assistant+`,`+result+`]`, "b")
			if test.recoveryEOF {
				if second.Code != http.StatusBadGateway || creates.Load() != 2 || httpCalls.Load() != 1 {
					t.Fatalf("expected a single recovery attempt: status=%d WS=%d HTTP=%d", second.Code, creates.Load(), httpCalls.Load())
				}
				return
			}
			if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), `"type":"message_stop"`) {
				t.Fatalf("expected Messages SSE, got %d: %s", second.Code, second.Body)
			}
			if test.rejectionFrame != "" {
				if creates.Load() != 2 || httpCalls.Load() != 1 {
					t.Fatalf("expected one HTTP recovery, websocket=%d http=%d", creates.Load(), httpCalls.Load())
				}
				attempt := ojson.NewDoc(<-frames)
				if attempt.Get("previous_response_id").String() != "resp_1" || attempt.Get("input.#").Int() != 1 {
					t.Fatalf("expected a rejected incremental create before recovery: %s", attempt.String())
				}
				body := ojson.NewDoc(<-fullRetry)
				if body.Get("previous_response_id").Exists() || body.Get("input.#").Int() != 3 {
					t.Fatalf("recovery must send full history: %s", body.String())
				}
				return
			}
			frame := ojson.NewDoc(<-frames)
			if test.payloadRules {
				if connections.Load() != 1 || frame.Get("previous_response_id").Exists() || frame.Get("input.#").Int() != 3 ||
					frame.Get("instructions").String() != "operator override" || frame.Get("parallel_tool_calls").Exists() {
					t.Fatalf("payload rules must be final on full input: connections=%d frame=%s", connections.Load(), frame.String())
				}
				return
			}
			if test.changeAgent || test.changeHeader {
				if connections.Load() != 2 || frame.Get("previous_response_id").Exists() || frame.Get("input.#").Int() != 3 {
					t.Fatalf("changed identity or header must reconnect with full input: connections=%d frame=%s", connections.Load(), frame.String())
				}
				if test.changeHeader {
					if first, second := <-origins, <-origins; first != "a" || second != "b" {
						t.Fatalf("wrong originator on handshake: %q then %q", first, second)
					}
				}
				return
			}
			if test.editPrefix {
				if frame.Get("previous_response_id").Exists() || frame.Get("input.#").Int() != 3 || frame.Get("input.0.content.0.text").String() != "edited prompt" {
					t.Fatalf("edited history must use full input: %s", frame.String())
				}
				return
			}
			if connections.Load() != 1 || frame.Get("previous_response_id").String() != "resp_1" || frame.Get("input.#").Int() != 1 {
				t.Fatalf("expected one connection and incremental second request, got %d and %s", connections.Load(), frame.String())
			}
		})
	}
}
