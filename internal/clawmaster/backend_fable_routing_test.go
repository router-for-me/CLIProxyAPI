package clawmaster

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const (
	testFableModel = "claude-fable-5-2"
	testOtherModel = "claude-opus-5-5"
)

// The headers of Anthropic's refusal of a Fable request at a used-up Fable allowance: the claim and the
// 7d_oi window rejected, the five-hour and weekly windows allowed.
func fableRefusalHeaders(reset time.Time) http.Header {
	h := http.Header{}
	h.Set("Anthropic-Ratelimit-Unified-Status", "rejected")
	h.Set("Anthropic-Ratelimit-Unified-Representative-Claim", "seven_day_overage_included")
	h.Set("Anthropic-Ratelimit-Unified-5h-Status", "allowed")
	h.Set("Anthropic-Ratelimit-Unified-7d-Status", "allowed")
	h.Set("Anthropic-Ratelimit-Unified-7d_oi-Status", "rejected")
	h.Set("Anthropic-Ratelimit-Unified-7d_oi-Utilization", "1.0")
	h.Set("Anthropic-Ratelimit-Unified-7d_oi-Reset", strconv.FormatInt(reset.Unix(), 10))
	return h
}

func TestBackendFableRejection(t *testing.T) {
	reset := time.Now().Add(48 * time.Hour)
	weeklyToo := fableRefusalHeaders(reset)
	weeklyToo.Set("Anthropic-Ratelimit-Unified-7d-Status", "rejected")
	claimOnly := fableRefusalHeaders(reset)
	claimOnly.Del("Anthropic-Ratelimit-Unified-7d_oi-Status")
	// The claim names the window nearest its limit, on a 429 for something else (a per-minute limit).
	claimNotRejected := http.Header{}
	claimNotRejected.Set("Anthropic-Ratelimit-Unified-Status", "allowed_warning")
	claimNotRejected.Set("Anthropic-Ratelimit-Unified-Representative-Claim", "seven_day_overage_included")
	claimNotRejected.Set("Anthropic-Ratelimit-Unified-7d_oi-Status", "allowed_warning")
	claimNotRejected.Set("Anthropic-Ratelimit-Unified-5h-Status", "allowed")
	claimNotRejected.Set("Anthropic-Ratelimit-Unified-7d-Status", "allowed")
	overage := http.Header{}
	overage.Set("Anthropic-Ratelimit-Unified-Status", "rejected")
	overage.Set("Anthropic-Ratelimit-Unified-Representative-Claim", "overage")
	overage.Set("Anthropic-Ratelimit-Unified-5h-Status", "allowed")
	overage.Set("Anthropic-Ratelimit-Unified-7d-Status", "allowed")
	for _, tc := range []struct {
		name       string
		status     int
		credential bool
		model      string
		headers    http.Header
		want       bool
	}{
		{name: "fable allowance used up", status: 429, model: testFableModel, headers: fableRefusalHeaders(reset), want: true},
		{name: "claim with the unified status rejected", status: 429, model: testFableModel, headers: claimOnly, want: true},
		{name: "claim on another 429", status: 429, model: testFableModel, headers: claimNotRejected},
		{name: "with a thinking suffix", status: 429, model: testFableModel + "(high)", headers: fableRefusalHeaders(reset), want: true},
		{name: "the week is used up too", status: 429, model: testFableModel, headers: weeklyToo},
		{name: "credential-wide", status: 429, credential: true, model: testFableModel, headers: fableRefusalHeaders(reset)},
		{name: "another model", status: 429, model: testOtherModel, headers: fableRefusalHeaders(reset)},
		{name: "overage, not Fable", status: 429, model: testFableModel, headers: overage},
		{name: "not a 429", status: 529, model: testFableModel, headers: fableRefusalHeaders(reset)},
		{name: "no headers", status: 429, model: testFableModel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := backendFableRejection(tc.status, tc.credential, tc.model, tc.headers); got != tc.want {
				t.Fatalf("backendFableRejection() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFableRejectionUntil(t *testing.T) {
	now := time.Date(2026, time.October, 2, 0, 0, 0, 0, time.UTC)
	header := now.Add(30 * time.Hour)
	if got, ok := fableRejectionUntil(fableRefusalHeaders(header), now.Add(time.Hour), now); !ok || !got.Equal(header) {
		t.Errorf("with a 7d_oi reset: %v %v, want %v", got, ok, header)
	}
	if got, ok := fableRejectionUntil(http.Header{}, now.Add(5*time.Hour), now); !ok || !got.Equal(now.Add(5*time.Hour)) {
		t.Errorf("with a known reset only: %v %v", got, ok)
	}
	if _, ok := fableRejectionUntil(http.Header{}, time.Time{}, now); ok {
		t.Error("with nothing known a reset was made up")
	}
	if _, ok := fableRejectionUntil(fableRefusalHeaders(now.Add(30*24*time.Hour)), time.Time{}, now); ok {
		t.Error("a reset past any weekly window was trusted")
	}
}

// With no reset known the hold backs off from a second, doubling; a reset that arrives later replaces the
// estimate either way; a Fable request served starts the backoff over.
func TestFableHoldWithoutAResetBacksOff(t *testing.T) {
	selector, _, reset := namedSelector(t)
	now := reset.Add(-time.Hour)
	selector.now = func() time.Time { return now }
	held := func() time.Duration {
		selector.mu.Lock()
		defer selector.mu.Unlock()
		if !selector.fableBlockedLocked("profile-a") {
			return 0
		}
		return selector.fableBlockedUntil["profile-a"].Sub(now)
	}
	bare := http.Header{"Anthropic-Ratelimit-Unified-7d_oi-Status": {"rejected"}}
	selector.observeFableRejection("profile-a", bare)
	if got := held(); got != time.Second {
		t.Fatalf("first hold = %v, want 1s", got)
	}
	selector.observeFableRejection("profile-a", bare)
	if got := held(); got != 2*time.Second {
		t.Fatalf("second hold = %v, want 2s", got)
	}
	selector.observeFableRejection("profile-a", fableRefusalHeaders(now.Add(3*time.Hour)))
	if got := held(); got != 3*time.Hour {
		t.Fatalf("with a reset: %v, want 3h", got)
	}
	selector.observeFableRejection("profile-a", fableRefusalHeaders(now.Add(time.Hour)))
	if got := held(); got != time.Hour {
		t.Fatalf("an earlier reset Anthropic sent did not replace the hold: %v", got)
	}
	selector.observeFableRejection("profile-a", bare)
	if got := held(); got != time.Hour {
		t.Fatalf("a backoff shortened or lengthened a known hold: %v", got)
	}
	now = now.Add(2 * time.Hour)
	selector.noteFableServed("profile-a")
	selector.observeFableRejection("profile-a", bare)
	if got := held(); got != time.Second {
		t.Fatalf("after a Fable request was served the backoff did not start over: %v", got)
	}
}

func modelRequest(session, model string) (string, coreexecutor.Options) {
	opts := plainRequest(session)
	opts.Metadata = map[string]any{coreexecutor.RequestedModelMetadataKey: model}
	return model, opts
}

func pickModel(t *testing.T, selector *backendSeriesSelector, auths []*coreauth.Auth, session, model string) (*coreauth.Auth, error) {
	t.Helper()
	m, opts := modelRequest(session, model)
	return selector.Pick(t.Context(), "claude", m, opts, auths)
}

func requirePickModel(t *testing.T, selector *backendSeriesSelector, auths []*coreauth.Auth, session, model, want string) {
	t.Helper()
	got, err := pickModel(t, selector, auths, session, model)
	if err != nil || got == nil || got.ID != want {
		t.Fatalf("Pick(%s, %s) = %#v, %v; want %s", session, model, got, err, want)
	}
}

// A subscription Anthropic refused Fable is skipped for Fable until the reset, keeps serving every other
// model, and takes Fable again once the reset passes.
func TestFableRefusalHoldsASubscriptionBackFromFableOnly(t *testing.T) {
	logs := captureLogs(t, "info")
	selector, auths, reset := namedSelector(t)
	now := reset.Add(-time.Hour)
	selector.now = func() time.Time { return now }
	fableReset := now.Add(36 * time.Hour)
	selector.observeFableRejection("profile-a", fableRefusalHeaders(fableReset))

	requirePickModel(t, selector, auths, "new-fable", testFableModel, "profile-b")
	requirePickModel(t, selector, auths, "new-opus", testOtherModel, "profile-a")
	if out := logs.String(); !strings.Contains(out, "profile out of its weekly Fable allowance") || !strings.Contains(out, "profile=claude-primary") {
		t.Errorf("the refusal was not logged:\n%s", out)
	}
	selector.logSnapshot(time.Minute)
	if out := logs.String(); !strings.Contains(out, "fable_rate_limited_for=36h0m0s") {
		t.Errorf("the quota line does not say Fable is refused:\n%s", out)
	}

	now = fableReset.Add(time.Second)
	requirePickModel(t, selector, auths, "later-fable", testFableModel, "profile-a")
}

// A conversation on the refused subscription moves for Fable when it can (its state is portable) and is
// refused with Anthropic's Fable 429 when it cannot.
func TestFableRefusalMovesAPortableConversationAndRefusesAnOpaqueOne(t *testing.T) {
	selector, auths, reset := namedSelector(t)
	now := reset.Add(-time.Hour)
	selector.now = func() time.Time { return now }
	requirePickModel(t, selector, auths, "portable", testFableModel, "profile-a")
	opaqueModel, opaque := modelRequest("opaque", testFableModel)
	if got, err := selector.Pick(t.Context(), "claude", opaqueModel, opaque, auths); err != nil || got.ID != "profile-a" {
		t.Fatalf("opaque first turn = %v, %v", got, err)
	}
	selector.observeFableRejection("profile-a", fableRefusalHeaders(now.Add(10*time.Hour)))

	requirePickModel(t, selector, auths, "portable", testFableModel, "profile-b")

	opaque.OriginalRequest = []byte(`{"messages":[{"role":"assistant","content":[{"type":"compaction","content":"summary","signature":"opaque"}]}]}`)
	_, err := selector.Pick(withBackendAttempt(t.Context()), "claude", opaqueModel, opaque, auths)
	var refusal *backendRefusalError
	if !errors.As(err, &refusal) || refusal.kind != backendRefusalQuota || !refusal.fable {
		t.Fatalf("an opaque conversation on a Fable-refused subscription: %v, want a Fable quota refusal", err)
	}
	// The same conversation on another model stays where it is.
	opaque.Metadata = map[string]any{coreexecutor.RequestedModelMetadataKey: testOtherModel}
	if got, err := selector.Pick(t.Context(), "claude", testOtherModel, opaque, auths); err != nil || got.ID != "profile-a" {
		t.Fatalf("the opaque conversation on another model: %v, %v; want profile-a", got, err)
	}
}

// With every subscription refused Fable, a Fable request gets the 429 Anthropic sends for the Fable limit,
// so Claude Code offers another model; it is not sent to the paid backup.
func TestEverySubscriptionOutOfFableAnswersWithAnthropicsFable429(t *testing.T) {
	selector, auths, reset := namedSelector(t)
	now := reset.Add(-time.Hour)
	selector.now = func() time.Time { return now }
	first, second := now.Add(3*time.Hour), now.Add(9*time.Hour)
	selector.observeFableRejection("profile-a", fableRefusalHeaders(second))
	selector.observeFableRejection("profile-b", fableRefusalHeaders(first))

	ctx := withBackendAttempt(context.Background())
	m, opts := modelRequest("fresh", testFableModel)
	if _, err := selector.Pick(ctx, "claude", m, opts, auths); err == nil {
		t.Fatal("a Fable request was routed with every subscription out of Fable")
	}
	w := httptest.NewRecorder()
	writeBackendFailure(ctx, w, http.StatusBadGateway, writeBackendNativeHeaders)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429: %s", w.Code, w.Body.String())
	}
	for key, want := range map[string]string{
		"Anthropic-Ratelimit-Unified-Representative-Claim": "seven_day_overage_included",
		"Anthropic-Ratelimit-Unified-7d_oi-Status":         "rejected",
		"Anthropic-Ratelimit-Unified-7d-Status":            "allowed",
		"Anthropic-Ratelimit-Unified-5h-Status":            "allowed",
		"Anthropic-Ratelimit-Unified-7d_oi-Reset":          strconv.FormatInt(first.Unix(), 10),
		"X-Should-Retry": "false",
	} {
		if got := w.Header().Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if w.Header().Get("Retry-After") != "" {
		t.Error("a Fable refusal tells the client to retry the same model")
	}
	requirePickModel(t, selector, auths, "other-model", testOtherModel, "profile-a")
}

// A Fable request goes first to a subscription with Fable left, even when the weekly order prefers another;
// requests for other models keep the weekly order.
func TestFableRequestsPreferASubscriptionWithFableLeft(t *testing.T) {
	selector, auths, reset := namedSelector(t)
	now := reset.Add(-time.Hour)
	selector.now = func() time.Time { return now }
	// profile-a resets first, so the week alone prefers it.
	selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: 0.3, resetsAt: reset})
	selector.observeQuota("profile-b", backendWeeklyQuota{known: true, used: 0.3, resetsAt: reset.Add(24 * time.Hour)})
	selector.observeFableHeaders("profile-a", ClaudeFableQuota{UsedFraction: 0.95, ResetsAt: reset})
	selector.observeFableHeaders("profile-b", ClaudeFableQuota{UsedFraction: 0.2, ResetsAt: reset.Add(24 * time.Hour)})

	requirePickModel(t, selector, auths, "fable", testFableModel, "profile-b")
	requirePickModel(t, selector, auths, "opus", testOtherModel, "profile-a")

	// A week in its reserve band outweighs Fable left on it.
	selector.observeQuota("profile-b", backendWeeklyQuota{known: true, used: 0.97, resetsAt: reset.Add(24 * time.Hour)})
	selector.observeFableHeaders("profile-a", ClaudeFableQuota{UsedFraction: 0.5, ResetsAt: reset.Add(7 * 24 * time.Hour)})
	requirePickModel(t, selector, auths, "fable-2", testFableModel, "profile-a")
}

// A Fable refusal reaching the result policy holds the subscription back from Fable, is not recorded as
// this request's failure (so the request may go elsewhere), and never benches the subscription.
func TestResultPolicyTurnsAFableRefusalIntoAFableHold(t *testing.T) {
	selector, _, reset := namedSelector(t)
	now := reset.Add(-time.Hour)
	selector.now = func() time.Time { return now }
	manager := coreauth.NewManager(nil, selector, nil)
	setBackendSeriesResultPolicy(manager, selector)
	ctx := withBackendAttempt(withResponseHeaders(fableRefusalHeaders(now.Add(20 * time.Hour))))
	manager.ResultPolicy().ApplyResultPolicy(ctx, coreauth.Result{
		AuthID: "profile-a", Model: testFableModel, Error: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests},
	})
	selector.mu.Lock()
	fableBlocked, weeklyBlocked := selector.fableBlockedLocked("profile-a"), selector.quotaBlockedLocked("profile-a")
	until := selector.fableBlockedUntil["profile-a"]
	selector.mu.Unlock()
	if !fableBlocked || !until.Equal(now.Add(20*time.Hour)) || weeklyBlocked {
		t.Fatalf("fable blocked %v until %v, weekly blocked %v", fableBlocked, until, weeklyBlocked)
	}
	if backendAttemptFailed(ctx, "", testFableModel) {
		t.Fatal("the Fable refusal was recorded as this request's failure, so it would not move")
	}

	// The same 429 for another model is that request's failure, as before.
	other := withBackendAttempt(withResponseHeaders(fableRefusalHeaders(now.Add(20 * time.Hour))))
	manager.ResultPolicy().ApplyResultPolicy(other, coreauth.Result{
		AuthID: "profile-b", Model: testOtherModel, Error: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests},
	})
	selector.mu.Lock()
	otherBlocked := selector.fableBlockedLocked("profile-b")
	selector.mu.Unlock()
	if otherBlocked || !backendAttemptFailed(other, "profile-b", testOtherModel) {
		t.Fatalf("a non-Fable 429 changed: fable blocked %v, recorded %v", otherBlocked, backendAttemptFailed(other, "profile-b", testOtherModel))
	}
}

