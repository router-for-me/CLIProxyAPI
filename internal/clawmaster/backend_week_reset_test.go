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
// every request until a restart. A poll that lags behind the 429's own 100% is the same story. A fall too small
// to be a reset keeps the hold.
func TestAWeekResetLiftsTheHoldMeasuredFromItsPeak(t *testing.T) {
	const model = "claude-opus-5-5"
	for _, tc := range []struct {
		name     string
		refused  string    // the 7d utilization on the 429
		polls    []float64 // the week's use each poll then reads, in order
		wantFree []bool    // whether the subscription is free after each poll
	}{
		{name: "refused at a polled 90%, then reset", refused: "0.9", polls: []float64{0.9, 0.9, 0.41}, wantFree: []bool{false, false, true}},
		{name: "refused at 100%, a lagging poll, then reset", refused: "1.0", polls: []float64{0.92, 0.41}, wantFree: []bool{false, true}},
		{name: "a small fall is not a reset", refused: "0.9", polls: []float64{0.9, 0.8}, wantFree: []bool{false, false}},
		{name: "a lagging poll is not a reset", refused: "1.0", polls: []float64{0.9, 0.9}, wantFree: []bool{false, false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selector, _, _ := namedSelector(t)
			now := time.Now()
			selector.now = func() time.Time { return now }
			weekEnds := now.Add(160 * time.Hour)
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
			poll := func(used float64) {
				body := `{"limits":[{"kind":"weekly_all","percent":` + strconv.FormatFloat(used*100, 'f', -1, 64) + `,"resets_at":"` + weekEnds.Format(time.RFC3339) + `"}]}`
				loadBackendWeeklyQuotas(t.Context(), manager, selector, []string{"profile-a"}, func(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
				})
			}
			usable := func() (selectorFree, coreFree bool) {
				selector.mu.Lock()
				selectorFree = !selector.quotaBlockedLocked("profile-a")
				selector.mu.Unlock()
				auth, _ := manager.GetByID("profile-a")
				picked, err := (&coreauth.FillFirstSelector{}).Pick(t.Context(), "claude", model, coreexecutor.Options{}, []*coreauth.Auth{auth})
				return selectorFree, err == nil && picked != nil
			}

			poll(0.9) // the poll before the refusal: the reserve band, as on 2026-11-25
			ctx := internallogging.WithResponseHeadersHolder(context.Background())
			internallogging.SetResponseHeaders(ctx, http.Header{
				"Anthropic-Ratelimit-Unified-Status":               {"rejected"},
				"Anthropic-Ratelimit-Unified-7d-Status":            {"rejected"},
				"Anthropic-Ratelimit-Unified-5h-Status":            {"allowed"},
				"Anthropic-Ratelimit-Unified-7d-Utilization":       {tc.refused},
				"Anthropic-Ratelimit-Unified-7d-Reset":             {weekEnds.Format(time.RFC3339)},
				"Anthropic-Ratelimit-Unified-Representative-Claim": {"seven_day"},
			})
			wait := 160 * time.Hour
			manager.MarkResult(ctx, coreauth.Result{
				AuthID: "profile-a", Provider: "claude", Model: model, CredentialScope: true, RetryAfter: &wait,
				Error: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests},
			})
			if selectorFree, coreFree := usable(); selectorFree || coreFree {
				t.Fatalf("after the 429: selector free %v, core free %v; want both held", selectorFree, coreFree)
			}
			for i, used := range tc.polls {
				now = now.Add(3 * time.Minute)
				poll(used)
				if selectorFree, coreFree := usable(); selectorFree != tc.wantFree[i] || coreFree != tc.wantFree[i] {
					t.Fatalf("after poll %d (%.0f%%): selector free %v, core free %v; want both %v", i+1, used*100, selectorFree, coreFree, tc.wantFree[i])
				}
			}
		})
	}
}
