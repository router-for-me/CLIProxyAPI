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
