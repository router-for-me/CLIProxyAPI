package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

type replaceAwareExecutor struct {
	id string

	mu               sync.Mutex
	closedSessionIDs []string
}

func (e *replaceAwareExecutor) Identifier() string {
	return e.id
}

func (e *replaceAwareExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *replaceAwareExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	ch := make(chan cliproxyexecutor.StreamChunk)
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (e *replaceAwareExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *replaceAwareExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *replaceAwareExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *replaceAwareExecutor) CloseExecutionSession(sessionID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closedSessionIDs = append(e.closedSessionIDs, sessionID)
}

func (e *replaceAwareExecutor) ClosedSessionIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.closedSessionIDs))
	copy(out, e.closedSessionIDs)
	return out
}

func TestManagerRegisterExecutorClosesReplacedExecutionSessions(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, nil, nil)
	replaced := &replaceAwareExecutor{id: "codex"}
	current := &replaceAwareExecutor{id: "codex"}

	manager.RegisterExecutor(replaced)
	manager.RegisterExecutor(current)

	closed := replaced.ClosedSessionIDs()
	if len(closed) != 1 {
		t.Fatalf("expected replaced executor close calls = 1, got %d", len(closed))
	}
	if closed[0] != CloseAllExecutionSessionsID {
		t.Fatalf("expected close marker %q, got %q", CloseAllExecutionSessionsID, closed[0])
	}
	if len(current.ClosedSessionIDs()) != 0 {
		t.Fatalf("expected current executor to stay open")
	}
}

func TestManagerExecutorReturnsRegisteredExecutor(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, nil, nil)
	current := &replaceAwareExecutor{id: "codex"}
	manager.RegisterExecutor(current)

	resolved, okResolved := manager.Executor("CODEX")
	if !okResolved {
		t.Fatal("expected registered executor to be found")
	}
	resolvedExecutor, okResolvedExecutor := resolved.(*replaceAwareExecutor)
	if !okResolvedExecutor {
		t.Fatalf("expected resolved executor type %T, got %T", current, resolved)
	}
	if resolvedExecutor != current {
		t.Fatal("expected resolved executor to match registered executor")
	}

	_, okMissing := manager.Executor("unknown")
	if okMissing {
		t.Fatal("expected unknown provider lookup to fail")
	}

	kimiExec := &replaceAwareExecutor{id: "kimi"}
	manager.RegisterExecutor(kimiExec)
	for _, provider := range []string{"kimi", "KIMI", "kimi-ai", "kimi.ai", "kimi.com"} {
		resolvedKimi, okKimi := manager.Executor(provider)
		if !okKimi {
			t.Fatalf("expected executor for %q to be found", provider)
		}
		if resolvedKimi != kimiExec {
			t.Fatalf("expected resolved executor for %q to match kimiExec", provider)
		}
	}
}

type refreshMockExecutor struct {
	id string
}

func (e *refreshMockExecutor) Identifier() string { return e.id }
func (e *refreshMockExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *refreshMockExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {}\n\n")}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}
func (e *refreshMockExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any)
	}
	updated.Metadata["refreshed"] = true
	return updated, nil
}
func (e *refreshMockExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *refreshMockExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestManagerRefreshAndLegacySelectionKimiAliases(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(&refreshMockExecutor{id: "kimi"})

	providers := []string{"kimi", "kimi-ai", "kimi.ai", "kimi.com"}
	for _, provider := range providers {
		authID := "auth-" + provider
		auth := &Auth{
			ID:       authID,
			Provider: provider,
			Status:   StatusActive,
			Metadata: map[string]any{"access_token": "token-" + provider},
		}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", provider, errRegister)
		}

		refreshed, errRefresh := manager.refreshAuthForRequest(context.Background(), authID, "")
		if errRefresh != nil {
			t.Fatalf("refreshAuthForRequest(%s) error = %v", provider, errRefresh)
		}
		if refreshed == nil || refreshed.Metadata["refreshed"] != true {
			t.Fatalf("expected refreshed metadata for provider %s", provider)
		}

		selectedAuth, exec, errPick := manager.pickNextLegacy(context.Background(), provider, "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickNextLegacy(%s) error = %v", provider, errPick)
		}
		if selectedAuth == nil || exec == nil {
			t.Fatalf("expected selectedAuth and exec for provider %s", provider)
		}
	}
}

