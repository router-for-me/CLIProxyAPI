package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
)

func TestSIWCWakeCancellationPreservesOuterRefreshFailure(t *testing.T) {
	for _, terminalOuter := range []bool{false, true} {
		t.Run(fmt.Sprintf("terminalOuter=%t", terminalOuter), func(t *testing.T) {
			var outerErr error
			if terminalOuter {
				outerErr = errors.New(`token refresh failed with status 401: {"error":{"code":"refresh_token_reused"}}`)
			}
			deferred := codex.WrapSIWCRefreshError(fmt.Errorf("SIWC renewal deferred until interactive macOS wake: %w", codex.ErrSIWCRefreshDeferred))
			executor := &issue6819OuterOnlyRefreshExecutor{id: "codex", refreshErr: codex.NewPartitionRefreshError(outerErr, deferred)}
			manager := NewManager(nil, nil, nil)
			manager.RegisterExecutor(executor)
			auth := &Auth{ID: "wake-cancellation", Provider: "codex", Metadata: map[string]any{
				"access_token": "outer-access", "refresh_token": "outer-refresh", "use_siwc": true,
				"expired": time.Now().Add(time.Hour).Format(time.RFC3339),
				"siwc": map[string]any{
					"access_token": "siwc-access", "refresh_token": "siwc-refresh", "client_id": "oaiapp_cancellation",
					"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
				},
			}}
			if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
				t.Fatal(errRegister)
			}
			if _, errRefresh := manager.ForceRefreshAuth(context.Background(), auth.ID); errRefresh == nil {
				t.Fatal("expected deferred refresh")
			}
			current, ok := manager.GetByID(auth.ID)
			if !ok {
				t.Fatal("credential disappeared")
			}
			if terminalOuter {
				if !current.RequiresSignIn || current.RejectedRefreshToken != "outer-refresh" {
					t.Fatal("SIWC cancellation hid the terminal outer refresh rejection")
				}
			} else if current.RequiresSignIn || current.RefreshError != nil || current.NextRefreshAfter.IsZero() {
				t.Fatal("pure SIWC cancellation did not use the cancellation rescheduler")
			}
			if current.RejectedSIWCRefreshToken != "" {
				t.Fatal("wake deferral marked the SIWC grant as rejected")
			}
			if current.CodexSIWCStatus(time.Now()) != SIWCStatusValid {
				t.Fatal("outer refresh rejection invalidated the working SIWC access token")
			}
		})
	}
}
