package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

func expiringTokenAuth(id, provider string, expiresAt time.Time) *Auth {
	return &Auth{
		ID:       id,
		Provider: provider,
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token": "access-token-" + id,
			"expired":      expiresAt.UTC().Format(time.RFC3339),
		},
	}
}

func readyAuthIDs(shard *modelScheduler) []string {
	var ids []string
	for _, priority := range shard.priorityOrder {
		for _, entry := range shard.readyByPriority[priority].all.flat {
			ids = append(ids, entry.auth.ID)
		}
	}
	return ids
}

func blockedAuthIDs(shard *modelScheduler) []string {
	ids := make([]string, 0, len(shard.blocked))
	for _, entry := range shard.blocked {
		ids = append(ids, fmt.Sprintf("%s/%d/%d", entry.auth.ID, entry.state, entry.nextRetryAt.UnixNano()))
	}
	return ids
}

func schedulerShard(manager *Manager, provider, model string) *modelScheduler {
	manager.scheduler.mu.Lock()
	defer manager.scheduler.mu.Unlock()
	providerState := manager.scheduler.providers[provider]
	if providerState == nil {
		return nil
	}
	return providerState.modelShards[canonicalModelKey(model)]
}

func TestModelScheduler_TokenExpiryScanWaitsForEarliestReadyExpiry(t *testing.T) {
	const model = "large-pool-expiry-model"
	base := time.Now().Truncate(time.Second)
	registerSchedulerModels(t, "gemini", model, "expiry-a", "expiry-b", "expiry-c")
	scheduler := newSchedulerForTest(&RoundRobinSelector{},
		expiringTokenAuth("expiry-a", "gemini", base.Add(10*time.Minute)),
		expiringTokenAuth("expiry-b", "gemini", base.Add(20*time.Minute)),
		&Auth{ID: "expiry-c", Provider: "gemini", Status: StatusActive},
	)

	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	providerState := scheduler.providers["gemini"]

	shard := providerState.ensureModelLocked(model, base)
	if got, want := shard.readyTokenExpiry, base.Add(10*time.Minute); !got.Equal(want) {
		t.Fatalf("readyTokenExpiry = %v, want %v", got, want)
	}

	shard = providerState.ensureModelLocked(model, base.Add(5*time.Minute))
	if got, want := readyAuthIDs(shard), []string{"expiry-a", "expiry-b", "expiry-c"}; !slices.Equal(got, want) {
		t.Fatalf("ready before the earliest expiry = %v, want %v", got, want)
	}

	shard = providerState.ensureModelLocked(model, base.Add(15*time.Minute))
	if got, want := readyAuthIDs(shard), []string{"expiry-b", "expiry-c"}; !slices.Equal(got, want) {
		t.Fatalf("ready after the first expiry = %v, want %v", got, want)
	}
	if got, want := shard.readyTokenExpiry, base.Add(20*time.Minute); !got.Equal(want) {
		t.Fatalf("readyTokenExpiry after the first expiry = %v, want %v", got, want)
	}

	shard = providerState.ensureModelLocked(model, base.Add(25*time.Minute))
	if got, want := readyAuthIDs(shard), []string{"expiry-c"}; !slices.Equal(got, want) {
		t.Fatalf("ready after the last expiry = %v, want %v", got, want)
	}
	if !shard.readyTokenExpiry.IsZero() {
		t.Fatalf("readyTokenExpiry = %v, want zero once no ready token can expire", shard.readyTokenExpiry)
	}
}

func TestModelScheduler_RefreshedTokenSurvivesStaleExpiryBound(t *testing.T) {
	const model = "large-pool-refresh-model"
	base := time.Now().Truncate(time.Second)
	registerSchedulerModels(t, "gemini", model, "refresh-a")
	scheduler := newSchedulerForTest(&RoundRobinSelector{}, expiringTokenAuth("refresh-a", "gemini", base.Add(10*time.Minute)))

	scheduler.mu.Lock()
	scheduler.providers["gemini"].ensureModelLocked(model, base)
	scheduler.mu.Unlock()

	refreshed := expiringTokenAuth("refresh-a", "gemini", base.Add(2*time.Hour))
	refreshed.Generation = 2
	scheduler.upsertAuthResult(refreshed, []string{model}, false)

	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	providerState := scheduler.providers["gemini"]
	if got, want := providerState.modelShards[model].readyTokenExpiry, base.Add(10*time.Minute); !got.Equal(want) {
		t.Fatalf("readyTokenExpiry after an in-place refresh = %v, want the conservative %v", got, want)
	}

	shard := providerState.ensureModelLocked(model, base.Add(15*time.Minute))
	if got, want := readyAuthIDs(shard), []string{"refresh-a"}; !slices.Equal(got, want) {
		t.Fatalf("ready after the stale bound passed = %v, want %v", got, want)
	}
	if got, want := shard.readyTokenExpiry, base.Add(2*time.Hour); !got.Equal(want) {
		t.Fatalf("readyTokenExpiry after rescanning = %v, want %v", got, want)
	}
}

