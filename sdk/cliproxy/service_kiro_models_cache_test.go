package cliproxy

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type kiroModelsTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *kiroModelsTestClock) current() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *kiroModelsTestClock) advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

func newKiroModelsTestCache() (*kiroModelsCache, *kiroModelsTestClock) {
	clock := &kiroModelsTestClock{now: time.Unix(1_700_000_000, 0)}
	return &kiroModelsCache{now: clock.current}, clock
}

func TestKiroModelsCacheScopesCatalogsByAuthIdentity(t *testing.T) {
	cache, _ := newKiroModelsTestCache()
	authEast := &coreauth.Auth{
		ID: "auth-east",
		Attributes: map[string]string{
			"profile_arn": "arn:aws:codewhisperer:us-east-1:111111111111:profile/east",
		},
	}
	authWest := &coreauth.Auth{
		ID: "auth-west",
		Attributes: map[string]string{
			"profile_arn": "arn:aws:codewhisperer:us-west-2:222222222222:profile/west",
		},
	}
	authEastRebound := &coreauth.Auth{
		ID: "auth-east",
		Attributes: map[string]string{
			"profile_arn": "arn:aws:codewhisperer:eu-central-1:333333333333:profile/rebound",
		},
	}

	var calls atomic.Int32
	fetch := func(modelID string) func(context.Context) ([]*ModelInfo, error) {
		return func(context.Context) ([]*ModelInfo, error) {
			calls.Add(1)
			return []*ModelInfo{{ID: modelID}}, nil
		}
	}

	east, err := cache.get(context.Background(), kiroModelsCacheKey(authEast), fetch("east-model"))
	if err != nil {
		t.Fatalf("east fetch: %v", err)
	}
	west, err := cache.get(context.Background(), kiroModelsCacheKey(authWest), fetch("west-model"))
	if err != nil {
		t.Fatalf("west fetch: %v", err)
	}
	if got := east[0].ID; got != "east-model" {
		t.Fatalf("east catalog model = %q, want east-model", got)
	}
	if got := west[0].ID; got != "west-model" {
		t.Fatalf("west catalog model = %q, want west-model", got)
	}
	rebound, err := cache.get(context.Background(), kiroModelsCacheKey(authEastRebound), fetch("rebound-model"))
	if err != nil {
		t.Fatalf("rebound fetch: %v", err)
	}
	if got := rebound[0].ID; got != "rebound-model" {
		t.Fatalf("rebound catalog model = %q, want rebound-model", got)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("upstream calls = %d, want one per auth and entitlement identity", got)
	}
}

func TestKiroModelsCacheKeySeparatesProfilelessCredentialRebind(t *testing.T) {
	first := &coreauth.Auth{
		ID:       "kiro-builder.json",
		Metadata: map[string]any{"client_id": "builder-client-a", "region": "us-east-1"},
	}
	second := &coreauth.Auth{
		ID:       "kiro-builder.json",
		Metadata: map[string]any{"client_id": "builder-client-b", "region": "us-east-1"},
	}
	if firstKey, secondKey := kiroModelsCacheKey(first), kiroModelsCacheKey(second); firstKey == secondKey {
		t.Fatalf("profileless credential rebind reused cache key %q", firstKey)
	}
}

func TestKiroModelsCacheCoalescesConcurrentSuccess(t *testing.T) {
	cache, _ := newKiroModelsTestCache()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	fetch := func(context.Context) ([]*ModelInfo, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return []*ModelInfo{{ID: "kiro-model"}}, nil
	}

	results, errs := runConcurrentKiroCacheGets(t, cache, "auth-a", fetch, started, release)
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1", got)
	}
	for i := range results {
		if errs[i] != nil || len(results[i]) != 1 || results[i][0].ID != "kiro-model" {
			t.Fatalf("caller %d: models=%v err=%v", i, results[i], errs[i])
		}
	}
}

