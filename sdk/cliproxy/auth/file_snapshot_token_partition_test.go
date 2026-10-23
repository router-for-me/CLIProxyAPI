package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// A Codex file can run on its outer tokens or, with use_siwc, on the SIWC tokens, and a result may carry either one. The
// stale-result rules (isStaleExecutionResult, MarkResult's stale branch) must hold in both token partitions.

const (
	partitionOuterToken = "outer-token"
	partitionSIWCToken  = "siwc-token"
)

func partitionMetadata(accountID string, useSIWC bool, idToken string) map[string]any {
	return map[string]any{
		"type":         "codex",
		"access_token": partitionOuterToken,
		"id_token":     idToken,
		"account_id":   accountID,
		"use_siwc":     useSIWC,
		"siwc": map[string]any{
			"access_token": partitionSIWCToken,
			"expires_at":   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		},
	}
}

func partitionLate(authID string, base *Auth, token string, err *Error) Result {
	return Result{AuthID: authID, Provider: "codex", Model: "m", AccessToken: token,
		CredentialVersion: base.CredentialVersion, RegistrationEpoch: base.RegistrationEpoch, Error: err}
}

func partitionUntouched(t *testing.T, got *Auth, what string) {
	t.Helper()
	if got.Unavailable || got.LastError != nil || got.Quota.Exceeded || got.RejectedAccessToken != "" || got.RejectedSIWCAccessToken != "" {
		t.Fatalf("%s: unavailable=%v last=%v quota=%+v rejected=%q rejectedSIWC=%q",
			what, got.Unavailable, got.LastError, got.Quota, got.RejectedAccessToken, got.RejectedSIWCAccessToken)
	}
	if blocked, _, _ := isAuthBlockedForModel(got, "m", time.Now()); blocked {
		t.Fatalf("%s: model blocked: %+v", what, got.ModelStates)
	}
}

// An in-flight request of the previous account, carrying either token of the file, must not land on the account the
// file switched to under the same tokens; a failure of the new account must.
func TestResultFromPreviousAccountIsIgnoredAfterSameTokenSwitchInBothPartitions(t *testing.T) {
	for _, tc := range []struct{ name, token string }{{"outer token", partitionOuterToken}, {"siwc token", partitionSIWCToken}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			manager := NewManager(nil, nil, nil)
			const authID = "partition-account-switch"
			if _, errRegister := manager.Register(ctx, &Auth{ID: authID, Provider: "codex", Status: StatusActive,
				Metadata: partitionMetadata("account-a", true, "id-1")}); errRegister != nil {
				t.Fatal(errRegister)
			}
			inflight, _ := manager.GetByID(authID)

			snapshot := bindTestSnapshot(t, authID, "codex", []byte(`{"type":"codex","account_id":"account-b"}`), map[string]string{})
			snapshot.Metadata = partitionMetadata("account-b", true, "id-1")
			if _, errUpdate := manager.Update(ctx, snapshot); errUpdate != nil {
				t.Fatalf("snapshot rejected: %v", errUpdate)
			}
			switched, _ := manager.GetByID(authID)
			if switched.CredentialVersion <= inflight.CredentialVersion {
				t.Fatalf("account switch did not advance the credential version: %d -> %d", inflight.CredentialVersion, switched.CredentialVersion)
			}
			// Both partitions must be live, or the test would not cover what it says.
			if authAccessToken(switched) != partitionSIWCToken || authOuterAccessToken(switched) != partitionOuterToken {
				t.Fatalf("partitions: active=%q outer=%q", authAccessToken(switched), authOuterAccessToken(switched))
			}

			manager.MarkResult(ctx, partitionLate(authID, inflight, tc.token, &Error{Message: "failed", HTTPStatus: http.StatusTooManyRequests}))
			manager.MarkResult(ctx, partitionLate(authID, inflight, tc.token, &Error{Message: "failed", HTTPStatus: http.StatusUnauthorized}))
			got, _ := manager.GetByID(authID)
			partitionUntouched(t, got, "old-account failure landed on the new account")

			fresh := partitionLate(authID, switched, tc.token, &Error{Message: "failed", HTTPStatus: http.StatusTooManyRequests})
			manager.MarkResult(ctx, fresh)
			if got, _ = manager.GetByID(authID); !got.Unavailable {
				t.Fatal("a failure of the new account was fenced out")
			}
		})
	}
}

// A credential change that is not an account switch must not fence out a result under an unchanged token, whichever
// token of the file the result carries.
func TestResultAfterNonAccountCredentialChangeStillCountsInBothPartitions(t *testing.T) {
	for _, tc := range []struct{ name, token string }{{"outer token", partitionOuterToken}, {"siwc token", partitionSIWCToken}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			manager := NewManager(nil, nil, nil)
			const authID = "partition-id-token-change"
			if _, errRegister := manager.Register(ctx, &Auth{ID: authID, Provider: "codex", Status: StatusActive,
				Metadata: partitionMetadata("account-a", true, "id-1")}); errRegister != nil {
				t.Fatal(errRegister)
			}
			inflight, _ := manager.GetByID(authID)
			updated := inflight.Clone()
			updated.Metadata["id_token"] = "id-2"
			if _, errUpdate := manager.Update(ctx, updated); errUpdate != nil {
				t.Fatalf("update rejected: %v", errUpdate)
			}
			if now, _ := manager.GetByID(authID); now.CredentialVersion <= inflight.CredentialVersion {
				t.Fatalf("the credential change did not advance the credential version: %d -> %d", inflight.CredentialVersion, now.CredentialVersion)
			}
			manager.MarkResult(ctx, partitionLate(authID, inflight, tc.token, &Error{Message: "rate limited", HTTPStatus: http.StatusTooManyRequests}))
			if got, _ := manager.GetByID(authID); !got.Unavailable {
				t.Fatal("a result under an unchanged token was fenced out by an unrelated credential change")
			}
		})
	}
}

