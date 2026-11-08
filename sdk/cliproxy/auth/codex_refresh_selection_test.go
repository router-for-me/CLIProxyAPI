package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
)

func TestCodexUnknownSIWCRefreshKeepsHealthyOuterAccess(t *testing.T) {
	now := time.Now()
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(&unauthorizedRefreshExecutor{id: "codex", refreshErr: codex.WrapSIWCRefreshError(&codex.RefreshOutcomeUnknownError{Stage: "transport"})})
	auth := &Auth{ID: t.Name(), Provider: "codex", Status: StatusActive, Metadata: map[string]any{
		"access_token": "outer", "refresh_token": "outer-refresh", "expired": now.Add(time.Hour).Format(time.RFC3339), "use_siwc": true,
		"siwc": map[string]any{"access_token": "siwc", "refresh_token": "siwc-refresh", "client_id": "client", "expires_at": now.Add(-time.Hour).Format(time.RFC3339)},
	}}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatal(errRegister)
	}
	if _, errRefresh := manager.ForceRefreshAuth(context.Background(), auth.ID); !codex.IsRefreshOutcomeUnknown(errRefresh) {
		t.Fatalf("refresh outcome = %v, want unknown", errRefresh)
	}
	base, ok := manager.GetByID(auth.ID)
	if !ok || !base.RequiresSignIn || !base.Unavailable || base.RejectedSIWCRefreshToken != "siwc-refresh" {
		t.Fatalf("SIWC recovery state was not retained: %#v", base)
	}
	for _, tc := range []struct {
		name    string
		native  bool
		mutate  func(*Auth)
		blocked bool
	}{
		{name: "healthy outer", native: true},
		{name: "expired SIWC", blocked: true},
		{name: "disabled", native: true, blocked: true, mutate: func(auth *Auth) { auth.Disabled = true }},
		{name: "expired outer", native: true, blocked: true, mutate: func(auth *Auth) { auth.Metadata["expired"] = now.Add(-time.Hour).Format(time.RFC3339) }},
		{name: "rejected outer", native: true, blocked: true, mutate: func(auth *Auth) { auth.RejectedAccessToken = "outer" }},
		{name: "other cooldown", native: true, blocked: true, mutate: func(auth *Auth) { auth.NextRetryAfter = now.Add(time.Hour) }},
		{name: "credential quota", native: true, blocked: true, mutate: func(auth *Auth) {
			auth.Quota = QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(time.Hour)}
		}},
		{name: "model cooldown", native: true, blocked: true, mutate: func(auth *Auth) {
			auth.ModelStates = map[string]*ModelState{"model": {Status: StatusError, Unavailable: true, NextRetryAfter: now.Add(time.Hour)}}
		}},
		{name: "unrelated error", native: true, blocked: true, mutate: func(auth *Auth) { auth.LastError = &Error{Code: "unrelated"} }},
		{name: "explicit unauthorized", native: true, blocked: true, mutate: func(auth *Auth) { auth.LastError = &Error{Code: "unauthorized", HTTPStatus: http.StatusUnauthorized} }},
		{name: "no SIWC fence", native: true, blocked: true, mutate: func(auth *Auth) { auth.RejectedSIWCRefreshToken = "" }},
		{name: "other provider", native: true, blocked: true, mutate: func(auth *Auth) { auth.Provider = "claude" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := base.Clone()
			if tc.mutate != nil {
				tc.mutate(current)
			}
			var headers http.Header
			if tc.native {
				headers = http.Header{"User-Agent": {"codex_cli_rs/1.0.0"}}
			}
			blocked, _, _ := isAuthBlockedForModelForRequest(context.Background(), current, "model", now, headers)
			if blocked != tc.blocked {
				t.Fatalf("blocked = %v, want %v", blocked, tc.blocked)
			}
		})
	}
}

func TestCodexUnknownSIWCRefreshCacheKeepsHealthyOuterAccess(t *testing.T) {
	now := time.Now()
	manager := NewManager(nil, nil, nil)
	executor := &unauthorizedRefreshExecutor{id: "codex", refreshErr: codex.WrapSIWCRefreshError(&codex.RefreshOutcomeUnknownError{Stage: "transport"})}
	manager.RegisterExecutor(executor)
	auth := &Auth{ID: t.Name(), Provider: "codex", Status: StatusActive, Metadata: map[string]any{
		"access_token": "outer", "expired": now.Add(time.Hour).Format(time.RFC3339), "use_siwc": true,
		"siwc": map[string]any{"access_token": "siwc", "refresh_token": "siwc-refresh", "client_id": "client", "expires_at": now.Add(-time.Hour).Format(time.RFC3339)},
	}}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatal(errRegister)
	}
	if _, errRefresh := manager.ForceRefreshAuth(context.Background(), auth.ID); !codex.IsRefreshOutcomeUnknown(errRefresh) {
		t.Fatalf("refresh outcome = %v, want unknown", errRefresh)
	}
	manager.refreshAuth(context.Background(), auth.ID)
	current, _ := manager.GetByID(auth.ID)
	if executor.RefreshCalls() != 1 || current.LastError == nil || current.LastError.Code != "refresh_outcome_unknown" {
		t.Fatal("cached recovery state lost its classification or resubmitted the grant")
	}
	headers := http.Header{"User-Agent": {"codex_cli_rs/1.0.0"}}
	if blocked, _, _ := isAuthBlockedForModelForRequest(context.Background(), current, "model", now, headers); blocked {
		t.Fatal("cached SIWC recovery state blocked healthy outer access")
	}
}

