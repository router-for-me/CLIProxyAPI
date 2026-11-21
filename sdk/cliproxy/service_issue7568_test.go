package cliproxy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// issue7568Wrapper delegates to the built-in executor the same way an SDK
// embedder wraps the Claude executor. It forwards execution-session
// lifecycle so replacing an instance preserves managed sessions.
type issue7568Wrapper struct {
	inner coreauth.ProviderExecutor
}

func (w *issue7568Wrapper) Identifier() string { return w.inner.Identifier() }

func (w *issue7568Wrapper) Execute(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return w.inner.Execute(ctx, auth, req, opts)
}

func (w *issue7568Wrapper) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return w.inner.ExecuteStream(ctx, auth, req, opts)
}

func (w *issue7568Wrapper) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return w.inner.Refresh(ctx, auth)
}

func (w *issue7568Wrapper) CountTokens(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return w.inner.CountTokens(ctx, auth, req, opts)
}

func (w *issue7568Wrapper) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	return w.inner.HttpRequest(ctx, auth, req)
}

var _ coreauth.ProviderExecutor = (*issue7568Wrapper)(nil)

func (w *issue7568Wrapper) CloseExecutionSession(sessionID string) {
	if closer, ok := w.inner.(coreauth.ExecutionSessionCloser); ok && closer != nil {
		closer.CloseExecutionSession(sessionID)
	}
}

func (w *issue7568Wrapper) SharesExecutionSessionsWith(other coreauth.ProviderExecutor) bool {
	if sharer, ok := w.inner.(coreauth.ExecutionSessionSharer); ok && sharer != nil {
		if unwrapper, okOther := other.(interface {
			Unwrap() coreauth.ProviderExecutor
		}); okOther && unwrapper != nil {
			if inner := unwrapper.Unwrap(); inner != nil {
				return sharer.SharesExecutionSessionsWith(inner)
			}
		}
		return sharer.SharesExecutionSessionsWith(other)
	}
	return false
}

func (w *issue7568Wrapper) CloseAuthExecutionSessions(authID string) {
	if closer, ok := w.inner.(coreauth.AuthExecutionSessionCloser); ok && closer != nil {
		closer.CloseAuthExecutionSessions(authID)
	}
}

func (w *issue7568Wrapper) UpstreamDisconnectChan(sessionID string) <-chan error {
	if watcher, ok := w.inner.(interface{ UpstreamDisconnectChan(string) <-chan error }); ok && watcher != nil {
		return watcher.UpstreamDisconnectChan(sessionID)
	}
	return nil
}

// Unwrap exposes the inner executor so the service can track the owned
// underlying executor across wrapping.
func (w *issue7568Wrapper) Unwrap() coreauth.ProviderExecutor {
	return w.inner
}

// issue7568NoUnwrapWrapper is an embedder wrapper without Unwrap, proving
// owned tracking works even when embedders do not expose the inner executor.
type issue7568NoUnwrapWrapper struct {
	inner coreauth.ProviderExecutor
}

func (w *issue7568NoUnwrapWrapper) Identifier() string { return w.inner.Identifier() }

func (w *issue7568NoUnwrapWrapper) Execute(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return w.inner.Execute(ctx, auth, req, opts)
}

func (w *issue7568NoUnwrapWrapper) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return w.inner.ExecuteStream(ctx, auth, req, opts)
}

func (w *issue7568NoUnwrapWrapper) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return w.inner.Refresh(ctx, auth)
}

func (w *issue7568NoUnwrapWrapper) CountTokens(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return w.inner.CountTokens(ctx, auth, req, opts)
}

func (w *issue7568NoUnwrapWrapper) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	return w.inner.HttpRequest(ctx, auth, req)
}

var _ coreauth.ProviderExecutor = (*issue7568NoUnwrapWrapper)(nil)

func newIssue7568Service(t *testing.T, cfg *config.Config, seedAuths ...*coreauth.Auth) *Service {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	for _, auth := range seedAuths {
		if auth != nil {
			manager.Register(context.Background(), auth)
		}
	}
	return &Service{
		cfg:         cfg,
		coreManager: manager,
	}
}

