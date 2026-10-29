package auth

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestClaudeQuotaPerAccountFreshness(testContext *testing.T) {
	for _, test := range []struct {
		name               string
		age                time.Duration
		eligible           bool
		index              string
		missingObservation bool
		want               string
	}{
		{name: "monitoring outage grace", age: 10 * time.Minute, eligible: true, want: "cached"},
		{name: "grace boundary", age: 30 * time.Minute, eligible: true, want: "cached"},
		{name: "expired observation", age: 30*time.Minute + time.Nanosecond, eligible: true, want: "healthy"},
		{name: "future observation", age: -time.Nanosecond, eligible: true, want: "healthy"},
		{name: "exhausted stays blocked", age: 10 * time.Minute, eligible: false, want: "healthy"},
		{name: "replaced credential", age: time.Minute, eligible: true, index: "different-index", want: "healthy"},
		{name: "missing new observation", missingObservation: true, eligible: true, index: "index", want: "healthy"},
	} {
		testContext.Run(test.name, func(testContext *testing.T) {
			candidate := quotaTestAuth("cached", "claude")
			candidate.Metadata = map[string]any{"access_token": "synthetic-token"}
			candidate.EnsureIndex()
			cached := quotaTestAccount(quotaTestNow, time.Hour, time.Hour)
			cached.ObservedAt = quotaTestNow.Add(-test.age)
			if test.missingObservation {
				cached.ObservedAt = time.Time{}
			}
			cached.Eligible = &test.eligible
			cached.AuthIndex = candidate.Index
			if test.index != "" {
				cached.AuthIndex = test.index
			}
			cached.CredentialFingerprint = AccessTokenSHA256(candidate)
			healthy := quotaTestAccount(quotaTestNow, 24*time.Hour, time.Hour)
			selector := &ClaudeQuotaSelector{SnapshotPath: quotaTestSnapshot(testContext, quotaTestNow, map[string]claudeQuotaAccount{"cached": cached, "healthy": healthy})}
			selected, err := selector.pick(context.Background(), "claude", "claude-opus-5-5", cliproxyexecutor.Options{}, []*Auth{candidate, quotaTestAuth("healthy", "claude")}, quotaTestNow)
			if err != nil || selected == nil || selected.ID != test.want {
				testContext.Fatalf("selected=%v error=%v want=%s", selected, err, test.want)
			}
		})
	}
}

func TestClaudeQuotaPublicationCannotFreshenAccount(testContext *testing.T) {
	candidate := quotaTestAuth("old", "claude")
	candidate.Metadata = map[string]any{"access_token": "synthetic-token"}
	candidate.EnsureIndex()
	account := quotaTestAccount(quotaTestNow, time.Hour, time.Hour)
	account.ObservedAt = quotaTestNow.Add(-31 * time.Minute)
	account.AuthIndex = candidate.Index
	account.CredentialFingerprint = AccessTokenSHA256(candidate)
	selector := &ClaudeQuotaSelector{SnapshotPath: quotaTestSnapshot(testContext, quotaTestNow, map[string]claudeQuotaAccount{"old": account})}
	selected, err := selector.pick(context.Background(), "claude", "claude-opus-5-5", cliproxyexecutor.Options{}, []*Auth{candidate}, quotaTestNow)
	if err == nil || selected != nil {
		testContext.Fatalf("stale account selected=%v error=%v", selected, err)
	}
}

func TestReadClaudeQuotaUsageUsesAccountObservation(testContext *testing.T) {
	account := quotaTestAccount(quotaTestNow, time.Hour, time.Hour)
	account.ObservedAt = quotaTestNow.Add(-10 * time.Minute)
	account.AuthIndex = "index"
	account.CredentialFingerprint = "synthetic-fingerprint"
	account.Usage = json.RawMessage(`{"five_hour":{"utilization":12,"resets_at":null}}`)
	path := quotaTestSnapshot(testContext, quotaTestNow, map[string]claudeQuotaAccount{"cached": account})
	usage, observed, err := ReadClaudeQuotaUsage(path, "cached", "index", "synthetic-fingerprint", quotaTestNow)
	if err != nil || !observed.Equal(account.ObservedAt) || string(usage) != string(account.Usage) {
		testContext.Fatalf("usage=%s observed=%s error=%v", usage, observed, err)
	}
	for _, index := range []string{"replaced", ""} {
		if _, _, err := ReadClaudeQuotaUsage(path, "cached", index, "synthetic-fingerprint", quotaTestNow); err == nil {
			testContext.Fatalf("accepted mismatched credential index %q", index)
		}
	}
	if _, _, err := ReadClaudeQuotaUsage(path, "cached", "index", "replaced-credential", quotaTestNow); err == nil {
		testContext.Fatal("accepted replaced credential at the same index")
	}
}

func TestClaudeQuotaReplacedCredentialAtSamePath(testContext *testing.T) {
	original := quotaTestAuth("account.json", "claude")
	original.FileName = "account.json"
	original.Metadata = map[string]any{"access_token": "original-synthetic-token"}
	original.EnsureIndex()
	account := quotaTestAccount(quotaTestNow, time.Hour, time.Hour)
	account.ObservedAt = quotaTestNow
	account.AuthIndex = original.Index
	account.CredentialFingerprint = AccessTokenSHA256(original)
	selector := &ClaudeQuotaSelector{SnapshotPath: quotaTestSnapshot(testContext, quotaTestNow, map[string]claudeQuotaAccount{original.ID: account})}
	replacement := original.Clone()
	replacement.Metadata["access_token"] = "replacement-synthetic-token"
	if replacement.Index != original.Index {
		testContext.Fatal("replacement did not retain its file-backed index")
	}
	selected, err := selector.pick(context.Background(), "claude", "claude-opus-5-5", cliproxyexecutor.Options{}, []*Auth{replacement}, quotaTestNow)
	if selected != nil || err == nil {
		testContext.Fatalf("replacement inherited old quota: selected=%v error=%v", selected, err)
	}
}
