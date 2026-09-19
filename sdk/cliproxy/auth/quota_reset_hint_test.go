package auth

import (
	"testing"
	"time"
)

func TestQuotaResetHintParsesVolcengineStyleMessage(t *testing.T) {
	now := time.Date(2026, 9, 20, 3, 0, 0, 0, time.FixedZone("CST", 8*3600))
	body := `{"error":{"code":"AccountQuotaExceeded","message":"You have exceeded the monthly usage quota. It will reset at 2026-09-23 23:59:59 +0800 CST. We recommend upgrading your plan for more quota, or waiting for the reset.","type":"TooManyRequests"}}`

	got, ok := quotaResetHint(body, now)
	if !ok {
		t.Fatalf("expected a reset hint")
	}
	want := time.Date(2026, 9, 23, 23, 59, 59, 0, time.FixedZone("CST", 8*3600))
	if !got.Equal(want) {
		t.Fatalf("reset hint = %s, want %s", got, want)
	}
}

func TestQuotaResetHintParsesRFC3339AndJSONKey(t *testing.T) {
	now := time.Date(2026, 9, 20, 3, 0, 0, 0, time.UTC)
	cases := []string{
		`{"reset_at":"2026-09-23T23:59:59+08:00"}`,
		`quota resets on 2026-09-23T23:59:59Z`,
		`will reset at 2026-09-23 23:59:59 +0800`,
	}
	for _, body := range cases {
		got, ok := quotaResetHint(body, now)
		if !ok {
			t.Fatalf("expected a reset hint for %q", body)
		}
		if !got.After(now) {
			t.Fatalf("hint %s should be in the future", got)
		}
	}
}

func TestQuotaResetHintRejectsUnsafeValues(t *testing.T) {
	now := time.Date(2026, 9, 20, 3, 0, 0, 0, time.UTC)
	cases := []string{
		``,
		`no timestamp here`,
		`reset at 2026-09-19 23:59:59 +0800 CST`, // past
		`reset at 2027-01-01 00:00:00 +0800 CST`, // too far out (> 45d)
		`reset at 2026-09-23 23:59:59`,           // no offset/zone
		`the reset of the model pricing table`,   // keyword without time
	}
	for _, body := range cases {
		if got, ok := quotaResetHint(body, now); ok {
			t.Fatalf("expected no hint for %q, got %s", body, got)
		}
	}
}
