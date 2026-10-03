package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestCopilotQuotaValidatesAccountBeforeRequest(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auth, err := manager.Register(context.Background(), &coreauth.Auth{ID: "other-provider", Provider: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	auth.EnsureIndex()
	h := &Handler{authManager: manager}
	for _, tc := range []struct {
		query  string
		status int
	}{{"", 400}, {"?auth_index=missing", 404}, {"?auth_index=" + auth.Index, 400}} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/github-copilot-quota"+tc.query, nil)
		h.GetCopilotQuota(c)
		if w.Code != tc.status {
			t.Errorf("query %s: status=%d body=%s", tc.query, w.Code, w.Body)
		}
	}
}

func TestCopilotV8LoginDispatch(t *testing.T) {
	recorder := httptest.NewRecorder()
	requestContext, cancel := context.WithCancel(context.Background())
	cancel()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v8/management/oauth/auth-url?provider=github-copilot", nil).WithContext(requestContext)
	(&Handler{}).StartOAuthV8(c)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusBadGateway, recorder.Body.String())
	}
}
