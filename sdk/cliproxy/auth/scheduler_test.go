package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type schedulerTestExecutor struct{}

func (schedulerTestExecutor) Identifier() string { return "test" }

func (schedulerTestExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (schedulerTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (schedulerTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (schedulerTestExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (schedulerTestExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

type trackingSelector struct {
	calls      int
	lastAuthID []string
}

func (s *trackingSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	s.calls++
	s.lastAuthID = s.lastAuthID[:0]
	for _, auth := range auths {
		s.lastAuthID = append(s.lastAuthID, auth.ID)
	}
	if len(auths) == 0 {
		return nil, nil
	}
	return auths[len(auths)-1], nil
}

func newSchedulerForTest(selector Selector, auths ...*Auth) *authScheduler {
	scheduler := newAuthScheduler(selector)
	scheduler.rebuild(auths)
	return scheduler
}

func registerSchedulerModels(t *testing.T, provider string, model string, authIDs ...string) {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	for _, authID := range authIDs {
		reg.RegisterClient(authID, provider, []*registry.ModelInfo{{ID: model}})
	}
	t.Cleanup(func() {
		for _, authID := range authIDs {
			reg.UnregisterClient(authID)
		}
	})
}

func TestSchedulerPick_RoundRobinHighestPriority(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "low", Provider: "gemini", Attributes: map[string]string{"priority": "0"}},
		&Auth{ID: "high-b", Provider: "gemini", Attributes: map[string]string{"priority": "10"}},
		&Auth{ID: "high-a", Provider: "gemini", Attributes: map[string]string{"priority": "10"}},
	)

	want := []string{"high-a", "high-b", "high-a"}
	for index, wantID := range want {
		got, errPick := scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickSingle() #%d auth = nil", index)
		}
		if got.ID != wantID {
			t.Fatalf("pickSingle() #%d auth.ID = %q, want %q", index, got.ID, wantID)
		}
	}
}

func TestSchedulerPick_FillFirstSticksToFirstReady(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&FillFirstSelector{},
		&Auth{ID: "b", Provider: "gemini"},
		&Auth{ID: "a", Provider: "gemini"},
		&Auth{ID: "c", Provider: "gemini"},
	)

	for index := 0; index < 3; index++ {
		got, errPick := scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickSingle() #%d auth = nil", index)
		}
		if got.ID != "a" {
			t.Fatalf("pickSingle() #%d auth.ID = %q, want %q", index, got.ID, "a")
		}
	}
}

func TestSchedulerPick_PromotesExpiredCooldownBeforePick(t *testing.T) {
	t.Parallel()

	model := "gemini-2.5-pro"
	registerSchedulerModels(t, "gemini", model, "cooldown-expired")
	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{
			ID:       "cooldown-expired",
			Provider: "gemini",
			ModelStates: map[string]*ModelState{
				model: {
					Status:         StatusError,
					Unavailable:    true,
					NextRetryAfter: time.Now().Add(-1 * time.Second),
				},
			},
		},
	)

	got, errPick := scheduler.pickSingle(context.Background(), "gemini", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() error = %v", errPick)
	}
	if got == nil {
		t.Fatalf("pickSingle() auth = nil")
	}
	if got.ID != "cooldown-expired" {
		t.Fatalf("pickSingle() auth.ID = %q, want %q", got.ID, "cooldown-expired")
	}
}