func TestProviderScheduler_EnsureModelLockedMatchesIncrementalIndexing(t *testing.T) {
	const model = "large-pool-index-model"
	now := time.Now()
	ids := make([]string, 0, 40)
	auths := make([]*Auth, 0, 40)
	for i := range 40 {
		id := fmt.Sprintf("index-%02d", i)
		auth := &Auth{
			ID:         id,
			Provider:   "gemini",
			Status:     StatusActive,
			Attributes: map[string]string{"priority": strconv.Itoa(i % 3)},
		}
		switch i % 4 {
		case 1:
			auth.ModelStates = map[string]*ModelState{model: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: now.Add(time.Duration(i) * time.Minute),
			}}
		case 2:
			auth.ModelStates = map[string]*ModelState{model: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: now.Add(5 * time.Minute),
				Quota:          QuotaState{Exceeded: true, NextRecoverAt: now.Add(5 * time.Minute)},
			}}
		case 3:
			auth.Metadata = map[string]any{
				"access_token": "access-token-" + id,
				"expired":      now.Add(time.Duration(i) * time.Hour).UTC().Format(time.RFC3339),
			}
		}
		ids = append(ids, id)
		auths = append(auths, auth)
	}
	registerSchedulerModels(t, "gemini", model, ids...)
	scheduler := newSchedulerForTest(&RoundRobinSelector{}, auths...)

	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	providerState := scheduler.providers["gemini"]
	batched := providerState.ensureModelLocked(model, now)

	incremental := &modelScheduler{
		modelKey:        batched.modelKey,
		entries:         make(map[string]*scheduledAuth),
		readyByPriority: make(map[int]*readyBucket),
	}
	for _, id := range ids {
		incremental.upsertEntryLocked(providerState.auths[id], now)
	}

	if !slices.Equal(batched.priorityOrder, incremental.priorityOrder) {
		t.Fatalf("priorityOrder = %v, want %v", batched.priorityOrder, incremental.priorityOrder)
	}
	if got, want := readyAuthIDs(batched), readyAuthIDs(incremental); !slices.Equal(got, want) {
		t.Fatalf("ready order = %v, want %v", got, want)
	}
	if got, want := blockedAuthIDs(batched), blockedAuthIDs(incremental); !slices.Equal(got, want) {
		t.Fatalf("blocked order = %v, want %v", got, want)
	}
	if !batched.readyTokenExpiry.Equal(incremental.readyTokenExpiry) {
		t.Fatalf("readyTokenExpiry = %v, want %v", batched.readyTokenExpiry, incremental.readyTokenExpiry)
	}
}

