package cliproxy

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
)

func TestBuilderRegistersEphemeralAPIKeyWithAccessManager(t *testing.T) {
	accessManager := sdkaccess.NewManager()
	cfg := &proxyconfig.Config{AuthDir: filepath.Join(t.TempDir(), "auths")}
	service, errBuild := NewBuilder().
		WithConfig(cfg).
		WithConfigPath(filepath.Join(t.TempDir(), "config.yaml")).
		WithRequestAccessManager(accessManager).
		WithEphemeralAPIKey("runtime-secret").
		Build()
	if errBuild != nil {
		t.Fatalf("Build() error = %v", errBuild)
	}

	request, errRequest := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost/v1/models", nil)
	if errRequest != nil {
		t.Fatalf("create request: %v", errRequest)
	}
	request.Header.Set("Authorization", "Bearer runtime-secret")
	result, authErr := accessManager.Authenticate(context.Background(), request)
	if authErr != nil {
		t.Fatalf("Authenticate() error = %v", authErr)
	}
	if result.Provider != "cliproxyapi-ephemeral" {
		t.Fatalf("provider = %q, want cliproxyapi-ephemeral", result.Provider)
	}
	if result.Principal == "runtime-secret" {
		t.Fatal("authentication result exposed the ephemeral key")
	}

	service.syncPluginRuntimeConfigForConfig(context.Background(), cfg)
	result, authErr = accessManager.Authenticate(context.Background(), request)
	if authErr != nil {
		t.Fatalf("Authenticate() after plugin sync error = %v", authErr)
	}
	if result == nil {
		t.Fatal("Authenticate() after plugin sync returned no result")
	}
	if result.Provider != "cliproxyapi-ephemeral" {
		t.Fatalf("provider after plugin sync = %q, want cliproxyapi-ephemeral", result.Provider)
	}

	request.Header.Set("Authorization", "Bearer wrong-secret")
	if _, authErr = accessManager.Authenticate(context.Background(), request); !sdkaccess.IsAuthErrorCode(authErr, sdkaccess.AuthErrorCodeInvalidCredential) {
		t.Fatalf("wrong key error = %v, want invalid credential", authErr)
	}
}

func TestBuilderRuntimeAccessPreservesUpstreamInitialization(t *testing.T) {
	for _, key := range []string{"", "   ", "runtime-secret"} {
		t.Run(key, func(t *testing.T) {
			cfg := &proxyconfig.Config{AuthDir: filepath.Join(t.TempDir(), "auths")}
			cfg.APIKeys = []string{"configured-key"}
			service, errBuild := NewBuilder().
				WithConfig(cfg).
				WithConfigPath(filepath.Join(t.TempDir(), "config.yaml")).
				WithEphemeralAPIKey(key).
				Build()
			if errBuild != nil {
				t.Fatalf("Build() error = %v", errBuild)
			}
			if service.discoveryManager == nil {
				t.Fatal("host authentication must not discard upstream discovery initialization")
			}
			if len(cfg.APIKeys) != 1 || cfg.APIKeys[0] != "configured-key" {
				t.Fatal("the ephemeral key must not modify persisted configuration")
			}
			wantRuntimeProviders := 0
			if key == "runtime-secret" {
				wantRuntimeProviders = 1
			}
			if len(service.runtimeAccessProviders) != wantRuntimeProviders {
				t.Fatalf("runtime providers = %d, want %d", len(service.runtimeAccessProviders), wantRuntimeProviders)
			}
			for i := 0; i < 3; i++ {
				service.syncPluginRuntimeConfigForConfig(context.Background(), cfg)
			}
			count := 0
			for _, provider := range service.accessManager.Providers() {
				if provider != nil && provider.Identifier() == "cliproxyapi-ephemeral" {
					count++
				}
			}
			if count != wantRuntimeProviders {
				t.Fatalf("runtime providers after reload = %d, want %d", count, wantRuntimeProviders)
			}
			request, errRequest := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost/v1/models", nil)
			if errRequest != nil {
				t.Fatalf("create request: %v", errRequest)
			}
			request.Header.Set("Authorization", "Bearer configured-key")
			if _, authErr := service.accessManager.Authenticate(request.Context(), request); authErr != nil {
				t.Fatalf("configured authentication was lost: %v", authErr)
			}
		})
	}
}

func TestBuilderRuntimeAccessIsServiceScoped(t *testing.T) {
	keys := []string{"first-runtime-key", "second-runtime-key"}
	services := make([]*Service, len(keys))
	for i, key := range keys {
		cfg := &proxyconfig.Config{AuthDir: filepath.Join(t.TempDir(), "auths")}
		service, errBuild := NewBuilder().
			WithConfig(cfg).
			WithConfigPath(filepath.Join(t.TempDir(), "config.yaml")).
			WithEphemeralAPIKey(key).
			Build()
		if errBuild != nil {
			t.Fatalf("Build() error = %v", errBuild)
		}
		services[i] = service
	}
	for i, service := range services {
		for j, key := range keys {
			request, errRequest := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost/v1/models", nil)
			if errRequest != nil {
				t.Fatalf("create request: %v", errRequest)
			}
			request.Header.Set("Authorization", "Bearer "+key)
			_, authErr := service.accessManager.Authenticate(request.Context(), request)
			if i == j && authErr != nil {
				t.Fatalf("service %d rejected its own key: %v", i, authErr)
			}
			if i != j && !sdkaccess.IsAuthErrorCode(authErr, sdkaccess.AuthErrorCodeInvalidCredential) {
				t.Fatalf("service %d accepted another service's key", i)
			}
		}
	}
}
