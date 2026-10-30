package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestRequestRefreshTerminalRenewalState(t *testing.T) {
	for _, tc := range []struct {
		code   string
		status int
	}{
		{"unauthorized", 401},
		{"refresh_token_reused", 400},
		{"refresh_token_expired", 400},
		{"refresh_token_invalidated", 400},
		{"token_revoked", 400},
		{"invalid_client", 400},
		{"invalid_grant", 400},
	} {
		t.Run(tc.code, func(t *testing.T) {
			m, a, _, e := demandManager(t, time.Now().Add(30*time.Second))
			cause := &Error{HTTPStatus: tc.status, Message: fmt.Sprintf(`{"error":{"message":"renewal failed","code":%q,"refresh_token":"synthetic-renewal-secret"}}`, tc.code)}
			e.refresh = func(context.Context, *Auth) (*Auth, error) { return nil, cause }
			_, err := m.EnsureRequestAuth(context.Background(), a, "demand-model")
			if !errors.Is(err, cause) || statusCodeFromError(err) != tc.status {
				t.Fatalf("lost typed cause/status: %v", err)
			}
			current, _ := m.GetByID(a.ID)
			if !current.RequiresSignIn || current.RefreshError == nil || current.RefreshError.Code != tc.code || current.RefreshError.HTTPStatus != tc.status || current.RefreshError.Retryable || current.RejectedRefreshToken != "old-refresh" || current.RenewalVerified || current.RenewalEventAt.IsZero() || !current.Unavailable {
				t.Fatalf("terminal renewal not projected: sign-in=%v error=%+v rejected=%q unavailable=%v", current.RequiresSignIn, current.RefreshError, current.RejectedRefreshToken, current.Unavailable)
			}
			if strings.Contains(err.Error(), "synthetic-renewal-secret") || strings.Contains(current.RefreshError.Message, "synthetic-renewal-secret") {
				t.Fatal("renewal diagnostic leaked a secret")
			}
			if _, err = m.ForceRefreshAuth(context.Background(), a.ID); err == nil || e.refreshes.Load() != 1 {
				t.Fatalf("terminal grant replayed under force: err=%v calls=%d", err, e.refreshes.Load())
			}
			current.Metadata["access_token"] = "distinct-login-access"
			current.Metadata["refresh_token"] = "distinct-login-refresh"
			current.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
			current, err = m.Update(context.Background(), current)
			if err != nil {
				t.Fatal(err)
			}
			fresh, err := m.EnsureRequestAuth(context.Background(), current, "demand-model")
			if err != nil || fresh.RequiresSignIn || fresh.RefreshError != nil || fresh.RejectedRefreshToken != "" || e.refreshes.Load() != 1 {
				t.Fatalf("distinct login inherited rejected grant: %v", err)
			}
		})
	}
}

func TestRequestRefreshRejectedGrantNeverReplays(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprint(expired), func(t *testing.T) {
			expiry := time.Now().Add(30 * time.Second)
			if expired {
				expiry = time.Now().Add(-time.Hour)
			}
			m, a, _, e := demandManager(t, expiry)
			m.mu.Lock()
			current := m.auths[a.ID]
			current.RequiresSignIn = true
			current.RejectedRefreshToken = "old-refresh"
			current.RefreshError = &Error{Code: "refresh_token_reused", Message: "refresh_token_reused", HTTPStatus: 400}
			m.mu.Unlock()
			if _, err := m.EnsureRequestAuth(context.Background(), a, "demand-model"); err == nil || e.refreshes.Load() != 0 {
				t.Fatalf("known rejected grant reached provider: err=%v calls=%d", err, e.refreshes.Load())
			}
			if _, err := m.ForceRefreshAuth(context.Background(), a.ID); err == nil || e.refreshes.Load() != 0 {
				t.Fatalf("force replayed rejected grant: err=%v calls=%d", err, e.refreshes.Load())
			}
		})
	}
}

func TestRequestRefreshTransientRenewalAndCommitState(t *testing.T) {
	m, a, store, e := demandManager(t, time.Now().Add(-time.Hour))
	cause := &Error{Message: "synthetic temporary renewal outage", HTTPStatus: 503, Retryable: true}
	e.refresh = func(context.Context, *Auth) (*Auth, error) { return nil, cause }
	if _, err := m.EnsureRequestAuth(context.Background(), a, "demand-model"); !errors.Is(err, cause) {
		t.Fatal(err)
	}
	current, _ := m.GetByID(a.ID)
	if current.RequiresSignIn || current.Unavailable || current.RejectedRefreshToken != "" || current.RefreshError == nil || current.RefreshError.HTTPStatus != 503 || !current.RefreshError.Retryable {
		t.Fatalf("transient renewal changed terminal state: %+v", current.RefreshError)
	}
	adopted, fingerprint := current.AdoptedAt, current.AdoptedGrantHash
	state := m.authRefreshState(a.ID)
	state.stateMu.Lock()
	state.outcome.retryAt = time.Now().Add(-time.Second)
	state.stateMu.Unlock()
	e.refresh = nil
	store.mu.Lock()
	store.fail = errors.New("synthetic renewal save failure")
	store.mu.Unlock()
	if _, err := m.EnsureRequestAuth(context.Background(), current, "demand-model"); err == nil {
		t.Fatal("failed Save succeeded")
	}
	current, _ = m.GetByID(a.ID)
	if current.RenewalVerified || current.Metadata["access_token"] != "old" {
		t.Fatal("uncommitted renewal published")
	}
	store.mu.Lock()
	store.fail = nil
	store.mu.Unlock()
	state.stateMu.Lock()
	state.outcome.retryAt = time.Now().Add(-time.Second)
	state.stateMu.Unlock()
	fresh, err := m.EnsureRequestAuth(context.Background(), current, "demand-model")
	if err != nil || !fresh.RenewalVerified || fresh.LastRenewalVerifiedAt.IsZero() || fresh.RefreshError != nil || !fresh.AdoptedAt.Equal(adopted) || fresh.AdoptedGrantHash != fingerprint || e.refreshes.Load() != 2 {
		t.Fatalf("saved renewal state incorrect: err=%v verified=%v refreshes=%d", err, fresh != nil && fresh.RenewalVerified, e.refreshes.Load())
	}
}

func TestRequestRefreshFreshAndOptOutRenewalControls(t *testing.T) {
	for _, policy := range []pluginapi.RefreshPolicy{pluginapi.RefreshOnDemand, pluginapi.RefreshDefault, ""} {
		t.Run(string(policy), func(t *testing.T) {
			m, a, _, e := demandManager(t, time.Now().Add(time.Hour))
			a.RefreshPolicy = policy
			a, _ = m.Update(context.Background(), a)
			m.mu.Lock()
			m.auths[a.ID].RequiresSignIn = true
			m.auths[a.ID].RejectedRefreshToken = "old-refresh"
			m.auths[a.ID].RefreshError = &Error{Code: "refresh_token_reused", HTTPStatus: 400}
			m.mu.Unlock()
			if _, err := m.EnsureRequestAuth(context.Background(), a, "demand-model"); err != nil || e.refreshes.Load() != 0 {
				t.Fatalf("fresh access was unnecessarily rotated or blocked: %v", err)
			}
		})
	}
}
