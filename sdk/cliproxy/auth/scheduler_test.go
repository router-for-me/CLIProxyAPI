package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
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

func TestSchedulerPick_GeminiVirtualParentRegroupsAfterPlainAuthRemoval(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "plain", Provider: "gemini-cli", Attributes: map[string]string{"priority": "0"}},
		&Auth{ID: "group-a", Provider: "gemini-cli", Attributes: map[string]string{"priority": "0", "gemini_virtual_parent": "parent-a"}},
		&Auth{ID: "group-b", Provider: "gemini-cli", Attributes: map[string]string{"priority": "0", "gemini_virtual_parent": "parent-b"}},
	)

	scheduler.removeAuth("plain")

	wantIDs := []string{"group-a", "group-b", "group-a", "group-b"}
	for index, wantID := range wantIDs {
		got, errPick := scheduler.pickSingle(context.Background(), "gemini-cli", "", cliproxyexecutor.Options{}, nil)
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

func TestSchedulerPick_MixedSingleCodexProviderWebsocketPrefersWebsocketEnabledSubset(t *testing.T) {
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
		got, provider, errPick := scheduler.pickMixed(ctx, []string{"codex"}, "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickMixed() #%d error = %v", index, errPick)
		}
		if got == nil {
			t.Fatalf("pickMixed() #%d auth = nil", index)
		}
		if provider != "codex" {
			t.Fatalf("pickMixed() #%d provider = %q, want %q", index, provider, "codex")
		}
		if got.ID != wantID {
			t.Fatalf("pickMixed() #%d auth.ID = %q, want %q", index, got.ID, wantID)
		}
	}
}

func TestSchedulerPick_CodexWebsocketRetryFallsBackToHTTPAuths(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "codex-http", Provider: "codex"},
		&Auth{ID: "codex-ws-a", Provider: "codex", Attributes: map[string]string{"websockets": "true"}},
		&Auth{ID: "codex-ws-b", Provider: "codex", Attributes: map[string]string{"websockets": "true"}},
	)

	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	tried := map[string]struct{}{
		"codex-ws-a": {},
		"codex-ws-b": {},
	}
	got, errPick := scheduler.pickSingle(ctx, "codex", "", cliproxyexecutor.Options{}, tried)
	if errPick != nil {
		t.Fatalf("pickSingle() error = %v", errPick)
	}
	if got == nil {
		t.Fatal("pickSingle() auth = nil")
	}
	if got.ID != "codex-http" {
		t.Fatalf("pickSingle() auth.ID = %q, want %q", got.ID, "codex-http")
	}
}

func TestSchedulerPick_MixedSingleCodexProviderWebsocketRetryFallsBackToHTTPAuths(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "codex-http", Provider: "codex"},
		&Auth{ID: "codex-ws-a", Provider: "codex", Attributes: map[string]string{"websockets": "true"}},
		&Auth{ID: "codex-ws-b", Provider: "codex", Attributes: map[string]string{"websockets": "true"}},
	)

	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	tried := map[string]struct{}{
		"codex-ws-a": {},
		"codex-ws-b": {},
	}
	got, provider, errPick := scheduler.pickMixed(ctx, []string{"codex"}, "", cliproxyexecutor.Options{}, tried)
	if errPick != nil {
		t.Fatalf("pickMixed() error = %v", errPick)
	}
	if got == nil {
		t.Fatal("pickMixed() auth = nil")
	}
	if provider != "codex" {
		t.Fatalf("pickMixed() provider = %q, want %q", provider, "codex")
	}
	if got.ID != "codex-http" {
		t.Fatalf("pickMixed() auth.ID = %q, want %q", got.ID, "codex-http")
	}
}

