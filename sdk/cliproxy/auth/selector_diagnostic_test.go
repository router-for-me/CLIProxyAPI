package auth

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestGetAvailableAuthsReportsWhyEveryAuthIsBlocked(t *testing.T) {
	now := time.Now()
	auths := []*Auth{
		{
			ID:            "cooling",
			Quota:         QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(90 * time.Second)},
			StatusMessage: "429 quota",
		},
		{ID: "off", Disabled: true},
		{ID: "flaky", Unavailable: true, NextRetryAfter: now.Add(30 * time.Second)},
	}

	_, err := getAvailableAuths(auths, "codex", "gpt-5.6-terra", now)
	if err == nil {
		t.Fatal("expected selection to fail when every auth is blocked")
	}
	var authErr *Error
	if !errors.As(err, &authErr) {
		t.Fatalf("error type = %T, want it to unwrap to *Error", err)
	}
	if authErr.Code != "auth_unavailable" {
		t.Fatalf("code = %q, want auth_unavailable", authErr.Code)
	}
	diagnostic := SelectionDiagnostic(err)
	if !strings.Contains(diagnostic, "cooling=cooldown for 1m30s (429 quota)") {
		t.Fatalf("diagnostic = %q, want the cooling auth with its remaining time and status message", diagnostic)
	}
	if !strings.Contains(diagnostic, "off=disabled") {
		t.Fatalf("diagnostic = %q, want the disabled auth", diagnostic)
	}
	if !strings.Contains(diagnostic, "flaky=unavailable for 30s") {
		t.Fatalf("diagnostic = %q, want the unavailable auth with its remaining time", diagnostic)
	}
	if strings.Contains(authErr.Message, "cooling") {
		t.Fatalf("message = %q, diagnostics must stay out of the client-facing message", authErr.Message)
	}
}

func TestSummarizeBlockedAuthsCapsTheListing(t *testing.T) {
	now := time.Now()
	auths := make([]*Auth, 0, blockedAuthSummaryLimit+2)
	for i := 0; i < blockedAuthSummaryLimit+2; i++ {
		auths = append(auths, &Auth{ID: string(rune('a' + i)), Disabled: true})
	}

	summary := summarizeBlockedAuths(auths, "gpt-5.6-terra", now)
	if got := strings.Count(summary, "=disabled"); got != blockedAuthSummaryLimit {
		t.Fatalf("listed %d auths, want %d; summary=%q", got, blockedAuthSummaryLimit, summary)
	}
	if !strings.HasSuffix(summary, "+2 more") {
		t.Fatalf("summary = %q, want the omitted count as a suffix", summary)
	}
}

func TestSummarizeBlockedAuthsIsEmptyWhenNothingIsBlocked(t *testing.T) {
	summary := summarizeBlockedAuths([]*Auth{{ID: "ready"}}, "gpt-5.6-terra", time.Now())
	if summary != "" {
		t.Fatalf("summary = %q, want empty when no auth is blocked", summary)
	}
}
