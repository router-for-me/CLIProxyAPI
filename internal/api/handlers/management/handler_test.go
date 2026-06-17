package management

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestAuthenticateManagementKey_LocalhostIPBan_BlocksCorrectKeyDuringBan(t *testing.T) {
	h := &Handler{
		cfg:            &config.Config{},
		failedAttempts: make(map[string]*attemptInfo),
		envSecret:      "test-secret",
	}

	for i := 0; i < 5; i++ {
		allowed, statusCode, errMsg := h.AuthenticateManagementKey("127.0.0.1", true, "wrong-secret")
		if allowed {
			t.Fatalf("expected auth to be denied at attempt %d", i+1)
		}
		if statusCode != http.StatusUnauthorized || errMsg != "invalid management key" {
			t.Fatalf("unexpected auth failure at attempt %d: status=%d msg=%q", i+1, statusCode, errMsg)
		}
	}

	allowed, statusCode, errMsg := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret")
	if allowed {
		t.Fatalf("expected correct key to be denied while banned")
	}
	if statusCode != http.StatusForbidden {
		t.Fatalf("expected forbidden status while banned, got %d", statusCode)
	}
	if !strings.HasPrefix(errMsg, "IP banned due to too many failed attempts. Try again in") {
		t.Fatalf("unexpected banned message: %q", errMsg)
	}
}

func TestMiddlewareSetsXCPASupportPluginHeader(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{
			RemoteManagement: config.RemoteManagement{AllowRemote: true},
		},
		failedAttempts: make(map[string]*attemptInfo),
		envSecret:       "test-management-key",
	}

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, "/v0/management/plugins", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer test-management-key")
	c.Request = req

	h.Middleware()(c)

	if got := rec.Result().Header.Get("X-CPA-SUPPORT-PLUGIN"); got != "true" {
		t.Fatalf("X-CPA-SUPPORT-PLUGIN header = %q, want %q", got, "true")
	}
	if rec.Result().StatusCode != http.StatusOK {
		t.Fatalf("expected allowed status = %d, got %d", http.StatusOK, rec.Code)
	}
}
