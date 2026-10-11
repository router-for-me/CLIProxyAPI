package claudemaster

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestParseClaudeFiveHourQuota(t *testing.T) {
	reset := time.Date(2026, time.October, 9, 15, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, payload string
		known         bool
		used          float64
		resets        time.Time
	}{
		{name: "top_level", payload: `{"five_hour":{"utilization":42,"resets_at":"2026-10-09T15:00:00Z"},"seven_day":{"utilization":10,"resets_at":"2026-10-12T00:00:00Z"}}`, known: true, used: 0.42, resets: reset},
		{name: "no_window_open", payload: `{"five_hour":{"utilization":0,"resets_at":null}}`, known: true},
		{name: "limits_row", payload: `{"limits":[{"name":"five_hour","utilization":61,"resets_at":"2026-10-09T15:00:00Z"},{"kind":"weekly_all","percent":91,"resets_at":"2026-10-12T00:00:00Z"}]}`, known: true, used: 0.61, resets: reset},
		{name: "limits_row_kind", payload: `{"limits":[{"kind":"5h","percent":7,"resets_at":1791558000}]}`, known: true, used: 0.07, resets: reset},
		{name: "wrapped_row", payload: `{"rate_limits":{"limits":[{"name":"five_hour","utilization":50,"resets_at":"2026-10-09T15:00:00Z"}]}}`, known: true, used: 0.5, resets: reset},
		{name: "keyed_row", payload: `{"limits":{"five_hour":{"percent":12,"resets_at":"2026-10-09T15:00:00Z"}}}`, known: true, used: 0.12, resets: reset},
		{name: "row_wins_over_top_level", payload: `{"five_hour":{"utilization":1,"resets_at":"2026-10-09T15:00:00Z"},"limits":[{"name":"five_hour","utilization":30,"resets_at":"2026-10-09T15:00:00Z"}]}`, known: true, used: 0.3, resets: reset},
		{name: "clamped", payload: `{"five_hour":{"utilization":150,"resets_at":"2026-10-09T15:00:00Z"}}`, known: true, used: 1, resets: reset},
		{name: "weekly_only", payload: `{"seven_day":{"utilization":10,"resets_at":"2026-10-12T00:00:00Z"},"limits":[{"kind":"weekly_all","percent":91,"resets_at":"2026-10-12T00:00:00Z"}]}`},
		{name: "no_utilization", payload: `{"five_hour":{"resets_at":"2026-10-09T15:00:00Z"}}`},
		{name: "negative", payload: `{"five_hour":{"utilization":-1,"resets_at":"2026-10-09T15:00:00Z"}}`},
		{name: "bad_reset", payload: `{"five_hour":{"utilization":5,"resets_at":"soon"}}`},
		{name: "not_an_object", payload: `[1,2]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			quota, known, err := ParseClaudeFiveHourQuota([]byte(tc.payload))
			if err != nil {
				t.Fatal(err)
			}
			if known != tc.known || quota.UsedFraction != tc.used || !quota.ResetsAt.Equal(tc.resets) {
				t.Fatalf("ParseClaudeFiveHourQuota() = %+v, %v; want used %v resets %v known %v", quota, known, tc.used, tc.resets, tc.known)
			}
		})
	}
	for _, bad := range []string{`{"five_hour":`, `{} {}`} {
		if _, _, err := ParseClaudeFiveHourQuota([]byte(bad)); err == nil {
			t.Errorf("%q parsed without an error", bad)
		}
	}
}

// The usage poll's five-hour row reaches the gauges and the quota log line, and stops counting as
// used once its reset has passed, so an idle account does not keep showing an old window.
func TestFiveHourWindowReachesTheGaugesFromThePoll(t *testing.T) {
	reader := startMetrics(t, nil)
	logs := captureLogs(t, "info")
	selector, _, reset := namedSelector(t) // now is an hour before reset
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
	windowEnds := now.Add(30 * time.Minute).Format(time.RFC3339)
	loadBackendWeeklyQuotas(t.Context(), manager, selector, selector.authIDs, func(_ context.Context, auth *coreauth.Auth, _ *http.Request) (*http.Response, error) {
		body := `{"five_hour":{"utilization":42,"resets_at":"` + windowEnds + `"},"seven_day":{"utilization":25,"resets_at":"2026-10-05T00:00:00Z"}}`
		if auth.ID == "profile-b" {
			body = `{"five_hour":{"utilization":0,"resets_at":null},"seven_day":{"utilization":5,"resets_at":"2026-10-05T00:00:00Z"}}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})

	src := &fixedSource{snap: selector.metricsSnapshot(nil)}
	registerStateSource(src)
	t.Cleanup(func() { unregisterStateSource(src) })
	rm := collect(t, reader)
	a, b := map[string]string{"profile": "claude-connor"}, map[string]string{"profile": "claude-ejc3"}
	if v, ok := gaugeOf(rm, "claude_master.quota.five_hour.used_fraction", a); !ok || v != 0.42 {
		t.Errorf("five_hour.used_fraction = %v (%v), want 0.42", v, ok)
	}
	if v, ok := gaugeOf(rm, "claude_master.quota.five_hour.resets_in_seconds", a); !ok || v != 1800 {
		t.Errorf("five_hour.resets_in_seconds = %v (%v), want 1800", v, ok)
	}
	if v, ok := gaugeOf(rm, "claude_master.quota.five_hour.used_fraction", b); !ok || v != 0 {
		t.Errorf("an account with no open window: used_fraction = %v (%v), want 0", v, ok)
	}
	if _, ok := gaugeOf(rm, "claude_master.quota.five_hour.resets_in_seconds", b); ok {
		t.Error("an account with no open window reports a reset countdown")
	}
	if v, ok := gaugeOf(rm, "claude_master.quota.used_fraction", a); !ok || v != 0.25 {
		t.Errorf("the weekly gauge changed: %v (%v), want 0.25", v, ok)
	}
	selector.logSnapshot(time.Minute)
	if out := logs.String(); !strings.Contains(out, "five_hour_used_pct=42") || !strings.Contains(out, "five_hour_resets_in=30m0s") || !strings.Contains(out, `five_hour_resets_in="no window"`) {
		t.Errorf("the quota log line lacks the five-hour window:\n%s", out)
	}

	// The window resets with no poll in between: it is over, not still 42% used.
	now = now.Add(31 * time.Minute)
	src.snap = selector.metricsSnapshot(nil)
	rm = collect(t, reader)
	if v, ok := gaugeOf(rm, "claude_master.quota.five_hour.used_fraction", a); !ok || v != 0 {
		t.Errorf("after the reset: used_fraction = %v (%v), want 0", v, ok)
	}
	if _, ok := gaugeOf(rm, "claude_master.quota.five_hour.resets_in_seconds", a); ok {
		t.Error("after the reset a countdown is still reported")
	}
}
