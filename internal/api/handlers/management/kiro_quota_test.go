package management

import (
	"encoding/json"
	"math"
	"testing"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kiro"
)

func TestKiroUsageToQuota(t *testing.T) {
	raw := `{"daysUntilReset":0,"nextDateReset":1.7960832E9,
	"subscriptionInfo":{"subscriptionTitle":"KIRO FREE","type":"Q_DEVELOPER_STANDALONE_FREE"},
	"usageBreakdownList":[{"currency":"USD","currentUsage":0,"currentUsageWithPrecision":12.5,
	"displayName":"Credit","displayNamePlural":"Credits","nextDateReset":1.7960832E9,
	"resourceType":"CREDIT","unit":"INVOCATIONS","usageLimit":50,"usageLimitWithPrecision":50.0}]}`
	var u kiroauth.UsageLimits
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	q := kiroUsageToQuota(&u)
	if q.Subscription == nil || q.Subscription.TierName != "KIRO FREE" {
		t.Fatalf("unexpected subscription: %#v", q.Subscription)
	}
	if len(q.Groups) != 1 || len(q.Groups[0].Buckets) != 1 {
		t.Fatalf("unexpected groups: %#v", q.Groups)
	}
	b := q.Groups[0].Buckets[0]
	if math.Abs(b.RemainingFraction-0.75) > 1e-9 {
		t.Fatalf("remaining = %v, want 0.75", b.RemainingFraction)
	}
	if b.ResetTime != "2026-12-01T00:00:00Z" {
		t.Fatalf("reset = %q", b.ResetTime)
	}
	if len(q.Summary) != 2 || q.Summary[0].Value != 12.5 || q.Summary[1].Value != 50 {
		t.Fatalf("unexpected summary: %#v", q.Summary)
	}
}
