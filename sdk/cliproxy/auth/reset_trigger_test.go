package auth

import (
	"strconv"
	"testing"
	"time"
)

func TestResetTriggerStateForAuth(t *testing.T) {
	now := time.Unix(1787279282, 0)
	oauth := map[string]string{AttributeAuthKind: AuthKindOAuth}
	claudeAuth := func(signals map[string]string) *Auth {
		return &Auth{ID: "c", Provider: "claude", Status: StatusActive, Attributes: oauth,
			Quota: QuotaState{ObservedAt: now.Add(-6 * time.Hour), Signals: signals}}
	}
	lapsed := now.Add(-time.Hour)
	weekly := now.Add(3 * 24 * time.Hour)

	exhausted := claudeQuotaSignals(lapsed, weekly)
	exhausted["Anthropic-Ratelimit-Unified-7d-Status"] = "rejected"

	codexSignals := map[string]string{
		"X-Codex-Primary-Window-Minutes":   "300",
		"X-Codex-Primary-Reset-At":         strconv.FormatInt(lapsed.Unix(), 10),
		"X-Codex-Primary-Used-Percent":     "40",
		"X-Codex-Secondary-Window-Minutes": "10080",
		"X-Codex-Secondary-Reset-At":       strconv.FormatInt(weekly.Unix(), 10),
		"X-Codex-Secondary-Used-Percent":   "100",
	}
	codexWeeklyOnly := map[string]string{
		"X-Codex-Primary-Window-Minutes": "10080",
		"X-Codex-Primary-Reset-At":       strconv.FormatInt(weekly.Unix(), 10),
		"X-Codex-Primary-Used-Percent":   "10",
	}

	tests := []struct {
		name      string
		auth      *Auth
		wantDue   bool
		wantIdle  time.Time
		wantGroup string
	}{
		{name: "claude lapsed 5h", auth: claudeAuth(claudeQuotaSignals(lapsed, weekly)), wantDue: true, wantIdle: lapsed, wantGroup: "5h"},
		{name: "claude active 5h", auth: claudeAuth(claudeQuotaSignals(now.Add(time.Hour), weekly))},
		{name: "claude without snapshot", auth: claudeAuth(nil), wantDue: true},
		{name: "claude weekly rejected", auth: claudeAuth(exhausted)},
		{name: "claude api key", auth: &Auth{ID: "k", Provider: "claude", Attributes: map[string]string{AttributeAPIKey: "sk"}}},
		{name: "claude disabled", auth: func() *Auth { a := claudeAuth(nil); a.Disabled = true; return a }()},
		{name: "claude cooling down", auth: func() *Auth {
			a := claudeAuth(nil)
			a.Unavailable = true
			a.NextRetryAfter = now.Add(time.Hour)
			return a
		}()},
		{name: "codex weekly used up", auth: &Auth{ID: "x", Provider: "codex", Attributes: oauth, Quota: QuotaState{ObservedAt: now, Signals: codexSignals}}},
		{name: "codex lapsed primary", auth: func() *Auth {
			signals := map[string]string{}
			for k, v := range codexSignals {
				signals[k] = v
			}
			signals["X-Codex-Secondary-Used-Percent"] = "30"
			return &Auth{ID: "x", Provider: "codex", Attributes: oauth, Quota: QuotaState{ObservedAt: now, Signals: signals}}
		}(), wantDue: true, wantIdle: lapsed, wantGroup: "primary"},
		{name: "codex weekly-only plan", auth: &Auth{ID: "x", Provider: "codex", Attributes: oauth, Quota: QuotaState{ObservedAt: now, Signals: codexWeeklyOnly}}},
		{name: "unsupported provider", auth: &Auth{ID: "g", Provider: "gemini", Attributes: oauth}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ResetTriggerStateForAuth(tc.auth, "m", now)
			if got.Due != tc.wantDue || !got.IdleSince.Equal(tc.wantIdle) || got.Window != tc.wantGroup {
				t.Fatalf("state = %+v, want due=%v idle=%v window=%q", got, tc.wantDue, tc.wantIdle, tc.wantGroup)
			}
		})
	}
}
