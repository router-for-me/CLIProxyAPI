package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestClaudeQuotaAPIErrorContract(t *testing.T) {
	for _, exhausted := range []bool{false, true} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("exhausted=%v/stream=%v", exhausted, streaming), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "quota.json")
				code, status := "claude_quota_unavailable", 503
				if exhausted {
					code, status = "claude_quota_exhausted", 429
					raw := fmt.Sprintf(`{"observed_at":%q,"accounts":{"a":{"eligible":false}}}`, time.Now().UTC().Format(time.RFC3339Nano))
					if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
						t.Fatal(err)
					}
				}
				selector := &coreauth.ClaudeQuotaSelector{SnapshotPath: path}
				_, err := selector.Pick(context.Background(), "claude", "claude-sonnet-4-6", coreexecutor.Options{}, []*coreauth.Auth{{ID: "a", Provider: "claude", Attributes: map[string]string{coreauth.AttributeAuthKind: coreauth.AuthKindOAuth}}})
				msg := executionErrorMessage(fmt.Errorf("selection failed: %w", err))
				if streaming {
					errs := make(chan *interfaces.ErrorMessage, 1)
					errs <- msg
					close(errs)
					var ok bool
					msg, ok = PendingStreamError(errs)
					if !ok {
						t.Fatal("missing bootstrap error")
					}
				}
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				NewBaseAPIHandlers(nil, nil).WriteErrorResponse(c, msg)
				if recorder.Code != status || recorder.Header().Get("Retry-After") != "60" {
					t.Fatalf("status=%d headers=%v", recorder.Code, recorder.Header())
				}
				var body struct {
					Error map[string]any `json:"error"`
				}
				if json.Unmarshal(recorder.Body.Bytes(), &body) != nil {
					t.Fatal(recorder.Body.String())
				}
				if body.Error["code"] != code || body.Error["retryable"] != true || body.Error["provider"] != "claude" || body.Error["model"] != "claude-sonnet-4-6" || body.Error["next_check_at"] == nil || body.Error["reset_at"] != nil {
					t.Fatalf("lost metadata: %s", recorder.Body.String())
				}
			})
		}
	}
}