func TestSchedulerPick_PromotesAllExpiredBlockedEntriesBeforeApplyingTriedFilter(t *testing.T) {
	t.Parallel()

	model := "gemini-2.5-pro"
	registerSchedulerModels(t, "gemini", model, "expired-a", "expired-b")
	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{
			ID:       "expired-a",
			Provider: "gemini",
			ModelStates: map[string]*ModelState{
				model: {Status: StatusError, Unavailable: true, NextRetryAfter: time.Now().Add(1 * time.Minute)},
			},
		},
		&Auth{
			ID:       "expired-b",
			Provider: "gemini",
			ModelStates: map[string]*ModelState{
				model: {Status: StatusError, Unavailable: true, NextRetryAfter: time.Now().Add(1 * time.Minute)},
			},
		},
	)

	scheduler.mu.Lock()
	providerState := scheduler.providers["gemini"]
	if providerState == nil {
		scheduler.mu.Unlock()
		t.Fatal("provider state missing")
	}
	shard := providerState.ensureModelLocked(model, time.Now())
	if shard == nil {
		scheduler.mu.Unlock()
		t.Fatal("model shard missing")
	}
	expiredAt := time.Now().Add(-1 * time.Second)
	for _, authID := range []string{"expired-a", "expired-b"} {
		entry := shard.entries[authID]
		if entry == nil || entry.auth == nil {
			scheduler.mu.Unlock()
			t.Fatalf("entry %q missing auth", authID)
		}
		if entry.auth.ModelStates == nil {
			entry.auth.ModelStates = make(map[string]*ModelState)
		}
		if entry.auth.ModelStates[model] == nil {
			entry.auth.ModelStates[model] = &ModelState{Status: StatusError, Unavailable: true}
		}
		entry.auth.ModelStates[model].NextRetryAfter = expiredAt
		entry.nextRetryAt = expiredAt
	}
	scheduler.mu.Unlock()

	got, errPick := scheduler.pickSingle(context.Background(), "gemini", model, cliproxyexecutor.Options{}, map[string]struct{}{"expired-a": {}})
	if errPick != nil {
		t.Fatalf("pickSingle() error = %v", errPick)
	}
	if got == nil {
		t.Fatal("pickSingle() auth = nil")
	}
	if got.ID != "expired-b" {
		t.Fatalf("pickSingle() auth.ID = %q, want %q", got.ID, "expired-b")
	}

	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	providerState = scheduler.providers["gemini"]
	shard = providerState.ensureModelLocked(model, time.Now())
	if len(shard.blocked) != 0 {
		t.Fatalf("blocked entries after promotion = %d, want 0", len(shard.blocked))
	}
}

func TestSchedulerPick_PromotesExpiredBlockedPrefixWithoutTouchingFutureTail(t *testing.T) {
	t.Parallel()

	model := "gemini-2.5-pro"
	expiredAID := "prefix-expired-a"
	expiredBID := "prefix-expired-b"
	futureCID := "prefix-future-c"
	registerSchedulerModels(t, "gemini", model, expiredAID, expiredBID, futureCID)
	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{
			ID:       expiredAID,
			Provider: "gemini",
			ModelStates: map[string]*ModelState{
				model: {Status: StatusError, Unavailable: true, NextRetryAfter: time.Now().Add(1 * time.Minute)},
			},
		},
		&Auth{
			ID:       expiredBID,
			Provider: "gemini",
			ModelStates: map[string]*ModelState{
				model: {Status: StatusError, Unavailable: true, NextRetryAfter: time.Now().Add(1 * time.Minute)},
			},
		},
		&Auth{
			ID:       futureCID,
			Provider: "gemini",
			ModelStates: map[string]*ModelState{
				model: {Status: StatusError, Unavailable: true, NextRetryAfter: time.Now().Add(5 * time.Minute)},
			},
		},
	)

	scheduler.mu.Lock()
	providerState := scheduler.providers["gemini"]
	if providerState == nil {
		scheduler.mu.Unlock()
		t.Fatal("provider state missing")
	}
	shard := providerState.ensureModelLocked(model, time.Now())
	if shard == nil {
		scheduler.mu.Unlock()
		t.Fatal("model shard missing")
	}
	expiredAt := time.Now().Add(-1 * time.Second)
	for _, authID := range []string{expiredAID, expiredBID} {
		entry := shard.entries[authID]
		if entry == nil || entry.auth == nil {
			scheduler.mu.Unlock()
			t.Fatalf("entry %q missing auth", authID)
		}
		if entry.auth.ModelStates == nil {
			entry.auth.ModelStates = make(map[string]*ModelState)
		}
		if entry.auth.ModelStates[model] == nil {
			entry.auth.ModelStates[model] = &ModelState{Status: StatusError, Unavailable: true}
		}
		entry.auth.ModelStates[model].NextRetryAfter = expiredAt
		entry.nextRetryAt = expiredAt
	}
	futureEntry := shard.entries[futureCID]
	if futureEntry == nil || futureEntry.auth == nil {
		scheduler.mu.Unlock()
		t.Fatal("future entry missing")
	}
	expectedFutureRetry := futureEntry.nextRetryAt
	scheduler.mu.Unlock()

	got, errPick := scheduler.pickSingle(context.Background(), "gemini", model, cliproxyexecutor.Options{}, map[string]struct{}{expiredAID: {}})
	if errPick != nil {
		t.Fatalf("pickSingle() error = %v", errPick)
	}
	if got == nil {
		t.Fatal("pickSingle() auth = nil")
	}
	if got.ID != expiredBID {
		t.Fatalf("pickSingle() auth.ID = %q, want %q", got.ID, expiredBID)
	}

	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	providerState = scheduler.providers["gemini"]
	shard = providerState.ensureModelLocked(model, time.Now())
	if len(shard.blocked) != 1 {
		t.Fatalf("blocked entries after promotion = %d, want 1", len(shard.blocked))
	}
	if shard.blocked[0] == nil || shard.blocked[0].auth == nil || shard.blocked[0].auth.ID != futureCID {
		t.Fatalf("blocked tail auth = %#v, want %s", shard.blocked[0], futureCID)
	}
	if !shard.blocked[0].nextRetryAt.Equal(expectedFutureRetry) {
		t.Fatalf("blocked tail retry = %v, want %v", shard.blocked[0].nextRetryAt, expectedFutureRetry)
	}
}