func TestSchedulerPick_GeminiVirtualParentUsesTwoLevelRotation(t *testing.T) {
	t.Parallel()

	registerSchedulerModels(t, "gemini-cli", "gemini-2.5-pro", "cred-a::proj-1", "cred-a::proj-2", "cred-b::proj-1", "cred-b::proj-2")
	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "cred-a::proj-1", Provider: "gemini-cli", Attributes: map[string]string{"gemini_virtual_parent": "cred-a"}},
		&Auth{ID: "cred-a::proj-2", Provider: "gemini-cli", Attributes: map[string]string{"gemini_virtual_parent": "cred-a"}},
		&Auth{ID: "cred-b::proj-1", Provider: "gemini-cli", Attributes: map[string]string{"gemini_virtual_parent": "cred-b"}},
		&Auth{ID: "cred-b::proj-2", Provider: "gemini-cli", Attributes: map[string]string{"gemini_virtual_parent": "cred-b"}},
	)

	wantParents := []string{"cred-a", "cred-b", "cred-a", "cred-b"}
	wantIDs := []string{"cred-a::proj-1", "cred-b::proj-1", "cred-a::proj-2", "cred-b::proj-2"}
	for index := range wantIDs {
		got, errPick := scheduler.pickSingle(context.Background(), "gemini-cli", "gemini-2.5-pro", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickSingle() #%d auth = nil", index)
		}
		if got.ID != wantIDs[index] {
			t.Fatalf("pickSingle() #%d auth.ID = %q, want %q", index, got.ID, wantIDs[index])
		}
		if got.Attributes["gemini_virtual_parent"] != wantParents[index] {
			t.Fatalf("pickSingle() #%d parent = %q, want %q", index, got.Attributes["gemini_virtual_parent"], wantParents[index])
		}
	}
}

func TestSchedulerPick_CodexWebsocketPrefersWebsocketEnabledSubset(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "codex-http", Provider: "codex"},
		&Auth{ID: "codex-ws-a", Provider: "codex", Attributes: map[string]string{"websockets": "true"}},
		&Auth{ID: "codex-ws-b", Provider: "codex", Attributes: map[string]string{"websockets": "true"}},
	)

	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	want := []string{"codex-ws-a", "codex-ws-b", "codex-ws-a"}
	for index, wantID := range want {
		got, errPick := scheduler.pickSingle(ctx, "codex", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickSingle() #%d auth = nil", index)
		}
		if got.ID != wantID {
			t.Fatalf("pickSingle() #%d auth.ID = %q, want %q", index, got.ID, wantID)
		}
	}
}

func TestSchedulerPick_CodexWebsocketPrefersWebsocketEnabledAcrossPriorities(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "codex-http", Provider: "codex", Attributes: map[string]string{"priority": "10"}},
		&Auth{ID: "codex-ws-a", Provider: "codex", Attributes: map[string]string{"priority": "0", "websockets": "true"}},
		&Auth{ID: "codex-ws-b", Provider: "codex", Attributes: map[string]string{"priority": "0", "websockets": "true"}},
	)

	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	want := []string{"codex-ws-a", "codex-ws-b", "codex-ws-a"}
	for index, wantID := range want {
		got, errPick := scheduler.pickSingle(ctx, "codex", "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickSingle() #%d auth = nil", index)
		}
		if got.ID != wantID {
			t.Fatalf("pickSingle() #%d auth.ID = %q, want %q", index, got.ID, wantID)
		}
	}
}

func TestSchedulerPick_MixedProvidersUsesWeightedProviderRotationOverReadyCandidates(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "gemini-a", Provider: "gemini"},
		&Auth{ID: "gemini-b", Provider: "gemini"},
		&Auth{ID: "claude-a", Provider: "claude"},
	)

	wantProviders := []string{"gemini", "gemini", "claude", "gemini"}
	wantIDs := []string{"gemini-a", "gemini-b", "claude-a", "gemini-a"}
	for index := range wantProviders {
		got, provider, errPick := scheduler.pickMixed(context.Background(), []string{"gemini", "claude"}, "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickMixed() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickMixed() #%d auth = nil", index)
		}
		if provider != wantProviders[index] {
			t.Fatalf("pickMixed() #%d provider = %q, want %q", index, provider, wantProviders[index])
		}
		if got.ID != wantIDs[index] {
			t.Fatalf("pickMixed() #%d auth.ID = %q, want %q", index, got.ID, wantIDs[index])
		}
	}
}

