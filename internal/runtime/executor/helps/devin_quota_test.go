package helps

import (
	"testing"
	"time"
)

func TestDevinUsageQuotaExhaustionPeriod(t *testing.T) {
	cases := []struct {
		name    string
		message string
		want    DevinQuotaPeriod
	}{
		{name: "weekly exact", message: "Your weekly usage quota has been exhausted.", want: DevinQuotaPeriodWeekly},
		{name: "daily exact", message: "Your daily usage quota has been exhausted.", want: DevinQuotaPeriodDaily},
		{name: "case-insensitive", message: "YOUR WEEKLY USAGE QUOTA HAS BEEN EXHAUSTED.", want: DevinQuotaPeriodWeekly},
		{name: "wrapped trailer text", message: "devin upstream error (failed_precondition): Your weekly usage quota has been exhausted.", want: DevinQuotaPeriodWeekly},
		{name: "weekly wins when both named", message: "Your daily and weekly usage quota has been exhausted.", want: DevinQuotaPeriodWeekly},
		{name: "monthly ACU out of scope", message: "User monthly ACU quota exhausted (trace ID: 12345)", want: DevinQuotaPeriodNone},
		{name: "rate limit without quota", message: "Rate limit exceeded. Please slow down.", want: DevinQuotaPeriodNone},
		{name: "weekly without exhausted", message: "Your weekly usage quota is nearly used up.", want: DevinQuotaPeriodNone},
		{name: "quota without period", message: "Usage quota exhausted.", want: DevinQuotaPeriodNone},
		{name: "weekly without quota", message: "Your weekly allowance has been exhausted.", want: DevinQuotaPeriodNone},
		{name: "empty", message: "", want: DevinQuotaPeriodNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DevinUsageQuotaExhaustionPeriod(tc.message); got != tc.want {
				t.Fatalf("DevinUsageQuotaExhaustionPeriod(%q) = %v, want %v", tc.message, got, tc.want)
			}
		})
	}
}

func TestDevinQuotaRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 16, 15, 37, 9, 0, time.UTC)

	cases := []struct {
		name   string
		signal string
		want   *time.Duration
	}{
		{name: "valid future weekly reset", signal: now.Add(72 * time.Hour).Format(time.RFC3339), want: durationPtr(72 * time.Hour)},
		{name: "valid future daily reset", signal: now.Add(3 * time.Hour).Format(time.RFC3339), want: durationPtr(3 * time.Hour)},
		{name: "past reset", signal: now.Add(-time.Minute).Format(time.RFC3339), want: nil},
		{name: "reset exactly now", signal: now.Format(time.RFC3339), want: nil},
		{name: "far future reset rejected", signal: now.Add(30 * 24 * time.Hour).Format(time.RFC3339), want: nil},
		{name: "unparseable", signal: "not-a-time", want: nil},
		{name: "empty", signal: "", want: nil},
		{name: "whitespace trimmed", signal: "  " + now.Add(time.Hour).Format(time.RFC3339) + "  ", want: durationPtr(time.Hour)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DevinQuotaRetryAfter(tc.signal, now)
			if tc.want == nil && got != nil {
				t.Fatalf("DevinQuotaRetryAfter(%q) = %v, want nil", tc.signal, *got)
			}
			if tc.want != nil && (got == nil || *got != *tc.want) {
				t.Fatalf("DevinQuotaRetryAfter(%q) = %v, want %v", tc.signal, got, *tc.want)
			}
		})
	}
}

func durationPtr(d time.Duration) *time.Duration { return &d }
