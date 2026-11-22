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
)

// The usage endpoint's rows as Claude Code reads them: kind, group, percent, resets_at, scope, severity, is_active.
const fableUsageBody = `{"limits":[` +
	`{"kind":"session","group":"session","percent":12,"resets_at":"2026-10-09T15:00:00Z","scope":null,"severity":"normal","is_active":false},` +
	`{"kind":"weekly_all","group":"weekly","percent":64,"resets_at":"2026-10-12T00:00:00Z","scope":null,"severity":"normal","is_active":true},` +
	`{"kind":"weekly_scoped","group":"weekly","percent":37,"resets_at":"2026-10-12T00:00:00Z","scope":{"model":{"display_name":"Fable"},"surface":null},"severity":"normal","is_active":false}]}`

func TestParseClaudeFableQuota(t *testing.T) {
	reset := time.Date(2026, time.October, 12, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, payload string
		known         bool
		used          float64
		resets        time.Time
	}{
		{name: "usage_endpoint_rows", payload: fableUsageBody, known: true, used: 0.37, resets: reset},
		{name: "versioned_name", payload: `{"limits":[{"kind":"weekly_scoped","percent":5,"resets_at":"2026-10-12T00:00:00Z","scope":{"model":{"display_name":"Fable 5.2"}}}]}`, known: true, used: 0.05, resets: reset},
		{name: "no_reset_yet", payload: `{"limits":[{"kind":"weekly_scoped","percent":0,"resets_at":null,"scope":{"model":{"display_name":"Fable"}}}]}`, known: true},
		{name: "wrapped", payload: `{"rate_limits":{"limits":[{"kind":"weekly_scoped","percent":50,"resets_at":"2026-10-12T00:00:00Z","scope":{"model":{"display_name":"fable"}}}]}}`, known: true, used: 0.5, resets: reset},
		{name: "keyed", payload: `{"limits":{"x":{"kind":"weekly_scoped","percent":100,"resets_at":1791763200,"scope":{"model":{"display_name":"Fable"}}}}}`, known: true, used: 1, resets: reset},
		{name: "clamped", payload: `{"limits":[{"kind":"weekly_scoped","percent":140,"resets_at":"2026-10-12T00:00:00Z","scope":{"model":{"display_name":"Fable"}}}]}`, known: true, used: 1, resets: reset},
		{name: "another_model", payload: `{"limits":[{"kind":"weekly_scoped","percent":90,"resets_at":"2026-10-12T00:00:00Z","scope":{"model":{"display_name":"Sonnet"}}}]}`},
		{name: "not_a_word_prefix", payload: `{"limits":[{"kind":"weekly_scoped","percent":90,"resets_at":"2026-10-12T00:00:00Z","scope":{"model":{"display_name":"Fableish"}}}]}`},
		{name: "a_surface", payload: `{"limits":[{"kind":"weekly_scoped","percent":90,"resets_at":"2026-10-12T00:00:00Z","scope":{"surface":{"display_name":"Fable"}}}]}`},
		{name: "not_scoped_kind", payload: `{"limits":[{"kind":"weekly_all","percent":90,"resets_at":"2026-10-12T00:00:00Z","scope":{"model":{"display_name":"Fable"}}}]}`},
		{name: "weekly_only", payload: `{"seven_day":{"utilization":10,"resets_at":"2026-10-12T00:00:00Z"}}`},
		{name: "negative", payload: `{"limits":[{"kind":"weekly_scoped","percent":-1,"resets_at":"2026-10-12T00:00:00Z","scope":{"model":{"display_name":"Fable"}}}]}`},
		{name: "bad_reset", payload: `{"limits":[{"kind":"weekly_scoped","percent":4,"resets_at":"soon","scope":{"model":{"display_name":"Fable"}}}]}`},
		{name: "not_an_object", payload: `[1,2]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			quota, known, err := ParseClaudeFableQuota([]byte(tc.payload))
			if err != nil {
				t.Fatal(err)
			}
			if known != tc.known || quota.UsedFraction != tc.used || !quota.ResetsAt.Equal(tc.resets) {
				t.Fatalf("ParseClaudeFableQuota() = %+v, %v; want used %v resets %v known %v", quota, known, tc.used, tc.resets, tc.known)
			}
		})
	}
	for _, bad := range []string{`{"limits":`, `{} {}`} {
		if _, _, err := ParseClaudeFableQuota([]byte(bad)); err == nil {
			t.Errorf("%q parsed without an error", bad)
		}
	}
	// The Fable row never becomes the account-wide weekly figure.
	if weekly, known, err := ParseClaudeWeeklyQuota([]byte(fableUsageBody)); err != nil || !known || weekly.UsedFraction != 0.64 {
		t.Fatalf("weekly figure = %+v, %v, %v; want 0.64 from weekly_all", weekly, known, err)
	}
}

func TestParseClaudeFableQuotaHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("Anthropic-Ratelimit-Unified-7d_oi-Utilization", "0.82")
	h.Set("Anthropic-Ratelimit-Unified-7d_oi-Reset", "1791763200")
	quota, ok := ParseClaudeFableQuotaHeaders(h)
	if !ok || quota.UsedFraction != 0.82 || !quota.ResetsAt.Equal(time.Date(2026, time.October, 12, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("ParseClaudeFableQuotaHeaders() = %+v, %v", quota, ok)
	}
	h.Del("Anthropic-Ratelimit-Unified-7d_oi-Reset")
	if _, ok := ParseClaudeFableQuotaHeaders(h); ok {
		t.Fatal("a utilization without a reset was accepted")
	}
	weekly := http.Header{}
	weekly.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.5")
	weekly.Set("Anthropic-Ratelimit-Unified-7d-Reset", "1791763200")
	if _, ok := ParseClaudeFableQuotaHeaders(weekly); ok {
		t.Fatal("the weekly window was read as the Fable allowance")
	}
}

// The usage poll's Fable row reaches the gauges and the quota log line, and stops counting once its
// reset has passed.
func TestFableAllowanceReachesTheGaugesFromThePoll(t *testing.T) {
	reader := startMetrics(t, nil)
	logs := captureLogs(t, "info")
	selector, _, reset := namedSelector(t)
	now := reset.Add(-time.Hour)
	selector.now = func() time.Time { return now }
	manager := coreauth.NewManager(nil, nil, nil)
	for _, authID := range selector.authIDs {
		if _, err := manager.Register(t.Context(), &coreauth.Auth{
			ID: authID, Provider: "claude", Status: coreauth.StatusActive,
			Attributes: map[string]string{coreauth.AttributeAuthKind: coreauth.AuthKindOAuth},
			Metadata:   map[string]any{"access_token": "opaque-" + authID, "expired": "2099-01-01T00:00:00Z"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	fableEnds := now.Add(2 * time.Hour).Format(time.RFC3339)
	loadBackendWeeklyQuotas(t.Context(), manager, selector, selector.authIDs, func(_ context.Context, auth *coreauth.Auth, _ *http.Request) (*http.Response, error) {
		body := `{"limits":[{"kind":"weekly_all","percent":25,"resets_at":"2026-10-05T00:00:00Z"},` +
			`{"kind":"weekly_scoped","percent":37,"resets_at":"` + fableEnds + `","scope":{"model":{"display_name":"Fable"}}}]}`
		if auth.ID == "profile-b" {
			// A seat without Fable in its plan has no Fable row.
			body = `{"limits":[{"kind":"weekly_all","percent":5,"resets_at":"2026-10-05T00:00:00Z"}]}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})

	src := &fixedSource{snap: selector.metricsSnapshot(nil)}
	registerStateSource(src)
	t.Cleanup(func() { unregisterStateSource(src) })
	rm := collect(t, reader)
	a, b := map[string]string{"profile": "claude-primary"}, map[string]string{"profile": "claude-secondary"}
	if v, ok := gaugeOf(rm, "claw_master.quota.fable.used_fraction", a); !ok || v != 0.37 {
		t.Errorf("fable.used_fraction = %v (%v), want 0.37", v, ok)
	}
	if v, ok := gaugeOf(rm, "claw_master.quota.fable.resets_in_seconds", a); !ok || v != 7200 {
		t.Errorf("fable.resets_in_seconds = %v (%v), want 7200", v, ok)
	}
	if _, ok := gaugeOf(rm, "claw_master.quota.fable.used_fraction", b); ok {
		t.Error("an account with no Fable row reports a Fable figure")
	}
	if v, ok := gaugeOf(rm, "claw_master.quota.used_fraction", a); !ok || v != 0.25 {
		t.Errorf("the weekly gauge changed: %v (%v), want 0.25", v, ok)
	}
	selector.logSnapshot(time.Minute)
	if out := logs.String(); !strings.Contains(out, "fable_used_pct=37") || !strings.Contains(out, "fable_resets_in=2h0m0s") || !strings.Contains(out, "fable_used_pct=unknown") {
		t.Errorf("the quota log line lacks the Fable allowance:\n%s", out)
	}

	// The week resets with no poll in between: nothing of the new week is used.
	now = now.Add(2*time.Hour + time.Minute)
	src.snap = selector.metricsSnapshot(nil)
	rm = collect(t, reader)
	if v, ok := gaugeOf(rm, "claw_master.quota.fable.used_fraction", a); !ok || v != 0 {
		t.Errorf("after the reset: fable.used_fraction = %v (%v), want 0", v, ok)
	}
	if _, ok := gaugeOf(rm, "claw_master.quota.fable.resets_in_seconds", a); ok {
		t.Error("after the reset a countdown is still reported")
	}
}

// Response headers raise the figure within a week and start a new week on a later reset; an older
// reading never lowers it. A poll started before headers arrived does not overwrite them.
func TestFableAllowanceFromHeadersAndPoll(t *testing.T) {
	selector, _, reset := namedSelector(t)
	now := reset.Add(-time.Hour)
	selector.now = func() time.Time { return now }
	week := now.Add(48 * time.Hour)
	read := func() ClaudeFableQuota {
		selector.mu.Lock()
		defer selector.mu.Unlock()
		quota, _ := selector.fableNowLocked("profile-a", now)
		return quota
	}

	revision := selector.fablePollRevision("profile-a")
	selector.observeFableHeaders("profile-a", ClaudeFableQuota{UsedFraction: 0.4, ResetsAt: week})
	if selector.observePolledFable("profile-a", ClaudeFableQuota{UsedFraction: 0.1, ResetsAt: week}, revision) {
		t.Fatal("a poll started before newer headers overwrote them")
	}
	if got := read(); got.UsedFraction != 0.4 {
		t.Fatalf("after a stale poll: %+v, want 0.4", got)
	}
	selector.observeFableHeaders("profile-a", ClaudeFableQuota{UsedFraction: 0.3, ResetsAt: week})
	if got := read(); got.UsedFraction != 0.4 {
		t.Fatalf("a lower reading in the same week lowered the figure: %+v", got)
	}
	selector.observeFableHeaders("profile-a", ClaudeFableQuota{UsedFraction: 0.9, ResetsAt: week.Add(-time.Hour)})
	if got := read(); got.UsedFraction != 0.4 {
		t.Fatalf("a reading of an earlier week replaced the figure: %+v", got)
	}
	selector.observeFableHeaders("profile-a", ClaudeFableQuota{UsedFraction: 0.05, ResetsAt: week.Add(7 * 24 * time.Hour)})
	if got := read(); got.UsedFraction != 0.05 {
		t.Fatalf("a new week did not start a new figure: %+v", got)
	}
	// A poll with no headers since it started is authoritative, also when lower.
	revision = selector.fablePollRevision("profile-a")
	if !selector.observePolledFable("profile-a", ClaudeFableQuota{UsedFraction: 0.02, ResetsAt: week.Add(7 * 24 * time.Hour)}, revision) {
		t.Fatal("a current poll was dropped")
	}
	if got := read(); got.UsedFraction != 0.02 {
		t.Fatalf("after a current poll: %+v, want 0.02", got)
	}
}

// The 7d_oi headers of an inference response, a refused one included, reach the selector through the
// result policy; a token count's do not, like the weekly headers.
func TestBackendSeriesResultPolicyReadsFableHeaders(t *testing.T) {
	selector := &backendSeriesSelector{authIDs: []string{"profile-a"}, provider: "claude"}
	t.Cleanup(selector.Stop)
	reset := time.Date(2026, time.October, 2, 0, 0, 0, 0, time.UTC)
	selector.now = func() time.Time { return reset.Add(-time.Hour) }
	manager := coreauth.NewManager(nil, selector, nil)
	setBackendSeriesResultPolicy(manager, selector)
	fable := func() (ClaudeFableQuota, bool) {
		selector.mu.Lock()
		defer selector.mu.Unlock()
		return selector.fableNowLocked("profile-a", reset.Add(-time.Hour))
	}
	ctx := internallogging.WithResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeaders(ctx, http.Header{
		"Anthropic-Ratelimit-Unified-7d_oi-Utilization": []string{"0.6"},
		"Anthropic-Ratelimit-Unified-7d_oi-Reset":       []string{strconv.FormatInt(reset.Unix(), 10)},
	})
	manager.ResultPolicy().ApplyResultPolicy(ctx, coreauth.Result{AuthID: "profile-a", Success: true, SkipQuotaObservation: true})
	if _, known := fable(); known {
		t.Fatal("a token count's headers set the Fable figure")
	}
	manager.ResultPolicy().ApplyResultPolicy(ctx, coreauth.Result{AuthID: "profile-a", Success: true})
	if got, known := fable(); !known || got.UsedFraction != 0.6 || !got.ResetsAt.Equal(reset) {
		t.Fatalf("after a response: %+v, %v; want 0.6 at %v", got, known, reset)
	}
	refused := internallogging.WithResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeaders(refused, http.Header{
		"Anthropic-Ratelimit-Unified-7d_oi-Utilization": []string{"1.0"},
		"Anthropic-Ratelimit-Unified-7d_oi-Reset":       []string{strconv.FormatInt(reset.Unix(), 10)},
		"Anthropic-Ratelimit-Unified-7d_oi-Status":      []string{"rejected"},
	})
	manager.ResultPolicy().ApplyResultPolicy(refused, coreauth.Result{AuthID: "profile-a", Model: "claude-fable-5-2", Error: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests}})
	if got, _ := fable(); got.UsedFraction != 1 {
		t.Fatalf("after a Fable refusal: %+v, want used up", got)
	}
}
