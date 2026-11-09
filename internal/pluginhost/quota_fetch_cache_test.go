package pluginhost

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type quotaTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *quotaTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *quotaTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func newQuotaDebounceHost(t *testing.T, provider *testQuotaProvider) (*Host, *quotaTestClock) {
	t.Helper()
	host := newHostWithRecords(capabilityRecord{
		id: "quota-plugin",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{QuotaProvider: provider},
		},
	})
	clock := &quotaTestClock{now: time.Unix(1_700_000_000, 0)}
	host.quotaFetches.nowFunc = clock.Now
	return host, clock
}

func countingQuotaProvider(calls *atomic.Int32) *testQuotaProvider {
	return &testQuotaProvider{
		identifier: "quota-test",
		fetchFn: func(_ context.Context, req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
			n := calls.Add(1)
			return pluginapi.QuotaFetchResponse{
				Subscription:       &pluginapi.QuotaSubscription{Plan: req.AuthIndex},
				ServerTimeOffsetMs: int64(n),
				Groups: []pluginapi.QuotaGroup{{
					DisplayName: "limits",
					Buckets:     []pluginapi.QuotaBucket{{Window: "daily", RemainingFraction: 0.5}},
				}},
			}, nil
		},
	}
}

func fetchQuotaForTest(t *testing.T, host *Host, authIndex string) pluginapi.QuotaFetchResponse {
	t.Helper()
	resp, handled, errFetch := host.FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{
		Provider:  "quota-test",
		AuthIndex: authIndex,
	})
	if errFetch != nil {
		t.Fatalf("unexpected fetch error: %v", errFetch)
	}
	if !handled {
		t.Fatal("expected fetch to be handled")
	}
	return resp
}

func TestQuotaFetchDebounce_ReusesRecentResult(t *testing.T) {
	var calls atomic.Int32
	host, clock := newQuotaDebounceHost(t, countingQuotaProvider(&calls))

	first := fetchQuotaForTest(t, host, "auth-1")
	clock.Advance(quotaFetchCacheTTL - time.Millisecond)
	second := fetchQuotaForTest(t, host, "auth-1")
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected 1 upstream fetch within debounce window, got %d", got)
	}
	if second.ServerTimeOffsetMs != first.ServerTimeOffsetMs {
		t.Fatalf("expected cached result, got offsets %d and %d", first.ServerTimeOffsetMs, second.ServerTimeOffsetMs)
	}

	clock.Advance(time.Millisecond)
	third := fetchQuotaForTest(t, host, "auth-1")
	if got := calls.Load(); got != 2 {
		t.Fatalf("expected fresh upstream fetch after debounce window, got %d calls", got)
	}
	if third.ServerTimeOffsetMs != 2 {
		t.Fatalf("expected fresh result, got offset %d", third.ServerTimeOffsetMs)
	}
}

func TestQuotaFetchDebounce_PerCredential(t *testing.T) {
	var calls atomic.Int32
	host, _ := newQuotaDebounceHost(t, countingQuotaProvider(&calls))

	a := fetchQuotaForTest(t, host, "auth-a")
	b := fetchQuotaForTest(t, host, "auth-b")
	if got := calls.Load(); got != 2 {
		t.Fatalf("expected separate fetches per credential, got %d", got)
	}
	if a.Subscription.Plan != "auth-a" || b.Subscription.Plan != "auth-b" {
		t.Fatalf("results crossed credentials: %q / %q", a.Subscription.Plan, b.Subscription.Plan)
	}
}

