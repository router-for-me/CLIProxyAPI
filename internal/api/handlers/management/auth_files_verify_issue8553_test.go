package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestVerifyAuthFileV8_ReleasesCredentialQuotaCooldown_Issue8553(t *testing.T) {
	gin.SetMode(gin.TestMode)

	exec := &failingRefreshManagementExecutor{
		provider: "claude",
		streamPayloads: [][]byte{
			[]byte(`data: {"type":"response.completed"}` + "\n\n"),
		},
	}
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(exec)

	ctx := coreauth.WithSkipPersist(context.Background())
	auth := &coreauth.Auth{
		ID:       "claude:apikey:issue8553-v8",
		Provider: "claude",
		Status:   coreauth.StatusActive,
	}
	if _, errReg := manager.Register(ctx, auth); errReg != nil {
		t.Fatalf("Register: %v", errReg)
	}

	retry := 2 * time.Hour
	manager.MarkResult(ctx, coreauth.Result{
		AuthID:          auth.ID,
		Provider:        "claude",
		Model:           "claude-test",
		Success:         false,
		Error:           &coreauth.Error{Code: "rate_limit_error", Message: "rate limited", HTTPStatus: http.StatusTooManyRequests},
		RetryAfter:      &retry,
		CredentialScope: true,
	})

	h := NewHandler(&internalconfig.Config{}, "", manager)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(ConfigV8ContextKey, true)
		c.Next()
	})
	router.POST("/v8/management/credentials/verify", h.VerifyAuthFileV8)

	req := httptest.NewRequest(http.MethodPost, "/v8/management/credentials/verify", strings.NewReader(`{"name":"claude:apikey:issue8553-v8","model":"claude-test"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /v8/management/credentials/verify status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &resp); errUnmarshal != nil {
		t.Fatalf("unmarshal response: %v", errUnmarshal)
	}
	if resp["status"] != "ok" || resp["verified"] != true {
		t.Fatalf("unexpected verify response: %#v", resp)
	}

	after, _ := manager.GetByID(auth.ID)
	if after == nil || !after.InferenceVerified || after.Unavailable || after.Quota.Exceeded || after.Quota.Reason == "credential_quota" {
		t.Fatalf("expected credential_quota cleared and InferenceVerified=true after HTTP verify, got %+v", after)
	}
}
