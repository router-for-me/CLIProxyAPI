package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestRegisterExecutorForAuth_PluginAuthProviderWrapsOpenAICompatRefresh(t *testing.T) {
	oldHasAuthProvider := pluginHostHasAuthProvider
	pluginHostHasAuthProvider = func(host *pluginhost.Host, provider string) bool {
		return host != nil && provider == "plugin-provider"
	}
	t.Cleanup(func() {
		pluginHostHasAuthProvider = oldHasAuthProvider
	})

	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
		pluginHost:  pluginhost.New(),
	}

	auth := &coreauth.Auth{
		ID:       "plugin-auth-1",
		Provider: "plugin-provider",
		Attributes: map[string]string{
			"base_url": "https://compat.example.com/v1",
			"api_key":  "expired-token",
		},
		Metadata: map[string]any{
			"access_token":  "expired-token",
			"refresh_token": "refresh-1",
		},
	}

	service.registerExecutorForAuth(auth, true)

	resolved, ok := service.coreManager.Executor("plugin-provider")
	if !ok || resolved == nil {
		t.Fatal("expected executor for plugin-provider")
	}
	if !pluginhost.IsPluginRefreshCompatExecutor(resolved) {
		t.Fatalf("executor type = %T, want plugin refresh compat wrapper", resolved)
	}
	inner, okInner := pluginhost.UnwrapPluginRefreshCompatExecutor(resolved)
	if !okInner {
		t.Fatal("expected unwrap of plugin refresh compat executor")
	}
	if _, okOpenAICompat := inner.(*runtimeexecutor.OpenAICompatExecutor); !okOpenAICompat {
		t.Fatalf("inner executor type = %T, want *executor.OpenAICompatExecutor", inner)
	}

	// Upgrading from bare OpenAICompat without forceReplace should still wrap.
	service.coreManager.RegisterExecutor(runtimeexecutor.NewOpenAICompatExecutor("plugin-provider", service.cfg))
	service.registerExecutorForAuth(auth, false)
	resolved, ok = service.coreManager.Executor("plugin-provider")
	if !ok || !pluginhost.IsPluginRefreshCompatExecutor(resolved) {
		t.Fatalf("upgrade path executor type = %T, want plugin refresh compat wrapper", resolved)
	}
}

func TestRegisterExecutorForAuth_OpenAICompatWithoutPluginAuthProviderStaysBare(t *testing.T) {
	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
		pluginHost:  pluginhost.New(),
	}
	auth := &coreauth.Auth{
		ID:       "compat-auth-1",
		Provider: "custom-compat",
		Attributes: map[string]string{
			"base_url": "https://compat.example.com/v1",
			"api_key":  "sk-test",
		},
	}

	service.registerExecutorForAuth(auth, true)

	resolved, ok := service.coreManager.Executor("custom-compat")
	if !ok || resolved == nil {
		t.Fatal("expected executor for custom-compat")
	}
	if pluginhost.IsPluginRefreshCompatExecutor(resolved) {
		t.Fatal("did not expect plugin refresh wrapper without AuthProvider")
	}
	if _, okOpenAICompat := resolved.(*runtimeexecutor.OpenAICompatExecutor); !okOpenAICompat {
		t.Fatalf("executor type = %T, want *executor.OpenAICompatExecutor", resolved)
	}
}

func TestRegisterExecutorForAuth_OpenAICompatInfoPathAlsoWrapsPluginRefresh(t *testing.T) {
	oldHasAuthProvider := pluginHostHasAuthProvider
	pluginHostHasAuthProvider = func(host *pluginhost.Host, provider string) bool {
		return host != nil && provider == "plugin-provider"
	}
	t.Cleanup(func() {
		pluginHostHasAuthProvider = oldHasAuthProvider
	})

	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
		pluginHost:  pluginhost.New(),
	}
	auth := &coreauth.Auth{
		ID:       "plugin-auth-compat",
		Provider: "plugin-provider",
		Attributes: map[string]string{
			"base_url":     "https://compat.example.com/v1",
			"compat_name":  "custom",
			"provider_key": "custom",
		},
		Metadata: map[string]any{
			"access_token":  "expired-token",
			"refresh_token": "refresh-1",
		},
	}

	service.registerExecutorForAuth(auth, true)

	resolved, ok := service.coreManager.Executor("openai-compatible-custom")
	if !ok || resolved == nil {
		t.Fatal("expected executor for openai-compatible-custom")
	}
	if !pluginhost.IsPluginRefreshCompatExecutor(resolved) {
		t.Fatalf("executor type = %T, want plugin refresh compat wrapper", resolved)
	}
}

func TestUnregisterOpenAICompatExecutorRemovesPluginRefreshWrapper(t *testing.T) {
	oldHasAuthProvider := pluginHostHasAuthProvider
	pluginHostHasAuthProvider = func(host *pluginhost.Host, provider string) bool {
		return host != nil && provider == "plugin-provider"
	}
	t.Cleanup(func() {
		pluginHostHasAuthProvider = oldHasAuthProvider
	})

	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
		pluginHost:  pluginhost.New(),
	}
	auth := &coreauth.Auth{
		ID:       "plugin-auth-1",
		Provider: "plugin-provider",
		Attributes: map[string]string{
			"base_url": "https://compat.example.com/v1",
		},
	}
	service.registerExecutorForAuth(auth, true)
	if _, ok := service.coreManager.Executor("plugin-provider"); !ok {
		t.Fatal("expected wrapper before unregister")
	}

	service.unregisterOpenAICompatExecutor("plugin-provider")
	if _, ok := service.coreManager.Executor("plugin-provider"); ok {
		t.Fatal("expected plugin-provider executor to be removed")
	}
}
