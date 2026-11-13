package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// A credential-wide 429 on one model copies the block onto every model of the
// credential. Once the credential-wide block has lifted, a model that saw no
// traffic in between must not stay blocked by its stale copy. Seen live on
// 2026-11-13: Opus and Fable worked on the account again while Sonnet stayed
// blocked for hours with reason credential_quota.
func TestStaleCredentialQuotaCopyDoesNotBlockSiblingModel(t *testing.T) {
	withQuotaCooldownEnabled(t)

	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "stale-wrap-claude", Provider: "claude"}
	if _, errRegister := manager.Register(WithSkipPersist(ctx), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	for _, model := range []string{"claude-opus-5-5", "claude-sonnet-5-5"} {
		manager.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: model, Success: true})
	}

	retryAfter := time.Hour
	manager.MarkResult(ctx, Result{
		AuthID:          auth.ID,
		Provider:        auth.Provider,
		Model:           "claude-opus-5-5",
		CredentialScope: true,
		RetryAfter:      &retryAfter,
		Error:           &Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate_limit_error"},
	})

	now := time.Now()
	manager.mu.Lock()
	live := manager.auths[auth.ID]
	sonnet := live.ModelStates[canonicalModelKey("claude-sonnet-5-5")]
	if sonnet == nil || sonnet.Quota.Reason != "credential_quota" {
		manager.mu.Unlock()
		t.Fatalf("expected the credential block to be copied onto sonnet, got %+v", sonnet)
	}
	// The credential-wide block lifts, while the sonnet copy carries a later
	// deadline, as it did live.
	past := now.Add(-time.Minute)
	live.Quota.NextRecoverAt = past
	live.NextRetryAfter = past
	if opus := live.ModelStates[canonicalModelKey("claude-opus-5-5")]; opus != nil {
		opus.NextRetryAfter = past
		opus.Quota.NextRecoverAt = past
	}
	sonnet.NextRetryAfter = now.Add(2 * time.Hour)
	sonnet.Quota.NextRecoverAt = now.Add(2 * time.Hour)
	snapshot := live.Clone()
	manager.mu.Unlock()

	if blocked, _, _ := isAuthBlockedForModel(snapshot, "claude-opus-5-5", now); blocked {
		t.Fatal("opus should be usable once the credential block has lifted")
	}
	if blocked, reason, next := isAuthBlockedForModel(snapshot, "claude-sonnet-5-5", now); blocked {
		t.Fatalf("sonnet still blocked by a stale credential copy: reason=%v until %v", reason, next)
	}
}

// A model's own cooldown recorded before the credential block (its independent
// layer) still applies after the credential block lifts.
func TestStaleCredentialQuotaCopyKeepsIndependentCooldown(t *testing.T) {
	now := time.Now()
	own := &ModelState{Unavailable: true, Status: StatusError, NextRetryAfter: now.Add(30 * time.Minute),
		Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(30 * time.Minute)}}
	auth := &Auth{ID: "stale-wrap-own", Provider: "claude", ModelStates: map[string]*ModelState{
		"claude-sonnet-5-5": {
			Unavailable:               true,
			Status:                    StatusError,
			NextRetryAfter:            now.Add(2 * time.Hour),
			Quota:                     QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(2 * time.Hour)},
			credentialQuotaShadow:     own,
			credentialQuotaPropagated: true,
		},
	}}
	blocked, _, next := isAuthBlockedForModel(auth, "claude-sonnet-5-5", now)
	if !blocked || !next.Equal(own.NextRetryAfter) {
		t.Fatalf("expected the model's own cooldown until %v, got blocked=%v until %v", own.NextRetryAfter, blocked, next)
	}
}
