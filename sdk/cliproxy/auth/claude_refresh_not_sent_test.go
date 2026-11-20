package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	baseauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth"
)

// After a long sleep the access token has expired and the first renewal can run
// before DNS is back. Such a refresh never reached the provider, so the credential
// must be retried on the short deferral cadence instead of being parked as
// "token expired" for refreshFailureBackoff.
func TestClaudeExpiredTokenRefreshNotSentRetriesOnDeferralCadence(t *testing.T) {
	for _, tc := range []struct {
		name         string
		refreshErr   error
		wantDeferred bool
	}{
		{
			name: "not-sent",
			refreshErr: fmt.Errorf("token refresh request not sent: %w: %w",
				&net.DNSError{Err: "no such host", Name: "platform.claude.com", IsNotFound: true}, baseauth.ErrRefreshDeferred),
			wantDeferred: true,
		},
		{
			name:       "ambiguous",
			refreshErr: errors.New("token refresh request failed: write: broken pipe"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			manager := NewManager(nil, nil, nil)
			executor := &unauthorizedRefreshExecutor{id: "claude", refreshErr: tc.refreshErr}
			manager.RegisterExecutor(executor)

			now := time.Now()
			registered, errRegister := manager.Register(ctx, &Auth{
				ID:       "claude-expired-after-sleep",
				Provider: "claude",
				Status:   StatusActive,
				Metadata: map[string]any{
					"access_token":  "expired-access",
					"refresh_token": "unused-refresh",
					"expired":       now.Add(-3 * time.Hour).Format(time.RFC3339),
				},
			})
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			loop := newAuthAutoRefreshLoop(manager, refreshCheckInterval, 1)
			job := manager.markRefreshPending(loop, registered.ID, registered.RegistrationEpoch, now)
			if job == nil {
				t.Fatal("expected pending refresh job")
			}
			if !manager.beginRefreshJob(ctx, job) {
				t.Fatal("expected refresh job to start")
			}
			if _, errRefresh := manager.refreshAuthForRequestAtEpoch(ctx, job.id, "", job.registrationEpoch); errRefresh == nil {
				t.Fatal("expected refresh error")
			}
			manager.finishRefreshJob(job, time.Time{}, false)

			updated, ok := manager.GetByID(registered.ID)
			if !ok || updated == nil {
				t.Fatal("credential missing after refresh")
			}
			retryIn := updated.NextRefreshAfter.Sub(now)
			if !tc.wantDeferred {
				// Unchanged behaviour for a refresh that may have reached the provider.
				if retryIn < refreshFailureBackoff-time.Second || updated.StatusMessage != "token expired" {
					t.Fatalf("ambiguous failure: retry in %v, status %q", retryIn, updated.StatusMessage)
				}
				return
			}
			if retryIn <= 0 || retryIn > refreshCheckInterval+time.Second {
				t.Fatalf("next refresh in %v, want within %v", retryIn, refreshCheckInterval)
			}
			if updated.StatusMessage == "token expired" || updated.RefreshError != nil || updated.LastError != nil || updated.RequiresSignIn {
				t.Fatalf("unsent refresh recorded a renewal failure: status=%q refresh=%v last=%v", updated.StatusMessage, updated.RefreshError, updated.LastError)
			}
		})
	}
}
