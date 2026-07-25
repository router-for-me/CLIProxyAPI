package cliproxy

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestEnsureExecutorsForAuth_CodexDoesNotReplaceInNormalMode(t *testing.T) {
	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}
	auth := &coreauth.Auth{
		ID:       "codex-auth-1",
		Provider: "codex",
		Status:   coreauth.StatusActive,
	}

	service.ensureExecutorsForAuth(auth)
	firstExecutor, okFirst := service.coreManager.Executor("codex")
	if !okFirst || firstExecutor == nil {
		t.Fatal("expected codex executor after first bind")
	}

	service.ensureExecutorsForAuth(auth)
	secondExecutor, okSecond := service.coreManager.Executor("codex")
	if !okSecond || secondExecutor == nil {
		t.Fatal("expected codex executor after second bind")
	}

	if firstExecutor != secondExecutor {
		t.Fatal("expected codex executor to stay unchanged in normal mode")
	}
}

func TestEnsureExecutorsForAuthWithMode_CodexForceReplace(t *testing.T) {
	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}
	auth := &coreauth.Auth{
		ID:       "codex-auth-2",
		Provider: "codex",
		Status:   coreauth.StatusActive,
	}

	service.ensureExecutorsForAuth(auth)
	firstExecutor, okFirst := service.coreManager.Executor("codex")
	if !okFirst || firstExecutor == nil {
		t.Fatal("expected codex executor after first bind")
	}

	service.ensureExecutorsForAuthWithMode(auth, true)
	secondExecutor, okSecond := service.coreManager.Executor("codex")
	if !okSecond || secondExecutor == nil {
		t.Fatal("expected codex executor after forced rebind")
	}

	if firstExecutor == secondExecutor {
		t.Fatal("expected codex executor replacement in force mode")
	}
}

func TestSyncPluginModelRuntime_UnrelatedAuthDoesNotReplaceWebsocketExecutor(t *testing.T) {
	testCases := []struct {
		name        string
		provider    string
		homeEnabled bool
	}{
		{name: "codex standard mode", provider: "codex"},
		{name: "codex home mode", provider: "codex", homeEnabled: true},
		{name: "xai standard mode", provider: "xai"},
		{name: "xai home mode", provider: "xai", homeEnabled: true},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			cfg := &config.Config{}
			cfg.Home.Enabled = tt.homeEnabled
			service := &Service{
				cfg:         cfg,
				coreManager: coreauth.NewManager(nil, nil, nil),
				pluginHost:  pluginhost.New(),
			}
			providerAuth := &coreauth.Auth{
				ID:       tt.provider + "-auth",
				Provider: tt.provider,
				Status:   coreauth.StatusActive,
			}
			unrelatedAuth := &coreauth.Auth{
				ID:       "unrelated-auth",
				Provider: "claude",
				Status:   coreauth.StatusActive,
			}
			t.Cleanup(func() {
				GlobalModelRegistry().UnregisterClient(providerAuth.ID)
				GlobalModelRegistry().UnregisterClient(unrelatedAuth.ID)
				sdkAuth.RegisterPluginAuthParser(nil)
				sdktranslator.SetPluginHooks(nil)
			})

			if _, errRegister := service.coreManager.Register(ctx, providerAuth); errRegister != nil {
				t.Fatalf("register %s auth: %v", tt.provider, errRegister)
			}
			if _, errRegister := service.coreManager.Register(ctx, unrelatedAuth); errRegister != nil {
				t.Fatalf("register unrelated auth: %v", errRegister)
			}
			service.ensureExecutorsForAuth(providerAuth)
			firstExecutor, okFirst := service.coreManager.Executor(tt.provider)
			if !okFirst || firstExecutor == nil {
				t.Fatalf("expected %s executor before plugin model sync", tt.provider)
			}

			updatedAuth := unrelatedAuth.Clone()
			updatedAuth.Label = "updated unrelated auth"
			service.handleAuthUpdate(ctx, watcher.AuthUpdate{
				Action: watcher.AuthUpdateActionModify,
				ID:     updatedAuth.ID,
				Auth:   updatedAuth,
			})

			secondExecutor, okSecond := service.coreManager.Executor(tt.provider)
			if !okSecond || secondExecutor == nil {
				t.Fatalf("expected %s executor after plugin model sync", tt.provider)
			}
			if firstExecutor != secondExecutor {
				t.Fatalf("expected unrelated auth sync to preserve the %s executor", tt.provider)
			}
		})
	}
}

