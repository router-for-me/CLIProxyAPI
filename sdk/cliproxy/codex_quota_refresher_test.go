package cliproxy

import (
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestIsCodexQuotaCooldown(t *testing.T) {
	auth := &coreauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"access_token": "token"},
		ModelStates: map[string]*coreauth.ModelState{
			"gpt-5": {Quota: coreauth.QuotaState{Exceeded: true, Reason: "quota"}},
		},
	}
	if !isCodexQuotaCooldown(auth) {
		t.Fatal("expected Codex quota cooldown")
	}
	auth.ModelStates["gpt-5"].Quota.Reason = "cloudflare challenge"
	if isCodexQuotaCooldown(auth) {
		t.Fatal("non-quota cooldown must not be actively refreshed")
	}
}

func TestCodexUsageExhausted(t *testing.T) {
	reached := true
	if !codexUsageExhausted(&codexUsageRateLimit{LimitReached: &reached}) {
		t.Fatal("limit_reached must be exhausted")
	}
	if !codexUsageExhausted(&codexUsageRateLimit{SecondaryWindow: &codexUsageQuotaWindow{UsedPercent: 100}}) {
		t.Fatal("100 percent secondary window must be exhausted")
	}
	if codexUsageExhausted(&codexUsageRateLimit{PrimaryWindow: &codexUsageQuotaWindow{UsedPercent: 25}}) {
		t.Fatal("available primary window must not be exhausted")
	}
}

func TestQuotaRefreshDuration(t *testing.T) {
	if got := quotaRefreshDuration("2m", time.Hour); got != 2*time.Minute {
		t.Fatalf("quotaRefreshDuration() = %v", got)
	}
	if got := quotaRefreshDuration("invalid", time.Hour); got != time.Hour {
		t.Fatalf("invalid quotaRefreshDuration() = %v", got)
	}
}
