package test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestCodexTerminalQuotaCoolsAccountAcrossModels(t *testing.T) {
	for _, transport := range []string{"sse", "websocket-error", "websocket-failed"} {
		t.Run(transport, func(t *testing.T) {
			const model, siblingModel = "gpt-5.4", "gpt-5.4-mini"
			const created = `{"type":"response.created","response":{"id":"quota-test-response"}}`
			const quota = `{"type":"usage_limit_reached","message":"You've hit your usage limit.","resets_in_seconds":3600}`
			const completed = `{"type":"response.completed","response":{"id":"quota-test-success","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`
			attempts := make(chan string, 8)
			upgrader := websocket.Upgrader{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				account := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				attempts <- account
				terminal := completed
				if account == "quota-high" {
					terminal = `{"type":"error","status":429,"error":` + quota + `}`
					if transport == "websocket-failed" {
						terminal = `{"type":"response.failed","response":{"error":` + quota + `}}`
					}
				}
				if transport == "sse" {
					w.Header().Set("Content-Type", "text/event-stream")
					if _, errWrite := fmt.Fprintf(w, "data: %s\n\ndata: %s\n\n", created, terminal); errWrite != nil {
						t.Errorf("write SSE: %v", errWrite)
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
				for _, event := range []string{created, terminal} {
					if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(event)); errWrite != nil {
						t.Errorf("write websocket event: %v", errWrite)
						return
					}
				}
			}))
			defer server.Close()
			manager := cliproxyauth.NewManager(nil, &cliproxyauth.RoundRobinSelector{}, nil)
			manager.SetRetryConfig(0, 0, 0)
			cfg := &config.Config{}
			if transport == "sse" {
				manager.RegisterExecutor(runtimeexecutor.NewCodexExecutor(cfg))
			} else {
				manager.RegisterExecutor(runtimeexecutor.NewCodexWebsocketsExecutor(cfg))
			}
			highID, lowID := "quota-high-"+transport, "quota-low-"+transport
			for i, id := range []string{highID, lowID} {
				key := "quota-high"
				if i == 1 {
					key = "quota-low"
				}
				registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model}, {ID: siblingModel}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
				if _, errRegister := manager.Register(context.Background(), &cliproxyauth.Auth{
					ID: id, Provider: "codex", Status: cliproxyauth.StatusActive,
					Attributes: map[string]string{"priority": fmt.Sprint(4 - i), "base_url": server.URL, "api_key": key},
					Metadata:   map[string]any{"disable_cooling": false},
				}); errRegister != nil {
					t.Fatal(errRegister)
				}
			}
			run := func(requestModel string) ([]byte, error) {
				t.Helper()
				result, errStream := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
					Model: requestModel, Payload: []byte(fmt.Sprintf(`{"model":%q,"input":"hello"}`, requestModel)),
				}, cliproxyexecutor.Options{Stream: true, SourceFormat: sdktranslator.FromString("openai-response")})
				if errStream != nil {
					t.Fatalf("stream must start before the terminal quota error: %v", errStream)
				}
				var payload []byte
				var terminalErr error
				for chunk := range result.Chunks {
					payload = append(payload, chunk.Payload...)
					if chunk.Err != nil {
						terminalErr = chunk.Err
					}
				}
				return payload, terminalErr
			}
			before := time.Now()
			payload, quotaErr := run(model)
			if !strings.Contains(string(payload), "response.created") || quotaErr == nil {
				t.Fatalf("expected payload then terminal quota error: payload=%s error=%v", payload, quotaErr)
			}
			var scoped interface{ IsCredentialScoped() bool }
			if !errors.As(quotaErr, &scoped) || !scoped.IsCredentialScoped() {
				t.Errorf("real Codex error is missing credential scope: %T %v", quotaErr, quotaErr)
			}
			high, _ := manager.GetByID(highID)
			if high.Quota.Reason != "credential_quota" || high.Quota.NextRecoverAt.Before(before.Add(time.Hour)) {
				t.Errorf("account cooldown missing scope or upstream reset: %+v", high.Quota)
			}
			payload, errSibling := run(siblingModel)
			if errSibling != nil || !strings.Contains(string(payload), "response.completed") {
				t.Errorf("sibling model did not fail over to the healthy account: payload=%s error=%v", payload, errSibling)
			}
			for _, want := range []string{"quota-high", "quota-low"} {
				select {
				case got := <-attempts:
					if got != want {
						t.Errorf("upstream account = %s, want %s", got, want)
					}
				default:
					t.Fatalf("missing upstream attempt for %s", want)
				}
			}
			if len(attempts) != 0 {
				t.Errorf("unexpected extra upstream attempts: %d", len(attempts))
			}
		})
	}
}
