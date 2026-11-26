package test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	openaihandlers "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestCodexResponsesBillingErrorFailsOverFromAPIKeyToOAuth(t *testing.T) {
	const (
		model        = "gpt-5.4"
		siblingModel = "gpt-5.4-mini"

		paidCreated    = `{"type":"response.created","sequence_number":0,"response":{"id":"resp_paid","status":"in_progress","output":[]}}`
		paidInProgress = `{"type":"response.in_progress","sequence_number":1,"response":{"id":"resp_paid","status":"in_progress","output":[]}}`
		paidItemAdded  = `{"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"id":"msg_paid","type":"message","status":"in_progress","role":"assistant","content":[]}}`
		paidTextDelta  = `{"type":"response.output_text.delta","sequence_number":3,"output_index":0,"content_index":0,"item_id":"msg_paid","delta":"partial paid output"}`
		paidToolEvent  = `{"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"id":"ws_paid","type":"web_search_call","status":"in_progress"}}`

		billingErrorFrame  = `{"type":"error","sequence_number":2,"error":{"type":"insufficient_quota","code":"credit_balance_exhausted","message":"Your credit balance is too low to access the API.","param":null}}`
		billingFailedFrame = `{"type":"response.failed","sequence_number":2,"response":{"id":"resp_paid","status":"failed","error":{"type":"insufficient_quota","code":"credit_balance_exhausted","message":"Your credit balance is too low to access the API.","param":null}}}`

		subCreated   = `{"type":"response.created","sequence_number":0,"response":{"id":"resp_sub","status":"in_progress","output":[]}}`
		subItemAdded = `{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"msg_sub","type":"message","status":"in_progress","role":"assistant","content":[]}}`
		subTextDelta = `{"type":"response.output_text.delta","sequence_number":2,"output_index":0,"content_index":0,"item_id":"msg_sub","delta":"hello from subscription"}`
		subCompleted = `{"type":"response.completed","sequence_number":3,"response":{"id":"resp_sub","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":3,"total_tokens":4}}}`
	)

	type testCase struct {
		name              string
		websocket         bool
		afterOutput       bool
		afterTool         bool
		modelLevelCooling bool
		terminalFrame     string
	}

	cases := []testCase{
		{
			name:          "sse/before_output/error",
			terminalFrame: billingErrorFrame,
		},
		{
			name:          "sse/before_output/response_failed",
			terminalFrame: billingFailedFrame,
		},
		{
			name:              "sse/before_output/model_level_cooling_still_cools_credential",
			modelLevelCooling: true,
			terminalFrame:     billingErrorFrame,
		},
		{
			name:          "websocket/before_output/error",
			websocket:     true,
			terminalFrame: billingErrorFrame,
		},
		{
			name:          "websocket/before_output/response_failed",
			websocket:     true,
			terminalFrame: billingFailedFrame,
		},
		{
			name:          "sse/after_output/error",
			afterOutput:   true,
			terminalFrame: billingErrorFrame,
		},
		{
			name:          "websocket/after_output/error",
			websocket:     true,
			afterOutput:   true,
			terminalFrame: billingErrorFrame,
		},
		{
			name:          "sse/after_tool_event/error",
			afterTool:     true,
			terminalFrame: billingErrorFrame,
		},
		{
			name:          "websocket/after_tool_event/error",
			websocket:     true,
			afterTool:     true,
			terminalFrame: billingErrorFrame,
		},
	}

	gin.SetMode(gin.TestMode)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var paidAttempts atomic.Int32
			var subAttempts atomic.Int32

			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			writeEvents := func(w http.ResponseWriter, r *http.Request, events []string) {
				if !tc.websocket {
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(http.StatusOK)
					for _, event := range events {
						if _, errWrite := fmt.Fprintf(w, "data: %s\n\n", event); errWrite != nil {
							t.Errorf("write SSE event: %v", errWrite)
							return
						}
					}
					if flusher, ok := w.(http.Flusher); ok {
						flusher.Flush()
					}
					return
				}

				conn, errUpgrade := upgrader.Upgrade(w, r, nil)
				if errUpgrade != nil {
					t.Errorf("upgrade websocket: %v", errUpgrade)
					return
				}
				defer func() {
					if errClose := conn.Close(); errClose != nil {
						t.Errorf("close websocket: %v", errClose)
					}
				}()
				if _, _, errRead := conn.ReadMessage(); errRead != nil {
					t.Errorf("read websocket request: %v", errRead)
					return
				}
				for _, event := range events {
					if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(event)); errWrite != nil {
						t.Errorf("write websocket event: %v", errWrite)
						return
					}
				}
			}

			paidServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paidAttempts.Add(1)
				events := []string{paidCreated, paidInProgress, tc.terminalFrame}
				if tc.afterOutput {
					events = []string{paidCreated, paidInProgress, paidItemAdded, paidTextDelta, tc.terminalFrame}
				} else if tc.afterTool {
					events = []string{paidCreated, paidInProgress, paidToolEvent, tc.terminalFrame}
				}
				writeEvents(w, r, events)
			}))
			defer paidServer.Close()

			subServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				subAttempts.Add(1)
				writeEvents(w, r, []string{subCreated, subItemAdded, subTextDelta, subCompleted})
			}))
			defer subServer.Close()

			cfg := &config.Config{}
			cfg.Codex.StreamBootstrapBuffering = true
			cfg.Codex.ModelLevelCooling = tc.modelLevelCooling

			manager := cliproxyauth.NewManager(nil, &cliproxyauth.RoundRobinSelector{}, nil)
			manager.SetRetryConfig(0, 0, 0)
			if tc.websocket {
				manager.RegisterExecutor(runtimeexecutor.NewCodexWebsocketsExecutor(cfg))
			} else {
				manager.RegisterExecutor(runtimeexecutor.NewCodexExecutor(cfg))
			}

			sanitized := strings.NewReplacer("/", "-").Replace(tc.name)
			paidID := "codex-billing-paid-" + sanitized
			subID := "codex-billing-sub-" + sanitized
			models := []*registry.ModelInfo{{ID: model}, {ID: siblingModel}}
			registry.GetGlobalRegistry().RegisterClient(paidID, "codex", models)
			registry.GetGlobalRegistry().RegisterClient(subID, "codex", models)
			t.Cleanup(func() {
				registry.GetGlobalRegistry().UnregisterClient(paidID)
				registry.GetGlobalRegistry().UnregisterClient(subID)
			})

			if _, errRegister := manager.Register(context.Background(), &cliproxyauth.Auth{
				ID:       paidID,
				Provider: "codex",
				Status:   cliproxyauth.StatusActive,
				Attributes: map[string]string{
					"priority": "10",
					"base_url": paidServer.URL,
					"api_key":  "sk-test-paid",
				},
				Metadata: map[string]any{
					"disable_cooling": false,
				},
			}); errRegister != nil {
				t.Fatalf("register paid auth: %v", errRegister)
			}

			if _, errRegister := manager.Register(context.Background(), &cliproxyauth.Auth{
				ID:       subID,
				Provider: "codex",
				Status:   cliproxyauth.StatusActive,
				Attributes: map[string]string{
					"priority": "1",
					"base_url": subServer.URL,
				},
				Metadata: map[string]any{
					"type":            "codex",
					"access_token":    "oauth-sub-access-token",
					"account_id":      "acct_sub_test",
					"disable_cooling": false,
				},
			}); errRegister != nil {
				t.Fatalf("register oauth auth: %v", errRegister)
			}

			sendResponses := func(reqModel string) *httptest.ResponseRecorder {
				t.Helper()
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses",
					strings.NewReader(fmt.Sprintf(`{"model":%q,"input":"hello","stream":true}`, reqModel)))
				c.Request.Header.Set("Content-Type", "application/json")

				base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
				openaihandlers.NewOpenAIResponsesAPIHandler(base).Responses(c)
				return recorder
			}

			first := sendResponses(model)
			firstBody := first.Body.String()

			if !tc.afterOutput && !tc.afterTool {
				if first.Code != http.StatusOK {
					t.Fatalf("first response status = %d, want 200; body=%s", first.Code, firstBody)
				}
				if strings.Contains(firstBody, "resp_paid") || strings.Contains(firstBody, "credit_balance_exhausted") {
					t.Fatalf("client body leaked exhausted credential stream instead of failing over: %s", firstBody)
				}
				if !strings.Contains(firstBody, "resp_sub") || !strings.Contains(firstBody, "hello from subscription") || !strings.Contains(firstBody, "response.completed") {
					t.Fatalf("client body missing healthy subscription stream: %s", firstBody)
				}
				if gotPaid, gotSub := paidAttempts.Load(), subAttempts.Load(); gotPaid != 1 || gotSub != 1 {
					t.Fatalf("first request attempts paid=%d sub=%d, want paid=1 sub=1", gotPaid, gotSub)
				}
			} else {
				if first.Code != http.StatusOK {
					t.Fatalf("committed-stream response status = %d, want 200; body=%s", first.Code, firstBody)
				}
				expectedMarker := "partial paid output"
				if tc.afterTool {
					expectedMarker = "web_search_call"
				}
				if !strings.Contains(firstBody, expectedMarker) || !strings.Contains(firstBody, "credit_balance_exhausted") {
					t.Fatalf("committed-stream body must deliver %s and in-stream billing error: %s", expectedMarker, firstBody)
				}
				if strings.Contains(firstBody, "resp_sub") {
					t.Fatalf("committed-stream must not replay on subscription credential: %s", firstBody)
				}
				if gotPaid, gotSub := paidAttempts.Load(), subAttempts.Load(); gotPaid != 1 || gotSub != 0 {
					t.Fatalf("committed-stream attempts paid=%d sub=%d, want paid=1 sub=0", gotPaid, gotSub)
				}
			}

			// Verify the exhausted API key cooled down at credential scope across sibling models.
			paidAuth, okPaid := manager.GetByID(paidID)
			if !okPaid || paidAuth == nil {
				t.Fatalf("paid auth %s missing from manager", paidID)
			}
			if !paidAuth.Quota.Exceeded || paidAuth.Quota.Reason != "credential_quota" {
				t.Fatalf("paid auth quota = %+v, want Exceeded=true Reason=credential_quota", paidAuth.Quota)
			}

			paidBeforeSibling := paidAttempts.Load()
			subBeforeSibling := subAttempts.Load()
			sibling := sendResponses(siblingModel)
			siblingBody := sibling.Body.String()
			if sibling.Code != http.StatusOK || !strings.Contains(siblingBody, "hello from subscription") {
				t.Fatalf("sibling model request failed: status=%d body=%s", sibling.Code, siblingBody)
			}
			if paidAttempts.Load() != paidBeforeSibling {
				t.Fatalf("exhausted paid key was retried for sibling model: paidAttempts=%d, want %d", paidAttempts.Load(), paidBeforeSibling)
			}
			if subAttempts.Load() != subBeforeSibling+1 {
				t.Fatalf("sibling model did not route to healthy subscription: subAttempts=%d, want %d", subAttempts.Load(), subBeforeSibling+1)
			}
		})
	}
}