func TestManagerReconcileScheduler_KeepsInSyncShardsAndRotation(t *testing.T) {
	ctx := context.Background()
	const model = "reconcile-rotation-model"
	ids := []string{"reconcile-a", "reconcile-b", "reconcile-c"}
	registerSchedulerModels(t, "gemini", model, ids...)
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "gemini"})
	for _, id := range ids {
		if _, errRegister := manager.Register(ctx, &Auth{ID: id, Provider: "gemini", Status: StatusActive}); errRegister != nil {
			t.Fatalf("register %s: %v", id, errRegister)
		}
	}

	first, _, errPick := manager.pickNext(ctx, "gemini", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil || first == nil || first.ID != "reconcile-a" {
		t.Fatalf("first pick = %v, %v; want reconcile-a", first, errPick)
	}
	shardBefore := schedulerShard(manager, "gemini", model)

	manager.reconcileScheduler()

	if shardAfter := schedulerShard(manager, "gemini", model); shardAfter != shardBefore {
		t.Fatal("reconcileScheduler rebuilt a shard whose auths were already in sync")
	}
	second, _, errPick := manager.pickNext(ctx, "gemini", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil || second == nil || second.ID != "reconcile-b" {
		t.Fatalf("pick after reconcile = %v, %v; want rotation to continue at reconcile-b", second, errPick)
	}
}

func TestManagerPickNext_HealsSchedulerEntryWithModelsRegisteredLater(t *testing.T) {
	ctx := context.Background()
	const model = "reconcile-late-model"
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "gemini"})
	if _, errRegister := manager.Register(ctx, &Auth{ID: "reconcile-late", Provider: "gemini", Status: StatusActive}); errRegister != nil {
		t.Fatalf("register: %v", errRegister)
	}
	// Models registered without RefreshSchedulerEntry leave the scheduler entry with an empty model set.
	registerSchedulerModels(t, "gemini", model, "reconcile-late")

	got, _, errPick := manager.pickNext(ctx, "gemini", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil || got == nil || got.ID != "reconcile-late" {
		t.Fatalf("pickNext() = %v, %v; want the failed pick to reconcile and select reconcile-late", got, errPick)
	}
}

func TestManagerReconcileScheduler_DropsAuthsUnknownToManager(t *testing.T) {
	ctx := context.Background()
	const model = "reconcile-ghost-model"
	registerSchedulerModels(t, "gemini", model, "reconcile-ghost")
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.scheduler.upsertAuth(&Auth{ID: "reconcile-ghost", Provider: "gemini", Status: StatusActive})
	if got, errPick := manager.scheduler.pickSingle(ctx, "gemini", model, cliproxyexecutor.Options{}, nil); errPick != nil || got == nil {
		t.Fatalf("pickSingle() before reconcile = %v, %v; want the scheduler-only auth", got, errPick)
	}

	manager.reconcileScheduler()

	if got, errPick := manager.scheduler.pickSingle(ctx, "gemini", model, cliproxyexecutor.Options{}, nil); errPick == nil {
		t.Fatalf("pickSingle() after reconcile = %v; want no auth once the manager does not hold it", got)
	}
}

func TestManagerReconcileScheduler_ReappliesAuthsChangedBehindScheduler(t *testing.T) {
	ctx := context.Background()
	const model = "reconcile-diverged-model"
	registerSchedulerModels(t, "gemini", model, "reconcile-disabled", "reconcile-kept")
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "gemini"})
	for _, id := range []string{"reconcile-disabled", "reconcile-kept"} {
		if _, errRegister := manager.Register(ctx, &Auth{ID: id, Provider: "gemini", Status: StatusActive}); errRegister != nil {
			t.Fatalf("register %s: %v", id, errRegister)
		}
	}
	if _, errPick := manager.scheduler.pickSingle(ctx, "gemini", model, cliproxyexecutor.Options{}, nil); errPick != nil {
		t.Fatalf("warm pickSingle(): %v", errPick)
	}

	manager.mu.Lock()
	current := manager.auths["reconcile-disabled"]
	current.Disabled = true
	current.Generation++
	manager.mu.Unlock()

	manager.reconcileScheduler()

	for range 4 {
		got, errPick := manager.scheduler.pickSingle(ctx, "gemini", model, cliproxyexecutor.Options{}, nil)
		if errPick != nil || got == nil || got.ID != "reconcile-kept" {
			t.Fatalf("pickSingle() = %v, %v; want only reconcile-kept after the disabled auth is reapplied", got, errPick)
		}
	}
}

