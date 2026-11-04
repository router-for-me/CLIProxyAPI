package cliproxy

import (
	"context"
	"errors"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestHooksConfigValidationRejectsBuild(t *testing.T) {
	cfg := &config.Config{}
	cfg.APIKeys = []string{"original-key"}
	rejected := errors.New("application rejected config")
	_, err := NewBuilder().WithConfig(cfg).WithConfigPath("unused-config.yaml").
		WithHooks(Hooks{ValidateConfig: func(snapshot *config.Config) error {
			snapshot.APIKeys[0] = "validator-mutation"
			return rejected
		}}).Build()
	if !errors.Is(err, rejected) {
		t.Fatalf("Build error=%v, want application rejection", err)
	}
	if cfg.APIKeys[0] != "original-key" {
		t.Fatal("validator mutated caller configuration")
	}
}

func TestHooksConfigValidationRejectsReloadBeforePublication(t *testing.T) {
	original := &config.Config{}
	original.APIKeys = []string{"original-key"}
	service := &Service{cfg: original, coreManager: coreauth.NewManager(nil, nil, nil)}
	validations := 0
	service.hooks.ValidateConfig = func(snapshot *config.Config) error {
		validations++
		if len(snapshot.APIKeys) == 0 {
			return errors.New("proxy API key required")
		}
		snapshot.APIKeys[0] = "validator-mutation"
		return nil
	}
	updated := original.CloneForRuntime()
	updated.APIKeys = nil
	updated.RequestRetry = 1
	if service.applyConfigUpdateWithAuthSynthesis(context.Background(), updated, false) {
		t.Fatal("key removal reload accepted")
	}
	if service.currentConfig() != original || service.configSequence != 0 {
		t.Fatal("rejected reload published config or sequence")
	}
	valid := original.CloneForRuntime()
	valid.RequestRetry = 2
	commit := service.commitConfigUpdate(valid)
	if commit.cfg == nil || validations != 2 {
		t.Fatal("validator was not called for every config commit")
	}
	if valid.APIKeys[0] != "original-key" || service.currentConfig().APIKeys[0] != "original-key" {
		t.Fatal("validator mutated accepted configuration")
	}
}

func TestHooksConfigValidationReachesManagementSaveBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("host: 127.0.0.1\nport: 8317\napi-keys: [client]\nremote-management: {secret-key: test-password}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AuthDir = t.TempDir()
	service, err := NewBuilder().WithConfig(cfg).WithConfigPath(path).WithCoreAuthManager(coreauth.NewManager(nil, nil, nil)).WithHooks(Hooks{ValidateConfig: func(next *config.Config) error {
		if next.Host != "127.0.0.1" {
			return errors.New("loopback required")
		}
		return nil
	}}).Build()
	if err != nil {
		t.Fatal(err)
	}
	options := append([]api.ServerOption(nil), service.serverOptions...)
	options = append(options, api.WithLocalManagementPassword("test-password"))
	server := api.NewServer(cfg, nil, nil, path, options...)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Stop(ctx); err != nil {
			t.Errorf("stop API fixture: %v", err)
		}
		if err := service.Shutdown(ctx); err != nil {
			t.Errorf("stop service fixture: %v", err)
		}
	})
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPatch, "/v8/management/config", strings.NewReader(`{"server":{"host":"0.0.0.0"}}`))
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer test-password")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("builder validator did not reach management: %d %s", response.Code, response.Body.String())
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("management persisted configuration rejected by embedding application")
	}
}

func TestHooksConfigValidationSeesHomeRuntimeConfig(t *testing.T) {
	cfg := &config.Config{}
	cfg.APIKeys = []string{"client"}
	cfg.Home.Enabled = true
	rejected := errors.New("proxy API key required")
	_, err := NewBuilder().WithConfig(cfg).WithConfigPath("unused-config.yaml").
		WithHooks(Hooks{ValidateConfig: func(snapshot *config.Config) error {
			if len(snapshot.APIKeys) == 0 {
				return rejected
			}
			return nil
		}}).Build()
	if !errors.Is(err, rejected) {
		t.Fatalf("Build error=%v, want rejection of the Home runtime config", err)
	}
	if len(cfg.APIKeys) != 1 {
		t.Fatal("validation snapshot mutated caller configuration")
	}
}

func TestHooksConfigValidationRejectsWatcherReloadBeforeCallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	authDir := filepath.Join(dir, "auth")
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(retry string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("auth-dir: "+authDir+"\nrequest-retry: "+retry+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("1")
	reloads := 0
	wrapper, err := defaultWatcherFactory(path, authDir, func(*config.Config) { reloads++ })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := wrapper.Stop(); err != nil {
			t.Errorf("stop watcher fixture: %v", err)
		}
	})
	wrapper.SetConfigValidator(func(next *config.Config) error {
		if next.RequestRetry > 3 {
			return errors.New("request-retry above application limit")
		}
		return nil
	})
	write("9")
	wrapper.ReloadConfigIfChanged()
	if reloads != 0 {
		t.Fatal("watcher published a config rejected by the embedding application")
	}
}

func TestHooksConfigValidationRejectsOnBeforeStartMutation(t *testing.T) {
	// Background metadata checks terminate at this local proxy; nothing leaves the host.
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer proxy.Close()
	dir := t.TempDir()
	// No catalog override: a file catalog would replace the process-wide model registry.
	cfg := &config.Config{Host: "127.0.0.1", Port: 0, AuthDir: filepath.Join(dir, "auth")}
	cfg.APIKeys = []string{"client"}
	cfg.ProxyURL = proxy.URL
	cfg.RemoteManagement.DisableControlPanel = true
	rejected := errors.New("proxy API key required")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started := false
	service, err := NewBuilder().WithConfig(cfg).WithConfigPath(filepath.Join(dir, "config.yaml")).
		WithCoreAuthManager(coreauth.NewManager(nil, nil, nil)).
		WithWatcherFactory(func(string, string, func(*config.Config)) (*WatcherWrapper, error) {
			return &WatcherWrapper{start: func(context.Context) error { return nil }, stop: func() error { return nil }}, nil
		}).
		WithHooks(Hooks{
			ValidateConfig: func(snapshot *config.Config) error {
				if len(snapshot.APIKeys) == 0 {
					return rejected
				}
				return nil
			},
			OnBeforeStart: func(live *config.Config) { live.APIKeys = nil },
			OnAfterStart:  func(*Service) { started = true; cancel() },
		}).Build()
	if err != nil {
		t.Fatal(err)
	}
	if errRun := service.Run(ctx); !errors.Is(errRun, rejected) {
		t.Fatalf("Run error = %v, want rejection of the OnBeforeStart config", errRun)
	}
	if started {
		t.Fatal("service started with a config rejected by the embedding application")
	}
}
