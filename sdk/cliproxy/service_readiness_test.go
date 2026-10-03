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
	cfg := &config.Config{Host: "127.0.0.1", AuthDir: t.TempDir(), CommercialMode: true}
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