func TestSchedulerApplyModelStateUpdate_FallsBackWhenAuthAggregateStateWouldChange(t *testing.T) {
	t.Parallel()

	model := "gpt-5.4"
	registerSchedulerModels(t, "codex", model, "codex-fast-path")
	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "codex-fast-path", Provider: "codex", Status: StatusActive},
	)
	scheduler.mu.Lock()
	if providerState := scheduler.providers["codex"]; providerState != nil {
		_ = providerState.ensureModelLocked(model, time.Now())
	}
	scheduler.mu.Unlock()

	applied := scheduler.applyModelStateUpdate("codex-fast-path", "codex", model, &ModelState{
		Status:         StatusError,
		Unavailable:    true,
		NextRetryAfter: time.Now().Add(1 * time.Minute),
	})
	if applied {
		t.Fatal("applyModelStateUpdate() = true, want false so caller can fall back to full auth upsert")
	}
}

func TestSchedulerApplyModelStateUpdate_UsesFastPathWhenAggregateStateStaysStable(t *testing.T) {
	t.Parallel()

	modelA := "gpt-5.4"
	modelB := "gpt-5.4-mini"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("codex-stable-fast-path", "codex", []*registry.ModelInfo{{ID: modelA}, {ID: modelB}})
	t.Cleanup(func() {
		reg.UnregisterClient("codex-stable-fast-path")
	})
	retryAt := time.Now().Add(2 * time.Minute)
	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{
			ID:             "codex-stable-fast-path",
			Provider:       "codex",
			Status:         StatusError,
			Unavailable:    true,
			NextRetryAfter: retryAt,
			Quota: QuotaState{
				Exceeded:      true,
				Reason:        "quota",
				NextRecoverAt: retryAt,
				BackoffLevel:  2,
				StrikeCount:   4,
			},
			ModelStates: map[string]*ModelState{
				modelA: {Status: StatusActive},
				modelB: {
					Status:         StatusError,
					Unavailable:    true,
					NextRetryAfter: retryAt,
					Quota: QuotaState{
						Exceeded:      true,
						Reason:        "quota",
						NextRecoverAt: retryAt,
						BackoffLevel:  2,
						StrikeCount:   4,
					},
				},
			},
		},
	)
	scheduler.mu.Lock()
	if providerState := scheduler.providers["codex"]; providerState != nil {
		_ = providerState.ensureModelLocked(modelA, time.Now())
		_ = providerState.ensureModelLocked(modelB, time.Now())
	}
	scheduler.mu.Unlock()

	applied := scheduler.applyModelStateUpdate("codex-stable-fast-path", "codex", modelA, &ModelState{
		Status:         StatusError,
		Unavailable:    true,
		NextRetryAfter: time.Now().Add(5 * time.Minute),
	})
	if !applied {
		t.Fatal("applyModelStateUpdate() = false, want true when aggregate state remains unchanged")
	}

	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	providerState := scheduler.providers["codex"]
	if providerState == nil {
		t.Fatal("provider state missing")
	}
	shard := providerState.ensureModelLocked(modelA, time.Now())
	if shard == nil {
		t.Fatal("model shard missing")
	}
	entry := shard.entries["codex-stable-fast-path"]
	if entry == nil || entry.auth == nil {
		t.Fatal("entry missing after fast path update")
	}
	if entry.state != scheduledStateBlocked {
		t.Fatalf("entry.state = %v, want %v", entry.state, scheduledStateBlocked)
	}
	if entry.auth.Status != StatusError || !entry.auth.Unavailable {
		t.Fatalf("auth aggregate unexpectedly changed: status=%v unavailable=%v", entry.auth.Status, entry.auth.Unavailable)
	}
	if !entry.auth.NextRetryAfter.Equal(retryAt) {
		t.Fatalf("auth.NextRetryAfter = %v, want %v", entry.auth.NextRetryAfter, retryAt)
	}
}