func TestKiroModelsCacheCoalescesConcurrentFailure(t *testing.T) {
	cache, _ := newKiroModelsTestCache()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	wantErr := errors.New("upstream unavailable")
	fetch := func(context.Context) ([]*ModelInfo, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return nil, wantErr
	}

	results, errs := runConcurrentKiroCacheGets(t, cache, "auth-a", fetch, started, release)
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1", got)
	}
	for i := range results {
		if results[i] != nil || !errors.Is(errs[i], wantErr) {
			t.Fatalf("caller %d: models=%v err=%v, want shared failure", i, results[i], errs[i])
		}
	}
}

func TestKiroModelsCacheAllowsDifferentKeysToFetchConcurrently(t *testing.T) {
	cache, _ := newKiroModelsTestCache()
	started := make(chan string, 2)
	release := make(chan struct{})
	var waitGroup sync.WaitGroup
	for _, key := range []string{"auth-east", "auth-west"} {
		key := key
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			_, _ = cache.get(context.Background(), key, func(context.Context) ([]*ModelInfo, error) {
				started <- key
				<-release
				return []*ModelInfo{{ID: key + "-model"}}, nil
			})
		}()
	}

	seen := make(map[string]bool, 2)
	for range 2 {
		select {
		case key := <-started:
			seen[key] = true
		case <-time.After(time.Second):
			close(release)
			waitGroup.Wait()
			t.Fatal("different cache keys serialized behind an in-flight fetch")
		}
	}
	close(release)
	waitGroup.Wait()
	if !seen["auth-east"] || !seen["auth-west"] {
		t.Fatalf("started keys = %v, want both auth identities", seen)
	}
}

func runConcurrentKiroCacheGets(
	t *testing.T,
	cache *kiroModelsCache,
	key string,
	fetch func(context.Context) ([]*ModelInfo, error),
	started <-chan struct{},
	release chan<- struct{},
) ([][]*ModelInfo, []error) {
	t.Helper()
	const callers = 32
	results := make([][]*ModelInfo, callers)
	errs := make([]error, callers)
	var waitGroup sync.WaitGroup

	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		results[0], errs[0] = cache.get(context.Background(), key, fetch)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first fetch did not start")
	}
	for i := 1; i < callers; i++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			results[index], errs[index] = cache.get(context.Background(), key, fetch)
		}(i)
	}
	waitForKiroModelsFlightWaiters(t, cache, key, callers)
	close(release)
	waitGroup.Wait()
	return results, errs
}

func waitForKiroModelsFlightWaiters(t *testing.T, cache *kiroModelsCache, key string, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		cache.mu.Lock()
		entry := cache.entries[key]
		got := 0
		if entry != nil && entry.flight != nil {
			got = entry.flight.waiters
		}
		cache.mu.Unlock()
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("in-flight waiters = %d, want %d", got, want)
		}
		runtime.Gosched()
	}
}

func TestKiroModelsCacheServesLastGoodOnRefreshFailure(t *testing.T) {
	testCases := []struct {
		name      string
		models    []*ModelInfo
		err       error
		wantError error
	}{
		{name: "transient error", err: errors.New("temporary outage")},
		{name: "timeout", err: context.DeadlineExceeded},
		{name: "empty catalog", models: []*ModelInfo{}, wantError: errKiroModelsEmpty},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			cache, clock := newKiroModelsTestCache()
			if _, err := cache.get(context.Background(), "auth-a", func(context.Context) ([]*ModelInfo, error) {
				return []*ModelInfo{{ID: "last-good"}}, nil
			}); err != nil {
				t.Fatalf("seed cache: %v", err)
			}
			clock.advance(kiroModelsCacheTTL + time.Second)

			var refreshCalls atomic.Int32
			models, err := cache.get(context.Background(), "auth-a", func(context.Context) ([]*ModelInfo, error) {
				refreshCalls.Add(1)
				return testCase.models, testCase.err
			})
			if testCase.wantError != nil {
				if !errors.Is(err, testCase.wantError) {
					t.Fatalf("refresh error = %v, want %v", err, testCase.wantError)
				}
			} else if !errors.Is(err, testCase.err) {
				t.Fatalf("refresh error = %v, want %v", err, testCase.err)
			}
			if len(models) != 1 || models[0].ID != "last-good" {
				t.Fatalf("refresh models = %v, want last-good", models)
			}

			models, err = cache.get(context.Background(), "auth-a", func(context.Context) ([]*ModelInfo, error) {
				refreshCalls.Add(1)
				return nil, errors.New("must not run during cooldown")
			})
			if err != nil || len(models) != 1 || models[0].ID != "last-good" {
				t.Fatalf("cooldown read: models=%v err=%v", models, err)
			}
			if got := refreshCalls.Load(); got != 1 {
				t.Fatalf("refresh calls = %d, want 1 during cooldown", got)
			}

			clock.advance(kiroModelsRetryCooldown + time.Second)
			models, err = cache.get(context.Background(), "auth-a", func(context.Context) ([]*ModelInfo, error) {
				refreshCalls.Add(1)
				return []*ModelInfo{{ID: "recovered"}}, nil
			})
			if err != nil || len(models) != 1 || models[0].ID != "recovered" {
				t.Fatalf("recovered refresh: models=%v err=%v", models, err)
			}
			if got := refreshCalls.Load(); got != 2 {
				t.Fatalf("refresh calls after recovery = %d, want 2", got)
			}
		})
	}
}