func TestSchedulerPick_MixedProvidersPrefersHighestPriorityTier(t *testing.T) {
	t.Parallel()

	model := "gpt-default"
	registerSchedulerModels(t, "provider-low", model, "low")
	registerSchedulerModels(t, "provider-high-a", model, "high-a")
	registerSchedulerModels(t, "provider-high-b", model, "high-b")

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "low", Provider: "provider-low", Attributes: map[string]string{"priority": "4"}},
		&Auth{ID: "high-a", Provider: "provider-high-a", Attributes: map[string]string{"priority": "7"}},
		&Auth{ID: "high-b", Provider: "provider-high-b", Attributes: map[string]string{"priority": "7"}},
	)

	providers := []string{"provider-low", "provider-high-a", "provider-high-b"}
	wantProviders := []string{"provider-high-a", "provider-high-b", "provider-high-a", "provider-high-b"}
	wantIDs := []string{"high-a", "high-b", "high-a", "high-b"}
	for index := range wantProviders {
		got, provider, errPick := scheduler.pickMixed(context.Background(), providers, model, cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickMixed() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickMixed() #%d auth = nil", index)
		}
		if provider != wantProviders[index] {
			t.Fatalf("pickMixed() #%d provider = %q, want %q", index, provider, wantProviders[index])
		}
		if got.ID != wantIDs[index] {
			t.Fatalf("pickMixed() #%d auth.ID = %q, want %q", index, got.ID, wantIDs[index])
		}
	}
}

func TestManager_PickNextMixed_UsesWeightedProviderRotationBeforeCredentialRotation(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	manager.executors["claude"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "gemini-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(gemini-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "gemini-b", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(gemini-b) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "claude-a", Provider: "claude"}); errRegister != nil {
		t.Fatalf("Register(claude-a) error = %v", errRegister)
	}

	wantProviders := []string{"gemini", "gemini", "claude", "gemini"}
	wantIDs := []string{"gemini-a", "gemini-b", "claude-a", "gemini-a"}
	for index := range wantProviders {
		got, _, provider, errPick := manager.pickNextMixed(context.Background(), []string{"gemini", "claude"}, "", cliproxyexecutor.Options{}, map[string]struct{}{})
		if errPick != nil {
			t.Fatalf("pickNextMixed() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickNextMixed() #%d auth = nil", index)
		}
		if provider != wantProviders[index] {
			t.Fatalf("pickNextMixed() #%d provider = %q, want %q", index, provider, wantProviders[index])
		}
		if got.ID != wantIDs[index] {
			t.Fatalf("pickNextMixed() #%d auth.ID = %q, want %q", index, got.ID, wantIDs[index])
		}
	}
}

func TestManager_PickNextMixed_DisallowFreeAuthSkipsCodexFreePlan(t *testing.T) {
	t.Parallel()

	model := "gpt-5.4-mini"
	registerSchedulerModels(t, "codex", model, "codex-a-free", "codex-b-plus")

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["codex"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-a-free", Provider: "codex", Attributes: map[string]string{"plan_type": "free"}}); errRegister != nil {
		t.Fatalf("Register(codex-a-free) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-b-plus", Provider: "codex", Attributes: map[string]string{"plan_type": "plus"}}); errRegister != nil {
		t.Fatalf("Register(codex-b-plus) error = %v", errRegister)
	}

	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.DisallowFreeAuthMetadataKey: true},
	}
	got, _, provider, errPick := manager.pickNextMixed(context.Background(), []string{"codex"}, model, opts, map[string]struct{}{})
	if errPick != nil {
		t.Fatalf("pickNextMixed() error = %v", errPick)
	}
	if got == nil {
		t.Fatalf("pickNextMixed() auth = nil")
	}
	if provider != "codex" {
		t.Fatalf("pickNextMixed() provider = %q, want %q", provider, "codex")
	}
	if got.ID != "codex-b-plus" {
		t.Fatalf("pickNextMixed() auth.ID = %q, want %q", got.ID, "codex-b-plus")
	}
}

