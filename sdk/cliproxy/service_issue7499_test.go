package cliproxy

import (
	"context"
	"errors"
	"net/http"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// recordingInterceptorHost7499 implements the public handlers.PluginInterceptorHost
// surface with the minimum behavior needed by issue 7499 regression tests.
type recordingInterceptorHost7499 struct{}

func (h *recordingInterceptorHost7499) InterceptRequestBeforeAuth(context.Context, pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	return pluginapi.RequestInterceptResponse{}
}

func (h *recordingInterceptorHost7499) InterceptRequestAfterAuth(context.Context, pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	return pluginapi.RequestInterceptResponse{}
}

func (h *recordingInterceptorHost7499) InterceptResponse(context.Context, pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
	return pluginapi.ResponseInterceptResponse{}
}

func (h *recordingInterceptorHost7499) InterceptStreamChunk(context.Context, pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
	return pluginapi.StreamChunkInterceptResponse{}
}

var _ handlers.PluginInterceptorHost = (*recordingInterceptorHost7499)(nil)

// stubProviderExecutor7499 is a minimal ProviderExecutor used to prove config
// apply preserves a caller-installed wrapper for a built-in provider.
type stubProviderExecutor7499 struct {
	provider string
}

func (e *stubProviderExecutor7499) Identifier() string { return e.provider }

func (e *stubProviderExecutor7499) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("stub executor")
}

func (e *stubProviderExecutor7499) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, errors.New("stub executor")
}

func (e *stubProviderExecutor7499) Refresh(context.Context, *coreauth.Auth) (*coreauth.Auth, error) {
	return nil, errors.New("stub executor")
}

func (e *stubProviderExecutor7499) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("stub executor")
}

func (e *stubProviderExecutor7499) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("stub executor")
}

var _ coreauth.ProviderExecutor = (*stubProviderExecutor7499)(nil)

// newIssue7499Service builds a Service whose runtime application is fully
// stubbed so config apply stays in memory without touching the network.
// seedAuths are pre-registered on the core manager so applyConfigRuntime's
// force-replace executor pass sees them the same way a real deployment would.
func newIssue7499Service(t *testing.T, cfg *config.Config, hooks Hooks, seedAuths ...*coreauth.Auth) *Service {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	for _, auth := range seedAuths {
		if auth != nil {
			manager.Register(context.Background(), auth)
		}
	}
	service := &Service{
		cfg:         cfg,
		coreManager: manager,
		hooks:       hooks,
		updateServerClientsContextFn: func(context.Context, *config.Config) bool {
			return true
		},
		applyPprofConfigContextFn: func(context.Context, *config.Config) bool {
			return true
		},
	}
	return service
}

// claudeWrapperExecutor7499 wraps a ProviderExecutor and exposes the current
// inner executor so tests can assert the hook re-wraps the rebuilt built-in
// executor rather than keeping a stale wrapper.
type claudeWrapperExecutor7499 struct {
	inner coreauth.ProviderExecutor
}

func (w *claudeWrapperExecutor7499) Identifier() string { return "claude" }

func (w *claudeWrapperExecutor7499) Execute(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return w.inner.Execute(ctx, auth, req, opts)
}

func (w *claudeWrapperExecutor7499) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return w.inner.ExecuteStream(ctx, auth, req, opts)
}

func (w *claudeWrapperExecutor7499) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return w.inner.Refresh(ctx, auth)
}

func (w *claudeWrapperExecutor7499) CountTokens(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return w.inner.CountTokens(ctx, auth, req, opts)
}

func (w *claudeWrapperExecutor7499) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	return w.inner.HttpRequest(ctx, auth, req)
}

var _ coreauth.ProviderExecutor = (*claudeWrapperExecutor7499)(nil)

// TestIssue7499OnConfigAppliedKeepsCustomExecutorWrapper installs a caller-owned
// wrapper for the built-in "claude" provider via OnConfigApplied and requires a
// subsequent watcher-style config apply to keep the wrapper in place instead of
// restoring the stock built-in executor.
func TestIssue7499OnConfigAppliedKeepsCustomExecutorWrapper(t *testing.T) {
	installed := &stubProviderExecutor7499{provider: "claude"}
	var appliedCfg *config.Config
	hookCalls := 0
	service := newIssue7499Service(t, &config.Config{}, Hooks{
		OnConfigApplied: func(s *Service, cfg *config.Config) {
			hookCalls++
			appliedCfg = cfg
			s.coreManager.RegisterExecutor(installed)
		},
	})

	newCfg := &config.Config{RequestRetry: 3}
	service.applyWatcherConfigUpdate(newCfg)

	if hookCalls != 1 {
		t.Fatalf("OnConfigApplied calls = %d, want 1", hookCalls)
	}
	if appliedCfg != service.cfg {
		t.Fatalf("OnConfigApplied config = %#v, want applied service config %#v", appliedCfg, service.cfg)
	}
	got, ok := service.coreManager.Executor("claude")
	if !ok {
		t.Fatal("claude executor missing after config apply")
	}
	if got != installed {
		t.Fatalf("claude executor = %#v (%T), want caller-installed wrapper %#v", got, got, installed)
	}
}