func TestManagerSchedulerFastPathKimiAI(t *testing.T) {
	t.Parallel()

	auth := &Auth{
		ID:       "auth-kimi-ai-fastpath",
		Provider: "kimi-ai",
		Status:   StatusActive,
		Metadata: map[string]any{"access_token": "token-ai"},
	}

	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{
		{ID: "kimi-k2"},
	})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	manager := NewManager(nil, nil, nil)
	kimiExec := &refreshMockExecutor{id: "kimi"}
	manager.RegisterExecutor(kimiExec)

	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register error = %v", errRegister)
	}

	if !manager.HasProviderAuth("kimi-ai") {
		t.Fatal("expected HasProviderAuth(kimi-ai) to be true")
	}
	if manager.HasProviderAuth("kimi") {
		t.Fatal("expected HasProviderAuth(kimi) to be false when only kimi-ai is registered")
	}

	// 1. Single provider fast-path pickNext
	pickedAuth, exec, errPick := manager.pickNext(context.Background(), "kimi-ai", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNext(kimi-ai) error = %v", errPick)
	}
	if pickedAuth == nil || pickedAuth.ID != auth.ID {
		t.Fatalf("pickNext(kimi-ai) auth = %v, want ID %s", pickedAuth, auth.ID)
	}
	if exec != kimiExec {
		t.Fatalf("pickNext(kimi-ai) exec = %v, want %v", exec, kimiExec)
	}

	// 2. Mixed provider fast-path pickNextMixed
	pickedMixed, execMixed, providerKey, errMixed := manager.pickNextMixed(context.Background(), []string{"kimi-ai"}, "", cliproxyexecutor.Options{}, nil)
	if errMixed != nil {
		t.Fatalf("pickNextMixed([kimi-ai]) error = %v", errMixed)
	}
	if pickedMixed == nil || pickedMixed.ID != auth.ID {
		t.Fatalf("pickNextMixed([kimi-ai]) auth = %v, want ID %s", pickedMixed, auth.ID)
	}
	if execMixed != kimiExec {
		t.Fatalf("pickNextMixed([kimi-ai]) exec = %v, want %v", execMixed, kimiExec)
	}
	if providerKey != "kimi-ai" {
		t.Fatalf("pickNextMixed([kimi-ai]) providerKey = %q, want kimi-ai", providerKey)
	}

	// 3. Pinned auth fast-path
	pinnedOpts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.PinnedAuthMetadataKey: auth.ID,
		},
	}
	pickedPinned, _, _, errPinned := manager.pickNextMixed(context.Background(), []string{"kimi-ai"}, "", pinnedOpts, nil)
	if errPinned != nil {
		t.Fatalf("pickNextMixed pinned error = %v", errPinned)
	}
	if pickedPinned == nil || pickedPinned.ID != auth.ID {
		t.Fatalf("pickNextMixed pinned auth = %v, want ID %s", pickedPinned, auth.ID)
	}

	// 4. Non-streaming Execute via manager
	resp, errExec := manager.Execute(context.Background(), []string{"kimi-ai"}, cliproxyexecutor.Request{Model: "kimi-k2"}, cliproxyexecutor.Options{})
	if errExec != nil {
		t.Fatalf("manager.Execute([kimi-ai]) error = %v", errExec)
	}
	_ = resp

	// 5. Streaming ExecuteStream via manager
	streamRes, errStream := manager.ExecuteStream(context.Background(), []string{"kimi-ai"}, cliproxyexecutor.Request{Model: "kimi-k2"}, cliproxyexecutor.Options{})
	if errStream != nil {
		t.Fatalf("manager.ExecuteStream([kimi-ai]) error = %v", errStream)
	}
	_ = streamRes
}

