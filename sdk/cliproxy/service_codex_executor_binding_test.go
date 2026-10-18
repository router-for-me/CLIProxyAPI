package cliproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
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

func TestApplyWatcherConfigUpdate_PreservesExistingWebsocketConnections(t *testing.T) {
	testCases := []struct {
		name     string
		provider string
		model    string
	}{
		{name: "codex", provider: "codex", model: "gpt-6-astra"},
		{name: "xai", provider: "xai", model: "grok-4.6"},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			var connections atomic.Int32
			var closedConnections atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, errUpgrade := upgrader.Upgrade(w, r, nil)
				if errUpgrade != nil {
					t.Errorf("upgrade websocket: %v", errUpgrade)
					return
				}
				connections.Add(1)
				defer func() {
					closedConnections.Add(1)
					_ = conn.Close()
				}()
				for {
					if _, _, errRead := conn.ReadMessage(); errRead != nil {
						return
					}
					completed := []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
					if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
						return
					}
				}
			}))
			defer server.Close()

			initialCfg := &config.Config{
				SDKConfig: config.SDKConfig{
					DisableImageGeneration: internalconfig.DisableImageGenerationAll,
				},
			}
			manager := coreauth.NewManager(nil, nil, nil)
			service := &Service{
				cfg:         initialCfg,
				coreManager: manager,
				pluginHost:  pluginhost.New(),
			}
			t.Cleanup(func() {
				sdkAuth.RegisterPluginAuthParser(nil)
				sdktranslator.SetPluginHooks(nil)
			})

			auth := &coreauth.Auth{
				ID:       tt.provider + "-ws-hot-reload-auth",
				Provider: tt.provider,
				Status:   coreauth.StatusActive,
				Attributes: map[string]string{
					"base_url":   server.URL,
					"websockets": "true",
				},
				Metadata: map[string]any{
					"access_token": "test-token",
					"websockets":   true,
				},
			}
			if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
				t.Fatalf("register auth: %v", errReg)
			}
			GlobalModelRegistry().RegisterClient(auth.ID, tt.provider, []*ModelInfo{{ID: tt.model}})
			t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })
			service.ensureExecutorsForAuth(auth)

			sessionID := "hot-reload-ws-session-" + tt.provider
			t.Cleanup(func() { manager.CloseExecutionSession(sessionID) })

			runTurn := func(ctx context.Context, payload string) {
				t.Helper()
				result, errStream := manager.ExecuteStream(
					ctx,
					[]string{tt.provider},
					cliproxyexecutor.Request{Model: tt.model, Payload: []byte(payload)},
					cliproxyexecutor.Options{
						Stream:         true,
						SourceFormat:   sdktranslator.FormatOpenAIResponse,
						ResponseFormat: sdktranslator.FormatOpenAIResponse,
						Metadata: map[string]any{
							cliproxyexecutor.ExecutionSessionMetadataKey: sessionID,
							cliproxyexecutor.PinnedAuthMetadataKey:       auth.ID,
						},
					},
				)
				if errStream != nil {
					t.Fatalf("ExecuteStream error = %v", errStream)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("ExecuteStream chunk error = %v", chunk.Err)
					}
				}
			}

			wsCtx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
			runTurn(wsCtx, `{"model":"`+tt.model+`","input":[{"type":"message","role":"user","content":"turn 1"}]}`)
			if got := connections.Load(); got != 1 {
				t.Fatalf("connections after turn 1 = %d, want 1", got)
			}

			reloadedCfg := &config.Config{
				SDKConfig: config.SDKConfig{
					ProxyURL:               "http://127.0.0.1:1",
					DisableImageGeneration: internalconfig.DisableImageGenerationAll,
				},
				Routing: internalconfig.RoutingConfig{
					Strategy: "fill-first",
				},
			}
			service.applyWatcherConfigUpdate(reloadedCfg)

			updatedExec, okUpdated := manager.Executor(tt.provider)
			if !okUpdated || updatedExec == nil {
				t.Fatalf("expected %s executor after config hot reload", tt.provider)
			}
			if cfgUser, okCfgUser := updatedExec.(interface{ UsesConfig(*config.Config) bool }); !okCfgUser || !cfgUser.UsesConfig(reloadedCfg) {
				t.Fatalf("expected %s executor to use reloaded config", tt.provider)
			}
			if got := closedConnections.Load(); got != 0 {
				t.Fatalf("closed upstream connections after config hot reload = %d, want 0", got)
			}

			continuationCtx := cliproxyexecutor.WithRequiredUpstreamWebsocket(wsCtx)
			runTurn(continuationCtx, `{"model":"`+tt.model+`","previous_response_id":"resp-1","input":[{"type":"message","role":"user","content":"turn 2"}]}`)
			if got := connections.Load(); got != 1 {
				t.Fatalf("connections after continuation turn = %d, want 1 (reused existing connection)", got)
			}

			newSessionID := sessionID + "-new"
			t.Cleanup(func() { manager.CloseExecutionSession(newSessionID) })
			_, errNewStream := manager.ExecuteStream(
				wsCtx,
				[]string{tt.provider},
				cliproxyexecutor.Request{Model: tt.model, Payload: []byte(`{"model":"` + tt.model + `","input":[{"type":"message","role":"user","content":"new session"}]}`)},
				cliproxyexecutor.Options{
					Stream:         true,
					SourceFormat:   sdktranslator.FormatOpenAIResponse,
					ResponseFormat: sdktranslator.FormatOpenAIResponse,
					Metadata: map[string]any{
						cliproxyexecutor.ExecutionSessionMetadataKey: newSessionID,
						cliproxyexecutor.PinnedAuthMetadataKey:       auth.ID,
					},
				},
			)
			if errNewStream == nil {
				t.Fatal("expected new session after config hot reload to use new proxy configuration and fail to dial 127.0.0.1:1")
			}
		})
	}
}

func TestRegisterExecutorsForAuths_DisabledXAIDoesNotSkipSubsequentForceReplace(t *testing.T) {
	initialCfg := &config.Config{}
	reloadedCfg := &config.Config{}
	service := &Service{
		cfg:         initialCfg,
		coreManager: coreauth.NewManager(nil, nil, nil),
	}
	disabledAuth := &coreauth.Auth{
		ID:       "xai-disabled",
		Provider: "xai",
		Disabled: true,
	}
	activeAuth := &coreauth.Auth{
		ID:       "xai-active",
		Provider: "xai",
		Status:   coreauth.StatusActive,
	}

	service.ensureExecutorsForAuth(activeAuth)
	firstExecutor, okFirst := service.coreManager.Executor("xai")
	if !okFirst || firstExecutor == nil {
		t.Fatal("expected initial xai executor")
	}

	service.cfg = reloadedCfg
	service.registerExecutorsForAuths([]*coreauth.Auth{disabledAuth, activeAuth}, true)

	secondExecutor, okSecond := service.coreManager.Executor("xai")
	if !okSecond || secondExecutor == nil {
		t.Fatal("expected xai executor after force replace")
	}
	if firstExecutor == secondExecutor {
		t.Fatal("expected active xai auth after disabled xai auth to replace executor")
	}
}
