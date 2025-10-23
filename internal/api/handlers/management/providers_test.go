package management

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/gin-gonic/gin"
    "github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

func TestGetProviders_IncludesPackycode(t *testing.T) {
    gin.SetMode(gin.TestMode)

    // Seed registry with packycode client
    reg := registry.GetGlobalRegistry()
    reg.RegisterClient("packycode:test", "packycode", registry.GetOpenAIModels())
    t.Cleanup(func() { reg.UnregisterClient("packycode:test") })

    h := &Handler{}
    r := gin.New()
    r.GET("/v0/management/providers", h.GetProviders)

    req := httptest.NewRequest(http.MethodGet, "/v0/management/providers", nil)
    w := httptest.NewRecorder()
    r.ServeHTTP(w, req)

    if w.Code != http.StatusOK {
        t.Fatalf("expected 200 OK, got %d", w.Code)
    }
    var body struct {
        Providers []string `json:"providers"`
    }
    if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
        t.Fatalf("unmarshal error: %v", err)
    }
    found := false
    for _, p := range body.Providers {
        if p == "packycode" {
            found = true
            break
        }
    }
    if !found {
        t.Fatalf("expected packycode in providers list")
    }
}

func TestGetModels_FilterByPackycode(t *testing.T) {
    gin.SetMode(gin.TestMode)

    // Seed registry with packycode client
    reg := registry.GetGlobalRegistry()
    reg.RegisterClient("packycode:test2", "packycode", registry.GetOpenAIModels())
    t.Cleanup(func() { reg.UnregisterClient("packycode:test2") })

    h := &Handler{}
    r := gin.New()
    r.GET("/v0/management/models", h.GetModels)

    req := httptest.NewRequest(http.MethodGet, "/v0/management/models?provider=packycode", nil)
    w := httptest.NewRecorder()
    r.ServeHTTP(w, req)

    if w.Code != http.StatusOK {
        t.Fatalf("expected 200 OK, got %d", w.Code)
    }
    var body struct {
        Object string `json:"object"`
        Data   []map[string]any `json:"data"`
    }
    if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
        t.Fatalf("unmarshal error: %v", err)
    }
    if body.Object != "list" {
        t.Fatalf("expected object=list, got %s", body.Object)
    }
    if len(body.Data) == 0 {
        t.Fatalf("expected some models for provider packycode")
    }
}

func TestGetProviders_IncludesCopilot(t *testing.T) {
    gin.SetMode(gin.TestMode)

    // Seed registry with copilot client
    reg := registry.GetGlobalRegistry()
    reg.RegisterClient("copilot:test", "copilot", registry.GetOpenAIModels())
    t.Cleanup(func() { reg.UnregisterClient("copilot:test") })

    h := &Handler{}
    r := gin.New()
    r.GET("/v0/management/providers", h.GetProviders)

    req := httptest.NewRequest(http.MethodGet, "/v0/management/providers", nil)
    w := httptest.NewRecorder()
    r.ServeHTTP(w, req)

    if w.Code != http.StatusOK {
        t.Fatalf("expected 200 OK, got %d", w.Code)
    }
    var body struct {
        Providers []string `json:"providers"`
    }
    if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
        t.Fatalf("unmarshal error: %v", err)
    }
    found := false
    for _, p := range body.Providers {
        if p == "copilot" {
            found = true
            break
        }
    }
    if !found {
        t.Fatalf("expected copilot in providers list")
    }
}

func TestGetModels_FilterByCopilot(t *testing.T) {
    gin.SetMode(gin.TestMode)

    // Seed registry with copilot client
    reg := registry.GetGlobalRegistry()
    reg.RegisterClient("copilot:test2", "copilot", registry.GetCopilotModels())
    t.Cleanup(func() { reg.UnregisterClient("copilot:test2") })

    h := &Handler{}
    r := gin.New()
    r.GET("/v0/management/models", h.GetModels)

    req := httptest.NewRequest(http.MethodGet, "/v0/management/models?provider=copilot", nil)
    w := httptest.NewRecorder()
    r.ServeHTTP(w, req)

    if w.Code != http.StatusOK {
        t.Fatalf("expected 200 OK, got %d", w.Code)
    }
    var body struct {
        Object string `json:"object"`
        Data   []map[string]any `json:"data"`
    }
    if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
        t.Fatalf("unmarshal error: %v", err)
    }
    if body.Object != "list" {
        t.Fatalf("expected object=list, got %s", body.Object)
    }
    if len(body.Data) == 0 {
        t.Fatalf("expected some models for provider copilot")
    }
    // Ensure gpt-5-mini is visible after registry seeded
    var hasMini bool
    for _, m := range body.Data {
        if id, ok := m["id"].(string); ok && id == "gpt-5-mini" {
            hasMini = true
            break
        }
    }
    if !hasMini {
        t.Fatalf("expected gpt-5-mini in copilot models list")
    }
}
