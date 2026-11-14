package openai

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Exercise the actual downstream handler, auth manager and Codex executor.
// All coordination uses protocol events, never sleep-based timing guesses.
func TestResponsesSteeringFullDuplexIntegration(t *testing.T) {
	for _, scenario := range []string{"successor", "tool_pending", "disconnect_accepted", "disconnect_pending"} {
		t.Run(scenario, func(t *testing.T) {
			var connections atomic.Int32
			done := make(chan struct{})
			control1 := []byte(`{"type":"response.steer","previous_response_id":"r1","input":"one"}`)
			control2 := []byte(`{"type":"response.steer","previous_response_id":"r1","input":"two"}`)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(done)
				connections.Add(1)
				c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer func() { _ = c.Close() }()
				_ = c.SetReadDeadline(time.Now().Add(8 * time.Second))
				read := func() []byte {
					_, b, err := c.ReadMessage()
					if err != nil {
						t.Errorf("upstream read: %v", err)
					}
					return b
				}
				write := func(s string) {
					if err := c.WriteMessage(websocket.TextMessage, []byte(s)); err != nil {
						t.Errorf("upstream write: %v", err)
					}
				}
				if ojson.GetBytesNoCopy(read(), "type").String() != "response.create" {
					t.Error("missing initial create")
				}
				write(`{"type":"response.created","response":{"id":"r1"}}`)
				if b := read(); !bytes.Equal(b, control1) {
					t.Errorf("first steer altered: %s", b)
				}
				if b := read(); !bytes.Equal(b, control2) {
					t.Errorf("second steer altered: %s", b)
				}
				write(`{"type":"response.steer.accepted","steer":{"id":"s1","previous_response_id":"r1"}}`)
				write(`{"type":"response.steer.accepted","steer":{"id":"s2","previous_response_id":"r1"}}`)
				if scenario == "disconnect_accepted" {
					return
				}
				if scenario == "tool_pending" || scenario == "disconnect_pending" {
					write(`{"type":"response.completed","response":{"id":"r1","output":[{"type":"function_call","call_id":"call1","name":"lookup","arguments":"{}"}]}}`)
					write(`{"type":"response.steer.pending","steer":{"id":"s1","previous_response_id":"r1"},"reason":"waiting_for_required_input","required_input":[{"type":"function_call_output","call_id":"call1","name":"lookup"}]}`)
					if scenario == "disconnect_pending" {
						return
					}
					b := read()
					if ojson.GetBytesNoCopy(b, "type").String() != "response.create" || ojson.GetBytesNoCopy(b, "input.0.call_id").String() != "call1" {
						t.Errorf("required input lost: %s", b)
					}
				} else {
					write(`{"type":"response.incomplete","response":{"id":"r1","incomplete_details":{"reason":"steered"},"output":[]}}`)
				}
				write(`{"type":"response.created","response":{"id":"r2"}}`)
				write(`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"STEER_OK"}]}}`)
				write(`{"type":"response.completed","response":{"id":"r2","output":[]}}`)
				_, _, _ = c.ReadMessage()
			}))
			defer upstream.Close()
			cfg := &config.Config{}
			cfg.Codex.ResponseSteering = true
			cfg.CodexResponseSteering = true
			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
			authID := "steering-integration-" + scenario
			_, err := manager.Register(context.Background(), &coreauth.Auth{ID: authID, Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"api_key": "test-key", "base_url": upstream.URL, "websockets": "true"}})
			if err != nil {
				t.Fatal(err)
			}
			registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: "steering-test-model"}})
			defer registry.GetGlobalRegistry().UnregisterClient(authID)
			base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
			base.SetModelRouterHost(&websocketLoggingRouter{
				record: func(ctx context.Context, _ string, body []byte) {
					// Model-router host HTTP calls share the duplex connection lifetime.
					helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{URL: "https://router.invalid/route", Body: body})
				},
			})
			h := NewOpenAIResponsesAPIHandler(base)
			router := gin.New()
			deferredCount := make(chan int, 1)
			router.GET("/v1/responses", func(c *gin.Context) {
				h.ResponsesWebsocket(c)
				value, _ := c.Get(logging.DeferredAPIRequestContextKey)
				requests, _ := value.([]logging.DeferredAPIRequest)
				deferredCount <- len(requests)
			})
			downstream := httptest.NewServer(router)
			defer downstream.Close()
			c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(downstream.URL, "http")+"/v1/responses", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = c.Close() }()
			_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
			send := func(b []byte) {
				if err := c.WriteMessage(websocket.TextMessage, b); err != nil {
					t.Fatal(err)
				}
			}
			send([]byte(`{"type":"response.create","model":"steering-test-model","input":[]}`))
			completed := false
			accepted := 0
			pendingSeen := false
			for {
				_, b, err := c.ReadMessage()
				if err != nil {
					t.Fatalf("downstream read before expected terminal event: %v", err)
				}
				kind := ojson.GetBytesNoCopy(b, "type").String()
				if kind == "error" {
					if !strings.HasPrefix(scenario, "disconnect_") || ojson.GetBytesNoCopy(b, "error.code").String() != "upstream_websocket_lost" {
						t.Fatalf("unexpected downstream error: %s", b)
					}
					break
				}
				if kind == "response.created" && ojson.GetBytesNoCopy(b, "response.id").String() == "r1" {
					send(control1)
					send(control2)
				}
				if kind == "response.steer.accepted" {
					accepted++
				}
				if kind == "response.steer.pending" {
					pendingSeen = true
				}
				if kind == "response.steer.pending" && scenario == "tool_pending" {
					send([]byte(`{"type":"response.create","previous_response_id":"r1","input":[{"type":"function_call_output","call_id":"call1","output":"found"}]}`))
				}
				if kind == "response.completed" && ojson.GetBytesNoCopy(b, "response.id").String() == "r2" {
					if strings.HasPrefix(scenario, "disconnect_") {
						t.Fatal("successor completed instead of reporting upstream disconnect")
					}
					completed = ojson.GetBytesNoCopy(b, "response.output.0.content.0.text").String() == "STEER_OK"
					_ = c.Close()
					break
				}
			}
			if accepted != 2 {
				t.Fatalf("expected both acknowledgements before termination; got %d", accepted)
			}
			if (scenario == "tool_pending" || scenario == "disconnect_pending") && !pendingSeen {
				t.Fatal("connection ended before the pending event was forwarded")
			}
			if !strings.HasPrefix(scenario, "disconnect_") && !completed {
				t.Fatal("successor did not complete")
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("connection cleanup stalled")
			}
			if connections.Load() != 1 {
				t.Fatalf("unexpected reconnect/replay: %d", connections.Load())
			}
			// An upstream disconnect can leave the downstream ready for another turn.
			_ = c.Close()
			select {
			case n := <-deferredCount:
				if n != 0 {
					t.Fatalf("duplex socket retained %d deferred upstream requests", n)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("duplex handler did not return")
			}
		})
	}
}

