package clawmaster

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Anthropic refuses a subscription for its week (a credential-wide 429, which the selector and the core
// each hold), then the poll reads that week reset to nothing used, as a limit reset does. Both holds are
// lifted, so the subscription takes work again. A rejection of the five-hour window, or a poll still near
// the weekly limit, keeps them.
func TestAWeekResetByAnthropicLiftsBothHolds(t *testing.T) {
	const model = "claude-opus-5-5"
	for _, tc := range []struct {
		name      string
		fiveHour  string  // the 5h status on the 429
		polled    float64 // the week's use the poll then reads
		wantLifts bool
	}{
		{name: "week reset", fiveHour: "allowed", polled: 0, wantLifts: true},
		{name: "still near the limit", fiveHour: "allowed", polled: 0.95},
		{name: "the five-hour window was rejected", fiveHour: "rejected", polled: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selector, _, _ := namedSelector(t)
			now := time.Now()
			selector.now = func() time.Time { return now }
			weekEnds := now.Add(55 * time.Hour)
			manager := coreauth.NewManager(nil, selector, nil)
			setBackendSeriesResultPolicy(manager, selector)
			for _, authID := range selector.authIDs {
				if _, err := manager.Register(t.Context(), &coreauth.Auth{
					ID: authID, Provider: "claude", Status: coreauth.StatusActive,
					Attributes: map[string]string{coreauth.AttributeAuthKind: coreauth.AuthKindOAuth},
					Metadata:   map[string]any{"access_token": "opaque-" + authID, "expired": "2099-01-01T00:00:00Z"},
				}); err != nil {
					t.Fatal(err)
				}
			}
			usable := func() (selectorFree, coreFree bool) {
				selector.mu.Lock()
				selectorFree = !selector.quotaBlockedLocked("profile-a")
				selector.mu.Unlock()
				auth, _ := manager.GetByID("profile-a")
				picked, err := (&coreauth.FillFirstSelector{}).Pick(t.Context(), "claude", model, coreexecutor.Options{}, []*coreauth.Auth{auth})
				return selectorFree, err == nil && picked != nil
			}

			ctx := internallogging.WithResponseHeadersHolder(context.Background())
			internallogging.SetResponseHeaders(ctx, http.Header{
				"Anthropic-Ratelimit-Unified-Status":               {"rejected"},
				"Anthropic-Ratelimit-Unified-7d-Status":            {"rejected"},
				"Anthropic-Ratelimit-Unified-5h-Status":            {tc.fiveHour},
				"Anthropic-Ratelimit-Unified-7d-Utilization":       {"1.0"},
				"Anthropic-Ratelimit-Unified-7d-Reset":             {weekEnds.Format(time.RFC3339)},
				"Anthropic-Ratelimit-Unified-Representative-Claim": {"seven_day"},
			})
			wait := 55 * time.Hour
			manager.MarkResult(ctx, coreauth.Result{
				AuthID: "profile-a", Provider: "claude", Model: model, CredentialScope: true, RetryAfter: &wait,
				Error: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests},
			})
			if selectorFree, coreFree := usable(); selectorFree || coreFree {
				t.Fatalf("after the 429: selector free %v, core free %v; want both held", selectorFree, coreFree)
			}

			polled := `{"limits":[{"kind":"weekly_all","percent":` + strconv.FormatFloat(tc.polled*100, 'f', -1, 64) + `,"resets_at":"` + weekEnds.Format(time.RFC3339) + `"}]}`
			loadBackendWeeklyQuotas(t.Context(), manager, selector, []string{"profile-a"}, func(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(polled))}, nil
			})
			selectorFree, coreFree := usable()
			if selectorFree != tc.wantLifts || coreFree != tc.wantLifts {
				t.Fatalf("after the poll: selector free %v, core free %v; want both %v", selectorFree, coreFree, tc.wantLifts)
			}
		})
	}
}

