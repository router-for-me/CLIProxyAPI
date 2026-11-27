package quota

import (
	"testing"
	"time"
)

func TestParseKimiUsage(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	body := []byte(`{
	  "usage": {"used": "300", "limit": "1000", "resetTime": "2026-11-30T00:00:00.123456789Z"},
	  "limits": [{"window": {"duration": 300, "timeUnit": "TIME_UNIT_MINUTE"}, "detail": {"limit": 100, "remaining": 25, "reset_in": 3600}}]
	}`)
	snap, err := ParseKimiUsage(body, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Windows) != 2 {
		t.Fatalf("windows: %+v", snap.Windows)
	}
	five, weekly := snap.Windows[0], snap.Windows[1]
	if five.Kind != FiveHour || five.UsedPercent != 75 || five.ResetsAt == nil || !five.ResetsAt.Equal(now.Add(time.Hour)) || five.Family != "" {
		t.Fatalf("five: %+v", five)
	}
	if weekly.Kind != Weekly || weekly.UsedPercent != 30 || weekly.ResetsAt == nil || weekly.ResetsAt.Day() != 30 {
		t.Fatalf("weekly: %+v", weekly)
	}
}

func TestParseKimiMonthlyRatio(t *testing.T) {
	snap, err := ParseKimiUsage([]byte(`{"usages":{"limit_month_total":{"used_ratio":0.42,"reset_time":"2026-12-01T00:00:00Z"}}}`), time.Now())
	if err != nil || len(snap.Windows) != 1 || snap.Windows[0].Kind != Monthly || snap.Windows[0].UsedPercent != 42 {
		t.Fatalf("%+v %v", snap, err)
	}
	if _, err := ParseKimiUsage([]byte(`{}`), time.Now()); err == nil {
		t.Fatal("expected empty error")
	}
}

func TestKimiUsageURLFor(t *testing.T) {
	cases := [][4]string{
		{"ai", "", "kimi", KimiAIUsageURL},
		{"", "https://api.kimi.ai/coding", "kimi", KimiAIUsageURL},
		{"", "", "kimi-ai", KimiAIUsageURL},
		{"", "", "kimi", KimiUsageURL},
		{"weird", "", "kimi-ai", KimiUsageURL},
	}
	for _, c := range cases {
		if got := KimiUsageURLFor(c[0], c[1], c[2]); got != c[3] {
			t.Errorf("%v: got %s", c, got)
		}
	}
}

func TestParseXaiBilling(t *testing.T) {
	now := time.Now()
	w, err := ParseXaiBilling([]byte(`{"config":{"creditUsagePercent":37.5,"currentPeriod":{"type":"BILLING_PERIOD_TYPE_WEEKLY","end":"2026-12-02T00:00:00Z"}}}`), now)
	if err != nil || w == nil || w.Kind != Weekly || w.UsedPercent != 37.5 || w.ResetsAt == nil {
		t.Fatalf("weekly: %+v %v", w, err)
	}
	w, err = ParseXaiBilling([]byte(`{"config":{"monthlyLimit":{"val":"2000"},"used":{"val":"500"},"billingPeriodEnd":"2026-12-15T00:00:00Z"}}`), now)
	if err != nil || w == nil || w.Kind != Monthly || w.UsedPercent != 25 {
		t.Fatalf("monthly: %+v %v", w, err)
	}
	if w, _ := ParseXaiBilling([]byte(`{"config":{}}`), now); w != nil {
		t.Fatalf("expected nil, got %+v", w)
	}
}