func withResponseHeaders(h http.Header) context.Context {
	ctx := internallogging.WithResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeaders(ctx, h)
	return ctx
}

// A conversation bound to a subscription out of Fable, with every other subscription out of Fable too, gets
// the pool's refusal (the earliest any subscription has Fable again), not "this conversation cannot move".
func TestPortableConversationWithNoFableLeftGetsThePoolsRefusal(t *testing.T) {
	selector, auths, reset := namedSelector(t)
	now := reset.Add(-time.Hour)
	selector.now = func() time.Time { return now }
	requirePickModel(t, selector, auths, "bound", testFableModel, "profile-a")
	selector.observeFableRejection("profile-a", fableRefusalHeaders(now.Add(50*time.Hour)))
	selector.observeFableRejection("profile-b", fableRefusalHeaders(now.Add(2*time.Hour)))
	_, err := pickModel(t, selector, auths, "bound", testFableModel)
	var refusal *backendRefusalError
	if !errors.As(err, &refusal) || !refusal.fable || !refusal.resetAt.Equal(now.Add(2*time.Hour)) || strings.Contains(refusal.reason, "cannot move") {
		t.Fatalf("refusal = %+v", refusal)
	}
}

// The bound subscription is left out of the candidates (the core cools Fable on it) and is held back from
// Fable, with no other subscription to take it: the Fable refusal, not "not available right now".
func TestBoundSubscriptionInCooldownAndOutOfFableIsAFableRefusal(t *testing.T) {
	selector, auths, reset := namedSelector(t)
	now := reset.Add(-time.Hour)
	selector.now = func() time.Time { return now }
	requirePickModel(t, selector, auths, "bound", testFableModel, "profile-a")
	selector.observeFableRejection("profile-a", fableRefusalHeaders(now.Add(5*time.Hour)))
	selector.observeFableRejection("profile-b", fableRefusalHeaders(now.Add(5*time.Hour)))
	_, err := pickModel(t, selector, auths[1:], "bound", testFableModel)
	var refusal *backendRefusalError
	if !errors.As(err, &refusal) || refusal.kind != backendRefusalQuota || !refusal.fable {
		t.Fatalf("refusal = %v, want the Fable refusal", err)
	}
}