// TestIssue7499OnConfigAppliedRewrapsRebuiltClaudeExecutor registers a real
// built-in Claude executor, applies two successive configs, and asserts the
// hook reinstalls a wrapper around the *rebuilt* executor each time so the
// embedder never retains a stale configuration snapshot.
func TestIssue7499OnConfigAppliedRewrapsRebuiltClaudeExecutor(t *testing.T) {
	service := newIssue7499Service(t, &config.Config{}, Hooks{}, &coreauth.Auth{ID: "claude-1", Provider: "claude"})
	// Seed the built-in claude executor the same way the service does.
	service.coreManager.RegisterExecutor(executor.NewClaudeExecutor(service.cfg))

	var wrappers []*claudeWrapperExecutor7499
	service.hooks = Hooks{
		OnConfigApplied: func(s *Service, cfg *config.Config) {
			current, ok := s.coreManager.Executor("claude")
			if !ok {
				return
			}
			// The force-replace registration may expose the previous wrapper
			// instead of the rebuilt stock executor; unwrap it so the embedder
			// always wraps the current built-in instance.
			for {
				if w, isWrapper := current.(*claudeWrapperExecutor7499); isWrapper {
					current = w.inner
					continue
				}
				break
			}
			wrapper := &claudeWrapperExecutor7499{inner: current}
			wrappers = append(wrappers, wrapper)
			s.coreManager.RegisterExecutor(wrapper)
		},
	}

	service.applyWatcherConfigUpdate(&config.Config{RequestRetry: 1})
	firstInner := wrappers[0].inner
	if _, isClaude := firstInner.(*executor.ClaudeExecutor); !isClaude {
		t.Fatalf("first inner executor = %T, want *executor.ClaudeExecutor", firstInner)
	}

	service.applyWatcherConfigUpdate(&config.Config{RequestRetry: 7})
	if len(wrappers) != 2 {
		t.Fatalf("wrapper installs = %d, want 2 across two applies", len(wrappers))
	}
	secondInner := wrappers[1].inner
	// The second apply must wrap a rebuilt built-in executor, not the previous
	// wrapper (which would hide the stock executor) and not the same instance
	// (which would carry stale configuration).
	if _, isClaude := secondInner.(*executor.ClaudeExecutor); !isClaude {
		t.Fatalf("second inner executor = %T, want *executor.ClaudeExecutor", secondInner)
	}
	if firstInner == secondInner {
		t.Fatal("hook re-wrapped the same executor instance; config rebuild was not applied")
	}
	if _, isWrapper := secondInner.(*claudeWrapperExecutor7499); isWrapper {
		t.Fatal("hook wrapped the previous wrapper instead of the rebuilt built-in executor")
	}

	got, ok := service.coreManager.Executor("claude")
	if !ok || got != wrappers[1] {
		t.Fatalf("claude executor = %#v, want second wrapper %#v", got, wrappers[1])
	}
}

// TestIssue7499OnConfigAppliedFiresOnEverySuccessfulApply ensures the hook is
// invoked once per successful config commit, matching the existing lifecycle
// contract that runtime state may be rebuilt on every apply.
func TestIssue7499OnConfigAppliedFiresOnEverySuccessfulApply(t *testing.T) {
	hookCalls := 0
	service := newIssue7499Service(t, &config.Config{}, Hooks{
		OnConfigApplied: func(*Service, *config.Config) {
			hookCalls++
		},
	})

	service.applyWatcherConfigUpdate(&config.Config{RequestRetry: 1})
	service.applyWatcherConfigUpdate(&config.Config{RequestRetry: 2})

	if hookCalls != 2 {
		t.Fatalf("OnConfigApplied calls = %d, want 2 across two successful applies", hookCalls)
	}
}

// TestIssue7499OnConfigAppliedNotCalledOnRejectedUpdate ensures the hook only
// fires after a successful runtime apply, not for rejected or aborted commits.
func TestIssue7499OnConfigAppliedNotCalledOnRejectedUpdate(t *testing.T) {
	hookCalls := 0
	service := newIssue7499Service(t, &config.Config{}, Hooks{
		OnConfigApplied: func(*Service, *config.Config) {
			hookCalls++
		},
	})

	invalidWeight := internalconfig.MaxCredentialWeight + 1
	service.applyConfigUpdate(&internalconfig.Config{
		VertexCompatAPIKey: []internalconfig.VertexCompatKey{{
			APIKey: "vertex-key",
			Weight: &invalidWeight,
		}},
	})

	if hookCalls != 0 {
		t.Fatalf("OnConfigApplied calls = %d, want 0 for rejected config", hookCalls)
	}
}

// TestIssue7499WithPluginInterceptorHost verifies the builder accepts any
// handlers.PluginInterceptorHost implementation and records it so the API
// server forwards the embedder host instead of forcing the internal plugin host
// onto handlers across startup and reload.
func TestIssue7499WithPluginInterceptorHost(t *testing.T) {
	tmpDir := t.TempDir()
	host := &recordingInterceptorHost7499{}

	service, errBuild := NewBuilder().
		WithConfig(&config.Config{AuthDir: tmpDir}).
		WithConfigPath(tmpDir + "/config.yaml").
		WithPluginInterceptorHost(host).
		Build()
	if errBuild != nil {
		t.Fatalf("Build() error = %v", errBuild)
	}
	if service == nil {
		t.Fatal("Build() returned nil service")
	}
	if service.pluginInterceptorHost != host {
		t.Fatalf("service plugin interceptor host = %#v, want %#v", service.pluginInterceptorHost, host)
	}
}