// TestIssue7568ExecutorWrapperSurvivesNonApplyPaths proves the wrapper hook is
// applied on the auth/model-refresh path (ensureExecutorsForAuthWithContext
// with forceReplace=false), which never fires OnConfigApplied.
func TestIssue7568ExecutorWrapperSurvivesNonApplyPaths(t *testing.T) {
	service := newIssue7568Service(t, &config.Config{}, &coreauth.Auth{ID: "claude-1", Provider: "claude"})
	wrapCalls := 0
	service.executorWrapper = func(provider string, exec coreauth.ProviderExecutor) coreauth.ProviderExecutor {
		wrapCalls++
		if provider != "claude" {
			return exec
		}
		return &issue7568Wrapper{inner: exec}
	}

	service.ensureExecutorsForAuthWithContext(context.Background(), &coreauth.Auth{ID: "claude-1", Provider: "claude"}, false)

	got, ok := service.coreManager.Executor("claude")
	if !ok {
		t.Fatal("claude executor missing after registration")
	}
	wrapper, isWrapper := got.(*issue7568Wrapper)
	if !isWrapper {
		t.Fatalf("claude executor = %T, want *issue7568Wrapper", got)
	}
	if _, isClaude := wrapper.inner.(*executor.ClaudeExecutor); !isClaude {
		t.Fatalf("wrapper inner = %T, want *executor.ClaudeExecutor", wrapper.inner)
	}
	if wrapCalls == 0 {
		t.Fatal("executor wrapper was not invoked")
	}
}

// TestIssue7568ExecutorWrapperRewrapsOnRebuild ensures each registration pass
// produces a fresh wrapper around a fresh built-in executor (no stale inner,
// no double wrapping) across successive non-apply paths.
func TestIssue7568ExecutorWrapperRewrapsOnRebuild(t *testing.T) {
	service := newIssue7568Service(t, &config.Config{}, &coreauth.Auth{ID: "claude-1", Provider: "claude"})
	var wrappers []*issue7568Wrapper
	service.executorWrapper = func(provider string, exec coreauth.ProviderExecutor) coreauth.ProviderExecutor {
		if provider != "claude" {
			return exec
		}
		wrapper := &issue7568Wrapper{inner: exec}
		wrappers = append(wrappers, wrapper)
		return wrapper
	}

	service.ensureExecutorsForAuthWithContext(context.Background(), &coreauth.Auth{ID: "claude-1", Provider: "claude"}, false)
	service.ensureExecutorsForAuthWithContext(context.Background(), &coreauth.Auth{ID: "claude-1", Provider: "claude"}, false)

	if len(wrappers) < 2 {
		t.Fatalf("wrapper installs = %d, want >= 2 across two passes", len(wrappers))
	}
	firstInner := wrappers[0].inner
	secondInner := wrappers[len(wrappers)-1].inner
	if _, isClaude := secondInner.(*executor.ClaudeExecutor); !isClaude {
		t.Fatalf("second inner executor = %T, want *executor.ClaudeExecutor", secondInner)
	}
	if _, isWrapper := secondInner.(*issue7568Wrapper); isWrapper {
		t.Fatal("wrapper wrapped the previous wrapper instead of the rebuilt built-in executor")
	}
	if firstInner == secondInner {
		t.Fatal("hook re-wrapped the same executor instance; rebuild was not applied")
	}
	got, ok := service.coreManager.Executor("claude")
	if !ok || got != wrappers[len(wrappers)-1] {
		t.Fatalf("claude executor = %#v, want latest wrapper %#v", got, wrappers[len(wrappers)-1])
	}
}

// TestIssue7568BuilderWithExecutorWrapper verifies the public builder API
// records the wrapper on the built service.
func TestIssue7568BuilderWithExecutorWrapper(t *testing.T) {
	tmpDir := t.TempDir()
	wrapperFn := func(provider string, exec coreauth.ProviderExecutor) coreauth.ProviderExecutor {
		return exec
	}
	service, errBuild := NewBuilder().
		WithConfig(&config.Config{AuthDir: tmpDir}).
		WithConfigPath(tmpDir + "/config.yaml").
		WithExecutorWrapper(wrapperFn).
		Build()
	if errBuild != nil {
		t.Fatalf("Build() error = %v", errBuild)
	}
	if service == nil {
		t.Fatal("Build() returned nil service")
	}
	if service.executorWrapper == nil {
		t.Fatal("service.executorWrapper is nil, want configured wrapper")
	}
}