func TestManagerCustomSelector_FallsBackToLegacyPath(t *testing.T) {
	t.Parallel()

	selector := &trackingSelector{}
	manager := NewManager(nil, selector, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	manager.auths["auth-a"] = &Auth{ID: "auth-a", Provider: "gemini"}
	manager.auths["auth-b"] = &Auth{ID: "auth-b", Provider: "gemini"}

	got, _, errPick := manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, map[string]struct{}{})
	if errPick != nil {
		t.Fatalf("pickNext() error = %v", errPick)
	}
	if got == nil {
		t.Fatalf("pickNext() auth = nil")
	}
	if selector.calls != 1 {
		t.Fatalf("selector.calls = %d, want %d", selector.calls, 1)
	}
	if len(selector.lastAuthID) != 2 {
		t.Fatalf("len(selector.lastAuthID) = %d, want %d", len(selector.lastAuthID), 2)
	}
	if got.ID != selector.lastAuthID[len(selector.lastAuthID)-1] {
		t.Fatalf("pickNext() auth.ID = %q, want selector-picked %q", got.ID, selector.lastAuthID[len(selector.lastAuthID)-1])
	}
}

func TestManager_InitializesSchedulerForBuiltInSelector(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	if manager.scheduler == nil {
		t.Fatalf("manager.scheduler = nil")
	}
	if manager.scheduler.strategy != schedulerStrategyRoundRobin {
		t.Fatalf("manager.scheduler.strategy = %v, want %v", manager.scheduler.strategy, schedulerStrategyRoundRobin)
	}

	manager.SetSelector(&FillFirstSelector{})
	if manager.scheduler.strategy != schedulerStrategyFillFirst {
		t.Fatalf("manager.scheduler.strategy = %v, want %v", manager.scheduler.strategy, schedulerStrategyFillFirst)
	}
}

func TestManager_SchedulerTracksRegisterAndUpdate(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-b", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-b) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-a) error = %v", errRegister)
	}

	got, errPick := manager.scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("scheduler.pickSingle() error = %v", errPick)
	}
	if got == nil || got.ID != "auth-a" {
		t.Fatalf("scheduler.pickSingle() auth = %v, want auth-a", got)
	}

	if _, errUpdate := manager.Update(context.Background(), &Auth{ID: "auth-a", Provider: "gemini", Disabled: true}); errUpdate != nil {
		t.Fatalf("Update(auth-a) error = %v", errUpdate)
	}

	got, errPick = manager.scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("scheduler.pickSingle() after update error = %v", errPick)
	}
	if got == nil || got.ID != "auth-b" {
		t.Fatalf("scheduler.pickSingle() after update auth = %v, want auth-b", got)
	}
}

func TestManager_PickNextMixed_UsesSchedulerRotation(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	manager.executors["claude"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "gemini-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(gemini-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "gemini-b", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(gemini-b) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "claude-a", Provider: "claude"}); errRegister != nil {
		t.Fatalf("Register(claude-a) error = %v", errRegister)
	}

	wantProviders := []string{"gemini", "gemini", "claude", "gemini"}
	wantIDs := []string{"gemini-a", "gemini-b", "claude-a", "gemini-a"}
	for index := range wantProviders {
		got, _, provider, errPick := manager.pickNextMixed(context.Background(), []string{"gemini", "claude"}, "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickNextMixed() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickNextMixed() #%d auth = nil", index)
		}
		if provider != wantProviders[index] {
			t.Fatalf("pickNextMixed() #%d provider = %q, want %q", index, provider, wantProviders[index])
		}
		if got.ID != wantIDs[index] {
			t.Fatalf("pickNextMixed() #%d auth.ID = %q, want %q", index, got.ID, wantIDs[index])
		}
	}
}

