package management

import (
	"context"
	"net/http/httptest"
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
