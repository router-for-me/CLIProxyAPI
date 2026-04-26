package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestDeepSeekKeysManagementCRUD(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{cfg: &config.Config{}, configFilePath: writeTestConfigFile(t)}

	putBody := []config.DeepSeekKey{{
		APIKey:   " ds-key ",
		Priority: 7,
		Prefix:   " team ",
		Models:   []config.DeepSeekModel{{Name: " deepseek-v4-pro ", Alias: " latest "}},
	}}
	putPayload, err := json.Marshal(putBody)
	if err != nil {
		t.Fatalf("marshal put body: %v", err)
	}
	putResp := performDeepSeekManagementRequest(http.MethodPut, "/v0/management/deepseek-api-key", putPayload, h.PutDeepSeekKeys)
	if putResp.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body=%s", putResp.Code, putResp.Body.String())
	}
	if len(h.cfg.DeepSeekKey) != 1 {
		t.Fatalf("DeepSeekKey length = %d, want 1", len(h.cfg.DeepSeekKey))
	}
	entry := h.cfg.DeepSeekKey[0]
	if entry.APIKey != "ds-key" {
		t.Fatalf("api key = %q", entry.APIKey)
	}
	if entry.BaseURL != config.DefaultDeepSeekBaseURL {
		t.Fatalf("base url = %q, want default", entry.BaseURL)
	}
	if entry.Prefix != "team" {
		t.Fatalf("prefix = %q", entry.Prefix)
	}
	if len(entry.Models) != 1 || entry.Models[0].Name != "deepseek-v4-pro" || entry.Models[0].Alias != "latest" {
		t.Fatalf("models not normalized: %#v", entry.Models)
	}

	patchPayload := []byte(`{"index":0,"value":{"priority":9,"base-url":"https://api.deepseek.com","models":[{"name":"deepseek-v4-flash","alias":"fast"}],"headers":{"X-Test":"ok"}}}`)
	patchResp := performDeepSeekManagementRequest(http.MethodPatch, "/v0/management/deepseek-api-key", patchPayload, h.PatchDeepSeekKey)
	if patchResp.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, body=%s", patchResp.Code, patchResp.Body.String())
	}
	entry = h.cfg.DeepSeekKey[0]
	if entry.Priority != 9 {
		t.Fatalf("priority = %d, want 9", entry.Priority)
	}
	if len(entry.Models) != 1 || entry.Models[0].Name != "deepseek-v4-flash" || entry.Models[0].Alias != "fast" {
		t.Fatalf("models after patch = %#v", entry.Models)
	}
	if entry.Headers["X-Test"] != "ok" {
		t.Fatalf("headers after patch = %#v", entry.Headers)
	}

	getResp := performDeepSeekManagementRequest(http.MethodGet, "/v0/management/deepseek-api-key", nil, h.GetDeepSeekKeys)
	if getResp.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body=%s", getResp.Code, getResp.Body.String())
	}
	var got map[string][]map[string]any
	if err := json.Unmarshal(getResp.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal GET body: %v", err)
	}
	if len(got["deepseek-api-key"]) != 1 {
		t.Fatalf("GET returned %#v", got)
	}

	deleteResp := performDeepSeekManagementRequest(http.MethodDelete, "/v0/management/deepseek-api-key?api-key=ds-key&base-url=https://api.deepseek.com", nil, h.DeleteDeepSeekKey)
	if deleteResp.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d, body=%s", deleteResp.Code, deleteResp.Body.String())
	}
	if len(h.cfg.DeepSeekKey) != 0 {
		t.Fatalf("DeepSeekKey length after delete = %d, want 0", len(h.cfg.DeepSeekKey))
	}
}

func performDeepSeekManagementRequest(method, target string, body []byte, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader(body)
	}
	c.Request = httptest.NewRequest(method, target, reader)
	c.Request.Header.Set("Content-Type", "application/json")
	handler(c)
	return w
}
