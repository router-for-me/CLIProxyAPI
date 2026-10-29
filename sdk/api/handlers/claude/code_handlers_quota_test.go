package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// The quota gate must reject before either execution path can call a provider.
type quotaNoCallExecutor struct {
	preFTKeepAliveClaudeExecutor
	t *testing.T
}

func (*quotaNoCallExecutor) Identifier() string { return "claude" }
func (e *quotaNoCallExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	e.t.Error("quota gate invoked model executor")
	return coreexecutor.Response{}, fmt.Errorf("unexpected execution")
}
func (e *quotaNoCallExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.t.Error("quota gate invoked streaming model executor")
	return nil, fmt.Errorf("unexpected execution")
}

func TestClaudeMessagesQuotaPreGenerationError(t *testing.T) {
	for _, exhausted := range []bool{false, true} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("exhausted=%v/stream=%v", exhausted, streaming), func(t *testing.T) {
				model := "claude-sonnet-4-6"
				a := &coreauth.Auth{ID: t.Name(), Provider: "claude", Status: coreauth.StatusActive, Metadata: map[string]any{"access_token": "synthetic"}, Attributes: map[string]string{coreauth.AttributeAuthKind: coreauth.AuthKindOAuth}}
				a.EnsureIndex()
				path := filepath.Join(t.TempDir(), "quota.json")
				now := time.Now().UTC()
				reset := now.Add(2 * time.Hour)
				if exhausted {
					raw, _ := json.Marshal(map[string]any{"observed_at": now, "accounts": map[string]any{a.ID: map[string]any{"observed_at": now, "auth_index": a.Index, "credential_fingerprint": coreauth.AccessTokenSHA256(a), "eligible": false, "usage": map[string]any{"five_hour": map[string]any{"utilization": 100, "resets_at": reset}}}}})
					if err := os.WriteFile(path, raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
				manager := coreauth.NewManager(nil, &coreauth.ClaudeQuotaSelector{SnapshotPath: path}, nil)
				manager.RegisterExecutor(&quotaNoCallExecutor{t: t})
				if _, err := manager.Register(context.Background(), a); err != nil {
					t.Fatal(err)
				}
				registry.GetGlobalRegistry().RegisterClient(a.ID, "claude", []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
				handler := NewClaudeCodeAPIHandler(handlers.NewBaseAPIHandlers(nil, manager))
				router := gin.New()
				router.POST("/v1/messages", handler.ClaudeMessages)
				req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":%v}`, model, streaming)))
				req.Header.Set("Content-Type", "application/json")
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, req)
				code, status := "claude_quota_unavailable", 503
				if exhausted {
					code, status = "claude_quota_exhausted", 429
				}
				var body struct {
					Type  string         `json:"type"`
					Error map[string]any `json:"error"`
				}
				_ = json.Unmarshal(recorder.Body.Bytes(), &body)
				if recorder.Code != status || body.Type != "error" || body.Error["code"] != code || body.Error["retryable"] != true || body.Error["provider"] != "claude" || body.Error["model"] != model {
					t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
				}
				seconds, err := strconv.Atoi(recorder.Header().Get("Retry-After"))
				if err != nil || seconds <= 0 {
					t.Fatalf("missing Retry-After: %v", recorder.Header())
				}
				if exhausted && (seconds < 7199 || seconds > 7200 || body.Error["reset_at"] != reset.Format(time.RFC3339Nano)) {
					t.Fatalf("bad reset: headers=%v body=%s", recorder.Header(), recorder.Body.String())
				}
				if !exhausted && (body.Error["next_check_at"] == nil || body.Error["reset_at"] != nil) {
					t.Fatalf("invented reset: %s", recorder.Body.String())
				}
			})
		}
	}
}
