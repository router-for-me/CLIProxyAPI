package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func newShowCodexThinkingModelsTestContext(method, target, body string) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	return ctx, rec
}

func TestShowCodexThinkingModelsManagementHandlers(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	initial := []byte("port: 8317\nshow-codex-thinking-models: false\n")
	if err := WriteConfig(configPath, initial); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := config.LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	h := NewHandler(cfg, configPath, nil)

	ctxGet, recGet := newShowCodexThinkingModelsTestContext(http.MethodGet, "/v0/management/show-codex-thinking-models", "")
	h.GetShowCodexThinkingModels(ctxGet)
	if recGet.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want %d; body=%s", recGet.Code, http.StatusOK, recGet.Body.String())
	}
	var getPayload map[string]bool
	if errDecode := json.Unmarshal(recGet.Body.Bytes(), &getPayload); errDecode != nil {
		t.Fatalf("failed to decode GET response: %v", errDecode)
	}
	value, ok := getPayload["show-codex-thinking-models"]
	if !ok {
		t.Fatalf("GET response missing show-codex-thinking-models: %#v", getPayload)
	}
	if value {
		t.Fatalf("expected default show-codex-thinking-models=false, got true")
	}

	ctxPut, recPut := newShowCodexThinkingModelsTestContext(http.MethodPut, "/v0/management/show-codex-thinking-models", `{"value":true}`)
	h.PutShowCodexThinkingModels(ctxPut)
	if recPut.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want %d; body=%s", recPut.Code, http.StatusOK, recPut.Body.String())
	}
	if !h.cfg.ShowCodexThinkingModels {
		t.Fatal("expected in-memory show-codex-thinking-models=true after PUT")
	}
	reloaded, err := config.LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("failed to reload config after PUT: %v", err)
	}
	if !reloaded.ShowCodexThinkingModels {
		t.Fatal("expected persisted show-codex-thinking-models=true after PUT")
	}

	ctxPatch, recPatch := newShowCodexThinkingModelsTestContext(http.MethodPatch, "/v0/management/show-codex-thinking-models", `{"value":false}`)
	h.PutShowCodexThinkingModels(ctxPatch)
	if recPatch.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, want %d; body=%s", recPatch.Code, http.StatusOK, recPatch.Body.String())
	}
	if h.cfg.ShowCodexThinkingModels {
		t.Fatal("expected in-memory show-codex-thinking-models=false after PATCH")
	}
	reloaded, err = config.LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("failed to reload config after PATCH: %v", err)
	}
	if reloaded.ShowCodexThinkingModels {
		t.Fatal("expected persisted show-codex-thinking-models=false after PATCH")
	}
}
