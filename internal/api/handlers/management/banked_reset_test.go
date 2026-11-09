package management

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestBankedResetManagementUsesIndexAndDisabledMode(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	a := &coreauth.Auth{ID: "banked-id", Provider: "codex", Status: coreauth.StatusActive, Metadata: map[string]any{"access_token": "test-only"}}
	index := a.EnsureIndex()
	if _, err := manager.Register(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"auth_index":"` + index + `"}`, 403},
		{`{"auth_index":"banked-id"}`, 404},
		{`{}`, 400},
	} {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest("POST", "/v8/management/credentials/banked-resets/redeem", strings.NewReader(tc.body))
		ctx.Request.Header.Set("Content-Type", "application/json")
		h.RedeemBankedReset(ctx)
		if rec.Code != tc.want {
			t.Fatalf("status=%d want %d body=%s", rec.Code, tc.want, rec.Body.String())
		}
	}
}

func TestBankedResetAPICallLeavesDeprecatedV0Untouched(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	cfg.QuotaExceeded.BankedReset.Mode = "manual"
	manager.SetConfig(cfg)
	a := &coreauth.Auth{ID: "api-call-banked", Provider: "codex", Status: coreauth.StatusActive, Metadata: map[string]any{"access_token": "fixture-only"}}
	if _, err := manager.Register(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	target, _ := url.Parse("https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume")
	for _, v8 := range []bool{false, true} {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest("POST", "/requests/api-call", nil)
		ctx.Set(ConfigV8ContextKey, v8)
		handled := h.handleBankedResetAPICall(ctx, a, "POST", target, `{}`)
		if handled != v8 {
			t.Fatalf("v8=%v handled=%v", v8, handled)
		}
	}
}
