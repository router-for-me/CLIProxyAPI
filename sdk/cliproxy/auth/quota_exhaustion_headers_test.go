package auth

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// usageLimitClientHeaders returns the headers a passthrough-headers client receives for a usage-limit 429.
func usageLimitClientHeaders(limit quotaLimit) http.Header {
	err := newModelCooldownError("m", "codex", time.Until(limit.ResetsAt))
	err.setUsageLimit(limit)
	return err.Headers()
}

// assertRetryAfterUntil allows one second for the clock to tick between response and check.
func assertRetryAfterUntil(t *testing.T, headers http.Header, resetsAt time.Time) {
	t.Helper()
	got, err := strconv.ParseInt(headers.Get("Retry-After"), 10, 64)
	want := secondsUntil(resetsAt, time.Now())
	if err != nil || got < want || got > want+1 {
		t.Errorf("Retry-After = %q, want %d (+1s)", headers.Get("Retry-After"), want)
	}
}

func TestUsageLimitClientHeaders_NameTheSpentWindow(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name         string
		limit        quotaLimit
		codexMinutes string
	}{
		{"five-hour window", quotaLimit{ResetsAt: now.Add(2 * time.Hour), Window: fiveHourWindow}, "300"},
		{"weekly window", quotaLimit{ResetsAt: now.Add(time.Hour), Window: weeklyWindow}, "10080"},
		{"unknown window", quotaLimit{ResetsAt: now.Add(2 * time.Hour)}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			headers := usageLimitClientHeaders(tc.limit)
			want := map[string]string{
				"X-Codex-Primary-Used-Percent":   "100",
				"X-Codex-Primary-Reset-At":       strconv.FormatInt(tc.limit.ResetsAt.Unix(), 10),
				"X-Codex-Primary-Window-Minutes": tc.codexMinutes,
				HeaderShouldRetry:                "false",
			}
			for name, value := range want {
				if got := headers.Get(name); got != value {
					t.Errorf("%s = %q, want %q", name, got, value)
				}
			}
			assertRetryAfterUntil(t, headers, tc.limit.ResetsAt)
		})
	}
}

// Claude's unified headers are upstream's passthrough-gated Headers(), never the always-sent set.
func TestUsageLimitClientHeaders_ClaudeUnifiedOnlyInHeaders(t *testing.T) {
	limit := quotaLimit{ResetsAt: time.Now().Add(2 * time.Hour), Window: fiveHourWindow, claude: true}
	err := newModelCooldownError("m", "claude", time.Until(limit.ResetsAt))
	err.setUsageLimit(limit)
	if got := err.SafeResponseHeaders().Get(claudeUnifiedSignalPrefix + "Status"); got != "" {
		t.Fatalf("always-sent headers carry Claude unified status %q", got)
	}
	if got := err.Headers().Get(claudeUnifiedSignalPrefix + "Status"); got != QuotaWindowStatusRejected {
		t.Fatalf("Headers() Claude unified status = %q, want %s", got, QuotaWindowStatusRejected)
	}
}

func TestUsageLimitClientHeaders_OrdinaryCooldownHasNone(t *testing.T) {
	for name := range newModelCooldownError("m", "codex", time.Minute).Headers() {
		if strings.HasPrefix(name, codexSignalPrefix) {
			t.Fatalf("retryable cooldown leaked usage-limit header %s", name)
		}
	}
}

// The proxy's own quota parser must read the Codex headers back as the same spent window.
func TestUsageLimitClientHeaders_RoundTripThroughQuotaParser(t *testing.T) {
	now := time.Now()
	limit := quotaLimit{ResetsAt: now.Add(3 * time.Hour).Truncate(time.Second), Window: fiveHourWindow}
	headers := usageLimitClientHeaders(limit)
	signals := map[string]string{}
	for name := range headers {
		signals[name] = headers.Get(name)
	}
	auth := &Auth{ID: "rt-codex", Provider: "codex", Quota: QuotaState{Signals: signals, ObservedAt: now}}
	got, ok := credentialQuotaLimit(auth, "m", now)
	if !ok || !got.ResetsAt.Equal(limit.ResetsAt) || got.Window != limit.Window {
		t.Errorf("parsed %+v %v, want %+v", got, ok, limit)
	}
}