func TestManager_PickNextMixed_SkipsProvidersWithoutExecutors(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["claude"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "gemini-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(gemini-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "claude-a", Provider: "claude"}); errRegister != nil {
		t.Fatalf("Register(claude-a) error = %v", errRegister)
	}

	got, _, provider, errPick := manager.pickNextMixed(context.Background(), []string{"gemini", "claude"}, "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNextMixed() error = %v", errPick)
	}
	if got == nil {
		t.Fatalf("pickNextMixed() auth = nil")
	}
	if provider != "claude" {
		t.Fatalf("pickNextMixed() provider = %q, want %q", provider, "claude")
	}
	if got.ID != "claude-a" {
		t.Fatalf("pickNextMixed() auth.ID = %q, want %q", got.ID, "claude-a")
	}
}

func TestManager_SchedulerTracksMarkResultCooldownAndRecovery(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("auth-a", "gemini", []*registry.ModelInfo{{ID: "test-model"}})
	reg.RegisterClient("auth-b", "gemini", []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		reg.UnregisterClient("auth-a")
		reg.UnregisterClient("auth-b")
	})
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-b", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-b) error = %v", errRegister)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   "auth-a",
		Provider: "gemini",
		Model:    "test-model",
		Success:  false,
		Error:    &Error{HTTPStatus: 429, Message: "quota"},
	})

	got, errPick := manager.scheduler.pickSingle(context.Background(), "gemini", "test-model", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("scheduler.pickSingle() after cooldown error = %v", errPick)
	}
	if got == nil || got.ID != "auth-b" {
		t.Fatalf("scheduler.pickSingle() after cooldown auth = %v, want auth-b", got)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   "auth-a",
		Provider: "gemini",
		Model:    "test-model",
		Success:  true,
	})

	seen := make(map[string]struct{}, 2)
	for index := 0; index < 2; index++ {
		got, errPick = manager.scheduler.pickSingle(context.Background(), "gemini", "test-model", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("scheduler.pickSingle() after recovery #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("scheduler.pickSingle() after recovery #%d auth = nil", index)
		}
		seen[got.ID] = struct{}{}
	}
	if len(seen) != 2 {
		t.Fatalf("len(seen) = %d, want %d", len(seen), 2)
	}
}

// TestManager_PromoteExpiredSkipsPromotedEntry verifies that when an auth is promoted
// from cooldown back to ready, the round-robin cursor advances past it so that other
// ready auths are picked first.
func TestManager_PromoteExpiredSkipsPromotedEntry(t *testing.T) {
	t.Parallel()

	model := t.Name()
	manager, ids := newTestManagerWithModel(t, "openai", model, "auth-a", "auth-b")
	a, b := ids[0], ids[1]
	ctx := context.Background()

	// auth-a picks first (round-robin cursor starts at 0).
	gotFirst, errPick := manager.scheduler.pickSingle(ctx, "openai", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("first pick error = %v", errPick)
	}
	if gotFirst.ID != a {
		t.Fatalf("first pick = %q, want %q", gotFirst.ID, a)
	}

	// Simulate 429 on auth-a → cooldown.
	markQuotaExceeded(t, manager, "openai", a, model)

	// auth-b is the only ready auth.
	gotB, errPick := manager.scheduler.pickSingle(ctx, "openai", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("during cooldown pick error = %v", errPick)
	}
	if gotB.ID != b {
		t.Fatalf("during cooldown pick = %q, want %q", gotB.ID, b)
	}

	// Recover auth-a (advance past cooldown).
	recoverAuthModel(t, manager, "openai", a, model)

	// After promotion, cursor should skip auth-a and pick auth-b.
	gotAfterPromote, errPick := manager.scheduler.pickSingle(ctx, "openai", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("after promote pick error = %v", errPick)
	}
	if gotAfterPromote.ID != b {
		t.Fatalf("after promote pick = %q, want %q (promoted entry should be skipped)", gotAfterPromote.ID, b)
	}
}