func TestQuotaFetchDebounce_CoalescesConcurrentRequests(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	provider := &testQuotaProvider{
		identifier: "quota-test",
		fetchFn: func(context.Context, pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
			if calls.Add(1) == 1 {
				close(started)
			}
			<-release
			return pluginapi.QuotaFetchResponse{Subscription: &pluginapi.QuotaSubscription{Plan: "Pro"}}, nil
		},
	}
	host, _ := newQuotaDebounceHost(t, provider)

	const callers = 8
	var wg sync.WaitGroup
	results := make(chan pluginapi.QuotaFetchResponse, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, _, errFetch := host.FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{Provider: "quota-test", AuthIndex: "auth-1"})
			if errFetch != nil {
				t.Errorf("unexpected fetch error: %v", errFetch)
				return
			}
			results <- resp
		}()
	}
	<-started
	close(release)
	wg.Wait()
	close(results)

	if got := calls.Load(); got != 1 {
		t.Fatalf("expected concurrent requests to share one upstream fetch, got %d", got)
	}
	count := 0
	for resp := range results {
		count++
		if resp.Subscription == nil || resp.Subscription.Plan != "Pro" {
			t.Fatalf("unexpected response: %+v", resp)
		}
	}
	if count != callers {
		t.Fatalf("expected %d results, got %d", callers, count)
	}
}

func TestQuotaFetchDebounce_DoesNotCacheErrors(t *testing.T) {
	var calls atomic.Int32
	provider := &testQuotaProvider{
		identifier: "quota-test",
		fetchFn: func(context.Context, pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
			if calls.Add(1) == 1 {
				return pluginapi.QuotaFetchResponse{}, errors.New("upstream unavailable")
			}
			return pluginapi.QuotaFetchResponse{Subscription: &pluginapi.QuotaSubscription{Plan: "Pro"}}, nil
		},
	}
	host, _ := newQuotaDebounceHost(t, provider)

	_, handled, errFetch := host.FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{Provider: "quota-test", AuthIndex: "auth-1"})
	if errFetch == nil || !handled {
		t.Fatalf("expected handled error on first fetch, got handled=%v err=%v", handled, errFetch)
	}
	resp := fetchQuotaForTest(t, host, "auth-1")
	if calls.Load() != 2 || resp.Subscription == nil || resp.Subscription.Plan != "Pro" {
		t.Fatalf("expected retry after error, calls=%d resp=%+v", calls.Load(), resp)
	}
}

func TestQuotaFetchDebounce_ResetInvalidates(t *testing.T) {
	var calls atomic.Int32
	host, _ := newQuotaDebounceHost(t, countingQuotaProvider(&calls))

	fetchQuotaForTest(t, host, "auth-1")
	if _, _, errReset := host.ResetQuota(context.Background(), pluginapi.QuotaResetRequest{Provider: "quota-test", AuthIndex: "auth-1"}); errReset != nil {
		t.Fatalf("unexpected reset error: %v", errReset)
	}
	fetchQuotaForTest(t, host, "auth-1")
	if got := calls.Load(); got != 2 {
		t.Fatalf("expected fresh fetch after reset, got %d calls", got)
	}
}

func TestQuotaFetchDebounce_ResetDuringInFlightFetchDiscardsResult(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	provider := &testQuotaProvider{
		identifier: "quota-test",
		fetchFn: func(context.Context, pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
			if calls.Add(1) == 1 {
				close(started)
				<-release
			}
			return pluginapi.QuotaFetchResponse{ServerTimeOffsetMs: int64(calls.Load())}, nil
		},
	}
	host, _ := newQuotaDebounceHost(t, provider)

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, _, errFetch := host.FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{Provider: "quota-test", AuthIndex: "auth-1"}); errFetch != nil {
			t.Errorf("unexpected in-flight fetch error: %v", errFetch)
		}
	}()
	<-started
	if _, _, errReset := host.ResetQuota(context.Background(), pluginapi.QuotaResetRequest{Provider: "quota-test", AuthIndex: "auth-1"}); errReset != nil {
		t.Fatalf("unexpected reset error: %v", errReset)
	}
	close(release)
	<-done

	resp := fetchQuotaForTest(t, host, "auth-1")
	if got := calls.Load(); got != 2 {
		t.Fatalf("expected pre-reset result to be discarded, got %d calls", got)
	}
	if resp.ServerTimeOffsetMs != 2 {
		t.Fatalf("expected post-reset result, got offset %d", resp.ServerTimeOffsetMs)
	}
}

