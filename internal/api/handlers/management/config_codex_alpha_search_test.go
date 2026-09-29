package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestPatchCodexKeySupportConfigurationUpdate(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{CodexKey: []config.CodexKey{{
			APIKey: "codex-key", BaseURL: "https://codex.example.com",
		}}},
		configFilePath: writeTestConfigFile(t),
	}

	for _, testCase := range []struct {
		name, method, body string
		enabled            string
	}{
		{
			name: "PATCH", method: http.MethodPatch,
			body:    `{"index":0,"value":{"models":[{"name":"custom-one","support-configuration-update":true},{"name":"custom-two"}]}}`,
			enabled: "custom-one",
		},
		{
			name: "PUT", method: http.MethodPut,
			body:    `[{"api-key":"codex-key","base-url":"https://codex.example.com","models":[{"name":"custom-two","support-configuration-update":true},{"name":"custom-one"}]}]`,
			enabled: "custom-two",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			ctx.Request = httptest.NewRequest(testCase.method, "/v0/management/codex-api-key", strings.NewReader(testCase.body))
			ctx.Request.Header.Set("Content-Type", "application/json")
			if testCase.method == http.MethodPatch {
				h.PatchCodexKey(ctx)
			} else {
				h.PutCodexKeys(ctx)
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("%s status = %d, want 200; body=%s", testCase.method, rec.Code, rec.Body.String())
			}
			if len(h.cfg.CodexKey) != 1 || len(h.cfg.CodexKey[0].Models) != 2 {
				t.Fatalf("unexpected models in config: %+v", h.cfg.CodexKey)
			}
			configModels := h.cfg.CodexKey[0].Models
			if configModels[0].Name != testCase.enabled || !configModels[0].SupportConfigurationUpdate || configModels[1].SupportConfigurationUpdate {
				t.Fatalf("unexpected config models: %+v", configModels)
			}

			getRec := httptest.NewRecorder()
			getCtx, _ := gin.CreateTestContext(getRec)
			getCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/codex-api-key", nil)
			h.GetCodexKeys(getCtx)
			if getRec.Code != http.StatusOK {
				t.Fatalf("GET status = %d, want 200; body=%s", getRec.Code, getRec.Body.String())
			}
			var result struct {
				CodexKeys []struct {
					Models []map[string]any `json:"models"`
				} `json:"codex-api-key"`
			}
			if errDecode := json.Unmarshal(getRec.Body.Bytes(), &result); errDecode != nil {
				t.Fatalf("decode GET response: %v", errDecode)
			}
			if len(result.CodexKeys) != 1 || len(result.CodexKeys[0].Models) != 2 {
				t.Fatalf("unexpected GET models: %s", getRec.Body.String())
			}
			models := result.CodexKeys[0].Models
			if models[0]["name"] != testCase.enabled || models[0]["support-configuration-update"] != true || models[1]["support-configuration-update"] == true {
				t.Fatalf("unexpected GET flags: %s", getRec.Body.String())
			}
		})
	}
}

func TestPatchCodexKeyUpdatesAlphaSearch(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{CodexKey: []config.CodexKey{{
			APIKey:  "codex-key",
			BaseURL: "https://codex.example.com",
		}}},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/codex-api-key", strings.NewReader(`{"index":0,"value":{"alpha-search":true}}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchCodexKey(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !h.cfg.CodexKey[0].AlphaSearch {
		t.Fatal("alpha-search = false, want true")
	}
}
