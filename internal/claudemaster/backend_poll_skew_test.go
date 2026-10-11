package claudemaster

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestPollJitterStaysWithinItsFractionAndVaries(t *testing.T) {
	const d = 3 * time.Minute
	low, high := d, d
	for range 2000 {
		got := backendPollJitter(d, backendPollJitterFraction)
		if got < d*8/10 || got > d*12/10 {
			t.Fatalf("jitter %s outside 80%%..120%% of %s", got, d)
		}
		low, high = min(low, got), max(high, got)
	}
	if high-low < d/5 {
		t.Fatalf("jitter barely varies: %s..%s", low, high)
	}
	if backendPollJitter(d, 0) != d || backendPollJitter(0, 0.2) != 0 {
		t.Fatal("no fraction or no interval must leave the interval as it is")
	}
	// The shortest jittered interval must outlast the shared cache, or one process re-reads its own answer.
	if usageCacheTTL >= backendQuotaPollInterval*8/10 {
		t.Fatalf("usage cache %s is not shorter than the shortest poll interval", usageCacheTTL)
	}
}

func TestQuotaPollPhasesSpreadAccountsAcrossHalfTheInterval(t *testing.T) {
	const interval = 3 * time.Minute
	n := 5
	var last time.Duration = -1
	for i := range n {
		phase := backendQuotaPollPhase(i, n, interval)
		if phase <= last || phase >= interval/2 {
			t.Fatalf("phase %d = %s: want increasing and under half the interval (previous %s)", i, phase, last)
		}
		last = phase
	}
	if got := backendQuotaPollPhase(1, n, interval) - backendQuotaPollPhase(0, n, interval); got != interval/2/time.Duration(n) {
		t.Fatalf("accounts %s apart, want %s", got, interval/2/time.Duration(n))
	}
	if backendQuotaPollPhase(0, 1, interval) != 0 {
		t.Fatal("a single account must not wait")
	}
}

func TestJitteredTicksTickAndStopWithTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	ticks := jitteredTicks(ctx, 20*time.Millisecond, backendPollJitterFraction)
	for range 3 {
		select {
		case <-ticks:
		case <-time.After(5 * time.Second):
			t.Fatal("no tick")
		}
	}
	cancel()
	select {
	case <-ticks:
		select {
		case <-ticks:
			t.Fatal("ticks continued after the context ended")
		case <-time.After(100 * time.Millisecond):
		}
	case <-time.After(100 * time.Millisecond):
	}
}

func TestQuotaPollWaitsEachAccountsPhaseBeforeAsking(t *testing.T) {
	manager, selector, now := backendQuotaPollFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var mu sync.Mutex
	asked := make(map[string]time.Time)
	request := func(_ context.Context, auth *coreauth.Auth, _ *http.Request) (*http.Response, error) {
		mu.Lock()
		asked[auth.ID] = time.Now()
		mu.Unlock()
		return backendQuotaPollResponse(20, now.Add(24*time.Hour)), nil
	}
	const step = 150 * time.Millisecond
	ticks := make(chan time.Time)
	done := pollBackendQuotaRounds(ctx, manager, selector, selector.authIDs, request, ticks, func(i, _ int) time.Duration { return time.Duration(i) * step })
	start := time.Now()
	ticks <- now
	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		n := len(asked)
		mu.Unlock()
		if n == 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("both accounts were not polled: %v", asked)
		case <-time.After(10 * time.Millisecond):
		}
	}
	close(ticks)
	<-done
	if gap := asked["poll-b"].Sub(asked["poll-a"]); gap < step*8/10 {
		t.Fatalf("second account asked %s after the first, want about its %s phase", gap, step)
	}
	if asked["poll-a"].Sub(start) > step/2 {
		t.Fatalf("first account waited %s, want no phase", asked["poll-a"].Sub(start))
	}

	// An account still waiting out its phase when the backend stops is not polled at all.
	ctx2, cancel2 := context.WithCancel(t.Context())
	calls := 0
	ticks2 := make(chan time.Time)
	done2 := pollBackendQuotaRounds(ctx2, manager, selector, []string{"poll-a"}, func(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
		calls++
		return backendQuotaPollResponse(20, now.Add(24*time.Hour)), nil
	}, ticks2, func(int, int) time.Duration { return time.Hour })
	ticks2 <- now
	cancel2()
	<-done2
	if calls != 0 {
		t.Fatalf("an account still in its phase was polled after shutdown (%d calls)", calls)
	}
}

func TestUsagePollBacksOffAfterA429(t *testing.T) {
	manager, selector, now := backendQuotaPollFixture(t)
	clock := now
	selector.now = func() time.Time { return clock }
	var mu sync.Mutex
	calls := make(map[string]int)
	request := func(_ context.Context, auth *coreauth.Auth, _ *http.Request) (*http.Response, error) {
		mu.Lock()
		calls[auth.ID]++
		mu.Unlock()
		if auth.ID == "poll-a" {
			return &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"600"}},
				Body: io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"rate_limit_error","message":"Rate limited"}}`))}, nil
		}
		return backendQuotaPollResponse(20, now.Add(24*time.Hour)), nil
	}
	round := func() {
		ticks := make(chan time.Time)
		done := pollBackendQuotaRounds(t.Context(), manager, selector, selector.authIDs, request, ticks, nil)
		ticks <- clock
		close(ticks)
		<-done
	}
	round()
	if selector.usagePollDue("poll-a") || !selector.usagePollDue("poll-b") {
		t.Fatal("a 429 did not hold that account's poll back, or held back the other")
	}
	clock = now.Add(5 * time.Minute)
	round()
	if calls["poll-a"] != 1 || calls["poll-b"] != 2 {
		t.Fatalf("calls %v: the refused account must sit out its Retry-After while the other is polled", calls)
	}
	clock = now.Add(10 * time.Minute)
	round()
	if calls["poll-a"] != 2 {
		t.Fatalf("calls %v: the account was not polled again once its Retry-After passed", calls)
	}
}

func TestUsagePollBackoffWithoutRetryAfterIsOneIntervalAndIsCapped(t *testing.T) {
	_, selector, now := backendQuotaPollFixture(t)
	selector.now = func() time.Time { return now }
	selector.deferUsagePoll("poll-a", 0)
	selector.deferUsagePoll("poll-b", 24*time.Hour)
	if got := selector.usagePollAfter["poll-a"].Sub(now); got != backendQuotaPollInterval {
		t.Fatalf("no Retry-After: backoff %s, want one interval", got)
	}
	if got := selector.usagePollAfter["poll-b"].Sub(now); got != backendQuotaPollMaxBackoff {
		t.Fatalf("a day's Retry-After: backoff %s, want the %s cap", got, backendQuotaPollMaxBackoff)
	}
}

func TestClaudeRetryAfterReadsSecondsAndDates(t *testing.T) {
	now := time.Date(2026, time.October, 10, 12, 0, 0, 0, time.UTC)
	for value, want := range map[string]time.Duration{
		"30": 30 * time.Second, " 600 ": 10 * time.Minute, "": 0, "soon": 0, "-5": 0,
		now.Add(90 * time.Second).Format(http.TimeFormat): 90 * time.Second,
		now.Add(-time.Minute).Format(http.TimeFormat):     0,
	} {
		if got := claudeRetryAfter(value, now); got != want {
			t.Errorf("Retry-After %q = %s, want %s", value, got, want)
		}
	}
	err := &claudeUsageStatusError{status: 429, retryAfter: 30 * time.Second}
	if !strings.Contains(err.Error(), "unexpected HTTP status 429 (retry after 30s)") {
		t.Fatalf("error text %q", err.Error())
	}
}