// TestManager_RoundRobinContinuesAfterPromote verifies the full round-robin cycle:
// pick → 429 cooldown → promote → skip promoted → rotation resumes.
func TestManager_RoundRobinContinuesAfterPromote(t *testing.T) {
	t.Parallel()

	model := t.Name()
	manager, ids := newTestManagerWithModel(t, "openai", model, "auth-a", "auth-b")
	a, b := ids[0], ids[1]
	ctx := context.Background()
	opts := cliproxyexecutor.Options{}

	// Step 1: Normal round-robin alternates A, B.
	got1, errPick := manager.scheduler.pickSingle(ctx, "openai", model, opts, nil)
	if errPick != nil {
		t.Fatalf("step 1a error = %v", errPick)
	}
	got2, errPick := manager.scheduler.pickSingle(ctx, "openai", model, opts, nil)
	if errPick != nil {
		t.Fatalf("step 1b error = %v", errPick)
	}
	if got1.ID == got2.ID {
		t.Fatalf("expected different auths, both got %q", got1.ID)
	}

	// Step 2: auth-a gets 429 → cooldown.
	markQuotaExceeded(t, manager, "openai", a, model)

	// Step 3: Only auth-b is ready.
	gotB, errPick := manager.scheduler.pickSingle(ctx, "openai", model, opts, nil)
	if errPick != nil {
		t.Fatalf("step 3 error = %v", errPick)
	}
	if gotB.ID != b {
		t.Fatalf("during cooldown = %q, want %q", gotB.ID, b)
	}

	// Step 4: Recover auth-a.
	recoverAuthModel(t, manager, "openai", a, model)

	// Step 5: After promote, should skip auth-a and pick auth-b.
	gotAfterPromote, errPick := manager.scheduler.pickSingle(ctx, "openai", model, opts, nil)
	if errPick != nil {
		t.Fatalf("step 5 error = %v", errPick)
	}
	if gotAfterPromote.ID != b {
		t.Fatalf("after promote = %q, want %q", gotAfterPromote.ID, b)
	}

	// Step 6: After recovery upsert rebuilt indexes, rotation resumes.
	// The next two picks must cover both auths.
	seen := make(map[string]struct{}, 2)
	for i := 0; i < 2; i++ {
		g, err := manager.scheduler.pickSingle(ctx, "openai", model, opts, nil)
		if err != nil {
			t.Fatalf("pickSingle() #%d error = %v", i, err)
		}
		seen[g.ID] = struct{}{}
	}
	if len(seen) != 2 {
		t.Fatalf("rotation after promote: seen %v, want both auths", seen)
	}
}

// TestManager_PromoteWithFillFirstStrategy verifies that promote recovery works
// with the FillFirst strategy. Note: FillFirst always picks the first entry in
// sorted order (ignoring cursor), so after promote it picks the promoted entry
// itself — this is expected fill-first semantics, not round-robin.
func TestManager_PromoteWithFillFirstStrategy(t *testing.T) {
	t.Parallel()

	model := t.Name()
	manager, ids := newTestManagerWithModel(t, "openai", model, "auth-a", "auth-b")
	a, b := ids[0], ids[1]
	// Switch to FillFirst after initial setup (which uses RoundRobin).
	manager.SetSelector(&FillFirstSelector{})

	// Put auth-a in cooldown.
	markQuotaExceeded(t, manager, "openai", a, model)

	// FillFirst picks first ready: auth-b.
	got, errPick := manager.scheduler.pickSingle(context.Background(), "openai", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("fill-first during cooldown error = %v", errPick)
	}
	if got.ID != b {
		t.Fatalf("fill-first during cooldown = %q, want %q", got.ID, b)
	}

	// Recover auth-a.
	recoverAuthModel(t, manager, "openai", a, model)

	// FillFirst always picks first sorted entry regardless of cursor.
	// After recovery, auth-a (sorted first) is picked — this is correct fill-first behavior.
	gotAfter, errPick := manager.scheduler.pickSingle(context.Background(), "openai", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("fill-first after promote error = %v", errPick)
	}
	if gotAfter.ID != a {
		t.Fatalf("fill-first after promote = %q, want %q", gotAfter.ID, a)
	}
}

