package usage

import (
	"net/http"
	"testing"
)

func TestCodexQuotaSnapshot_ParseAndStore(t *testing.T) {
	headers := make(http.Header)
	headers.Set("x-codex-plan-type", "team")
	headers.Set("x-codex-primary-used-percent", "12.5")
	headers.Set("x-codex-primary-reset-after-seconds", "3600")
	headers.Set("x-codex-primary-reset-at", "123")
	headers.Set("x-codex-primary-window-minutes", "10080")
	headers.Set("x-codex-secondary-used-percent", "33.0")
	headers.Set("x-codex-secondary-reset-after-seconds", "1800")
	headers.Set("x-codex-secondary-window-minutes", "300")
	headers.Set("x-codex-credits-has-credits", "False")
	headers.Set("x-codex-credits-balance", "10")

	snap := ParseCodexQuotaSnapshot(headers)
	if snap == nil {
		t.Fatal("expected snapshot to be parsed")
	}
	if snap.PlanType != "team" {
		t.Fatalf("unexpected plan type: %q", snap.PlanType)
	}
	if snap.PrimaryUsedPercent == nil || *snap.PrimaryUsedPercent != 12.5 {
		t.Fatalf("unexpected primary used percent: %#v", snap.PrimaryUsedPercent)
	}
	if snap.PrimaryResetAtSeconds == nil || *snap.PrimaryResetAtSeconds != 123 {
		t.Fatalf("unexpected primary reset at seconds: %#v", snap.PrimaryResetAtSeconds)
	}
	if snap.CreditsHasCredits == nil || *snap.CreditsHasCredits != false {
		t.Fatalf("unexpected credits_has_credits: %#v", snap.CreditsHasCredits)
	}
	if snap.CreditsBalance == nil || *snap.CreditsBalance != "10" {
		t.Fatalf("unexpected credits_balance: %#v", snap.CreditsBalance)
	}

	UpdateCodexQuotaSnapshot("auth-1", snap)
	got := GetCodexQuotaSnapshot("auth-1")
	if got == nil {
		t.Fatal("expected stored snapshot")
	}
	if got.SecondaryUsedPercent == nil || *got.SecondaryUsedPercent != 33.0 {
		t.Fatalf("unexpected secondary used percent: %#v", got.SecondaryUsedPercent)
	}
}
