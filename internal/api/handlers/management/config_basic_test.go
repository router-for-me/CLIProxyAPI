package management

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestPutMaxInvalidRequestRetries_ClampsNegativeValue(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("max-invalid-request-retries: 5\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg := &config.Config{MaxInvalidRequestRetries: 5}
	h := NewHandler(cfg, configPath, nil)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPut, "/v0/management/config/max-invalid-request-retries", bytes.NewBufferString(`{"value":-7}`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	h.PutMaxInvalidRequestRetries(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if cfg.MaxInvalidRequestRetries != 0 {
		t.Fatalf("cfg.MaxInvalidRequestRetries = %d, want 0", cfg.MaxInvalidRequestRetries)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(data), "max-invalid-request-retries: 0") {
		t.Fatalf("persisted config did not clamp value, got:\n%s", string(data))
	}
}
