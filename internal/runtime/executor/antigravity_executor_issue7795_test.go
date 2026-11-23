package executor

import (
	"context"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestAntigravityCreditsStateDoesNotCrossRegistrationEpoch(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	oldAuth := &cliproxyauth.Auth{
		ID:                "antigravity-issue7795-reuse.json",
		Provider:          "antigravity",
		RegistrationEpoch: 1,
		Metadata: map[string]any{
			"refresh_token": "old-refresh-token",
		},
	}
	newAuth := oldAuth.Clone()
	newAuth.RegistrationEpoch++
	newAuth.Metadata["refresh_token"] = "new-refresh-token"

	markAntigravityCreditsPermanentlyDisabled(oldAuth)
	if errMark := markAntigravityShortCooldownRequired(context.Background(), oldAuth, "gemini-3-flash", time.Now(), 2*time.Minute); errMark != nil {
		t.Fatalf("markAntigravityShortCooldownRequired() error = %v", errMark)
	}
	t.Cleanup(func() {
		cliproxyauth.SetAntigravityCreditsHint(oldAuth.ID, cliproxyauth.AntigravityCreditsHint{}, oldAuth.RegistrationEpoch)
		cliproxyauth.SetAntigravityCreditsHint(newAuth.ID, cliproxyauth.AntigravityCreditsHint{}, newAuth.RegistrationEpoch)
	})

	if _, loaded := antigravityCreditsFailureByAuth.Load(newAntigravityCreditsCacheKey(newAuth.ID, cliproxyauth.AntigravityCreditsScope(newAuth))); loaded {
		t.Error("replacement auth inherited permanent credits failure state")
	}
	inCooldown, _, errCooldown := antigravityIsInShortCooldownRequired(context.Background(), newAuth, "gemini-3-flash", time.Now())
	if errCooldown != nil {
		t.Fatalf("antigravityIsInShortCooldownRequired() error = %v", errCooldown)
	}
	if inCooldown {
		t.Error("replacement auth inherited short cooldown")
	}
	hasCredits, errCredits := antigravityAuthHasCreditsRequired(context.Background(), newAuth)
	if errCredits != nil {
		t.Fatalf("antigravityAuthHasCreditsRequired() error = %v", errCredits)
	}
	if !hasCredits {
		t.Error("replacement auth inherited exhausted credits balance")
	}
	if hint, ok := cliproxyauth.GetAntigravityCreditsHintForAuth(newAuth); ok && hint.Known {
		t.Errorf("replacement auth inherited credits hint: %+v", hint)
	}
	if _, loaded := antigravityCreditsFailureByAuth.Load(newAntigravityCreditsCacheKey(oldAuth.ID, cliproxyauth.AntigravityCreditsScope(oldAuth))); loaded {
		t.Error("old registration failure state was not reclaimed")
	}
	if _, loaded := antigravityCreditsBalanceByAuth.Load(newAntigravityCreditsCacheKey(oldAuth.ID, cliproxyauth.AntigravityCreditsScope(oldAuth))); loaded {
		t.Error("old registration balance state was not reclaimed")
	}
}