// One subscription out of Fable and the other only rate limited: an ordinary rate-limit refusal the client
// retries (the other comes back soon), not "the Fable limit is reached".
func TestFableRefusalOnlyWhenEverySubscriptionIsOutOfFable(t *testing.T) {
	selector, auths, reset := namedSelector(t)
	now := reset.Add(-time.Hour)
	selector.now = func() time.Time { return now }
	selector.observeFableRejection("profile-a", fableRefusalHeaders(now.Add(30*time.Hour)))
	selector.mu.Lock()
	selector.initializeLocked()
	selector.quotaBlockedUntil["profile-b"] = now.Add(10 * time.Second)
	selector.mu.Unlock()
	_, err := pickModel(t, selector, auths, "fresh", testFableModel)
	var refusal *backendRefusalError
	if !errors.As(err, &refusal) || refusal.kind != backendRefusalQuota || refusal.fable {
		t.Fatalf("refusal = %+v, want a plain rate-limit refusal", refusal)
	}

	// The retry time is when the first can take Fable again: here the Fable hold ends first.
	selector.observeFableRejection("profile-a", fableRefusalHeaders(now.Add(30*time.Hour)))
	selector.mu.Lock()
	selector.fableBlockedUntil["profile-a"] = now.Add(10 * time.Second)
	selector.quotaBlockedUntil["profile-b"] = now.Add(time.Hour)
	selector.mu.Unlock()
	_, err = pickModel(t, selector, auths, "fresh-2", testFableModel)
	if !errors.As(err, &refusal) || refusal.fable || !refusal.resetAt.Equal(now.Add(10*time.Second)) {
		t.Fatalf("refusal = %+v, want a plain refusal retried in 10s", refusal)
	}
}