func TestKiroModelsCacheColdFailureCooldownAndRecovery(t *testing.T) {
	cache, clock := newKiroModelsTestCache()
	var calls atomic.Int32
	fetch := func(context.Context) ([]*ModelInfo, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("cold outage")
		}
		return []*ModelInfo{{ID: "recovered"}}, nil
	}

	if models, err := cache.get(context.Background(), "auth-a", fetch); models != nil || err == nil {
		t.Fatalf("cold failure: models=%v err=%v", models, err)
	}
	if models, err := cache.get(context.Background(), "auth-a", fetch); models != nil || err != nil {
		t.Fatalf("cooldown read: models=%v err=%v", models, err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls during cooldown = %d, want 1", got)
	}

	clock.advance(kiroModelsRetryCooldown + time.Second)
	models, err := cache.get(context.Background(), "auth-a", fetch)
	if err != nil || len(models) != 1 || models[0].ID != "recovered" {
		t.Fatalf("recovery: models=%v err=%v", models, err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("calls after recovery = %d, want 2", got)
	}
}

func TestKiroModelsCachePrunesInactiveEntries(t *testing.T) {
	cache, clock := newKiroModelsTestCache()
	if _, err := cache.get(context.Background(), "inactive-auth", func(context.Context) ([]*ModelInfo, error) {
		return []*ModelInfo{{ID: "old-model"}}, nil
	}); err != nil {
		t.Fatalf("seed inactive entry: %v", err)
	}
	clock.advance(kiroModelsEntryRetention + time.Second)
	if _, err := cache.get(context.Background(), "active-auth", func(context.Context) ([]*ModelInfo, error) {
		return []*ModelInfo{{ID: "new-model"}}, nil
	}); err != nil {
		t.Fatalf("create active entry: %v", err)
	}

	cache.mu.Lock()
	_, inactiveExists := cache.entries["inactive-auth"]
	_, activeExists := cache.entries["active-auth"]
	cache.mu.Unlock()
	if inactiveExists || !activeExists {
		t.Fatalf("cache entries: inactive=%v active=%v, want false/true", inactiveExists, activeExists)
	}
}

func TestKiroModelsCacheDoesNotPruneActiveFlight(t *testing.T) {
	cache, clock := newKiroModelsTestCache()
	started := make(chan struct{})
	release := make(chan struct{})
	activeDone := make(chan struct{})
	go func() {
		defer close(activeDone)
		_, _ = cache.get(context.Background(), "active-flight", func(context.Context) ([]*ModelInfo, error) {
			close(started)
			<-release
			return []*ModelInfo{{ID: "active-model"}}, nil
		})
	}()
	<-started
	clock.advance(kiroModelsEntryRetention + time.Second)
	if _, err := cache.get(context.Background(), "new-auth", func(context.Context) ([]*ModelInfo, error) {
		return []*ModelInfo{{ID: "new-model"}}, nil
	}); err != nil {
		t.Fatalf("create new entry: %v", err)
	}

	cache.mu.Lock()
	activeEntry := cache.entries["active-flight"]
	cache.mu.Unlock()
	if activeEntry == nil || activeEntry.flight == nil {
		t.Fatal("active flight was pruned")
	}
	close(release)
	<-activeDone
}

func TestKiroModelsCacheCanceledWaiterReturnsPromptly(t *testing.T) {
	cache, _ := newKiroModelsTestCache()
	started := make(chan struct{})
	release := make(chan struct{})
	ownerDone := make(chan struct{})
	go func() {
		defer close(ownerDone)
		_, _ = cache.get(context.Background(), "auth-a", func(context.Context) ([]*ModelInfo, error) {
			close(started)
			<-release
			return []*ModelInfo{{ID: "kiro-model"}}, nil
		})
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	waiterDone := make(chan error, 1)
	go func() {
		_, err := cache.get(ctx, "auth-a", func(context.Context) ([]*ModelInfo, error) {
			return nil, errors.New("waiter must not fetch")
		})
		waiterDone <- err
	}()
	cancel()
	select {
	case err := <-waiterDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled waiter remained blocked")
	}

	close(release)
	select {
	case <-ownerDone:
	case <-time.After(time.Second):
		t.Fatal("owner did not finish")
	}
}

func TestKiroModelsCacheOwnerCancellationDoesNotPoisonLiveWaiter(t *testing.T) {
	cache, _ := newKiroModelsTestCache()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	fetch := func(ctx context.Context) ([]*ModelInfo, error) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
			return []*ModelInfo{{ID: "kiro-model"}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	ownerCtx, cancelOwner := context.WithCancel(context.Background())
	ownerDone := make(chan error, 1)
	go func() {
		_, err := cache.get(ownerCtx, "auth-a", fetch)
		ownerDone <- err
	}()
	<-started

	waiterDone := make(chan struct {
		models []*ModelInfo
		err    error
	}, 1)
	go func() {
		models, err := cache.get(context.Background(), "auth-a", fetch)
		waiterDone <- struct {
			models []*ModelInfo
			err    error
		}{models: models, err: err}
	}()
	waitForKiroModelsFlightWaiters(t, cache, "auth-a", 2)
	cancelOwner()
	if err := <-ownerDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("owner error = %v, want context canceled", err)
	}
	close(release)
	result := <-waiterDone
	if result.err != nil || len(result.models) != 1 || result.models[0].ID != "kiro-model" {
		t.Fatalf("live waiter: models=%v err=%v", result.models, result.err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want shared flight to survive owner cancellation", got)
	}
}

func TestKiroModelsCacheReturnsIndependentSnapshots(t *testing.T) {
	cache, _ := newKiroModelsTestCache()
	source := []*ModelInfo{{
		ID:                       "original",
		SupportedParameters:      []string{"temperature"},
		SupportedInputModalities: []string{"TEXT"},
		Thinking:                 &registry.ThinkingSupport{Levels: []string{"high"}},
		Config:                   &registry.ModelConfig{OverrideHeader: map[string]string{"x-test": "original"}},
	}}

	first, err := cache.get(context.Background(), "auth-a", func(context.Context) ([]*ModelInfo, error) {
		return source, nil
	})
	if err != nil {
		t.Fatalf("first get: %v", err)
	}
	source[0].ID = "mutated-source"
	first[0].SupportedParameters[0] = "mutated-return"
	first[0].SupportedInputModalities[0] = "IMAGE"
	first[0].Thinking.Levels[0] = "low"
	first[0].Config.OverrideHeader["x-test"] = "mutated"

	second, err := cache.get(context.Background(), "auth-a", func(context.Context) ([]*ModelInfo, error) {
		return nil, errors.New("cached read should not fetch")
	})
	if err != nil {
		t.Fatalf("second get: %v", err)
	}
	if second[0].ID != "original" ||
		second[0].SupportedParameters[0] != "temperature" ||
		second[0].SupportedInputModalities[0] != "TEXT" ||
		second[0].Thinking.Levels[0] != "high" ||
		second[0].Config.OverrideHeader["x-test"] != "original" {
		t.Fatalf("cached snapshot was mutated: %+v", second[0])
	}
}
