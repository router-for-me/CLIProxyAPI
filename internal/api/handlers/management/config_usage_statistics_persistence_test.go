package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestGetUsageStatisticsPersistenceEnabled(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage-statistics-persistence-enabled", nil)

	h := &Handler{cfg: &config.Config{UsageStatisticsPersistenceEnabled: true}}
	h.GetUsageStatisticsPersistenceEnabled(c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	var body map[string]bool
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v", err)
	}
	if !body["usage-statistics-persistence-enabled"] {
		t.Fatal("usage-statistics-persistence-enabled = false, want true")
	}
}

func TestPutUsageStatisticsPersistenceEnabled(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("usage-statistics-persistence-enabled: true\n"), 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	body := bytes.NewBufferString(`{"value":false}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/usage-statistics-persistence-enabled", body)
	c.Request.Header.Set("Content-Type", "application/json")

	h := NewHandler(&config.Config{UsageStatisticsPersistenceEnabled: true}, configPath, nil)
	h.PutUsageStatisticsPersistenceEnabled(c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if h.cfg.UsageStatisticsPersistenceEnabled {
		t.Fatal("UsageStatisticsPersistenceEnabled = true, want false")
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("os.ReadFile returned error: %v", err)
	}
	if !bytes.Contains(data, []byte("usage-statistics-persistence-enabled: false")) {
		t.Fatalf("config file missing updated persistence setting: %s", string(data))
	}
}
