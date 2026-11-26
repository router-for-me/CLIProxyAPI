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

const (
	billingPaidCreated    = `{"type":"response.created","sequence_number":0,"response":{"id":"resp_paid","status":"in_progress","output":[]}}`
	billingPaidInProgress = `{"type":"response.in_progress","sequence_number":1,"response":{"id":"resp_paid","status":"in_progress","output":[]}}`
	billingPaidDelta      = `{"type":"response.output_text.delta","sequence_number":2,"item_id":"msg_paid","output_index":0,"content_index":0,"delta":"PAID_PARTIAL"}`
	billingPaidToolCall   = `{"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"id":"ws_paid","type":"web_search_call","status":"in_progress"}}`
	billingPaidError      = `{"type":"error","sequence_number":3,"error":{"type":"insufficient_quota","code":"credit_balance_exhausted","message":"Your credit balance is too low to access the API.","param":null}}`
)

var billingSubscriptionEvents = []string{
	`{"type":"response.created","response":{"id":"resp_subscription","status":"in_progress","output":[]}}`,
	`{"type":"response.output_text.delta","item_id":"msg_subscription","output_index":0,"content_index":0,"delta":"SUBSCRIPTION_OK"}`,
	`{"type":"response.completed","response":{"id":"resp_subscription","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
}

func billingUpstream(t *testing.T, events []string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var attempts atomic.Int32
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		if !websocket.IsWebSocketUpgrade(r) {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, event := range events {
				if _, errWrite := fmt.Fprintf(w, "data: %s\n\n", event); errWrite != nil {
					t.Errorf("write SSE: %v", errWrite)
					return
				}
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
	}))
	t.Cleanup(server.Close)
	return server, &attempts
}

// TestCodexResponsesBillingErrorFailsOverFromAPIKeyToOAuth drives one client request through
// /v1/responses and the real credential selector: a higher-priority OpenAI API key opens an
// HTTP 200 stream and rejects it with a billing error, and a lower-priority Codex OAuth
// credential serves the same model.
func TestCodexResponsesBillingErrorFailsOverFromAPIKeyToOAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, transport := range []string{"sse", "websocket"} {
		for _, tc := range []struct {
			name       string
			buffering  bool
			paidEvents []string
			failover   bool
		}{
			{name: "before output", buffering: true, paidEvents: []string{billingPaidCreated, billingPaidInProgress, billingPaidError}, failover: true},
			{name: "after text output", buffering: true, paidEvents: []string{billingPaidCreated, billingPaidInProgress, billingPaidDelta, billingPaidError}},
			{name: "after tool event", buffering: true, paidEvents: []string{billingPaidCreated, billingPaidInProgress, billingPaidToolCall, billingPaidError}},
			{name: "buffering disabled", paidEvents: []string{billingPaidCreated, billingPaidInProgress, billingPaidError}},
		} {
			t.Run(transport+"/"+tc.name, func(t *testing.T) {
				model := "gpt-billing-" + transport
				siblingModel := model + "-sibling"
				paid, paidAttempts := billingUpstream(t, tc.paidEvents)
				oauth, oauthAttempts := billingUpstream(t, billingSubscriptionEvents)

				cfg := &config.Config{}
				cfg.Codex.StreamBootstrapBuffering = tc.buffering
				manager := cliproxyauth.NewManager(nil, nil, nil)
				manager.SetConfig(cfg)
				manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
				paidID, oauthID := "billing-paid-"+t.Name(), "billing-oauth-"+t.Name()
				paidAttrs := map[string]string{"api_key": "sk-test-paid", "base_url": paid.URL, "priority": "10"}
				oauthAttrs := map[string]string{"base_url": oauth.URL, "priority": "1"}
				if transport == "websocket" {
					paidAttrs["websockets"] = "true"
				}
				for _, auth := range []*cliproxyauth.Auth{
					{ID: paidID, Provider: "codex", Status: cliproxyauth.StatusActive, Attributes: paidAttrs},
					{ID: oauthID, Provider: "codex", Status: cliproxyauth.StatusActive, Attributes: oauthAttrs,
						Metadata: map[string]any{"type": "codex", "access_token": "oauth-test-token"}},
				} {
					registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: model}, {ID: siblingModel}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
					if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
						t.Fatalf("register %s: %v", auth.ID, errRegister)
					}
				}
				router := gin.New()
				router.POST("/v1/responses", openaihandlers.NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)).Responses)
				send := func(requestModel string) (int, string) {
					recorder := httptest.NewRecorder()
					request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"`+requestModel+`","input":"hello","stream":true}`))
					request.Header.Set("Content-Type", "application/json")
					router.ServeHTTP(recorder, request)
					return recorder.Code, recorder.Body.String()
				}

				status, stream := send(model)
				if !tc.failover {
					if strings.Contains(stream, "SUBSCRIPTION_OK") || !strings.Contains(stream, "credit_balance_exhausted") {
						t.Fatalf("committed paid stream must keep its billing error and must not replay: status %d, body %s", status, stream)
					}
					if paidAttempts.Load() != 1 || oauthAttempts.Load() != 0 {
						t.Fatalf("attempts paid=%d oauth=%d, want paid=1 oauth=0", paidAttempts.Load(), oauthAttempts.Load())
					}
					return
				}
				if status != http.StatusOK || !strings.Contains(stream, "SUBSCRIPTION_OK") || !strings.Contains(stream, `"type":"response.completed"`) {
					t.Fatalf("request did not complete through the subscription: status %d, body %s", status, stream)
				}
				if strings.Contains(stream, "resp_paid") || strings.Contains(stream, "credit_balance_exhausted") {
					t.Fatalf("failed paid attempt leaked to the client: %s", stream)
				}
				if paidAttempts.Load() != 1 || oauthAttempts.Load() != 1 {
					t.Fatalf("attempts paid=%d oauth=%d, want paid=1 oauth=1", paidAttempts.Load(), oauthAttempts.Load())
				}
				paidAuth, _ := manager.GetByID(paidID)
				if paidAuth == nil || paidAuth.Quota.Reason != "credential_quota" {
					t.Fatalf("exhausted API key must cool down across models: %+v", paidAuth)
				}

				status, stream = send(siblingModel)
				if status != http.StatusOK || !strings.Contains(stream, "SUBSCRIPTION_OK") {
					t.Fatalf("sibling model did not use the subscription: status %d, body %s", status, stream)
				}
				if paidAttempts.Load() != 1 || oauthAttempts.Load() != 2 {
					t.Fatalf("sibling model attempts paid=%d oauth=%d, want paid=1 oauth=2", paidAttempts.Load(), oauthAttempts.Load())
				}
			})
		}
	}
}
