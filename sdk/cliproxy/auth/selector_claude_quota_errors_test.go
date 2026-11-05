package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestClaudeQuotaStructuredExhaustion(t *testing.T) {
	for _, tc := range []struct {
		name, model, usage string
		want               time.Duration
	}{
		{"latest blocking window", "claude-sonnet-4-6", `{"five_hour":{"utilization":100,"resets_at":"2026-10-28T13:00:00Z"},"seven_day":{"utilization":100,"resets_at":"2026-10-28T15:00:00Z"}}`, 3 * time.Hour},
		{"ignore other family", "claude-sonnet-4-6", `{"five_hour":{"utilization":100,"resets_at":"2026-10-28T13:00:00Z"},"seven_day_opus":{"utilization":100,"resets_at":"2026-10-29T12:00:00Z"}}`, time.Hour},
		{"scoped limits", "claude-opus-4-6", `{"limits":[{"percent":100,"resets_at":"2026-10-28T14:00:00Z","scope":{"model":{"id":"opus"}}},{"percent":100,"resets_at":"2026-10-29T12:00:00Z","scope":{"model":{"display_name":"Sonnet"}}}]}`, 2 * time.Hour},
		{"unknown reset", "claude-sonnet-4-6", `{"five_hour":{"utilization":100}}`, 0},
		{"one unknown blocking reset", "claude-sonnet-4-6", `{"five_hour":{"utilization":100,"resets_at":"2026-10-28T13:00:00Z"},"seven_day":{"utilization":100}}`, 0},
		{"expired reset", "claude-sonnet-4-6", `{"five_hour":{"utilization":100,"resets_at":"2026-10-28T11:00:00Z"}}`, 0},
		{"unknown sentinel", "claude-sonnet-4-6", `{"limits":[{"percent":100,"resets_at":"9999-12-31T23:59:59Z"}]}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := quotaTestAuth("a", "claude")
			a.Metadata = map[string]any{"access_token": "synthetic"}
			a.EnsureIndex()
			data := quotaTestAccount(quotaTestNow, 24*time.Hour, time.Hour)
			*data.Eligible = false
			data.ObservedAt = quotaTestNow
			data.AuthIndex = a.Index
			data.CredentialFingerprint = AccessTokenSHA256(a)
			data.Usage = json.RawMessage(tc.usage)
			selector := &ClaudeQuotaSelector{SnapshotPath: quotaTestSnapshot(t, quotaTestNow, map[string]claudeQuotaAccount{"a": data})}
			_, err := selector.pick(context.Background(), "claude", tc.model, cliproxyexecutor.Options{}, []*Auth{a}, quotaTestNow)
			assertQuotaContract(t, err, "claude_quota_exhausted", 429, tc.model, tc.want)
		})
	}
}

func assertQuotaContract(t *testing.T, err error, code string, status int, model string, delay time.Duration) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	carrier, ok := err.(interface{ StatusCode() int })
	if !ok || carrier.StatusCode() != status {
		t.Fatalf("status error: %v", err)
	}
	raw, _ := json.Marshal(err)
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil {
		t.Fatalf("invalid JSON %s", raw)
	}
	if body["code"] != code || body["retryable"] != true || body["provider"] != "claude" || body["model"] != model {
		t.Fatalf("missing contract: %s", raw)
	}
	if delay > 0 {
		if body["reset_at"] != quotaTestNow.Add(delay).Format(time.RFC3339) || body["retry_after_seconds"] != delay.Seconds() {
			t.Fatalf("wrong reset: %s", raw)
		}
	} else if body["reset_at"] != nil {
		t.Fatalf("invented reset: %s", raw)
	}
	if delay == 0 && body["next_check_at"] == nil {
		t.Fatalf("missing next check: %s", raw)
	}
}

func TestClaudeQuotaStructuredUnavailable(t *testing.T) {
	selector := &ClaudeQuotaSelector{SnapshotPath: t.TempDir() + "/missing"}
	_, err := selector.pick(context.Background(), "claude", "alias", cliproxyexecutor.Options{}, []*Auth{quotaTestAuth("a", "claude")}, quotaTestNow)
	assertQuotaContract(t, err, "claude_quota_unavailable", 503, "alias", 0)
}

func TestClaudeQuotaEarliestAccountReset(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprint(unknown), func(t *testing.T) {
			accounts := map[string]claudeQuotaAccount{}
			auths := []*Auth{}
			for i, delays := range [][2]time.Duration{{time.Hour, 4 * time.Hour}, {2 * time.Hour, 3 * time.Hour}} {
				id := fmt.Sprint(i)
				a := quotaTestAuth(id, "claude")
				a.Metadata = map[string]any{"access_token": "synthetic"}
				a.EnsureIndex()
				auths = append(auths, a)
				account := quotaTestAccount(quotaTestNow, delays[1], delays[0])
				*account.Eligible = false
				account.ObservedAt = quotaTestNow
				account.AuthIndex = a.Index
				account.CredentialFingerprint = AccessTokenSHA256(a)
				account.Usage = json.RawMessage(fmt.Sprintf(`{"five_hour":{"utilization":100,"resets_at":%q},"seven_day":{"utilization":100,"resets_at":%q}}`, quotaTestNow.Add(delays[0]).Format(time.RFC3339), quotaTestNow.Add(delays[1]).Format(time.RFC3339)))
				if unknown && i == 0 {
					account.Usage = json.RawMessage(`{"five_hour":{"utilization":100}}`)
				}
				accounts[id] = account
			}
			selector := &ClaudeQuotaSelector{SnapshotPath: quotaTestSnapshot(t, quotaTestNow, accounts)}
			_, err := selector.pick(context.Background(), "claude", "claude-sonnet-4-6", cliproxyexecutor.Options{}, auths, quotaTestNow)
			want := 3 * time.Hour
			if unknown {
				want = 0
			}
			assertQuotaContract(t, err, "claude_quota_exhausted", 429, "claude-sonnet-4-6", want)
		})
	}
}

func TestClaudeQuotaResetRespectsResolvedAlias(t *testing.T) {
	a := quotaTestAuth("a", "claude")
	a.Metadata = map[string]any{"access_token": "synthetic"}
	a.EnsureIndex()
	account := quotaTestAccount(quotaTestNow, 24*time.Hour, time.Hour)
	account.BlockedModelFamilies = []string{"opus"}
	account.ObservedAt = quotaTestNow
	account.AuthIndex = a.Index
	account.CredentialFingerprint = AccessTokenSHA256(a)
	account.Usage = json.RawMessage(`{"seven_day_opus":{"utilization":100,"resets_at":"2026-10-28T14:00:00Z"},"seven_day_sonnet":{"utilization":100,"resets_at":"2026-10-28T16:00:00Z"}}`)
	selector := &ClaudeQuotaSelector{SnapshotPath: quotaTestSnapshot(t, quotaTestNow, map[string]claudeQuotaAccount{"a": account})}
	ctx := context.WithValue(context.Background(), claudeQuotaModelsKey{}, map[string]string{"a": "claude-opus-4-6"})
	_, err := selector.pick(ctx, "claude", "alias", cliproxyexecutor.Options{}, []*Auth{a}, quotaTestNow)
	assertQuotaContract(t, err, "claude_quota_exhausted", 429, "alias", 2*time.Hour)
}

func TestClaudeQuotaUnavailableNextCheckRespectsCollectorBackoff(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		t.Run(fmt.Sprint(replaced), func(t *testing.T) {
			a := quotaTestAuth("a", "claude")
			a.Metadata = map[string]any{"access_token": "synthetic"}
			a.EnsureIndex()
			retry := claudeQuotaRetry{AuthIndex: a.Index, CredentialFingerprint: AccessTokenSHA256(a), NextAttemptAt: quotaTestNow.Add(time.Hour)}
			if replaced {
				retry.CredentialFingerprint = "old"
			}
			snap := claudeQuotaSnapshot{ObservedAt: quotaTestNow, Accounts: map[string]claudeQuotaAccount{}, RetryState: map[string]claudeQuotaRetry{"a": retry}, GlobalRetryAt: quotaTestNow.Add(5 * time.Minute)}
			path := t.TempDir() + "/quota.json"
			raw, _ := json.Marshal(snap)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			_, err := (&ClaudeQuotaSelector{SnapshotPath: path}).pick(context.Background(), "claude", "alias", cliproxyexecutor.Options{}, []*Auth{a}, quotaTestNow)
			raw, _ = json.Marshal(err)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			want := time.Hour
			if replaced {
				want = 5 * time.Minute
			}
			if body["next_check_at"] != quotaTestNow.Add(want).Format(time.RFC3339) || body["reset_at"] != nil {
				t.Fatalf("bad recheck: %s", raw)
			}
		})
	}
}
