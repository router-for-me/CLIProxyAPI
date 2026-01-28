package test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func newRoutingStrategyTestHandler(t *testing.T) (*management.Handler, string) {
	t.Helper()
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	cfg := &config.Config{
		Routing: config.RoutingConfig{Strategy: "round-robin"},
	}

	if err := os.WriteFile(configPath, []byte("port: 8080\n"), 0o644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	h := management.NewHandler(cfg, configPath, nil)
	return h, configPath
}

func setupRoutingStrategyRouter(h *management.Handler) *gin.Engine {
	r := gin.New()
	mgmt := r.Group("/v0/management")
	{
		mgmt.GET("/routing/strategy", h.GetRoutingStrategy)
		mgmt.PUT("/routing/strategy", h.PutRoutingStrategy)
		mgmt.PATCH("/routing/strategy", h.PutRoutingStrategy)
		mgmt.GET("/routing/preference", h.GetRoutingPreference)
		mgmt.PUT("/routing/preference", h.PutRoutingPreference)
		mgmt.PATCH("/routing/preference", h.PutRoutingPreference)
	}
	return r
}

func TestPutRoutingStrategy_AcceptsProviderFirst(t *testing.T) {
	h, configPath := newRoutingStrategyTestHandler(t)
	r := setupRoutingStrategyRouter(h)

	body := `{"value":"provider-first"}`
	req := httptest.NewRequest(http.MethodPut, "/v0/management/routing/strategy", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	loaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("failed to load config from disk: %v", err)
	}
	if got := loaded.Routing.Preference; got != "provider-first" {
		t.Fatalf("expected preference %q, got %q", "provider-first", got)
	}
	if got := loaded.Routing.Strategy; got != "round-robin" {
		t.Fatalf("expected strategy %q, got %q", "round-robin", got)
	}
}

func TestPutRoutingStrategy_AcceptsFillFirst(t *testing.T) {
	h, configPath := newRoutingStrategyTestHandler(t)
	r := setupRoutingStrategyRouter(h)

	body := `{"value":"fill-first"}`
	req := httptest.NewRequest(http.MethodPut, "/v0/management/routing/strategy", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	loaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("failed to load config from disk: %v", err)
	}
	if got := loaded.Routing.Strategy; got != "fill-first" {
		t.Fatalf("expected strategy %q, got %q", "fill-first", got)
	}
}

func TestPutRoutingPreference_WritesPreference(t *testing.T) {
	h, configPath := newRoutingStrategyTestHandler(t)
	r := setupRoutingStrategyRouter(h)

	body := `{"value":"credential-first"}`
	req := httptest.NewRequest(http.MethodPut, "/v0/management/routing/preference", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	loaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("failed to load config from disk: %v", err)
	}
	if got := loaded.Routing.Preference; got != "credential-first" {
		t.Fatalf("expected preference %q, got %q", "credential-first", got)
	}
}
