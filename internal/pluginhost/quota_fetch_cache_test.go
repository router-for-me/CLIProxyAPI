package pluginhost

import (
	"context"
	"errors"
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

func TestQuotaFetchDebounce_CallerCancellationDoesNotAffectOthers(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	provider := &testQuotaProvider{
		identifier: "quota-test",
		fetchFn: func(ctx context.Context, _ pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
			calls.Add(1)
			close(started)
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
	<-started
	cancel()
	if errFetch := <-cancelled; !errors.Is(errFetch, context.Canceled) {
		t.Fatalf("expected cancelled caller to return context.Canceled, got %v", errFetch)
	}

	close(release)
	resp := fetchQuotaForTest(t, host, "auth-1")
	if resp.Subscription == nil || resp.Subscription.Plan != "Pro" {
		t.Fatalf("expected shared fetch to complete for remaining callers, got %+v", resp)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected a single upstream fetch, got %d", got)
	}
}