// Only a served generation starts the backoff over: a token count says nothing about the Fable allowance.
func TestTokenCountDoesNotResetTheFableBackoff(t *testing.T) {
	selector, _, reset := namedSelector(t)
	now := reset.Add(-time.Hour)
	selector.now = func() time.Time { return now }
	manager := coreauth.NewManager(nil, selector, nil)
	setBackendSeriesResultPolicy(manager, selector)
	bare := http.Header{"Anthropic-Ratelimit-Unified-7d_oi-Status": {"rejected"}}
	selector.observeFableRejection("profile-a", bare)
	selector.observeFableRejection("profile-a", bare)
	manager.ResultPolicy().ApplyResultPolicy(withBackendAttempt(withResponseHeaders(http.Header{})), coreauth.Result{
		AuthID: "profile-a", Model: testFableModel, Success: true, SkipQuotaObservation: true,
	})
	selector.mu.Lock()
	level := selector.fableBackoffLevel["profile-a"]
	selector.mu.Unlock()
	if level != 2 {
		t.Fatalf("after a token count the backoff level is %d, want 2", level)
	}
	manager.ResultPolicy().ApplyResultPolicy(withBackendAttempt(withResponseHeaders(http.Header{})), coreauth.Result{
		AuthID: "profile-a", Model: testFableModel, Success: true,
	})
	selector.mu.Lock()
	level = selector.fableBackoffLevel["profile-a"]
	selector.mu.Unlock()
	if level != 0 {
		t.Fatalf("after a served Fable request the backoff level is %d, want 0", level)
	}
}