// The fall that lifts a weekly hold is measured from the highest use read during the hold, not from the poll
// just before. On 2026-11-25 Anthropic refused a subscription its poll read at 90%, the polls kept reading 90%,
// and the week's reset (90% to 41%) did not lift the hold: with every other subscription out, the pool refused
// every request until a restart. Two polls of the same week are the same meter, so a small fall between them
// (a Fable-sized reset) lifts; against the 429's own figure only a fall too large for a poll's lag does. A poll
// of another week lifts nothing.
func TestAWeekResetLiftsTheHoldMeasuredFromItsPeak(t *testing.T) {
	type reading struct {
		used    float64
		newWeek bool // the poll reads a later week (resets_at moved)
		free    bool // whether the subscription is free after it
	}
	for _, tc := range []struct {
		name    string
		refused string // the 7d utilization on the 429
		polls   []reading
	}{
		{name: "refused at a polled 90%, then reset", refused: "0.9", polls: []reading{{used: 0.9}, {used: 0.9}, {used: 0.41, free: true}}},
		{name: "refused at 100%, a lagging poll, then reset", refused: "1.0", polls: []reading{{used: 0.92}, {used: 0.41, free: true}}},
		{name: "refused at 100%, a lagging poll, then a 20-point fall", refused: "1.0", polls: []reading{{used: 0.9}, {used: 0.7, free: true}}},
		{name: "a Fable-sized reset between polls", refused: "1.0", polls: []reading{{used: 0.9}, {used: 0.9}, {used: 0.85, free: true}}},
		{name: "the first poll far below the 429", refused: "1.0", polls: []reading{{used: 0.7, free: true}}},
		{name: "the first poll a little below the 429 is lag", refused: "1.0", polls: []reading{{used: 0.85}}},
		{name: "a one-point fall is not a reset", refused: "0.9", polls: []reading{{used: 0.9}, {used: 0.89}}},
		{name: "a lagging poll is not a reset", refused: "1.0", polls: []reading{{used: 0.9}, {used: 0.9}}},
		{name: "a poll of another week lifts nothing", refused: "1.0", polls: []reading{{used: 0.9}, {used: 0.1, newWeek: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newWeekHoldHarness(t)
			h.poll(0.9, false) // the poll before the refusal: the reserve band, as on 2026-11-25
			h.refuse(tc.refused, 160*time.Hour)
			if selectorFree, coreFree := h.usable(); selectorFree || coreFree {
				t.Fatalf("after the 429: selector free %v, core free %v; want both held", selectorFree, coreFree)
			}
			for i, r := range tc.polls {
				h.advance(3 * time.Minute)
				h.poll(r.used, r.newWeek)
				if selectorFree, coreFree := h.usable(); selectorFree != r.free || coreFree != r.free {
					t.Fatalf("after poll %d (%.0f%%): selector free %v, core free %v; want both %v", i+1, r.used*100, selectorFree, coreFree, r.free)
				}
			}
		})
	}
}

// A hold's peaks end with the hold: the week is reset after one hold ends, Anthropic refuses the subscription
// again at the new, lower figure, and a poll of that figure must not lift the new hold against the old peaks.
func TestAWeeklyHoldsPeaksDoNotCarryIntoTheNextHold(t *testing.T) {
	h := newWeekHoldHarness(t)
	h.refuse("1.0", time.Hour)
	h.advance(3 * time.Minute)
	h.poll(0.9, false)
	h.advance(2 * time.Hour) // the first hold has ended
	h.poll(0.6, false)       // the week was reset meanwhile
	h.refuse("0.6", 160*time.Hour)
	h.advance(3 * time.Minute)
	h.poll(0.6, false)
	if selectorFree, _ := h.usable(); selectorFree {
		t.Fatal("the second hold was lifted by the first hold's peaks")
	}
}

type weekHoldHarness struct {
	t        *testing.T
	selector *backendSeriesSelector
	manager  *coreauth.Manager
	now      time.Time
	weekEnds time.Time
}

func newWeekHoldHarness(t *testing.T) *weekHoldHarness {
	selector, _, _ := namedSelector(t)
	h := &weekHoldHarness{t: t, selector: selector, now: time.Now()}
	h.weekEnds = h.now.Add(160 * time.Hour)
	selector.now = func() time.Time { return h.now }
	h.manager = coreauth.NewManager(nil, selector, nil)
	setBackendSeriesResultPolicy(h.manager, selector)
	for _, authID := range selector.authIDs {
		if _, err := h.manager.Register(t.Context(), &coreauth.Auth{
			ID: authID, Provider: "claude", Status: coreauth.StatusActive,
			Attributes: map[string]string{coreauth.AttributeAuthKind: coreauth.AuthKindOAuth},
			Metadata:   map[string]any{"access_token": "opaque-" + authID, "expired": "2099-01-01T00:00:00Z"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func (h *weekHoldHarness) advance(d time.Duration) { h.now = h.now.Add(d) }

func (h *weekHoldHarness) poll(used float64, newWeek bool) {
	resets := h.weekEnds
	if newWeek {
		resets = resets.Add(7 * 24 * time.Hour)
	}
	body := `{"limits":[{"kind":"weekly_all","percent":` + strconv.FormatFloat(used*100, 'f', -1, 64) + `,"resets_at":"` + resets.Format(time.RFC3339) + `"}]}`
	loadBackendWeeklyQuotas(h.t.Context(), h.manager, h.selector, []string{"profile-a"}, func(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
}

// refuse is Anthropic's weekly 429 on profile-a, carrying the week's use as refused.
func (h *weekHoldHarness) refuse(refused string, wait time.Duration) {
	ctx := internallogging.WithResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeaders(ctx, http.Header{
		"Anthropic-Ratelimit-Unified-Status":               {"rejected"},
		"Anthropic-Ratelimit-Unified-7d-Status":            {"rejected"},
		"Anthropic-Ratelimit-Unified-5h-Status":            {"allowed"},
		"Anthropic-Ratelimit-Unified-7d-Utilization":       {refused},
		"Anthropic-Ratelimit-Unified-7d-Reset":             {h.weekEnds.Format(time.RFC3339)},
		"Anthropic-Ratelimit-Unified-Representative-Claim": {"seven_day"},
	})
	h.manager.MarkResult(ctx, coreauth.Result{
		AuthID: "profile-a", Provider: "claude", Model: "claude-opus-5-5", CredentialScope: true, RetryAfter: &wait,
		Error: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests},
	})
}

func (h *weekHoldHarness) usable() (selectorFree, coreFree bool) {
	h.selector.mu.Lock()
	selectorFree = !h.selector.quotaBlockedLocked("profile-a")
	h.selector.mu.Unlock()
	auth, _ := h.manager.GetByID("profile-a")
	picked, err := (&coreauth.FillFirstSelector{}).Pick(h.t.Context(), "claude", "claude-opus-5-5", coreexecutor.Options{}, []*coreauth.Auth{auth})
	return selectorFree, err == nil && picked != nil
}