func TestManagerPickNext_ExhaustedPoolKeepsShardsAcrossFailedPicks(t *testing.T) {
	ctx := context.Background()
	const model = "reconcile-exhausted-model"
	ids := []string{"exhausted-a", "exhausted-b", "exhausted-c"}
	registerSchedulerModels(t, "gemini", model, ids...)
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "gemini"})
	retryAfter := time.Hour
	for _, id := range ids {
		if _, errRegister := manager.Register(ctx, &Auth{ID: id, Provider: "gemini", Status: StatusActive}); errRegister != nil {
			t.Fatalf("register %s: %v", id, errRegister)
		}
		manager.MarkResult(ctx, Result{
			AuthID:     id,
			Provider:   "gemini",
			Model:      model,
			RouteModel: model,
			RetryAfter: &retryAfter,
			Error:      &Error{Code: "rate_limited", Message: "quota exhausted", HTTPStatus: http.StatusTooManyRequests},
		})
	}

	_, _, errFirst := manager.pickNext(ctx, "gemini", model, cliproxyexecutor.Options{}, nil)
	var cooldownErr *modelCooldownError
	if !errors.As(errFirst, &cooldownErr) {
		t.Fatalf("pickNext() error = %v, want a model cooldown error", errFirst)
	}
	shardBefore := schedulerShard(manager, "gemini", model)

	_, _, errSecond := manager.pickNext(ctx, "gemini", model, cliproxyexecutor.Options{}, nil)
	if !errors.As(errSecond, &cooldownErr) {
		t.Fatalf("second pickNext() error = %v, want a model cooldown error", errSecond)
	}
	if shardAfter := schedulerShard(manager, "gemini", model); shardAfter == nil || shardAfter != shardBefore {
		t.Fatal("a failed pick on an exhausted pool rebuilt the scheduler shard")
	}
}

func TestManagerReconcileScheduler_ConvergesUnderConcurrentLifecycle(t *testing.T) {
	ctx := context.Background()
	const model = "reconcile-stress-model"
	const poolSize = 12
	const iterations = 300
	ids := make([]string, 0, poolSize)
	for i := range poolSize {
		ids = append(ids, fmt.Sprintf("reconcile-stress-%02d", i))
	}
	registerSchedulerModels(t, "gemini", model, ids...)
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "gemini"})
	for _, id := range ids {
		if _, errRegister := manager.Register(ctx, expiringTokenAuth(id, "gemini", time.Now().Add(time.Hour))); errRegister != nil {
			t.Fatalf("register %s: %v", id, errRegister)
		}
	}
	previousOutput := log.StandardLogger().Out
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(previousOutput) })

	var wg sync.WaitGroup
	run := func(step func(i int)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range iterations {
				step(i)
			}
		}()
	}
	run(func(int) {
		_, _, _ = manager.pickNext(ctx, "gemini", model, cliproxyexecutor.Options{}, nil)
	})
	run(func(int) {
		_, _, _, _ = manager.pickNextMixed(ctx, []string{"gemini"}, model, cliproxyexecutor.Options{}, nil)
	})
	run(func(i int) {
		result := Result{AuthID: ids[i%poolSize], Provider: "gemini", Model: model, RouteModel: model, Success: i%2 == 0}
		if !result.Success {
			retryAfter := time.Duration(i%3) * time.Millisecond
			result.RetryAfter = &retryAfter
			result.Error = &Error{Code: "rate_limited", Message: "rate limited", HTTPStatus: http.StatusTooManyRequests}
		}
		manager.MarkResult(ctx, result)
	})
	run(func(int) {
		manager.reconcileScheduler()
	})
	run(func(i int) {
		current, ok := manager.GetByID(ids[(i*5)%poolSize])
		if !ok {
			return
		}
		current.Metadata["expired"] = time.Now().Add(time.Hour + time.Duration(i)*time.Second).UTC().Format(time.RFC3339)
		_, _ = manager.Update(ctx, current)
	})
	run(func(i int) {
		id := ids[(i*7)%poolSize]
		if i%2 == 0 {
			manager.Remove(ctx, id)
			return
		}
		_, _ = manager.Register(ctx, expiringTokenAuth(id, "gemini", time.Now().Add(time.Hour)))
	})
	wg.Wait()

	manager.reconcileScheduler()

	reg := registry.GetGlobalRegistry()
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	states := make([]schedulerAuthState, 0, len(manager.auths))
	for _, auth := range manager.auths {
		states = append(states, newSchedulerAuthState(auth, reg))
	}
	if divergent := manager.scheduler.divergentAuthStates(states); len(divergent) != 0 {
		t.Fatalf("scheduler diverges from %d manager auths after reconcile", len(divergent))
	}
	manager.scheduler.mu.Lock()
	defer manager.scheduler.mu.Unlock()
	for authID := range manager.scheduler.authProviders {
		if _, ok := manager.auths[authID]; !ok {
			t.Fatalf("scheduler still schedules %s after the manager removed it", authID)
		}
	}
}