// A Fable request ranks by the worse of the week and Fable: a subscription whose week the poll calls used up
// cannot spend its Fable either (Fable draws on the week), so one with its week and some Fable left goes first.
func TestFableRankingTakesTheWorseOfWeekAndFable(t *testing.T) {
	selector, auths, reset := namedSelector(t)
	now := reset.Add(-time.Hour)
	selector.now = func() time.Time { return now }
	selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: 1, resetsAt: reset})
	selector.observeFableHeaders("profile-a", ClaudeFableQuota{UsedFraction: 0.2, ResetsAt: reset})
	selector.observeQuota("profile-b", backendWeeklyQuota{known: true, used: 0.2, resetsAt: reset.Add(24 * time.Hour)})
	selector.observeFableHeaders("profile-b", ClaudeFableQuota{UsedFraction: 0.95, ResetsAt: reset.Add(24 * time.Hour)})
	requirePickModel(t, selector, auths, "fable", testFableModel, "profile-b")
}

// The poll's reset carries fractions of a second and the headers whole seconds: the headers of the same
// week still raise the figure.
func TestFableHeadersOfTheSameWeekAreKeptDespiteRounding(t *testing.T) {
	selector, _, reset := namedSelector(t)
	selector.now = func() time.Time { return reset.Add(-time.Hour) }
	week := reset.Add(48*time.Hour + 400*time.Millisecond)
	if !selector.observePolledFable("profile-a", ClaudeFableQuota{UsedFraction: 0.3, ResetsAt: week}, selector.fablePollRevision("profile-a")) {
		t.Fatal("poll dropped")
	}
	selector.observeFableHeaders("profile-a", ClaudeFableQuota{UsedFraction: 0.45, ResetsAt: week.Truncate(time.Second)})
	selector.mu.Lock()
	got, _ := selector.fableNowLocked("profile-a", reset.Add(-time.Hour))
	selector.mu.Unlock()
	if got.UsedFraction != 0.45 {
		t.Fatalf("a header of the same week was dropped: %+v", got)
	}
}

