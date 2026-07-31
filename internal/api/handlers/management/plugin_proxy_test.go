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
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestGetPluginProxy(t *testing.T) {
	gin.SetMode(gin.TestMode)

	configPath := writePluginProxyTestConfig(t, "proxy-url: http://system:1\nplugin-proxy:\n  url: socks5://custom:1080\n  status: 2\n")
	cfg, errLoad := config.LoadConfig(configPath)
	if errLoad != nil {
		t.Fatalf("LoadConfig: %v", errLoad)
	}

	h := &Handler{cfg: cfg, configFilePath: configPath}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/plugin-proxy", nil)
	h.GetPluginProxy(c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var payload map[string]any
	if errDecode := json.Unmarshal(recorder.Body.Bytes(), &payload); errDecode != nil {
		t.Fatalf("decode: %v", errDecode)
	}
	if payload["effective"] != "http://system:1" {
		t.Fatalf("effective = %#v", payload["effective"])
	}
	if payload["accelerator"] != "" {
		t.Fatalf("accelerator = %#v", payload["accelerator"])
	}
}

func TestPutPluginProxyCustomRetainsValueAcrossModeChanges(t *testing.T) {
	gin.SetMode(gin.TestMode)

	configPath := writePluginProxyTestConfig(t, "proxy-url: http://system:1\n")
	cfg, errLoad := config.LoadConfig(configPath)
	if errLoad != nil {
		t.Fatalf("LoadConfig: %v", errLoad)
	}
	h := &Handler{cfg: cfg, configFilePath: configPath}

	putPluginProxy(t, h, `{"value":{"status":1,"url":"socks5://user:pass@127.0.0.1:1080"}}`, http.StatusOK)
	if got := config.EffectivePluginStoreProxyURL(h.cfg); got != "socks5://user:pass@127.0.0.1:1080" {
		t.Fatalf("effective custom proxy = %q", got)
	}

	putPluginProxy(t, h, `{"status":0}`, http.StatusOK)
	if h.cfg.PluginProxy.URL != "socks5://user:pass@127.0.0.1:1080" {
		t.Fatalf("custom URL was not retained: %q", h.cfg.PluginProxy.URL)
	}
	if got := config.EffectivePluginStoreProxyURL(h.cfg); got != "http://system:1" {
		t.Fatalf("effective none/fallback proxy = %q, expected global proxy-url", got)
	}

	putPluginProxy(t, h, `{"status":-1}`, http.StatusOK)
	if got := config.EffectivePluginStoreProxyURL(h.cfg); got != "" {
		t.Fatalf("effective direct proxy = %q, expected empty (direct)", got)
	}

	putPluginProxy(t, h, `{"status":2}`, http.StatusOK)
	if got := config.EffectivePluginStoreProxyURL(h.cfg); got != "http://system:1" {
		t.Fatalf("effective system proxy = %q", got)
	}
}

func TestPutPluginProxyAccelerator(t *testing.T) {
	gin.SetMode(gin.TestMode)

	configPath := writePluginProxyTestConfig(t, "proxy-url: http://system:1\n")
	cfg, errLoad := config.LoadConfig(configPath)
	if errLoad != nil {
		t.Fatalf("LoadConfig: %v", errLoad)
	}
	h := &Handler{cfg: cfg, configFilePath: configPath}

	putPluginProxy(t, h, `{"value":{"status":3,"accelerator":"https://gh-proxy.com"}}`, http.StatusOK)
	if got := config.EffectivePluginStoreProxyURL(h.cfg); got != "" {
		t.Fatalf("accelerator mode proxy = %q", got)
	}
	if got := config.EffectivePluginStoreAcceleratorBase(h.cfg); got != "https://gh-proxy.com/" {
		t.Fatalf("accelerator base = %q", got)
	}
}

func TestNewPluginStoreClientBypassesEnvironmentProxy(t *testing.T) {
	h := &Handler{}
	for _, status := range []int{config.PluginProxyStatusDirect, config.PluginProxyStatusAccelerator} {
		client := h.newPluginStoreClient("http://environment-proxy:8080", "", "", status, nil)
		httpClient, okHTTPClient := client.HTTPClient.(*http.Client)
		if !okHTTPClient {
			t.Fatalf("status %d HTTPClient = %T, want *http.Client", status, client.HTTPClient)
		}
		transport, okTransport := httpClient.Transport.(*http.Transport)
		if !okTransport {
			t.Fatalf("status %d transport = %T, want *http.Transport", status, httpClient.Transport)
		}
		if transport.Proxy != nil {
			t.Fatalf("status %d transport uses proxy; want direct transport", status)
		}
	}
}

func TestValidatePluginProxyURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{}

	validatePluginProxy(t, h, `{"url":"ftp://invalid"}`, http.StatusBadRequest)
	validatePluginProxy(t, h, `{"url":"https://proxy.example:8443"}`, http.StatusOK)
	validatePluginProxy(t, h, `{"status":3,"accelerator":"socks5://invalid"}`, http.StatusBadRequest)
	validatePluginProxy(t, h, `{"status":3,"accelerator":"https://gh-proxy.com"}`, http.StatusOK)
}

func writePluginProxyTestConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(path, []byte(content), 0o600); errWrite != nil {
		t.Fatalf("write config: %v", errWrite)
	}
	return path
}

func putPluginProxy(t *testing.T, h *Handler, body string, wantStatus int) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/plugin-proxy", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h.PutPluginProxy(c)
	if recorder.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, wantStatus, recorder.Body.String())
	}
}

func validatePluginProxy(t *testing.T, h *Handler, body string, wantStatus int) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/plugin-proxy/validate", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h.ValidatePluginProxyURL(c)
	if recorder.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, wantStatus, recorder.Body.String())
	}
}