// What a stale 401 does to the rejected-token state, per kind of 401 and per reason for being stale.
//   - a plain 401 condemns the token it carries, unless that token is still the file's own (an account switch under it);
//   - a 401 matched by a -and-cooldown rule (ErrorCodeForceCooldown) only cools: it never condemns a token;
//   - a result flagged request-scoped (NewRequestScopedError, the marker a request-scoped-errors rule puts on the error; the rule
//     evaluation itself is covered by conductor_issue6865_test.go) is the request's own failure and condemns nothing either.
func TestStaleUnauthorizedAndTheRejectedTokenState(t *testing.T) {
	plain := func() *Error { return &Error{Message: "unauthorized", HTTPStatus: http.StatusUnauthorized} }
	scoped := func() *Error {
		return NewRequestScopedError("request-scoped 401 rule matched", http.StatusUnauthorized)
	}
	forced := func() *Error {
		return &Error{Code: ErrorCodeForceCooldown, Message: "marker", HTTPStatus: http.StatusUnauthorized}
	}

	t.Run("stale by credential version, token not the active one: a plain 401 condemns it, a forced or request-scoped one does not", func(t *testing.T) {
		for _, tc := range []struct {
			name         string
			err          func() *Error
			wantRejected bool
		}{{"plain 401", plain, true}, {"force-cooldown 401", forced, false}, {"request-scoped 401", scoped, false}} {
			t.Run(tc.name, func(t *testing.T) {
				ctx := context.Background()
				manager := NewManager(nil, nil, nil)
				const authID = "partition-stale-401"
				// use_siwc is off: the SIWC token is not in the active partition, so the fast path does not vouch for it.
				if _, errRegister := manager.Register(ctx, &Auth{ID: authID, Provider: "codex", Status: StatusActive,
					Metadata: partitionMetadata("account-a", false, "id-1")}); errRegister != nil {
					t.Fatal(errRegister)
				}
				inflight, _ := manager.GetByID(authID)
				updated := inflight.Clone()
				updated.Metadata["id_token"] = "id-2"
				if _, errUpdate := manager.Update(ctx, updated); errUpdate != nil {
					t.Fatal(errUpdate)
				}
				manager.MarkResult(ctx, partitionLate(authID, inflight, partitionSIWCToken, tc.err()))
				got, _ := manager.GetByID(authID)
				if tc.wantRejected {
					if got.RejectedSIWCAccessToken != partitionSIWCToken {
						t.Fatalf("a plain stale 401 did not condemn the SIWC token: RejectedSIWCAccessToken=%q", got.RejectedSIWCAccessToken)
					}
				} else if got.RejectedAccessToken != "" || got.RejectedSIWCAccessToken != "" {
					t.Fatalf("a stale 401 that must condemn nothing left rejected tokens: RejectedAccessToken=%q RejectedSIWCAccessToken=%q",
						got.RejectedAccessToken, got.RejectedSIWCAccessToken)
				}
			})
		}
	})

	t.Run("stale by an account switch under the same token: no kind condemns the file's own tokens", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			err   func() *Error
			token string
		}{
			{"plain 401, outer token", plain, partitionOuterToken},
			{"plain 401, siwc token", plain, partitionSIWCToken},
			{"force-cooldown 401, outer token", forced, partitionOuterToken},
			{"force-cooldown 401, siwc token", forced, partitionSIWCToken},
			{"request-scoped 401, outer token", scoped, partitionOuterToken},
			{"request-scoped 401, siwc token", scoped, partitionSIWCToken},
		} {
			t.Run(tc.name, func(t *testing.T) {
				ctx := context.Background()
				manager := NewManager(nil, nil, nil)
				const authID = "partition-stale-account-switch-401"
				if _, errRegister := manager.Register(ctx, &Auth{ID: authID, Provider: "codex", Status: StatusActive,
					Metadata: partitionMetadata("account-a", true, "id-1")}); errRegister != nil {
					t.Fatal(errRegister)
				}
				inflight, _ := manager.GetByID(authID)
				snapshot := bindTestSnapshot(t, authID, "codex", []byte(`{"type":"codex","account_id":"account-b"}`), map[string]string{})
				snapshot.Metadata = partitionMetadata("account-b", true, "id-1")
				if _, errUpdate := manager.Update(ctx, snapshot); errUpdate != nil {
					t.Fatal(errUpdate)
				}
				manager.MarkResult(ctx, partitionLate(authID, inflight, tc.token, tc.err()))
				got, _ := manager.GetByID(authID)
				partitionUntouched(t, got, "a stale 401 of the previous account touched the new account")
			})
		}
	})
}
