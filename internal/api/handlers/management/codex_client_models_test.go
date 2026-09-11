package management

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestCodexClientModelsEndpoints(t *testing.T) {
	dir := t.TempDir()
	overridePath := filepath.Join(dir, registry.CodexClientModelsOverrideFileName)
	registry.SyncCodexClientModelsOverrideFile(filepath.Join(dir, "config.yaml"))
	t.Cleanup(func() { _ = registry.ClearCodexClientModelsOverride() })

	engine := gin.New()
	h := &Handler{
		cfg:            &config.Config{},
		configFilePath: filepath.Join(dir, "config.yaml"),
		failedAttempts: make(map[string]*attemptInfo),
		envSecret:      "test-secret",
	}
	middleware := h.Middleware()
	engine.GET("/v0/management/codex-client-models", middleware, h.GetCodexClientModels)
	engine.PUT("/v0/management/codex-client-models/override", middleware, h.PutCodexClientModelsOverride)
	engine.DELETE("/v0/management/codex-client-models/override", middleware, h.DeleteCodexClientModelsOverride)
	engine.PUT("/v0/management/codex-client-models/override/:slug", middleware, h.PutCodexClientModelsOverrideEntry)
	engine.DELETE("/v0/management/codex-client-models/override/:slug", middleware, h.DeleteCodexClientModelsOverrideEntry)

	rec := performCodexClientModelsRequest(t, engine, http.MethodGet, "/v0/management/codex-client-models", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Header().Get(CodexClientModelsOverrideSupportHeader); got != "1" {
		t.Fatalf("%s = %q, want %q", CodexClientModelsOverrideSupportHeader, got, "1")
	}
	payload := decodeCodexClientModelsResponse(t, rec)
	if len(codexClientModelsResponseSlugs(t, payload)) == 0 {
		t.Fatal("GET returned no models")
	}
	if len(payload.Override) != 0 {
		t.Fatalf("override = %v, want empty", payload.Override)
	}

	rec = performCodexClientModelsRequest(t, engine, http.MethodPut, "/v0/management/codex-client-models/override", `{"gpt-5.5":{"display_name":"Endpoint Override"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	payload = decodeCodexClientModelsResponse(t, rec)
	if got := codexClientModelsResponseValue(t, payload, "gpt-5.5", "display_name"); got != "Endpoint Override" {
		t.Fatalf("display_name = %v, want %q", got, "Endpoint Override")
	}
	if payload.Origins["gpt-5.5"] != registry.CodexClientModelsOriginOverride {
		t.Fatalf("origin = %q, want %q", payload.Origins["gpt-5.5"], registry.CodexClientModelsOriginOverride)
	}
	if _, errStat := os.Stat(overridePath); errStat != nil {
		t.Fatalf("override file was not written: %v", errStat)
	}

	rec = performCodexClientModelsRequest(t, engine, http.MethodPut, "/v0/management/codex-client-models/override", `{"gpt-5.5":{"slug":"other"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid PUT status = %d, want %d (body %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	rec = performCodexClientModelsRequest(t, engine, http.MethodPut, "/v0/management/codex-client-models/override", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty PUT status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	rec = performCodexClientModelsRequest(t, engine, http.MethodPut, "/v0/management/codex-client-models/override/gpt-5.5", `{"description":"Per Slug"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("entry PUT status = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	payload = decodeCodexClientModelsResponse(t, rec)
	if got := codexClientModelsResponseValue(t, payload, "gpt-5.5", "description"); got != "Per Slug" {
		t.Fatalf("description = %v, want %q", got, "Per Slug")
	}

	rec = performCodexClientModelsRequest(t, engine, http.MethodDelete, "/v0/management/codex-client-models/override/gpt-5.5", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("entry DELETE status = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	payload = decodeCodexClientModelsResponse(t, rec)
	if len(payload.Override) != 0 {
		t.Fatalf("override after entry DELETE = %v, want empty", payload.Override)
	}

	rec = performCodexClientModelsRequest(t, engine, http.MethodDelete, "/v0/management/codex-client-models/override", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	if _, errStat := os.Stat(overridePath); !os.IsNotExist(errStat) {
		t.Fatalf("override file still present after DELETE: %v", errStat)
	}
}

type codexClientModelsResponse struct {
	Models   []map[string]any                            `json:"models"`
	Origins  map[string]registry.CodexClientModelsOrigin `json:"origins"`
	Override map[string]json.RawMessage                  `json:"override"`
}

func performCodexClientModelsRequest(t *testing.T, engine *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Management-Key", "test-secret")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func decodeCodexClientModelsResponse(t *testing.T, rec *httptest.ResponseRecorder) codexClientModelsResponse {
	t.Helper()
	var payload codexClientModelsResponse
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("decode response %s: %v", rec.Body.String(), errUnmarshal)
	}
	return payload
}

func codexClientModelsResponseSlugs(t *testing.T, payload codexClientModelsResponse) []string {
	t.Helper()
	slugs := make([]string, 0, len(payload.Models))
	for _, model := range payload.Models {
		slug, _ := model["slug"].(string)
		if slug != "" {
			slugs = append(slugs, slug)
		}
	}
	return slugs
}

func codexClientModelsResponseValue(t *testing.T, payload codexClientModelsResponse, slug, field string) any {
	t.Helper()
	for _, model := range payload.Models {
		if model["slug"] == slug {
			return model[field]
		}
	}
	t.Fatalf("model %q not found in response", slug)
	return nil
}