type codexPartialRefreshExecutor struct {
	unauthorizedRefreshExecutor
	refresh func(*Auth) (*Auth, error)
}

func (e *codexPartialRefreshExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return e.refresh(auth)
}

func TestCodexPartialRefreshKeepsUnknownSelectedPartitionBlocked(t *testing.T) {
	for _, useSIWC := range []bool{false, true} {
		name := "outer"
		if useSIWC {
			name = "siwc"
		}
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			auth := &Auth{ID: t.Name(), Provider: "codex", Status: StatusActive, Metadata: map[string]any{
				"access_token": "outer", "refresh_token": "outer-refresh", "expired": now.Add(-time.Hour).Format(time.RFC3339), "use_siwc": useSIWC,
				"siwc": map[string]any{"access_token": "siwc", "refresh_token": "siwc-refresh", "client_id": "client", "expires_at": now.Add(-time.Hour).Format(time.RFC3339)},
			}}
			manager := NewManager(nil, nil, nil)
			manager.RegisterExecutor(&codexPartialRefreshExecutor{
				unauthorizedRefreshExecutor: unauthorizedRefreshExecutor{id: "codex"},
				refresh: func(before *Auth) (*Auth, error) {
					updated := before.Clone()
					var outerErr, siwcErr error
					outcome := &codex.RefreshOutcomeUnknownError{Stage: "transport"}
					if useSIWC {
						updated.Metadata["access_token"] = "outer-new"
						updated.Metadata["expired"] = now.Add(time.Hour).Format(time.RFC3339)
						siwcErr = codex.WrapSIWCRefreshError(outcome)
					} else {
						siwc := updated.Metadata["siwc"].(map[string]any)
						siwc["access_token"] = "siwc-new"
						siwc["expires_at"] = now.Add(time.Hour).Format(time.RFC3339)
						outerErr = outcome
					}
					updated.CodexRefreshPatch = codex.NewCredentialRefreshPatch(before.Metadata, updated.Metadata, useSIWC, !useSIWC)
					updated.CodexRefreshPatch.SetPartitionErrors(outerErr, siwcErr)
					return updated, nil
				},
			})
			if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
				t.Fatal(errRegister)
			}
			if _, errRefresh := manager.ForceRefreshAuth(context.Background(), auth.ID); errRefresh != nil {
				t.Fatal(errRefresh)
			}
			current, _ := manager.GetByID(auth.ID)
			if !current.Unavailable || !current.RequiresSignIn || current.LastError == nil || current.LastError.Code != "refresh_outcome_unknown" {
				t.Fatalf("partial refresh lost the unresolved selected partition: unavailable=%v sign_in=%v last_error=%#v refresh_error=%#v", current.Unavailable, current.RequiresSignIn, current.LastError, current.RefreshError)
			}
			if blocked, _, _ := isAuthBlockedForModelForRequest(context.Background(), current, "model", now); !blocked {
				t.Fatal("partial refresh restored an expired selected partition")
			}
			headers := http.Header{"User-Agent": {"codex_cli_rs/1.0.0"}}
			if blocked, _, _ := isAuthBlockedForModelForRequest(context.Background(), current, "model", now, headers); blocked == useSIWC {
				t.Fatal("native request availability does not match its outer access token")
			}
		})
	}
}

func TestCodexMissingRecoveryAccessBlocksBeforeHealthyModelState(t *testing.T) {
	now := time.Now()
	for _, useSIWC := range []bool{false, true} {
		for _, unavailable := range []bool{false, true} {
			auth := &Auth{Provider: "codex", Status: StatusError, RequiresSignIn: true, Unavailable: unavailable,
				RefreshError: &Error{Code: "refresh_outcome_unknown"},
				Metadata: map[string]any{
					"refresh_token": "outer-refresh", "use_siwc": useSIWC,
					"siwc": map[string]any{"refresh_token": "siwc-refresh", "client_id": "client"},
				},
				ModelStates: map[string]*ModelState{"model": {Status: StatusActive}},
			}
			if useSIWC {
				auth.Metadata["access_token"] = "outer-valid"
				auth.Metadata["expired"] = now.Add(time.Hour).Format(time.RFC3339)
				auth.RejectedSIWCRefreshToken = "siwc-refresh"
			} else {
				siwc := auth.Metadata["siwc"].(map[string]any)
				siwc["access_token"] = "siwc-valid"
				siwc["expires_at"] = now.Add(time.Hour).Format(time.RFC3339)
				auth.RejectedRefreshToken = "outer-refresh"
			}
			blocked, _, _ := isAuthBlockedForModelForRequest(context.Background(), auth, "model", now)
			if !blocked || !isAuthBlockedByQuotaOrAuthForModelForRequest(context.Background(), auth, "model", now) {
				t.Fatalf("healthy model state admitted a missing token: use_siwc=%v unavailable=%v", useSIWC, unavailable)
			}
		}
	}
}

