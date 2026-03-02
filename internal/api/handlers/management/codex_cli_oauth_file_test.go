package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestDownloadCodexCLIOAuthFile_Success(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := &Handler{cfg: &config.Config{SDKConfig: config.SDKConfig{APIKeys: []string{"sk-test-1", "sk-test-2"}}}}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/codex-cli-oauth-file?index=1", nil)

	h.DownloadCodexCLIOAuthFile(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var payload codexCLIOAuthFile
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode payload: %v", err)
	}
	if payload.AuthMode != "apikey" {
		t.Fatalf("expected auth_mode apikey, got %q", payload.AuthMode)
	}
	if payload.OpenAIAPIKey != "sk-test-2" {
		t.Fatalf("expected OPENAI_API_KEY to be selected api key, got %q", payload.OpenAIAPIKey)
	}
	if payload.Tokens != nil {
		t.Fatalf("expected tokens to be omitted for proxy auth payload")
	}
}

func TestDownloadCodexCLIOAuthFile_Errors(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name       string
		handler    *Handler
		query      string
		statusCode int
	}{
		{name: "missing config", handler: &Handler{}, query: "", statusCode: http.StatusServiceUnavailable},
		{name: "empty api keys", handler: &Handler{cfg: &config.Config{}}, query: "", statusCode: http.StatusBadRequest},
		{name: "invalid index", handler: &Handler{cfg: &config.Config{SDKConfig: config.SDKConfig{APIKeys: []string{"sk-test-1"}}}}, query: "index=abc", statusCode: http.StatusBadRequest},
		{name: "index out of range", handler: &Handler{cfg: &config.Config{SDKConfig: config.SDKConfig{APIKeys: []string{"sk-test-1"}}}}, query: "index=9", statusCode: http.StatusBadRequest},
	}

	for _, tc := range cases {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		url := "/v0/management/codex-cli-oauth-file"
		if tc.query != "" {
			url += "?" + tc.query
		}
		c.Request = httptest.NewRequest(http.MethodGet, url, nil)
		tc.handler.DownloadCodexCLIOAuthFile(c)
		if rec.Code != tc.statusCode {
			t.Fatalf("%s: expected %d, got %d body=%s", tc.name, tc.statusCode, rec.Code, rec.Body.String())
		}
	}
}