// TestManager_RepeatedPromoteAlternates runs multiple cooldown/recovery cycles
// to verify that round-robin alternation is preserved consistently.
func TestManager_RepeatedPromoteAlternates(t *testing.T) {
	t.Parallel()

	model := t.Name()
	manager, ids := newTestManagerWithModel(t, "openai", model, "auth-a", "auth-b")
	a, b := ids[0], ids[1]
	ctx := context.Background()
	opts := cliproxyexecutor.Options{}

	for cycle := 0; cycle < 5; cycle++ {
		// Pick an auth (round-robin).
		got, errPick := manager.scheduler.pickSingle(ctx, "openai", model, opts, nil)
		if errPick != nil {
			t.Fatalf("cycle %d: initial pick error = %v", cycle, errPick)
		}
		if got == nil {
			t.Fatalf("cycle %d: pickSingle() = nil", cycle)
		}

		// auth-a gets 429.
		markQuotaExceeded(t, manager, "openai", a, model)

		// Only auth-b available.
		gotB, errPick := manager.scheduler.pickSingle(ctx, "openai", model, opts, nil)
		if errPick != nil {
			t.Fatalf("cycle %d: during cooldown error = %v", cycle, errPick)
		}
		if gotB == nil || gotB.ID != b {
			t.Fatalf("cycle %d: during cooldown = %v, want %q", cycle, gotB, b)
		}

		// Recover auth-a.
		recoverAuthModel(t, manager, "openai", a, model)

		// After promote, should skip auth-a.
		gotAfter, errPick := manager.scheduler.pickSingle(ctx, "openai", model, opts, nil)
		if errPick != nil {
			t.Fatalf("cycle %d: after promote error = %v", cycle, errPick)
		}
		if gotAfter == nil || gotAfter.ID != b {
			t.Fatalf("cycle %d: after promote = %v, want %q", cycle, gotAfter, b)
		}
	}
}

// TestManager_PromoteDoesNotBreakNormalRotation verifies that after the promoted
// entry is skipped once, normal round-robin rotation resumes.
func TestManager_PromoteDoesNotBreakNormalRotation(t *testing.T) {
	t.Parallel()

	model := t.Name()
	manager, ids := newTestManagerWithModel(t, "openai", model, "auth-a", "auth-b")
	a, b := ids[0], ids[1]
	ctx := context.Background()
	opts := cliproxyexecutor.Options{}

	// Normal picks: A, B, A, B...
	got1, errPick := manager.scheduler.pickSingle(ctx, "openai", model, opts, nil)
	if errPick != nil {
		t.Fatalf("initial pick #1 error = %v", errPick)
	}
	got2, errPick := manager.scheduler.pickSingle(ctx, "openai", model, opts, nil)
	if errPick != nil {
		t.Fatalf("initial pick #2 error = %v", errPick)
	}
	if got1.ID == got2.ID {
		t.Fatalf("expected alternation, both got %q", got1.ID)
	}

	// Cooldown + recover auth-a.
	markQuotaExceeded(t, manager, "openai", a, model)
	if _, errPick = manager.scheduler.pickSingle(ctx, "openai", model, opts, nil); errPick != nil {
		t.Fatalf("during cooldown error = %v", errPick)
	}
	recoverAuthModel(t, manager, "openai", a, model)

	// Skip promoted auth-a.
	if _, errPick = manager.scheduler.pickSingle(ctx, "openai", model, opts, nil); errPick != nil {
		t.Fatalf("after promote error = %v", errPick)
	}

	// Normal rotation should resume: next is auth-a, then auth-b.
	gotA, errPick := manager.scheduler.pickSingle(ctx, "openai", model, opts, nil)
	if errPick != nil {
		t.Fatalf("rotation resume #1 error = %v", errPick)
	}
	if gotA.ID != a {
		t.Fatalf("rotation resume #1 = %q, want %q", gotA.ID, a)
	}
	gotB, errPick := manager.scheduler.pickSingle(ctx, "openai", model, opts, nil)
	if errPick != nil {
		t.Fatalf("rotation resume #2 error = %v", errPick)
	}
	if gotB.ID != b {
		t.Fatalf("rotation resume #2 = %q, want %q", gotB.ID, b)
	}
}