func TestManagerKimiDomainIsolation(t *testing.T) {
	t.Parallel()

	authCom := &Auth{
		ID:       "auth-kimi-com-domain",
		Provider: "kimi",
		Status:   StatusActive,
		Metadata: map[string]any{"access_token": "token-com"},
	}
	authAI := &Auth{
		ID:       "auth-kimi-ai-domain",
		Provider: "kimi-ai",
		Status:   StatusActive,
		Metadata: map[string]any{"access_token": "token-ai"},
	}

	registry.GetGlobalRegistry().RegisterClient(authCom.ID, authCom.Provider, []*registry.ModelInfo{{ID: "kimi-k2"}})
	registry.GetGlobalRegistry().RegisterClient(authAI.ID, authAI.Provider, []*registry.ModelInfo{{ID: "kimi-k2"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authCom.ID)
		registry.GetGlobalRegistry().UnregisterClient(authAI.ID)
	})

	manager := NewManager(nil, nil, nil)
	kimiExec := &refreshMockExecutor{id: "kimi"}
	manager.RegisterExecutor(kimiExec)

	if _, errRegCom := manager.Register(context.Background(), authCom); errRegCom != nil {
		t.Fatalf("Register com error = %v", errRegCom)
	}
	if _, errRegAI := manager.Register(context.Background(), authAI); errRegAI != nil {
		t.Fatalf("Register ai error = %v", errRegAI)
	}

	if !manager.HasProviderAuth("kimi") {
		t.Fatal("expected HasProviderAuth(kimi) to be true")
	}
	if !manager.HasProviderAuth("kimi-ai") {
		t.Fatal("expected HasProviderAuth(kimi-ai) to be true")
	}
	if !manager.HasProviderAuth("kimi.ai") {
		t.Fatal("expected HasProviderAuth(kimi.ai) alias to be true")
	}

	// Request for kimi (or kimi.com) MUST only pick authCom
	pickedCom, _, errCom := manager.pickNext(context.Background(), "kimi", "kimi-k2", cliproxyexecutor.Options{}, nil)
	if errCom != nil {
		t.Fatalf("pickNext(kimi) error = %v", errCom)
	}
	if pickedCom.ID != authCom.ID {
		t.Fatalf("pickNext(kimi) picked %s, want %s", pickedCom.ID, authCom.ID)
	}

	// Request for kimi-ai (or kimi.ai) MUST only pick authAI
	pickedAI, _, errAI := manager.pickNext(context.Background(), "kimi-ai", "kimi-k2", cliproxyexecutor.Options{}, nil)
	if errAI != nil {
		t.Fatalf("pickNext(kimi-ai) error = %v", errAI)
	}
	if pickedAI.ID != authAI.ID {
		t.Fatalf("pickNext(kimi-ai) picked %s, want %s", pickedAI.ID, authAI.ID)
	}

	// Cross-domain isolation when one provider is absent
	mgrOnlyCom := NewManager(nil, nil, nil)
	mgrOnlyCom.RegisterExecutor(kimiExec)
	if _, err := mgrOnlyCom.Register(context.Background(), authCom); err != nil {
		t.Fatalf("Register error = %v", err)
	}
	if mgrOnlyCom.HasProviderAuth("kimi-ai") {
		t.Fatal("expected HasProviderAuth(kimi-ai) to be false on com-only manager")
	}
	_, _, errPickAIOnCom := mgrOnlyCom.pickNext(context.Background(), "kimi-ai", "kimi-k2", cliproxyexecutor.Options{}, nil)
	if errPickAIOnCom == nil {
		t.Fatal("expected error picking kimi-ai on com-only manager, got nil")
	}

	mgrOnlyAI := NewManager(nil, nil, nil)
	mgrOnlyAI.RegisterExecutor(kimiExec)
	if _, err := mgrOnlyAI.Register(context.Background(), authAI); err != nil {
		t.Fatalf("Register error = %v", err)
	}
	if mgrOnlyAI.HasProviderAuth("kimi") {
		t.Fatal("expected HasProviderAuth(kimi) to be false on ai-only manager")
	}
	_, _, errPickComOnAI := mgrOnlyAI.pickNext(context.Background(), "kimi", "kimi-k2", cliproxyexecutor.Options{}, nil)
	if errPickComOnAI == nil {
		t.Fatal("expected error picking kimi on ai-only manager, got nil")
	}
}