func TestQuotaFetchDebounce_CallerMutationDoesNotLeak(t *testing.T) {
	var calls atomic.Int32
	host, _ := newQuotaDebounceHost(t, countingQuotaProvider(&calls))

	first := fetchQuotaForTest(t, host, "auth-1")
	first.Subscription.Plan = "mutated"
	first.Groups[0].Buckets[0].RemainingFraction = 0

	second := fetchQuotaForTest(t, host, "auth-1")
	if second.Subscription.Plan != "auth-1" || second.Groups[0].Buckets[0].RemainingFraction != 0.5 {
		t.Fatalf("cached response was mutated by caller: %+v", second)
	}
}

// waitForQuotaWaiters blocks until the in-flight call for any key has the wanted number of waiters.
func waitForQuotaWaiters(t *testing.T, host *Host, want int) {
	t.Helper()
	for i := 0; i < 1_000_000; i++ {
		host.quotaFetches.mu.Lock()
		got := 0
		for _, call := range host.quotaFetches.inflight {
			got += call.waiters
		}
		host.quotaFetches.mu.Unlock()
		if got == want {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("timed out waiting for %d quota waiters", want)
}

func TestQuotaFetchDebounce_CallerCancellationDoesNotAffectOthers(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	provider := &testQuotaProvider{
		identifier: "quota-test",
		fetchFn: func(ctx context.Context, _ pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
			calls.Add(1)
			<-release
			if errCtx := ctx.Err(); errCtx != nil {
				return pluginapi.QuotaFetchResponse{}, errCtx
			}
			return pluginapi.QuotaFetchResponse{Subscription: &pluginapi.QuotaSubscription{Plan: "Pro"}}, nil
		},
	}
	host, _ := newQuotaDebounceHost(t, provider)

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancelled := make(chan error, 1)
	go func() {
		_, _, errFetch := host.FetchQuota(cancelCtx, pluginapi.QuotaFetchRequest{Provider: "quota-test", AuthIndex: "auth-1"})
		cancelled <- errFetch
	}()
	waitForQuotaWaiters(t, host, 1)
	remaining := make(chan pluginapi.QuotaFetchResponse, 1)
	go func() {
		resp, _, errFetch := host.FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{Provider: "quota-test", AuthIndex: "auth-1"})
		if errFetch != nil {
			t.Errorf("unexpected fetch error for remaining caller: %v", errFetch)
		}
		remaining <- resp
	}()
	waitForQuotaWaiters(t, host, 2)

	cancel()
	if errFetch := <-cancelled; !errors.Is(errFetch, context.Canceled) {
		t.Fatalf("expected cancelled caller to return context.Canceled, got %v", errFetch)
	}
	close(release)
	resp := <-remaining
	if resp.Subscription == nil || resp.Subscription.Plan != "Pro" {
		t.Fatalf("expected shared fetch to complete for the remaining caller, got %+v", resp)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected a single upstream fetch, got %d", got)
	}
}

func TestQuotaFetchDebounce_LastWaiterLeavingCancelsSharedCall(t *testing.T) {
	var calls atomic.Int32
	providerCanceled := make(chan struct{})
	provider := &testQuotaProvider{
		identifier: "quota-test",
		fetchFn: func(ctx context.Context, _ pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
			if calls.Add(1) == 1 {
				// Simulate a provider call that hangs until it is canceled.
				<-ctx.Done()
				close(providerCanceled)
				return pluginapi.QuotaFetchResponse{}, ctx.Err()
			}
			return pluginapi.QuotaFetchResponse{Subscription: &pluginapi.QuotaSubscription{Plan: "Fresh"}}, nil
		},
	}
	host, _ := newQuotaDebounceHost(t, provider)

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancelled := make(chan error, 1)
	go func() {
		_, _, errFetch := host.FetchQuota(cancelCtx, pluginapi.QuotaFetchRequest{Provider: "quota-test", AuthIndex: "auth-1"})
		cancelled <- errFetch
	}()
	waitForQuotaWaiters(t, host, 1)
	cancel()
	if errFetch := <-cancelled; !errors.Is(errFetch, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", errFetch)
	}
	<-providerCanceled // The hung provider call must observe cancellation.

	// A new caller must start a fresh call rather than join the abandoned one.
	resp := fetchQuotaForTest(t, host, "auth-1")
	if resp.Subscription == nil || resp.Subscription.Plan != "Fresh" {
		t.Fatalf("expected a fresh fetch after abandonment, got %+v", resp)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("expected 2 upstream fetches, got %d", got)
	}
}

func TestQuotaFetchDebounce_DifferentCredentialPayloadsDoNotShare(t *testing.T) {
	var calls atomic.Int32
	provider := &testQuotaProvider{
		identifier: "quota-test",
		fetchFn: func(_ context.Context, req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
			calls.Add(1)
			return pluginapi.QuotaFetchResponse{Subscription: &pluginapi.QuotaSubscription{Plan: string(req.StorageJSON)}}, nil
		},
	}
	host, _ := newQuotaDebounceHost(t, provider)

	fetch := func(authIndex, storage string) string {
		t.Helper()
		resp, _, errFetch := host.FetchQuotaByPlugin(context.Background(), "quota-plugin", pluginapi.QuotaFetchRequest{
			Provider:    "quota-test",
			AuthIndex:   authIndex,
			StorageJSON: []byte(storage),
		})
		if errFetch != nil {
			t.Fatalf("unexpected fetch error: %v", errFetch)
		}
		return resp.Subscription.Plan
	}

	// Same credential identity but different credential material must not share results.
	if got := fetch("auth-1", `{"account":"a"}`); got != `{"account":"a"}` {
		t.Fatalf("unexpected result: %s", got)
	}
	if got := fetch("auth-1", `{"account":"b"}`); got != `{"account":"b"}` {
		t.Fatalf("result crossed credential payloads: %s", got)
	}
	// Requests without any credential identity are never debounced.
	if got := fetch("", `{"account":"c"}`); got != `{"account":"c"}` {
		t.Fatalf("unexpected result: %s", got)
	}
	if got := fetch("", `{"account":"d"}`); got != `{"account":"d"}` {
		t.Fatalf("result crossed anonymous credential payloads: %s", got)
	}
	fetch("", `{"account":"d"}`)
	if got := calls.Load(); got != 5 {
		t.Fatalf("expected every distinct or anonymous request to reach the provider, got %d calls", got)
	}

	// Identical identity and payload still share a result.
	fetch("auth-1", `{"account":"a"}`)
	if got := calls.Load(); got != 5 {
		t.Fatalf("expected identical request to be served from cache, got %d calls", got)
	}
}

func TestQuotaFetchDebounce_AbandonedCallDoesNotOverwriteNewerResult(t *testing.T) {
	var calls atomic.Int32
	staleStarted := make(chan struct{})
	releaseStale := make(chan struct{})
	provider := &testQuotaProvider{
		identifier: "quota-test",
		fetchFn: func(context.Context, pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
			if calls.Add(1) == 1 {
				close(staleStarted)
				// Simulate an upstream response that wins the race against cancellation.
				<-releaseStale
				return pluginapi.QuotaFetchResponse{Subscription: &pluginapi.QuotaSubscription{Plan: "stale"}}, nil
			}
			return pluginapi.QuotaFetchResponse{Subscription: &pluginapi.QuotaSubscription{Plan: "fresh"}}, nil
		},
	}
	host, _ := newQuotaDebounceHost(t, provider)

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancelled := make(chan error, 1)
	go func() {
		_, _, errFetch := host.FetchQuota(cancelCtx, pluginapi.QuotaFetchRequest{Provider: "quota-test", AuthIndex: "auth-1"})
		cancelled <- errFetch
	}()
	// Wait for the provider itself to be running, not just for the waiter to register, so the
	// replacement fetch below cannot reach the provider first.
	<-staleStarted
	host.quotaFetches.mu.Lock()
	var staleCall *quotaFetchCall
	for _, call := range host.quotaFetches.inflight {
		staleCall = call
	}
	host.quotaFetches.mu.Unlock()
	cancel()
	<-cancelled

	if got := fetchQuotaForTest(t, host, "auth-1"); got.Subscription.Plan != "fresh" {
		t.Fatalf("expected replacement call result, got %+v", got.Subscription)
	}
	close(releaseStale)
	<-staleCall.done

	if got := fetchQuotaForTest(t, host, "auth-1"); got.Subscription.Plan != "fresh" {
		t.Fatalf("abandoned call overwrote newer cached result: %+v", got.Subscription)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("expected 2 upstream fetches, got %d", got)
	}
}

func TestQuotaFetchCacheKey_IncludesPluginPath(t *testing.T) {
	req := pluginapi.QuotaFetchRequest{Provider: "quota-test", AuthIndex: "auth-1"}
	oldKey := quotaFetchCacheKey(capabilityRecord{id: "quota-plugin", path: "/plugins/old/quota.so", version: "1.0.0"}, req)
	newKey := quotaFetchCacheKey(capabilityRecord{id: "quota-plugin", path: "/plugins/new/quota.so", version: "1.0.0"}, req)
	if oldKey == "" || oldKey == newKey {
		t.Fatalf("expected distinct keys for plugins replaced at a different path, got %q and %q", oldKey, newKey)
	}
}

func TestQuotaFetchCacheKey_MetadataDigest(t *testing.T) {
	record := capabilityRecord{id: "quota-plugin", path: "test/quota-plugin", version: "1.0.0"}
	key := func(metadata map[string]any) string {
		return quotaFetchCacheKey(record, pluginapi.QuotaFetchRequest{Provider: "quota-test", AuthIndex: "auth-1", Metadata: metadata})
	}

	base := key(map[string]any{"email": "a@example.com", "nested": map[string]any{"x": 1.0, "y": []any{"p", true, nil}}})
	same := key(map[string]any{"nested": map[string]any{"y": []any{"p", true, nil}, "x": 1.0}, "email": "a@example.com"})
	if base == "" || base != same {
		t.Fatalf("expected equal metadata to produce a stable key, got %q and %q", base, same)
	}
	for name, metadata := range map[string]map[string]any{
		"different value":      {"email": "b@example.com", "nested": map[string]any{"x": 1.0, "y": []any{"p", true, nil}}},
		"different type":       {"email": "a@example.com", "nested": map[string]any{"x": "1", "y": []any{"p", true, nil}}},
		"string vs bool":       {"email": "a@example.com", "nested": map[string]any{"x": 1.0, "y": []any{"p", "true", nil}}},
		"value moved into key": {"email": "a@example.com", "nested": map[string]any{"x": 1.0, "y": []any{"p", true}}},
	} {
		if got := key(metadata); got == base {
			t.Fatalf("%s: expected a different key", name)
		}
	}
	if got := key(map[string]any{"opaque": struct{ A int }{A: 1}}); got != "" {
		t.Fatalf("expected unsupported metadata types to disable debouncing, got %q", got)
	}
}

func TestQuotaFetchCacheKey_CyclicMetadataDisablesDebouncing(t *testing.T) {
	record := capabilityRecord{id: "quota-plugin", path: "test/quota-plugin", version: "1.0.0"}
	cyclic := map[string]any{"email": "a@example.com"}
	cyclic["self"] = cyclic
	// Two self-references would grow exponentially without a visit budget.
	cyclic["again"] = cyclic
	list := []any{"x"}
	list = append(list, nil)
	list[1] = list
	for name, metadata := range map[string]map[string]any{
		"cyclic map":   cyclic,
		"cyclic slice": {"list": list},
	} {
		if got := quotaFetchCacheKey(record, pluginapi.QuotaFetchRequest{Provider: "quota-test", AuthIndex: "auth-1", Metadata: metadata}); got != "" {
			t.Fatalf("%s: expected debouncing to be disabled, got %q", name, got)
		}
	}
}

func TestQuotaFetchDebounce_CanceledCallerDoesNotGetCacheHit(t *testing.T) {
	var calls atomic.Int32
	host, _ := newQuotaDebounceHost(t, countingQuotaProvider(&calls))

	fetchQuotaForTest(t, host, "auth-1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, errFetch := host.FetchQuotaByPlugin(ctx, "quota-plugin", pluginapi.QuotaFetchRequest{Provider: "quota-test", AuthIndex: "auth-1"}); !errors.Is(errFetch, context.Canceled) {
		t.Fatalf("expected context.Canceled for a canceled caller, got %v", errFetch)
	}
}