// TestManager_PromoteBothAuthsCooldown verifies that when both auths cool down
// and then recover, the scheduler correctly resumes rotation.
func TestManager_PromoteBothAuthsCooldown(t *testing.T) {
	t.Parallel()

	model := t.Name()
	manager, ids := newTestManagerWithModel(t, "openai", model, "auth-a", "auth-b")
	a, b := ids[0], ids[1]
	ctx := context.Background()
	opts := cliproxyexecutor.Options{}

	// Both auths get 429.
	markQuotaExceeded(t, manager, "openai", a, model)
	markQuotaExceeded(t, manager, "openai", b, model)

	// Both cooling — pickSingle should error.
	_, errPick := manager.scheduler.pickSingle(ctx, "openai", model, opts, nil)
	if errPick == nil {
		t.Fatal("pickSingle() with both cooldowns should return error")
	}

	// Recover auth-a only.
	recoverAuthModel(t, manager, "openai", a, model)

	// auth-a is the only ready auth; it must be picked.
	got, errPick := manager.scheduler.pickSingle(ctx, "openai", model, opts, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() after auth-a recover error = %v", errPick)
	}
	if got == nil || got.ID != a {
		t.Fatalf("after auth-a recover = %v, want %q", got, a)
	}

	// Recover auth-b.
	recoverAuthModel(t, manager, "openai", b, model)

	// Both ready; verify rotation produces both auths over two picks.
	seen := make(map[string]struct{}, 2)
	for i := 0; i < 2; i++ {
		g, err := manager.scheduler.pickSingle(ctx, "openai", model, opts, nil)
		if err != nil {
			t.Fatalf("pickSingle() #%d error = %v", i, err)
		}
		if g == nil {
			t.Fatalf("pickSingle() #%d = nil", i)
		}
		seen[g.ID] = struct{}{}
	}
	if len(seen) != 2 {
		t.Fatalf("expected both auths seen, got %v", seen)
	}
}

// newTestManagerWithModel creates a Manager with two registered auths for testing.
// Auth IDs are prefixed with the test name to avoid cross-test interference in the
// shared global registry. Returns the manager and the qualified auth IDs.
func newTestManagerWithModel(t *testing.T, provider, model string, authIDs ...string) (*Manager, []string) {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	prefix := t.Name() + "/"
	qualified := make([]string, len(authIDs))
	for i, id := range authIDs {
		qualified[i] = prefix + id
		reg.RegisterClient(qualified[i], provider, []*registry.ModelInfo{{ID: model}})
	}
	t.Cleanup(func() {
		for _, id := range qualified {
			reg.UnregisterClient(id)
		}
	})
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	for _, id := range qualified {
		if _, err := manager.Register(context.Background(), &Auth{ID: id, Provider: provider}); err != nil {
			t.Fatalf("Register(%s) error = %v", id, err)
		}
	}
	return manager, qualified
}

// markQuotaExceeded simulates a 429 quota error on the given auth/model with a
// 30-second cooldown to avoid timing flakes in tests.
func markQuotaExceeded(t *testing.T, manager *Manager, provider, authID, model string) {
	t.Helper()
	cooldown := 30 * time.Second
	manager.MarkResult(context.Background(), Result{
		AuthID:     authID,
		Provider:   provider,
		Model:      model,
		Success:    false,
		RetryAfter: &cooldown,
		Error:      &Error{HTTPStatus: 429, Message: "quota exceeded"},
	})
}

// recoverAuthModel simulates a successful request recovering the auth/model from cooldown.
func recoverAuthModel(t *testing.T, manager *Manager, provider, authID, model string) {
	t.Helper()
	manager.MarkResult(context.Background(), Result{
		AuthID:   authID,
		Provider: provider,
		Model:    model,
		Success:  true,
	})
}
