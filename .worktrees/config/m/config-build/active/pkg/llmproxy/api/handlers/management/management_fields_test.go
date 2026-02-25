package management

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config"
)

func setupTestHandler(cfg *config.Config) (*Handler, string, func()) {
	tmpFile, _ := os.CreateTemp("", "config*.yaml")
	_, _ = tmpFile.Write([]byte("{}"))
	_ = tmpFile.Close()

	h := &Handler{cfg: cfg, configFilePath: tmpFile.Name()}
	cleanup := func() {
		_ = os.Remove(tmpFile.Name())
	}
	return h, tmpFile.Name(), cleanup
}

func TestBoolFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	h, _, cleanup := setupTestHandler(cfg)
	defer cleanup()

	tests := []struct {
		name   string
		getter func(*gin.Context)
		setter func(*gin.Context)
		field  *bool
		key    string
	}{
		{"UsageStatisticsEnabled", h.GetUsageStatisticsEnabled, h.PutUsageStatisticsEnabled, &cfg.UsageStatisticsEnabled, "usage-statistics-enabled"},
		{"LoggingToFile", h.GetLoggingToFile, h.PutLoggingToFile, &cfg.LoggingToFile, "logging-to-file"},
		{"WebsocketAuth", h.GetWebsocketAuth, h.PutWebsocketAuth, &cfg.WebsocketAuth, "ws-auth"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Test Getter
			*tc.field = true
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			tc.getter(c)
			if w.Code != 200 {
				t.Errorf("getter failed: %d", w.Code)
			}

			// Test Setter
			*tc.field = false
			w = httptest.NewRecorder()
			c, _ = gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("PUT", "/", strings.NewReader(`{"value": true}`))
			tc.setter(c)
			if w.Code != 200 {
				t.Errorf("setter failed: %d, body: %s", w.Code, w.Body.String())
			}
			if !*tc.field {
				t.Errorf("field not updated")
			}
		})
	}
}

func TestIntFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	h, _, cleanup := setupTestHandler(cfg)
	defer cleanup()

	tests := []struct {
		name   string
		getter func(*gin.Context)
		setter func(*gin.Context)
		field  *int
		key    string
	}{
		{"LogsMaxTotalSizeMB", h.GetLogsMaxTotalSizeMB, h.PutLogsMaxTotalSizeMB, &cfg.LogsMaxTotalSizeMB, "logs-max-total-size-mb"},
		{"ErrorLogsMaxFiles", h.GetErrorLogsMaxFiles, h.PutErrorLogsMaxFiles, &cfg.ErrorLogsMaxFiles, "error-logs-max-files"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			*tc.field = 100
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			tc.getter(c)
			if w.Code != 200 {
				t.Errorf("getter failed: %d", w.Code)
			}

			w = httptest.NewRecorder()
			c, _ = gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("PUT", "/", strings.NewReader(`{"value": 200}`))
			tc.setter(c)
			if w.Code != 200 {
				t.Errorf("setter failed: %d", w.Code)
			}
			if *tc.field != 200 {
				t.Errorf("field not updated")
			}
		})
	}
}

func TestProxyURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	h, _, cleanup := setupTestHandler(cfg)
	defer cleanup()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("PUT", "/", strings.NewReader(`{"value": "http://proxy:8080"}`))
	h.PutProxyURL(c)
	if cfg.ProxyURL != "http://proxy:8080" {
		t.Errorf("proxy url not updated")
	}

	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	h.GetProxyURL(c)
	if w.Code != 200 {
		t.Errorf("getter failed: %d", w.Code)
	}

	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	h.DeleteProxyURL(c)
	if cfg.ProxyURL != "" {
		t.Errorf("proxy url not deleted")
	}
}

func TestQuotaExceededFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	h, _, cleanup := setupTestHandler(cfg)
	defer cleanup()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("PUT", "/", strings.NewReader(`{"value": true}`))
	h.PutSwitchProject(c)
	if !cfg.QuotaExceeded.SwitchProject {
		t.Errorf("SwitchProject not updated")
	}

	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("PUT", "/", strings.NewReader(`{"value": true}`))
	h.PutSwitchPreviewModel(c)
	if !cfg.QuotaExceeded.SwitchPreviewModel {
		t.Errorf("SwitchPreviewModel not updated")
	}
}

func TestAPIKeys(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{SDKConfig: config.SDKConfig{APIKeys: []string{"key1"}}}
	h, _, cleanup := setupTestHandler(cfg)
	defer cleanup()

	// GET
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	h.GetAPIKeys(c)
	if w.Code != 200 {
		t.Errorf("GET failed")
	}

	// PUT
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("PUT", "/", strings.NewReader(`["key2"]`))
	h.PutAPIKeys(c)
	if len(cfg.APIKeys) != 1 || cfg.APIKeys[0] != "key2" {
		t.Errorf("PUT failed: %v", cfg.APIKeys)
	}

	// PATCH
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("PATCH", "/", strings.NewReader(`{"old":"key2", "new":"key3"}`))
	h.PatchAPIKeys(c)
	if cfg.APIKeys[0] != "key3" {
		t.Errorf("PATCH failed: %v", cfg.APIKeys)
	}

	// DELETE
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("DELETE", "/?value=key3", nil)
	h.DeleteAPIKeys(c)
	if len(cfg.APIKeys) != 0 {
		t.Errorf("DELETE failed: %v", cfg.APIKeys)
	}
}
