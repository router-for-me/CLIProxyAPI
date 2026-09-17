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
	codexmodels "github.com/router-for-me/CLIProxyAPI/v7/internal/client/codex/models"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestCodexClientModelsEndpoints(t *testing.T) {
	dir := t.TempDir()
	overridePath := filepath.Join(dir, registry.CodexClientModelsOverrideFileName)
	registry.SyncCodexClientModelsOverrideFile(filepath.Join(dir, "config.yaml"))
	t.Cleanup(func() { _ = registry.ClearCodexClientModelsOverride() })

	// A model the registry serves, so the response carries the entry the server
	// assembled for it.
	clientID := "codex-client-endpoints-test"
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(clientID, "openai-compatibility", []*registry.ModelInfo{{ID: "gpt-5.5", DisplayName: "Served 5.5"}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })

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
	if got := codexClientModelsEffectiveValue(t, payload, "gpt-5.5", "display_name"); got != "Endpoint Override" {
		t.Fatalf("display_name = %v, want %q", got, "Endpoint Override")
	}
	if payload.Origins["gpt-5.5"] != registry.CodexClientModelsOriginOverride {
		t.Fatalf("origin = %q, want %q", payload.Origins["gpt-5.5"], registry.CodexClientModelsOriginOverride)
	}
	if _, errStat := os.Stat(overridePath); errStat != nil {
		t.Fatalf("override file was not written: %v", errStat)
	}

	// An entry the server cannot apply is stored and reported, so the model keeps the
	// entry the server assembled for it.
	rec = performCodexClientModelsRequest(t, engine, http.MethodPut, "/v0/management/codex-client-models/override", `{"gpt-5.5":{"slug":"other"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("unstorable PUT status = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	payload = decodeCodexClientModelsResponse(t, rec)
	if len(payload.OverrideErrors) != 1 || payload.OverrideErrors[0].Slug != "gpt-5.5" {
		t.Fatalf("override errors = %#v, want one issue for %q", payload.OverrideErrors, "gpt-5.5")
	}
	if got := codexClientModelsEffectiveValue(t, payload, "gpt-5.5", "display_name"); got == "other" {
		t.Fatalf("display_name = %v, want the assembled default", got)
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
	if got := codexClientModelsEffectiveValue(t, payload, "gpt-5.5", "description"); got != "Per Slug" {
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
	Models         []map[string]any                            `json:"models"`
	Origins        map[string]registry.CodexClientModelsOrigin `json:"origins"`
	Override       map[string]json.RawMessage                  `json:"override"`
	OverrideErrors []registry.CodexClientModelsOverrideIssue   `json:"override_errors"`
	ServedModels   []codexmodels.ServedModelSummary            `json:"served_models"`
}

// codexClientModelsEffectiveValue reports the value clients receive for a slug: the
// default entry the response carries with the response's override document applied.
func codexClientModelsEffectiveValue(t *testing.T, payload codexClientModelsResponse, slug, field string) any {
	t.Helper()
	defaults := make(map[string]map[string]any, len(payload.Models))
	for _, model := range payload.Models {
		entrySlug, _ := model["slug"].(string)
		defaults[strings.TrimSpace(entrySlug)] = model
	}
	served, _ := registry.ResolveCodexClientModelOverrides(defaults, payload.Override)
	model, ok := served[strings.TrimSpace(slug)]
	if !ok {
		t.Fatalf("model %q is not served", slug)
	}
	return model[field]
}

func TestCodexClientModelsReportsServedModels(t *testing.T) {
	dir := t.TempDir()
	registry.SyncCodexClientModelsOverrideFile(filepath.Join(dir, "config.yaml"))
	t.Cleanup(func() { _ = registry.ClearCodexClientModelsOverride() })

	// A model the catalog does not define is still served to Codex clients: the server
	// assembles its entry from the default template and reports that origin.
	clientID := "codex-client-served-summary-test"
	modelID := clientID + "-model"
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(clientID, "openai-compatibility", []*registry.ModelInfo{{
		ID:          modelID,
		DisplayName: "Served Summary",
	}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })

	engine := gin.New()
	h := &Handler{
		cfg:            &config.Config{},
		configFilePath: filepath.Join(dir, "config.yaml"),
		failedAttempts: make(map[string]*attemptInfo),
		envSecret:      "test-secret",
	}
	engine.GET("/v0/management/codex-client-models", h.Middleware(), h.GetCodexClientModels)

	rec := performCodexClientModelsRequest(t, engine, http.MethodGet, "/v0/management/codex-client-models", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	payload := decodeCodexClientModelsResponse(t, rec)
	served := codexClientModelsServedSummary(payload, modelID)
	if served == nil {
		t.Fatalf("served_models is missing %q: %+v", modelID, payload.ServedModels)
	}
	if got := payload.Origins[modelID]; got != registry.CodexClientModelsOriginServed {
		t.Fatalf("origin = %q, want %q for a model without a catalog entry", got, registry.CodexClientModelsOriginServed)
	}
	if served.DisplayName != "Served Summary" {
		t.Fatalf("display_name = %q, want %q", served.DisplayName, "Served Summary")
	}
	// A model the catalog does not define is ordered after every catalog entry, so
	// the summary has to carry the position a management UI preserves on adoption.
	if served.Priority <= 0 {
		t.Fatalf("priority = %d, want the served position", served.Priority)
	}
	if served.ContextWindow <= 0 || served.MaxContextWindow <= 0 {
		t.Fatalf("context windows = %d/%d, want the served values", served.ContextWindow, served.MaxContextWindow)
	}
	// The entry the server assembles for a served model is the default configuration a
	// management UI lists and edits against.
	if got := codexClientModelsResponseSlugs(t, payload); !containsCodexClientModelsSlug(got, modelID) {
		t.Fatalf("catalog slugs %v are missing the served model %q", got, modelID)
	}
}

func TestCodexClientModelsServedModelsFollowOverrides(t *testing.T) {
	dir := t.TempDir()
	registry.SyncCodexClientModelsOverrideFile(filepath.Join(dir, "config.yaml"))
	t.Cleanup(func() { _ = registry.ClearCodexClientModelsOverride() })

	clientID := "codex-client-served-override-test"
	modelID := clientID + "-model"
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(clientID, "openai-compatibility", []*registry.ModelInfo{{
		ID:          modelID,
		DisplayName: "Served Override",
	}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })

	engine := gin.New()
	h := &Handler{
		cfg:            &config.Config{},
		configFilePath: filepath.Join(dir, "config.yaml"),
		failedAttempts: make(map[string]*attemptInfo),
		envSecret:      "test-secret",
	}
	middleware := h.Middleware()
	engine.PUT("/v0/management/codex-client-models/override/:slug", middleware, h.PutCodexClientModelsOverrideEntry)
	engine.DELETE("/v0/management/codex-client-models/override/:slug", middleware, h.DeleteCodexClientModelsOverrideEntry)

	entry := `{"$inherit":"gpt-5.5","slug":"` + modelID + `","display_name":"Dedicated Entry","description":"Dedicated Entry","context_window":128000,"max_context_window":128000,"priority":200}`
	rec := performCodexClientModelsRequest(t, engine, http.MethodPut, "/v0/management/codex-client-models/override/"+modelID, entry)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	payload := decodeCodexClientModelsResponse(t, rec)
	served := codexClientModelsServedSummary(payload, modelID)
	if served == nil {
		t.Fatalf("served_models is missing %q", modelID)
	}
	if got := payload.Origins[modelID]; got != registry.CodexClientModelsOriginOverride {
		t.Fatalf("origin = %q, want %q once the override applies", got, registry.CodexClientModelsOriginOverride)
	}
	if served.ContextWindow != 128000 {
		t.Fatalf("context_window = %d, want %d", served.ContextWindow, 128000)
	}
	if served.MaxContextWindow != 128000 {
		t.Fatalf("max_context_window = %d, want %d", served.MaxContextWindow, 128000)
	}
	if served.Priority != 200 {
		t.Fatalf("priority = %d, want %d", served.Priority, 200)
	}

	rec = performCodexClientModelsRequest(t, engine, http.MethodDelete, "/v0/management/codex-client-models/override/"+modelID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	payload = decodeCodexClientModelsResponse(t, rec)
	served = codexClientModelsServedSummary(payload, modelID)
	if served == nil {
		t.Fatalf("served_models is missing %q after the override was removed", modelID)
	}
	if got := payload.Origins[modelID]; got != registry.CodexClientModelsOriginServed {
		t.Fatalf("origin after DELETE = %q, want %q", got, registry.CodexClientModelsOriginServed)
	}
}

func codexClientModelsServedSummary(payload codexClientModelsResponse, slug string) *codexmodels.ServedModelSummary {
	for index := range payload.ServedModels {
		if payload.ServedModels[index].Slug == slug {
			return &payload.ServedModels[index]
		}
	}
	return nil
}

func containsCodexClientModelsSlug(slugs []string, slug string) bool {
	for _, candidate := range slugs {
		if candidate == slug {
			return true
		}
	}
	return false
}

func TestPutCodexClientModelsOverrideRejectsOversizedBody(t *testing.T) {
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
	engine.PUT("/v0/management/codex-client-models/override", middleware, h.PutCodexClientModelsOverride)

	// The body has to fit into a bounded override file, so one far above that bound
	// is refused before the server holds it in memory.
	body := `{"gpt-5.5":{"description":"` + strings.Repeat("x", registry.CodexClientModelsOverrideMaxFileSize) + `"}}`
	rec := performCodexClientModelsRequest(t, engine, http.MethodPut, "/v0/management/codex-client-models/override", body)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized PUT status = %d, want %d (body %s)", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}
	if _, errStat := os.Stat(overridePath); !os.IsNotExist(errStat) {
		t.Fatalf("oversized PUT wrote %s: %v", overridePath, errStat)
	}
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