func TestResponsesWebsocketIsCompatLocalCompactionIntegration_Issue7406(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const marker = "WS-HANDLER-COMPACT-7406"
	for _, steering := range []bool{false, true} {
		t.Run(fmt.Sprintf("steering=%t", steering), func(t *testing.T) {
			wsReceived := make(chan []byte, 8)
			httpReceived := make(chan []byte, 4)

			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if websocket.IsWebSocketUpgrade(r) {
					conn, errUp := (&websocket.Upgrader{}).Upgrade(w, r, nil)
					if errUp != nil {
						t.Errorf("upgrade ws: %v", errUp)
						return
					}
					defer func() {
						if errClose := conn.Close(); errClose != nil {
							t.Errorf("close websocket: %v", errClose)
						}
					}()
					_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

					for idx := 1; ; idx++ {
						_, frame, errRead := conn.ReadMessage()
						if errRead != nil {
							return
						}
						wsReceived <- frame
						respID := fmt.Sprintf("resp_ws_%d", idx)
						_ = conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.created","response":{"id":%q,"output":[]}}`, respID)))
						_ = conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ws-ok-%d"}]}],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}`, respID, idx)))
					}
				}

				body, _ := io.ReadAll(r.Body)
				httpReceived <- body
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w,
					"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_sum\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Compacted summary keeping "+marker+".\"}]}}\n\n"+
						"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_http_sum\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":4,\"total_tokens\":14}}}\n\n",
				)
			}))
			defer upstream.Close()

			modelID := fmt.Sprintf("compat-ws-model-%t", steering)
			cfg := &config.Config{
				CodexKey: []config.CodexKey{{
					APIKey:  "test-compat-key",
					BaseURL: upstream.URL,
					Models:  []config.CodexModel{{Name: modelID, IsCompat: true}},
				}},
			}
			cfg.Codex.ResponseSteering = steering
			cfg.CodexResponseSteering = steering

			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
			authID := fmt.Sprintf("compat-ws-auth-%t", steering)
			if _, errReg := manager.Register(context.Background(), &coreauth.Auth{
				ID:         authID,
				Provider:   "codex",
				Status:     coreauth.StatusActive,
				Attributes: map[string]string{"api_key": "test-compat-key", "base_url": upstream.URL, "websockets": "true"},
			}); errReg != nil {
				t.Fatal(errReg)
			}
			registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: modelID, IsCompat: true}})
			defer registry.GetGlobalRegistry().UnregisterClient(authID)

			h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
			router := gin.New()
			router.GET("/v1/responses", h.ResponsesWebsocket)
			downstream := httptest.NewServer(router)
			defer downstream.Close()

			conn, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(downstream.URL, "http")+"/v1/responses", nil)
			if errDial != nil {
				t.Fatal(errDial)
			}
			defer func() {
				if errClose := conn.Close(); errClose != nil {
					t.Errorf("close websocket: %v", errClose)
				}
			}()
			_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

			readUntil := func(wantType string) ojson.Result {
				t.Helper()
				for {
					_, b, errRead := conn.ReadMessage()
					if errRead != nil {
						t.Fatalf("downstream read waiting for %s: %v", wantType, errRead)
					}
					parsed := ojson.ParseBytes(b)
					gotType := parsed.Get("type").String()
					if gotType == wantType {
						return parsed
					}
					if gotType == "error" && wantType != "error" {
						t.Fatalf("unexpected error while waiting for %s: %s", wantType, b)
					}
				}
			}

			// Turn 1: First turn is local compaction_trigger; socket must stay open even when steering=true.
			if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"PRE_COMPACT_RAW_TEXT"}]},{"type":"compaction_trigger"}]}`, modelID))); errWrite != nil {
				t.Fatal(errWrite)
			}
			compactedEv := readUntil("response.completed")
			synthID := compactedEv.Get("response.id").String()
			if !strings.HasPrefix(synthID, "resp_ag_compact_") {
				t.Fatalf("expected synthetic compaction response ID, got %q (%s)", synthID, compactedEv.Raw)
			}
			<-httpReceived

			// Turn 2a: Bogus synthetic previous_response_id is rejected locally without hitting upstream.
			if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.create","model":%q,"previous_response_id":"resp_ag_compact_wrong","input":[{"type":"message","role":"user","content":"next"}]}`, modelID))); errWrite != nil {
				t.Fatal(errWrite)
			}
			bogusErr := readUntil("error")
			if bogusErr.Get("error.code").String() != "previous_response_not_found" {
				t.Fatalf("expected previous_response_not_found for wrong synthetic ID, got %s", bogusErr.Raw)
			}

			// Turn 2b: Valid synthetic previous_response_id starts a fresh root WS turn with expanded summary and NO pre-compaction raw text or synthetic ID.
			if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.create","previous_response_id":%q,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Post-compaction question"}]}]}`, synthID))); errWrite != nil {
				t.Fatal(errWrite)
			}
			turn2Done := readUntil("response.completed")
			if turn2Done.Get("response.id").String() != "resp_ws_1" {
				t.Fatalf("turn 2 response.id = %q, want resp_ws_1", turn2Done.Get("response.id").String())
			}
			wsRoot := <-wsReceived
			if ojson.GetBytes(wsRoot, "previous_response_id").Exists() {
				t.Fatalf("synthetic previous_response_id leaked to upstream WS: %s", wsRoot)
			}
			if bytes.Contains(wsRoot, []byte("PRE_COMPACT_RAW_TEXT")) {
				t.Fatalf("pre-compaction history leaked alongside summary capsule: %s", wsRoot)
			}
			if bytes.Contains(wsRoot, []byte("cpa-ag-compact-v1:")) || !bytes.Contains(wsRoot, []byte(marker)) {
				t.Fatalf("expected expanded summary marker without raw capsule: %s", wsRoot)
			}

			// Turn 3: Normal incremental continuation on upstream WS works seamlessly.
			if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","previous_response_id":"resp_ws_1","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Incremental follow-up"}]}]}`)); errWrite != nil {
				t.Fatal(errWrite)
			}
			turn3Done := readUntil("response.completed")
			if turn3Done.Get("response.id").String() != "resp_ws_2" {
				t.Fatalf("turn 3 response.id = %q, want resp_ws_2", turn3Done.Get("response.id").String())
			}
			wsCont := <-wsReceived
			if gotPrev := ojson.GetBytes(wsCont, "previous_response_id").String(); gotPrev != "resp_ws_1" {
				t.Fatalf("turn 3 WS previous_response_id = %q, want resp_ws_1; frame=%s", gotPrev, wsCont)
			}
		})
	}
}

