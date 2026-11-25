package executor

import (
	"context"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestAntigravityCreditsStateDoesNotCrossRegistrationEpochHomeKV(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)

	createdAt := time.Now().Add(-time.Hour)
	oldAuth := &cliproxyauth.Auth{ID: "antigravity-issue7795-home-reuse.json", Provider: "antigravity", CreatedAt: createdAt, UpdatedAt: createdAt, Metadata: map[string]any{"email": "old@example.com"}, Attributes: map[string]string{}}
	newAuth := oldAuth.Clone()
	newAuth.UpdatedAt = createdAt.Add(time.Minute)
	newAuth.Metadata = map[string]any{"email": "new@example.com"}
	newAuth.Attributes = map[string]string{}
	newAuth.Metadata["refresh_token"] = "new-token"
	oldAuth.Metadata["refresh_token"] = "old-token"
	client := newFakeAntigravityKVClient()
	useFakeAntigravityKVClient(t, client, true, nil)

	balance := antigravityCreditsBalance{CreditAmount: 0, MinCreditAmount: 1, Known: true}
	storeAntigravityCreditsBalanceBestEffort(oldAuth, balance)
	oldHasCredits, errOldCredits := antigravityAuthHasCreditsRequired(context.Background(), oldAuth)
	if errOldCredits != nil {
		t.Fatalf("old antigravityAuthHasCreditsRequired() error = %v", errOldCredits)
	}
	if oldHasCredits {
		t.Fatal("old auth did not read the exhausted balance written to Home KV")
	}
	cliproxyauth.SetAntigravityCreditsHintForAuth(oldAuth, cliproxyauth.AntigravityCreditsHint{Known: true, Available: false})
	if errMark := markAntigravityShortCooldownRequired(context.Background(), oldAuth, "gemini-3-flash", time.Now(), 2*time.Minute); errMark != nil {
		t.Fatalf("markAntigravityShortCooldownRequired() error = %v", errMark)
	}
	if hint, ok := cliproxyauth.GetAntigravityCreditsHintForAuth(oldAuth); !ok || !hint.Known || hint.Available {
		t.Fatalf("old auth did not read its Home KV hint: hint=%#v ok=%v", hint, ok)
	}

	inCooldown, _, errCooldown := antigravityIsInShortCooldownRequired(context.Background(), newAuth, "gemini-3-flash", time.Now())
	if errCooldown != nil {
		t.Fatalf("antigravityIsInShortCooldownRequired() error = %v", errCooldown)
	}
	if inCooldown {
		t.Error("replacement auth inherited Home KV short cooldown")
	}
	if hint, ok := cliproxyauth.GetAntigravityCreditsHintForAuth(newAuth); ok && hint.Known {
		t.Errorf("replacement auth inherited Home KV hint: %+v", hint)
	}
	hasCredits, errCredits := antigravityAuthHasCreditsRequired(context.Background(), newAuth)
	if errCredits != nil {
		t.Fatalf("antigravityAuthHasCreditsRequired() error = %v", errCredits)
	}
	if !hasCredits {
		t.Error("replacement auth inherited Home KV exhausted credits balance")
	}
	storeAntigravityCreditsBalanceBestEffort(oldAuth, balance)
	cliproxyauth.SetAntigravityCreditsHintForAuth(oldAuth, cliproxyauth.AntigravityCreditsHint{Known: true, Available: false})
	hasCredits, errCredits = antigravityAuthHasCreditsRequired(context.Background(), newAuth)
	if errCredits != nil {
		t.Fatalf("antigravityAuthHasCreditsRequired() after stale write error = %v", errCredits)
	}
	if !hasCredits {
		t.Error("stale balance or hint write changed replacement auth state")
	}
	if oldKey := antigravityCreditsBalanceKey(oldAuth.ID, cliproxyauth.AntigravityCreditsScope(oldAuth)); oldKey == antigravityCreditsBalanceKey(newAuth.ID, cliproxyauth.AntigravityCreditsScope(newAuth)) {
		t.Fatalf("Home KV balance keys collide across epochs: %s", oldKey)
	}
	if oldKey := antigravityShortCooldownKVKey(oldAuth, "gemini-3-flash"); oldKey == antigravityShortCooldownKVKey(newAuth, "gemini-3-flash") {
		t.Fatalf("Home KV cooldown keys collide across epochs: %s", oldKey)
	}
	if oldKey := antigravityCreditsRefreshLockKey(oldAuth.ID, cliproxyauth.AntigravityCreditsScope(oldAuth)); oldKey == antigravityCreditsRefreshLockKey(newAuth.ID, cliproxyauth.AntigravityCreditsScope(newAuth)) {
		t.Fatalf("Home KV refresh lock keys collide across epochs: %s", oldKey)
	}
}