func TestEnsureExecutorsForAuth_XAIDoesNotReplaceInNormalMode(t *testing.T) {
	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}
	auth := &coreauth.Auth{
		ID:       "xai-auth-1",
		Provider: "xai",
		Status:   coreauth.StatusActive,
	}

	service.ensureExecutorsForAuth(auth)
	firstExecutor, okFirst := service.coreManager.Executor("xai")
	if !okFirst || firstExecutor == nil {
		t.Fatal("expected xai executor after first bind")
	}
	if _, isXAIAutoExecutor := firstExecutor.(*executor.XAIAutoExecutor); !isXAIAutoExecutor {
		t.Fatalf("xai executor type = %T, want *executor.XAIAutoExecutor", firstExecutor)
	}

	service.ensureExecutorsForAuth(auth)
	secondExecutor, okSecond := service.coreManager.Executor("xai")
	if !okSecond || secondExecutor == nil {
		t.Fatal("expected xai executor after second bind")
	}
	if firstExecutor != secondExecutor {
		t.Fatal("expected xai executor to stay unchanged in normal mode")
	}
}

func TestEnsureExecutorsForAuthWithMode_XAIForceReplace(t *testing.T) {
	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}
	auth := &coreauth.Auth{
		ID:       "xai-auth-2",
		Provider: "xai",
		Status:   coreauth.StatusActive,
	}

	service.ensureExecutorsForAuth(auth)
	firstExecutor, okFirst := service.coreManager.Executor("xai")
	if !okFirst || firstExecutor == nil {
		t.Fatal("expected xai executor after first bind")
	}

	service.ensureExecutorsForAuthWithMode(auth, true)
	secondExecutor, okSecond := service.coreManager.Executor("xai")
	if !okSecond || secondExecutor == nil {
		t.Fatal("expected xai executor after forced rebind")
	}
	if firstExecutor == secondExecutor {
		t.Fatal("expected xai executor replacement in force mode")
	}
	if _, isXAIAutoExecutor := secondExecutor.(*executor.XAIAutoExecutor); !isXAIAutoExecutor {
		t.Fatalf("xai executor type = %T, want *executor.XAIAutoExecutor", secondExecutor)
	}
}

func TestEnsureExecutorsForAuth_XAIReplacesExecutorAfterConfigUpdate(t *testing.T) {
	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
		pluginHost:  pluginhost.New(),
	}
	t.Cleanup(func() {
		sdkAuth.RegisterPluginAuthParser(nil)
		sdktranslator.SetPluginHooks(nil)
	})
	auth := &coreauth.Auth{
		ID:       "xai-auth-config-update",
		Provider: "xai",
		Status:   coreauth.StatusActive,
	}

	service.ensureExecutorsForAuth(auth)
	firstExecutor, okFirst := service.coreManager.Executor("xai")
	if !okFirst || firstExecutor == nil {
		t.Fatal("expected xai executor before config update")
	}

	service.applyWatcherConfigUpdate(&config.Config{})
	service.ensureExecutorsForAuth(auth)

	secondExecutor, okSecond := service.coreManager.Executor("xai")
	if !okSecond || secondExecutor == nil {
		t.Fatal("expected xai executor after config update")
	}
	if firstExecutor == secondExecutor {
		t.Fatal("expected stale xai executor replacement after config update")
	}
	if _, isXAIAutoExecutor := secondExecutor.(*executor.XAIAutoExecutor); !isXAIAutoExecutor {
		t.Fatalf("xai executor type = %T, want *executor.XAIAutoExecutor", secondExecutor)
	}
}