// A hold that is only a backoff ends at the Fable reset a later poll or header gives, if that comes
// first; a hold to a reset Anthropic gave is not shortened by a later figure.
func TestALaterResetShortensAGuessedFableHold(t *testing.T) {
	selector, _, reset := namedSelector(t)
	now := reset.Add(-time.Hour)
	selector.now = func() time.Time { return now }
	held := func() time.Duration {
		selector.mu.Lock()
		defer selector.mu.Unlock()
		return selector.fableBlockedUntil["profile-a"].Sub(now)
	}
	bare := http.Header{"Anthropic-Ratelimit-Unified-7d_oi-Status": {"rejected"}}
	for range 12 {
		selector.observeFableRejection("profile-a", bare)
	}
	if got := held(); got != 30*time.Minute {
		t.Fatalf("backoff hold = %v, want 30m", got)
	}
	selector.observePolledFable("profile-a", ClaudeFableQuota{UsedFraction: 1, ResetsAt: now.Add(time.Minute)}, selector.fablePollRevision("profile-a"))
	if got := held(); got != time.Minute {
		t.Fatalf("after a poll with the reset a minute away: %v, want 1m", got)
	}

	firm := func() time.Duration {
		selector.mu.Lock()
		defer selector.mu.Unlock()
		return selector.fableBlockedUntil["profile-b"].Sub(now)
	}
	selector.observeFableRejection("profile-b", fableRefusalHeaders(now.Add(5*time.Hour)))
	// A refusal with no reset while that hold is in force keeps it as a reset Anthropic gave: a later
	// poll does not shorten it.
	selector.observeFableRejection("profile-b", bare)
	selector.observePolledFable("profile-b", ClaudeFableQuota{UsedFraction: 1, ResetsAt: now.Add(time.Hour)}, selector.fablePollRevision("profile-b"))
	if got := firm(); got != 5*time.Hour {
		t.Fatalf("a refusal without a reset turned a known hold into a guess: %v", got)
	}
	selector.observeFableHeaders("profile-b", ClaudeFableQuota{UsedFraction: 0.2, ResetsAt: now.Add(2 * time.Hour)})
	if got := firm(); got != 5*time.Hour {
		t.Fatalf("a reset Anthropic gave was shortened by a later figure: %v", got)
	}

	// The backoff outruns a reset Anthropic gave: a later, earlier figure never ends the hold before
	// that reset.
	selector.mu.Lock()
	delete(selector.fableBlockedUntil, "profile-b")
	delete(selector.fableBackoffLevel, "profile-b")
	delete(selector.fable, "profile-b") // no Fable reset known: the later refusals are a backoff
	selector.mu.Unlock()
	selector.observeFableRejection("profile-b", fableRefusalHeaders(now.Add(time.Second)))
	for range 3 {
		selector.observeFableRejection("profile-b", bare)
	}
	selector.observePolledFable("profile-b", ClaudeFableQuota{UsedFraction: 1, ResetsAt: now.Add(500 * time.Millisecond)}, selector.fablePollRevision("profile-b"))
	if got := firm(); got < time.Second {
		t.Fatalf("a guessed hold was shortened below the reset Anthropic gave: %v", got)
	}

	// A poll that started before the refusal brings a reset already past: the hold stays.
	selector.mu.Lock()
	delete(selector.fableBlockedUntil, "profile-a")
	delete(selector.fableBackoffLevel, "profile-a")
	delete(selector.fableHoldFloor, "profile-a")
	delete(selector.fable, "profile-a")
	selector.mu.Unlock()
	selector.observeFableRejection("profile-a", bare)
	selector.observePolledFable("profile-a", ClaudeFableQuota{UsedFraction: 1, ResetsAt: now.Add(-time.Minute)}, selector.fablePollRevision("profile-a"))
	if got := held(); got != time.Second {
		t.Fatalf("a reset already past ended the hold: %v", got)
	}
}