func TestCodexUnknownDiagnostic401UsesPriorUnauthorizedState(t *testing.T) {
	for _, mode := range []string{"refresh", "cached"} {
		for _, tokenState := range []string{"expired", "missing"} {
			for _, priorUnauthorized := range []bool{false, true} {
				priorState := "active"
				if priorUnauthorized {
					priorState = "unauthorized"
				}
				t.Run(mode+"/"+tokenState+"/"+priorState, func(t *testing.T) {
					now := time.Now()
					outcome := &codex.RefreshOutcomeUnknownError{Stage: "transport", Cause: oauthStatusError{code: http.StatusUnauthorized, msg: "transport diagnostic"}}
					executor := &unauthorizedRefreshExecutor{id: "codex", refreshErr: codex.WrapSIWCRefreshError(outcome)}
					manager := NewManager(nil, nil, nil)
					manager.RegisterExecutor(executor)
					siwc := map[string]any{"refresh_token": "siwc-refresh", "client_id": "client"}
					if tokenState == "expired" {
						siwc["access_token"] = "siwc-expired"
						siwc["expires_at"] = now.Add(-time.Hour).Format(time.RFC3339)
					}
					auth := &Auth{ID: t.Name(), Provider: "codex", Status: StatusActive, Metadata: map[string]any{
						"access_token": "outer-valid", "expired": now.Add(time.Hour).Format(time.RFC3339), "use_siwc": true, "siwc": siwc,
					}}
					if mode == "cached" {
						auth.RequiresSignIn = true
						auth.RejectedSIWCRefreshToken = "siwc-refresh"
						auth.RefreshError = &Error{Code: "refresh_outcome_unknown", Message: outcome.Error(), HTTPStatus: http.StatusUnauthorized}
						auth.LastError = &Error{Code: "unauthorized", Message: outcome.Error(), HTTPStatus: http.StatusUnauthorized}
					}
					if priorUnauthorized {
						auth.Unavailable = true
						auth.Status = StatusError
						auth.LastError = &Error{Code: "unauthorized", Message: "access token rejected", HTTPStatus: http.StatusUnauthorized}
					}
					registered, errRegister := manager.Register(context.Background(), auth)
					if errRegister != nil || hasUnauthorizedAuthFailure(registered) != priorUnauthorized {
						t.Fatalf("incorrect prior state: err=%v terminal=%v", errRegister, hasUnauthorizedAuthFailure(registered))
					}
					wantCalls := 0
					if mode == "cached" {
						manager.refreshAuth(context.Background(), auth.ID)
					} else {
						wantCalls = 1
						if _, errRefresh := manager.ForceRefreshAuth(context.Background(), auth.ID); !codex.IsRefreshOutcomeUnknown(errRefresh) {
							t.Fatalf("refresh error = %v, want unknown", errRefresh)
						}
					}
					current, _ := manager.GetByID(auth.ID)
					if executor.RefreshCalls() != wantCalls || current.RefreshError == nil || current.RefreshError.Code != "refresh_outcome_unknown" || !current.RequiresSignIn {
						t.Fatalf("recovery state or replay protection changed: calls=%d refresh_error=%#v", executor.RefreshCalls(), current.RefreshError)
					}
					if priorUnauthorized {
						if !hasUnauthorizedAuthFailure(current) || current.LastError.Code != "unauthorized" {
							t.Fatal("unknown refresh cleared the prior terminal rejection")
						}
					} else if current.LastError == nil || current.LastError.Code != "refresh_outcome_unknown" || current.LastError.HTTPStatus == http.StatusUnauthorized || hasUnauthorizedAuthFailure(current) {
						t.Fatalf("diagnostic 401 created a terminal rejection: %#v", current.LastError)
					}
					headers := http.Header{"User-Agent": {"codex_cli_rs/1.0.0"}}
					if blocked, _, _ := isAuthBlockedForModelForRequest(context.Background(), current, "model", now, headers); blocked != priorUnauthorized {
						t.Fatalf("native outer blocked=%v, want %v", blocked, priorUnauthorized)
					}
					if blocked, _, _ := isAuthBlockedForModelForRequest(context.Background(), current, "model", now); !blocked {
						t.Fatal("unusable SIWC access was admitted")
					}
				})
			}
		}
	}
}
