package cliproxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestServiceRun_RequestsRetryUntilInitialModelsAreRegistered(t *testing.T) {
	authID := "startup-readiness-test"
	registry.GetGlobalRegistry().UnregisterClient(authID)
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan *Service, 1)
	allowLoad := make(chan struct{})
	fixture := func(name string) string {
		t.Helper()
		path, err := filepath.Abs(filepath.Join("..", "..", "internal", "registry", "models", name))
		if err != nil {
			t.Fatal(err)
		}
		return path
	}
	cfg := &config.Config{Host: "127.0.0.1", AuthDir: t.TempDir(), CommercialMode: true,
		Models: config.ModelCatalogs{
			Catalog:      fixture("models.json"),
			CodexCatalog: fixture("codex_client_models.json"),
			DevinCatalog: fixture("devin_models.json"),
		},
	}
	service, err := NewBuilder().WithConfig(cfg).WithConfigPath(filepath.Join(cfg.AuthDir, "config.yaml")).Build()
	if err != nil {
		t.Fatal(err)
	}
	// Isolate this service from the process-global token store used by other tests.
	service.coreManager = coreauth.NewManager(nil, nil, nil)
	service.hooks.OnAfterStart = func(s *Service) { started <- s }
	service.watcherFactory = func(string, string, func(*config.Config)) (*WatcherWrapper, error) {
		return &WatcherWrapper{
			start: func(context.Context) error {
				select {
				case <-allowLoad:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			},
			snapshotAuths: func() []*coreauth.Auth {
				return []*coreauth.Auth{{ID: authID, Provider: "codex", Status: coreauth.StatusActive, LastRefreshedAt: time.Now(), Metadata: map[string]any{"expired": "2099-01-01T00:00:00Z"}, Attributes: map[string]string{"plan_type": "pro"}}}
			},
		}, nil
	}
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("service never started")
	}
	handler := service.server.Handler()
	request := func(method, path, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(recorder, req)
		return recorder
	}
	for _, entry := range []struct{ method, path string }{{http.MethodPost, "/v1/responses"}, {http.MethodGet, "/v1/models"}} {
		response := request(entry.method, entry.path, `{"model":"gpt-6.1-sol","input":"test"}`)
		if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "1" {
			t.Fatalf("initializing response = %d %s, retry = %q", response.Code, response.Body.String(), response.Header().Get("Retry-After"))
		}
	}
	close(allowLoad)
	readyDeadline := time.After(5 * time.Second)
	// A request must never receive an empty catalog or model_not_found while
	// the background auth dispatch is still catching up.
	for {
		select {
		case <-readyDeadline:
			t.Fatal("initial registration never completed")
		default:
		}
		response := request(http.MethodGet, "/v1/models", "")
		if response.Code == http.StatusServiceUnavailable {
			continue
		}
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "gpt-6-astra") {
			t.Fatalf("ready catalog = %d %s", response.Code, response.Body.String())
		}
		break
	}
	response := request(http.MethodPost, "/v1/responses", `{"model":"definitely-unknown-startup-model","input":"test"}`)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "model_not_found") {
		t.Fatalf("unknown model after ready = %d %s", response.Code, response.Body.String())
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("service failed to stop")
	}
	response = request(http.MethodGet, "/v1/models", "")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("stopped response = %d", response.Code)
	}
}

func TestServiceReadinessGateDoesNotChangeDirectServerConstruction(t *testing.T) {
	server := api.NewServer(&config.Config{CommercialMode: true}, nil, nil, "")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("direct server = %d", response.Code)
	}
}

func TestServiceReadinessManagementExemptions(t *testing.T) {
	service := &Service{}
	engine := gin.New()
	engine.Use(service.readinessMiddleware())
	engine.NoRoute(func(c *gin.Context) { c.Status(http.StatusNoContent) })
	for _, path := range []string{
		"/v0/management", "/v0/management/config", "/v8/management", "/v8/management/config",
		"/v0/resource/plugins/example", "/management.html",
	} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusNoContent || response.Header().Get("Retry-After") != "" {
				t.Fatalf("control-plane route gated: status=%d retry=%q", response.Code, response.Header().Get("Retry-After"))
			}
		})
	}
	for _, path := range []string{"/v1/models", "/v1/responses", "/v8/management-other", "/management.html/other"} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "1" {
				t.Fatalf("model route not gated: status=%d retry=%q", response.Code, response.Header().Get("Retry-After"))
			}
		})
	}
}

func TestServiceStartupReadinessFailsOpenForStalledCatalogs(t *testing.T) {
	fetchEntered := make(chan struct{}, 2)
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetchEntered <- struct{}{}
		<-r.Context().Done()
	}))
	t.Cleanup(stalled.Close)
	fixture, err := filepath.Abs(filepath.Join("..", "..", "internal", "registry", "models", "codex_client_models.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Host: "127.0.0.1", AuthDir: t.TempDir(), CommercialMode: true,
		Models: config.ModelCatalogs{Catalog: stalled.URL, CodexCatalog: fixture, DevinCatalog: stalled.URL},
	}
	service, err := NewBuilder().WithConfig(cfg).WithConfigPath(filepath.Join(cfg.AuthDir, "config.yaml")).Build()
	if err != nil {
		t.Fatal(err)
	}
	service.coreManager = coreauth.NewManager(nil, nil, nil)
	const authID = "startup-stalled-catalog-test"
	registry.GetGlobalRegistry().UnregisterClient(authID)
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	started := make(chan struct{})
	service.hooks.OnAfterStart = func(*Service) { close(started) }
	service.watcherFactory = func(string, string, func(*config.Config)) (*WatcherWrapper, error) {
		return &WatcherWrapper{
			start: func(context.Context) error { return nil },
			snapshotAuths: func() []*coreauth.Auth {
				return []*coreauth.Auth{{ID: authID, Provider: "codex", Status: coreauth.StatusActive,
					LastRefreshedAt: time.Now(), Metadata: map[string]any{"expired": "2099-01-01T00:00:00Z"},
					Attributes: map[string]string{"plan_type": "pro"}}}
			},
		}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case errRun := <-done:
			if errRun != nil && !errors.Is(errRun, context.Canceled) {
				t.Error(errRun)
			}
		case <-time.After(5 * time.Second):
			t.Error("service failed to stop")
		}
	})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("service never started")
	}
	for range 2 {
		select {
		case <-fetchEntered:
		case <-time.After(5 * time.Second):
			t.Fatal("both catalog fetches must be stalled")
		}
	}
	request := func() *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		service.server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
		return response
	}
	if response := request(); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("must gate while catalog refreshes are pending, got %d", response.Code)
	}
	// This is one shared 10-second budget, not 10 seconds for each catalog.
	deadline := time.NewTimer(12 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("startup readiness did not fail open within the shared catalog wait bound")
		case <-ticker.C:
			if !service.requestReady.Load() {
				continue
			}
			response := request()
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "gpt-6-astra") {
				t.Fatalf("last-valid catalog unavailable after startup deadline: %d %s", response.Code, response.Body.String())
			}
			return
		}
	}
}
