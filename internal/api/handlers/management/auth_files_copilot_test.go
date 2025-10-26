package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestCopilotAuthURL_ReturnsURL(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	cfg := &config.Config{Port: 53555, AuthDir: tmpDir}
	cfg.Copilot.AuthURL = "https://auth.copilot.example.com/oauth/authorize"
	cfg.Copilot.TokenURL = "https://auth.copilot.example.com/oauth/token"
	cfg.Copilot.ClientID = "test-client"
	cfg.Copilot.RedirectPort = 54556
	cfg.Copilot.Scope = "openid email profile offline_access"
	h := NewHandler(cfg, filepath.Join(tmpDir, "config.yaml"), nil)

	r := gin.New()
	r.GET("/v0/management/copilot-auth-url", h.RequestCopilotToken)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/copilot-auth-url", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}
	var body struct {
		Status string `json:"status"`
		URL    string `json:"url"`
		State  string `json:"state"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if body.Status != "ok" {
		t.Fatalf("expected status=ok, got %s", body.Status)
	}
	if body.URL == "" || body.State == "" {
		t.Fatalf("expected non-empty url and state")
	}
}
