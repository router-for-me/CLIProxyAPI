package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestSaveTokenRecordAppliesProxyURLFromOAuthRequest(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	store := &memoryAuthStore{}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	h.tokenStore = store

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/codex-auth-url?proxy_url=socks5://proxy.local:1080", nil)

	record := &coreauth.Auth{
		ID:       "codex-test.json",
		FileName: "codex-test.json",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex"},
	}
	if _, err := h.saveTokenRecord(PopulateAuthContext(context.Background(), ctx), record); err != nil {
		t.Fatalf("saveTokenRecord() error = %v", err)
	}

	saved := store.items["codex-test.json"]
	if saved == nil {
		t.Fatalf("expected saved auth record")
	}
	if got := saved.ProxyURL; got != "socks5://proxy.local:1080" {
		t.Fatalf("ProxyURL = %q, want %q", got, "socks5://proxy.local:1080")
	}
	if got, _ := saved.Metadata["proxy_url"].(string); got != "socks5://proxy.local:1080" {
		t.Fatalf("metadata.proxy_url = %q, want %q", got, "socks5://proxy.local:1080")
	}
}
