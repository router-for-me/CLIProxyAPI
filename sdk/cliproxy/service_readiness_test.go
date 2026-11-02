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
	for _, entry := range []struct {
		path string
		want int
	}{
		{"/anthropic/callback", http.StatusOK},
		{"/codex/callback", http.StatusOK},
		{"/antigravity/callback", http.StatusOK},
		{"/callback", http.StatusBadRequest},
		{"/devin/callback", http.StatusBadRequest},
		{"/healthz", http.StatusOK},
		{"/", http.StatusOK},
	} {
		response := request(http.MethodGet, entry.path, "")
		if response.Code != entry.want || response.Header().Get("Retry-After") != "" {
			t.Errorf("non-model route %s while initializing = %d %s", entry.path, response.Code, response.Body.String())
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

func TestServiceReadinessCORS(t *testing.T) {
	service := &Service{}
	server := api.NewServer(&config.Config{CommercialMode: true}, nil, nil, "", api.WithMiddleware(service.readinessMiddleware()))
	for _, method := range []string{http.MethodPost, http.MethodOptions} {
		t.Run(method, func(t *testing.T) {
			response := httptest.NewRecorder()
			req := httptest.NewRequest(method, "/v1/responses", nil)
			req.Header.Set("Origin", "https://example.com")
			req.Header.Set("Access-Control-Request-Method", http.MethodPost)
			req.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type")
			server.Handler().ServeHTTP(response, req)
			wantStatus := http.StatusServiceUnavailable
			if method == http.MethodOptions {
				wantStatus = http.StatusNoContent
			} else if response.Header().Get("Retry-After") != "1" {
				t.Error("initializing response must retain Retry-After")
			}
			if response.Code != wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, wantStatus)
			}
			if response.Header().Get("Access-Control-Allow-Origin") != "*" ||
				!strings.Contains(response.Header().Get("Access-Control-Allow-Methods"), http.MethodPost) ||
				!strings.Contains(response.Header().Get("Access-Control-Allow-Headers"), "Authorization") {
				t.Fatalf("missing CORS headers: %v", response.Header())
			}
		})
	}
}

func TestServiceReadinessOnlyGatesModelRoutes(t *testing.T) {
	service := &Service{}
	engine := gin.New()
	engine.Use(service.readinessMiddleware())
	engine.NoRoute(func(c *gin.Context) { c.Status(http.StatusNoContent) })
	for _, path := range []string{
		"/v0/management", "/v0/management/config", "/v8/management", "/v8/management/config",
		"/v0/resource/plugins/example", "/management.html", "/keep-alive",
		"/anthropic/callback", "/codex/callback", "/antigravity/callback", "/callback", "/devin/callback",
		"/", "/healthz", "/assets/app.js", "/new-control-route",
		"/v8/management-other", "/management.html/other", "/v1-other", "/v1beta-other",
		"/openai/v1-other", "/backend-api/codex-other",
	} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusNoContent || response.Header().Get("Retry-After") != "" {
				t.Fatalf("control-plane route gated: status=%d retry=%q", response.Code, response.Header().Get("Retry-After"))
			}
		})
	}
	for _, path := range []string{
		"/v1", "/v1/models", "/v1/responses", "/v1/realtime", "/v1/ws",
		"/v1beta", "/v1beta/models", "/v1beta/interactions", "/v1beta/models/gemini:generateContent",
		"/openai/v1", "/openai/v1/videos", "/backend-api/codex", "/backend-api/codex/responses",
	} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "1" {
				t.Fatalf("model route not gated: status=%d retry=%q", response.Code, response.Header().Get("Retry-After"))
			}
			service.requestReady.Store(true)
			defer service.requestReady.Store(false)
			response = httptest.NewRecorder()
			engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusNoContent || response.Header().Get("Retry-After") != "" {
				t.Fatalf("ready model route gated: status=%d retry=%q", response.Code, response.Header().Get("Retry-After"))
			}
		})
	}
}

func TestServiceStartupReadinessKeepAliveFailsOpenForStalledCatalogs(t *testing.T) {
	fetchEntered := make(chan struct{}, 3)
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetchEntered <- struct{}{}
		<-r.Context().Done()
	}))
	t.Cleanup(stalled.Close)
	cfg := &config.Config{Host: "127.0.0.1", AuthDir: t.TempDir(), CommercialMode: true,
		Models: config.ModelCatalogs{Catalog: stalled.URL, CodexCatalog: stalled.URL, DevinCatalog: stalled.URL},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Match local-password CLI mode: the watchdog starts in NewServer, before
	// the shared 10-second catalog wait. Heartbeats must reset it during startup.
	const localPassword = "startup-readiness-local-password"
	service, err := NewBuilder().WithConfig(cfg).WithConfigPath(filepath.Join(cfg.AuthDir, "config.yaml")).
		WithLocalManagementPassword(localPassword).
		WithServerOptions(api.WithKeepAliveEndpoint(10*time.Second, cancel)).Build()
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
	done := make(chan error, 1)
	startTime := time.Now()
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
	for range 3 {
		select {
		case <-fetchEntered:
		case <-time.After(5 * time.Second):
			t.Fatal("all three catalog fetches must be stalled")
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
	keepAlive := func(password string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/keep-alive", nil)
		req.Header.Set("X-Local-Password", password)
		service.server.Handler().ServeHTTP(response, req)
		return response
	}
	if response := keepAlive("wrong-password"); response.Code != http.StatusUnauthorized {
		t.Errorf("keep-alive must still authenticate while initializing, got %d", response.Code)
	}
	if response := keepAlive(localPassword); response.Code != http.StatusOK || response.Header().Get("Retry-After") != "" {
		t.Errorf("keep-alive gated while catalog refreshes are stalled: %d %s", response.Code, response.Body.String())
	}
	// This is one shared 10-second budget, not 10 seconds for each catalog.
	deadline := time.NewTimer(12 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("local-password keep-alive watchdog canceled startup before readiness could fail open")
		case <-deadline.C:
			t.Fatal("startup readiness did not fail open within the shared catalog wait bound")
		case <-ticker.C:
			keepAlive(localPassword)
			if !service.requestReady.Load() {
				continue
			}
			response := request()
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "gpt-6-astra") {
				t.Fatalf("last-valid catalog unavailable after startup deadline: %d %s", response.Code, response.Body.String())
			}
			if ctx.Err() != nil {
				t.Fatal("keep-alive watchdog canceled the service")
			}
			t.Logf("readiness failed open after %s with all three catalogs stalled and local-password watchdog active", time.Since(startTime))
			return
		}
	}
}