func TestSchedulerPick_ReturnsDetachedClone(t *testing.T) {
	t.Parallel()

	model := "gemini-2.5-pro"
	registerSchedulerModels(t, "gemini", model, "detached")
	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{
			ID:       "detached",
			Provider: "gemini",
			Attributes: map[string]string{
				"tag": "original",
			},
			ModelStates: map[string]*ModelState{
				model: &ModelState{Status: StatusActive},
			},
		},
	)

	got, errPick := scheduler.pickSingle(context.Background(), "gemini", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() error = %v", errPick)
	}
	if got == nil {
		t.Fatal("pickSingle() auth = nil")
	}

	got.Attributes["tag"] = "mutated"
	got.ModelStates[model].Status = StatusError
	got.ModelStates[model].Unavailable = true
	got.ModelStates["other"] = &ModelState{Status: StatusDisabled}

	gotAgain, errPick := scheduler.pickSingle(context.Background(), "gemini", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() second error = %v", errPick)
	}
	if gotAgain == nil {
		t.Fatal("pickSingle() second auth = nil")
	}
	if gotAgain.Attributes["tag"] != "original" {
		t.Fatalf("pickSingle() returned shared Attributes map, got %q", gotAgain.Attributes["tag"])
	}
	state := gotAgain.ModelStates[model]
	if state == nil {
		t.Fatalf("pickSingle() missing model state for %q", model)
	}
	if state.Status != StatusActive || state.Unavailable {
		t.Fatalf("pickSingle() returned shared model state: %+v", state)
	}
	if _, ok := gotAgain.ModelStates["other"]; ok {
		t.Fatal("pickSingle() returned shared ModelStates map")
	}
}

func TestManager_Register_SchedulerSnapshotDetachedFromManagerState(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	model := "scheduler-detached-model"
	registerSchedulerModels(t, "gemini", model, "manager-detached")

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	_, errRegister := manager.Register(ctx, &Auth{
		ID:       "manager-detached",
		Provider: "gemini",
		Attributes: map[string]string{
			"tag": "original",
		},
		ModelStates: map[string]*ModelState{
			model: &ModelState{Status: StatusActive},
		},
	})
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	manager.mu.Lock()
	current := manager.auths["manager-detached"]
	if current == nil {
		manager.mu.Unlock()
		t.Fatal("manager auth missing after register")
	}
	current.Attributes["tag"] = "manager-mutated"
	current.ModelStates[model] = &ModelState{
		Status:         StatusError,
		Unavailable:    true,
		NextRetryAfter: time.Now().Add(1 * time.Minute),
	}
	manager.auths["manager-detached"] = current
	manager.mu.Unlock()

	got, errPick := manager.scheduler.pickSingle(ctx, "gemini", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("scheduler.pickSingle() error = %v", errPick)
	}
	if got == nil {
		t.Fatal("scheduler.pickSingle() auth = nil")
	}
	if got.Attributes["tag"] != "original" {
		t.Fatalf("scheduler snapshot mutated with manager state, got %q", got.Attributes["tag"])
	}
	state := got.ModelStates[model]
	if state == nil {
		t.Fatalf("scheduler.pickSingle() missing model state for %q", model)
	}
	if state.Status != StatusActive || state.Unavailable {
		t.Fatalf("scheduler snapshot unexpectedly changed: %+v", state)
	}
}

func TestSchedulerPick_MixedProvidersUsesProviderRotationOverReadyCandidates(t *testing.T) {
	t.Parallel()

	scheduler := newSchedulerForTest(
		&RoundRobinSelector{},
		&Auth{ID: "gemini-a", Provider: "gemini"},
		&Auth{ID: "gemini-b", Provider: "gemini"},
		&Auth{ID: "claude-a", Provider: "claude"},
	)

	wantProviders := []string{"gemini", "claude", "gemini", "claude"}
	wantIDs := []string{"gemini-a", "claude-a", "gemini-b", "claude-a"}
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

func TestManager_PickNextMixed_UsesProviderRotationBeforeCredentialRotation(t *testing.T) {
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

	wantProviders := []string{"gemini", "claude", "gemini", "claude"}
	wantIDs := []string{"gemini-a", "claude-a", "gemini-b", "claude-a"}
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

	wantProviders := []string{"gemini", "claude", "gemini", "claude"}
	wantIDs := []string{"gemini-a", "claude-a", "gemini-b", "claude-a"}
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