func TestResponsesWebsocketExistingWSCompactionRejectsStaleParent_Issue7406(t *testing.T) {
	gin.SetMode(gin.TestMode)
	wsReceived := make(chan []byte, 8)
	var wsConnCount atomic.Int32

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			connNum := wsConnCount.Add(1)
			conn, errUp := (&websocket.Upgrader{}).Upgrade(w, r, nil)
			if errUp != nil {
				return
			}
			defer func() {
				if errClose := conn.Close(); errClose != nil {
					t.Errorf("close websocket: %v", errClose)
				}
			}()
			_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

			for turn := 1; ; turn++ {
				_, frame, errRead := conn.ReadMessage()
				if errRead != nil {
					return
				}
				wsReceived <- frame
				respID := fmt.Sprintf("resp_ws_c%d_t%d", connNum, turn)
				_ = conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.created","response":{"id":%q,"output":[]}}`, respID)))
				_ = conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":4,"output_tokens":2,"total_tokens":6}}}`, respID)))
			}
		}

		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w,
			"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_sum\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Summary after turn 1.\"}]}}\n\n"+
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_http_sum\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":8,\"output_tokens\":4,\"total_tokens\":12}}}\n\n",
		)
	}))
	defer upstream.Close()

	const modelID = "compat-ws-stale-parent-model"
	cfg := &config.Config{
		CodexKey: []config.CodexKey{{
			APIKey:  "test-compat-key",
			BaseURL: upstream.URL,
			Models:  []config.CodexModel{{Name: modelID, IsCompat: true}},
		}},
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
	const authID = "compat-ws-stale-parent-auth"
	if _, errReg := manager.Register(context.Background(), &coreauth.Auth{
		ID:         authID,
		Provider:   "codex",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"api_key": "test-compat-key", "base_url": upstream.URL, "websockets": "true"},
	}); errReg != nil {
		t.Fatal(errReg)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: modelID, IsCompat: true}})
	defer registry.GetGlobalRegistry().UnregisterClient(authID)

	h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
	router := gin.New()
	router.GET("/v1/responses", h.ResponsesWebsocket)
	downstream := httptest.NewServer(router)
	defer downstream.Close()

	conn, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(downstream.URL, "http")+"/v1/responses", nil)
	if errDial != nil {
		t.Fatal(errDial)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Errorf("close websocket: %v", errClose)
		}
	}()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	readUntil := func(wantType string) ojson.Result {
		t.Helper()
		for {
			_, b, errRead := conn.ReadMessage()
			if errRead != nil {
				t.Fatalf("downstream read waiting for %s: %v", wantType, errRead)
			}
			parsed := ojson.ParseBytes(b)
			gotType := parsed.Get("type").String()
			if gotType == wantType {
				return parsed
			}
			if gotType == "error" && wantType != "error" {
				t.Fatalf("unexpected error while waiting for %s: %s", wantType, b)
			}
		}
	}

	// Turn 1: Real upstream WS response -> resp_ws_c1_t1.
	_ = conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"type":"message","role":"user","content":"turn 1"}]}`, modelID)))
	turn1Done := readUntil("response.completed")
	oldWSRespID := turn1Done.Get("response.id").String()
	if oldWSRespID != "resp_ws_c1_t1" {
		t.Fatalf("turn 1 response.id = %q, want resp_ws_c1_t1", oldWSRespID)
	}
	<-wsReceived

	// Turn 2: Full local compaction_trigger invalidates c1 and establishes compaction boundary.
	_ = conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"type":"message","role":"user","content":"turn 1"},{"type":"compaction_trigger"}]}`, modelID)))
	compactDone := readUntil("response.completed")
	synthID := compactDone.Get("response.id").String()
	if !strings.HasPrefix(synthID, "resp_ag_compact_") {
		t.Fatalf("turn 2 response.id = %q, want resp_ag_compact_*", synthID)
	}

	// Turn 3a: Stale pre-compaction WS response ID (resp_ws_c1_t1) MUST be rejected with 409 previous_response_not_found, NOT silently grafted onto synthID.
	_ = conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.create","model":%q,"previous_response_id":%q,"input":[{"type":"message","role":"user","content":"wrong branch"}]}`, modelID, oldWSRespID)))
	staleErr := readUntil("error")
	if staleErr.Get("status").Int() != http.StatusConflict || staleErr.Get("error.code").String() != "previous_response_not_found" {
		t.Fatalf("expected 409 previous_response_not_found for pre-compaction WS ID %q, got %s", oldWSRespID, staleErr.Raw)
	}

	// Turn 3b: Valid continuation referencing synthID dials fresh WS c2 with expanded summary and no previous_response_id.
	_ = conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.create","previous_response_id":%q,"input":[{"type":"message","role":"user","content":"clean post-compact turn"}]}`, synthID)))
	turn3Done := readUntil("response.completed")
	if turn3Done.Get("response.id").String() != "resp_ws_c2_t1" {
		t.Fatalf("turn 3b response.id = %q, want resp_ws_c2_t1", turn3Done.Get("response.id").String())
	}
	wsTurn3 := <-wsReceived
	if ojson.GetBytes(wsTurn3, "previous_response_id").Exists() {
		t.Fatalf("unexpected previous_response_id on fresh WS root: %s", wsTurn3)
	}
	if !bytes.Contains(wsTurn3, []byte("Summary after turn 1.")) {
		t.Fatalf("expected expanded summary in Turn 3b WS root: %s", wsTurn3)
	}
}