// TestIssue7568ClaudeThreadSurvivesWrappedRebinding proves actual thread
// state survives a wrapped rebind: seeding a thread through the first wrapper
// and continuing it after a second registration must reuse the same
// underlying ClaudeExecutor via WithConfig (shared stores), so the second
// Execute succeeds instead of failing with a thread conflict.
func TestIssue7568ClaudeThreadSurvivesWrappedRebinding(t *testing.T) {
	service := newIssue7568Service(t, &config.Config{})
	service.executorWrapper = func(provider string, exec coreauth.ProviderExecutor) coreauth.ProviderExecutor {
		if provider != "claude" {
			return exec
		}
		return &issue7568Wrapper{inner: exec}
	}

	auth := &coreauth.Auth{ID: t.Name(), Provider: "claude", Attributes: map[string]string{"api_key": "key-7568-thread"}}
	service.ensureExecutorsForAuthWithContext(context.Background(), auth, false)
	first, ok := service.coreManager.Executor("claude")
	if !ok {
		t.Fatal("claude executor missing after first registration")
	}
	if _, isWrapper := first.(*issue7568Wrapper); !isWrapper {
		t.Fatalf("claude executor = %T, want *issue7568Wrapper", first)
	}
	calls := 0
	transport := claudeThreadServiceRoundTripper(func(req *http.Request) (*http.Response, error) {
		calls++
		if got := strings.Join(helps.HeaderValuesCaseInsensitive(req.Header, "Anthropic-Beta"), ","); got != "message-threads-2026-08-12,effort-2025-11-24" {
			t.Errorf("wire beta = %q, want original Thread profile", got)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"msg_7568_thread","type":"message","role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)), Request: req}, nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: http.Header{"Anthropic-Beta": []string{"message-threads-2026-08-12,effort-2025-11-24"}}}
	request := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(`{"model":"claude-opus-5","thread":{"type":"create"},"messages":[{"role":"user","content":"first"}]}`)}
	if _, errExecute := first.Execute(ctx, auth, request, opts); errExecute != nil {
		t.Fatal(errExecute)
	}

	service.ensureExecutorsForAuthWithContext(context.Background(), &coreauth.Auth{ID: "unrelated-credential", Provider: "claude"}, false)
	second, ok := service.coreManager.Executor("claude")
	if !ok {
		t.Fatal("claude executor missing after second registration")
	}
	if _, isWrapper := second.(*issue7568Wrapper); !isWrapper {
		t.Fatalf("claude executor = %T, want *issue7568Wrapper", second)
	}
	request.Payload = []byte(`{"model":"claude-opus-5","thread":{"type":"continue","previous_message_id":"msg_7568_thread"},"thinking":{"type":"disabled"},"messages":[{"role":"user","content":"second"}]}`)
	if _, errExecute := second.Execute(ctx, auth, request, opts); errExecute != nil {
		t.Fatalf("active Thread lost after wrapped rebind: %v", errExecute)
	}
	if calls != 2 {
		t.Fatalf("upstream requests = %d, want 2", calls)
	}
}

// TestIssue7568NoUnwrapWrapperPreservesClaudeRebinding proves owned tracking
// works when the embedder wrapper exposes no Unwrap: the second registration
// must still consult the owned ClaudeExecutor and rebuild via WithConfig
// (pointer identity changes, wrapper remains published, no double wrap).
func TestIssue7568NoUnwrapWrapperPreservesClaudeRebinding(t *testing.T) {
	service := newIssue7568Service(t, &config.Config{})
	service.executorWrapper = func(provider string, exec coreauth.ProviderExecutor) coreauth.ProviderExecutor {
		if provider != "claude" {
			return exec
		}
		return &issue7568NoUnwrapWrapper{inner: exec}
	}

	service.ensureExecutorsForAuthWithContext(context.Background(), &coreauth.Auth{ID: "claude-1", Provider: "claude"}, false)
	gotFirst, ok := service.coreManager.Executor("claude")
	if !ok {
		t.Fatal("claude executor missing after first registration")
	}
	wrapperFirst, isWrapperFirst := gotFirst.(*issue7568NoUnwrapWrapper)
	if !isWrapperFirst {
		t.Fatalf("claude executor = %T, want *issue7568NoUnwrapWrapper", gotFirst)
	}
	firstInner := wrapperFirst.inner
	ownedFirst, hasOwnedFirst := service.ownedExecutor("claude")
	if !hasOwnedFirst {
		t.Fatal("owned executor missing after first registration")
	}

	service.ensureExecutorsForAuthWithContext(context.Background(), &coreauth.Auth{ID: "claude-1", Provider: "claude"}, false)
	gotSecond, ok := service.coreManager.Executor("claude")
	if !ok {
		t.Fatal("claude executor missing after second registration")
	}
	wrapperSecond, isWrapperSecond := gotSecond.(*issue7568NoUnwrapWrapper)
	if !isWrapperSecond {
		t.Fatalf("claude executor = %T, want *issue7568NoUnwrapWrapper", gotSecond)
	}
	// WithConfig copies the owned executor, so the inner pointer must change
	// while still being a *ClaudeExecutor rather than a fresh NewClaudeExecutor
	// copy would also change pointers; the owned cache identity proves the path
	// consulted the owned executor instead of the published wrapper.
	if wrapperSecond.inner == firstInner {
		t.Fatal("no-unwrap rebind kept the same inner instance; want WithConfig rebuild")
	}
	if _, isClaude := wrapperSecond.inner.(*executor.ClaudeExecutor); !isClaude {
		t.Fatalf("second inner = %T, want *executor.ClaudeExecutor", wrapperSecond.inner)
	}
	ownedSecond, hasOwnedSecond := service.ownedExecutor("claude")
	if !hasOwnedSecond {
		t.Fatal("owned executor missing after second registration")
	}
	if ownedSecond != wrapperSecond.inner {
		t.Fatalf("owned executor = %#v, want latest wrapper inner %#v", ownedSecond, wrapperSecond.inner)
	}
	if ownedFirst == ownedSecond {
		t.Fatal("owned executor identity unchanged across no-unwrap rebind; want fresh WithConfig copy")
	}
}

// TestIssue7568CodexWrapperPreservesReuse proves an unchanged config does not
// rebuild the codex executor when the published executor is a wrapper: the
// reuse decision consults the owned underlying executor.
func TestIssue7568CodexWrapperPreservesReuse(t *testing.T) {
	service := newIssue7568Service(t, &config.Config{})
	service.executorWrapper = func(provider string, exec coreauth.ProviderExecutor) coreauth.ProviderExecutor {
		if provider != "codex" {
			return exec
		}
		return &issue7568Wrapper{inner: exec}
	}

	service.ensureExecutorsForAuthWithContext(context.Background(), &coreauth.Auth{ID: "codex-1", Provider: "codex"}, false)
	first, ok := service.coreManager.Executor("codex")
	if !ok {
		t.Fatal("codex executor missing after first registration")
	}
	service.ensureExecutorsForAuthWithContext(context.Background(), &coreauth.Auth{ID: "codex-1", Provider: "codex"}, false)
	second, ok := service.coreManager.Executor("codex")
	if !ok {
		t.Fatal("codex executor missing after second registration")
	}
	if first != second {
		t.Fatal("codex executor rebuilt despite unchanged config; want reuse of wrapped instance")
	}
}

// TestIssue7568WrappedCompatUnregisterRemovesOwnership proves plugin handoff
// removes a wrapped native OpenAI-compat executor by consulting the owned
// underlying executor, and clears ownership tracking.
func TestIssue7568WrappedCompatUnregisterRemovesOwnership(t *testing.T) {
	service := newIssue7568Service(t, &config.Config{})
	service.executorWrapper = func(provider string, exec coreauth.ProviderExecutor) coreauth.ProviderExecutor {
		return &issue7568Wrapper{inner: exec}
	}

	// Register a wrapped native compat executor through the service gate.
	service.registerExecutor(executor.NewOpenAICompatExecutor("openai-compatibility", service.cfg))
	if _, ok := service.coreManager.Executor("openai-compatibility"); !ok {
		t.Fatal("wrapped compat executor missing after registration")
	}
	got, _ := service.coreManager.Executor("openai-compatibility")
	if _, isWrapper := got.(*issue7568Wrapper); !isWrapper {
		t.Fatalf("compat executor = %T, want *issue7568Wrapper", got)
	}

	service.unregisterOpenAICompatExecutor("openai-compatibility")
	if _, ok := service.coreManager.Executor("openai-compatibility"); ok {
		t.Fatal("wrapped compat executor should have been unregistered")
	}
	if _, ok := service.ownedExecutor("openai-compatibility"); ok {
		t.Fatal("owned compat executor should have been cleared")
	}
}

// TestIssue7568WrappedCompatUpgradesToPluginRefresh proves a wrapped native
// compat executor is still upgraded when a plugin AuthProvider appears: the
// upgrade decision must consult the owned underlying executor.
func TestIssue7568WrappedCompatUpgradesToPluginRefresh(t *testing.T) {
	oldHasAuthProvider := pluginHostHasAuthProvider
	hasProvider := false
	pluginHostHasAuthProvider = func(host *pluginhost.Host, provider string) bool {
		return host != nil && hasProvider
	}
	t.Cleanup(func() {
		pluginHostHasAuthProvider = oldHasAuthProvider
	})

	service := newIssue7568Service(t, &config.Config{})
	service.pluginHost = pluginhost.New()
	service.executorWrapper = func(provider string, exec coreauth.ProviderExecutor) coreauth.ProviderExecutor {
		return &issue7568Wrapper{inner: exec}
	}
	auth := &coreauth.Auth{
		ID:       "plugin-7568-1",
		Provider: "plugin-provider",
		Attributes: map[string]string{
			"base_url": "https://compat.example.com/v1",
			"api_key":  "token",
		},
	}

	// First registration without plugin provider: native compat, wrapped.
	service.registerExecutorForAuth(auth, false)
	first, ok := service.coreManager.Executor("plugin-provider")
	if !ok {
		t.Fatal("executor missing after first registration")
	}
	if pluginhost.IsPluginRefreshCompatExecutor(first) {
		t.Fatal("unexpected plugin wrapper before AuthProvider available")
	}

	// Plugin AuthProvider becomes available; non-forced sync must upgrade.
	hasProvider = true
	service.registerExecutorForAuth(auth, false)
	second, ok := service.coreManager.Executor("plugin-provider")
	if !ok {
		t.Fatal("executor missing after upgrade registration")
	}
	// Published is embedder wrapper around plugin-refresh wrapper.
	wrapped, isWrapped := second.(*issue7568Wrapper)
	if !isWrapped {
		t.Fatalf("executor = %T, want embedder wrapper around plugin refresh", second)
	}
	if !pluginhost.IsPluginRefreshCompatExecutor(wrapped.inner) {
		t.Fatalf("inner executor = %T, want plugin refresh compat wrapper", wrapped.inner)
	}
}

// TestIssue7568UnregisterInvalidatesOwnedCache proves an externally removed
// executor is recreated on the next non-forced sync: a stale cache must not
// cause the reuse check to skip registration.
func TestIssue7568UnregisterInvalidatesOwnedCache(t *testing.T) {
	service := newIssue7568Service(t, &config.Config{})
	service.executorWrapper = func(provider string, exec coreauth.ProviderExecutor) coreauth.ProviderExecutor {
		return &issue7568Wrapper{inner: exec}
	}

	service.ensureExecutorsForAuthWithContext(context.Background(), &coreauth.Auth{ID: "codex-1", Provider: "codex"}, false)
	if _, ok := service.coreManager.Executor("codex"); !ok {
		t.Fatal("codex executor missing after first registration")
	}
	service.coreManager.UnregisterExecutor("codex")
	if _, ok := service.coreManager.Executor("codex"); ok {
		t.Fatal("codex executor should be removed")
	}

	service.ensureExecutorsForAuthWithContext(context.Background(), &coreauth.Auth{ID: "codex-1", Provider: "codex"}, false)
	got, ok := service.coreManager.Executor("codex")
	if !ok {
		t.Fatal("codex executor not recreated after external unregister")
	}
	if _, isWrapper := got.(*issue7568Wrapper); !isWrapper {
		t.Fatalf("recreated executor = %T, want wrapper", got)
	}
}

// TestIssue7568ExternalReplacementDoesNotInheritOwnership proves an
// externally registered independent executor is not removed by compat
// unregistration based on stale service ownership.
func TestIssue7568ExternalReplacementDoesNotInheritOwnership(t *testing.T) {
	service := newIssue7568Service(t, &config.Config{})
	service.executorWrapper = func(provider string, exec coreauth.ProviderExecutor) coreauth.ProviderExecutor {
		return &issue7568Wrapper{inner: exec}
	}

	service.registerExecutor(executor.NewOpenAICompatExecutor("openai-compatibility", service.cfg))
	// External replacement with an independent executor type.
	external := &stubProviderExecutor7499{provider: "openai-compatibility"}
	service.coreManager.RegisterExecutor(external)

	service.unregisterOpenAICompatExecutor("openai-compatibility")
	got, ok := service.coreManager.Executor("openai-compatibility")
	if !ok {
		t.Fatal("external replacement was removed by stale ownership")
	}
	if got != external {
		t.Fatalf("executor = %#v, want external replacement %#v", got, external)
	}
}

// nonComparableServiceWrapper is a value-receiver embedder wrapper holding a
// slice, with no Unwrap. It proves ownership checks never panic or hang on
// non-comparable opaque wrappers. Identifier delegates to the inner executor.
type nonComparableServiceWrapper struct {
	inner  coreauth.ProviderExecutor
	fields []string
}

func (w nonComparableServiceWrapper) Identifier() string { return w.inner.Identifier() }

func (w nonComparableServiceWrapper) Execute(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return w.inner.Execute(ctx, auth, req, opts)
}

func (w nonComparableServiceWrapper) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return w.inner.ExecuteStream(ctx, auth, req, opts)
}

func (w nonComparableServiceWrapper) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return w.inner.Refresh(ctx, auth)
}

func (w nonComparableServiceWrapper) CountTokens(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return w.inner.CountTokens(ctx, auth, req, opts)
}

func (w nonComparableServiceWrapper) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	return w.inner.HttpRequest(ctx, auth, req)
}

// cycleServiceWrapper supports two-node unwrap cycles.
type cycleServiceWrapper struct {
	inner coreauth.ProviderExecutor
}

func (w *cycleServiceWrapper) Identifier() string { return "claude" }

func (w *cycleServiceWrapper) Execute(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (w *cycleServiceWrapper) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (w *cycleServiceWrapper) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (w *cycleServiceWrapper) CountTokens(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (w *cycleServiceWrapper) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

func (w *cycleServiceWrapper) Unwrap() coreauth.ProviderExecutor { return w.inner }

// TestIssue7568NonComparableOpaqueWrapperPreservesClaudeRebinding proves
// successive registrations with a non-comparable opaque value wrapper do not
// panic and still preserve Claude WithConfig rebinding.
func TestIssue7568NonComparableOpaqueWrapperPreservesClaudeRebinding(t *testing.T) {
	service := newIssue7568Service(t, &config.Config{})
	service.executorWrapper = func(provider string, exec coreauth.ProviderExecutor) coreauth.ProviderExecutor {
		if provider != "claude" {
			return exec
		}
		return nonComparableServiceWrapper{inner: exec, fields: []string{"w"}}
	}

	service.ensureExecutorsForAuthWithContext(context.Background(), &coreauth.Auth{ID: "claude-1", Provider: "claude"}, false)
	first, ok := service.coreManager.Executor("claude")
	if !ok {
		t.Fatal("claude executor missing after first registration")
	}
	service.ensureExecutorsForAuthWithContext(context.Background(), &coreauth.Auth{ID: "claude-1", Provider: "claude"}, false)
	second, ok := service.coreManager.Executor("claude")
	if !ok {
		t.Fatal("claude executor missing after second registration")
	}
	if _, isWrapper := second.(nonComparableServiceWrapper); !isWrapper {
		t.Fatalf("second executor = %T, want nonComparableServiceWrapper", second)
	}
	owned, hasOwned := service.ownedExecutor("claude")
	if !hasOwned {
		t.Fatal("owned executor missing after second registration")
	}
	if _, isClaude := owned.(*executor.ClaudeExecutor); !isClaude {
		t.Fatalf("owned executor = %T, want *executor.ClaudeExecutor", owned)
	}
	_ = first
}

// TestIssue7568ServiceWrapperCycleTerminates proves an A->B->A unwrap cycle
// in a published wrapper does not hang manager-side ownership resolution.
func TestIssue7568ServiceWrapperCycleTerminates(t *testing.T) {
	owned := executor.NewClaudeExecutor(&config.Config{})
	a := &cycleServiceWrapper{}
	b := &cycleServiceWrapper{}
	a.inner = b
	b.inner = a
	// Revision tracking makes service-side cycle traversal unnecessary; the
	// manager's unwrap helper must terminate on this cycle.
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(a)
	manager.RegisterExecutor(a)
	_ = owned
}
