package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestResetQuotaIfUnchanged(t *testing.T) {
	for _, change := range []string{"none", "new failure", "registration epoch", "disabled", "remote owner", "canceled"} {
		t.Run(change, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			authID, model := t.Name(), t.Name()+"-model"
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { reg.UnregisterClient(authID) })
			if _, errRegister := manager.Register(t.Context(), &Auth{ID: authID, Provider: "codex", Status: StatusActive}); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}
			duration := 24 * time.Hour
			manager.MarkResult(t.Context(), Result{
				AuthID: authID, Provider: "codex", Model: model, CredentialScope: true, RetryAfter: &duration,
				Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exceeded"},
			})
			expected, _ := manager.GetByID(authID)
			ctx := t.Context()
			switch change {
			case "new failure":
				manager.MarkResult(ctx, Result{AuthID: authID, Provider: "codex", Model: model, Error: &Error{HTTPStatus: http.StatusUnauthorized}})
			case "registration epoch":
				expected.RegistrationEpoch++
			case "disabled":
				manager.mu.Lock()
				manager.auths[authID].Disabled = true
				manager.mu.Unlock()
			case "remote owner":
				cfg := &internalconfig.Config{}
				cfg.Home.Enabled = true
				manager.SetConfig(cfg)
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			before, _ := manager.GetByID(authID)
			updated, _, errReset := manager.ResetQuotaIfUnchanged(ctx, expected)
			if change == "canceled" {
				if !errors.Is(errReset, context.Canceled) {
					t.Fatalf("reset error = %v, want context.Canceled", errReset)
				}
			} else if errReset != nil {
				t.Fatalf("reset quota: %v", errReset)
			}
			after, _ := manager.GetByID(authID)
			if change != "none" {
				if updated != nil || after.Generation != before.Generation || !after.Quota.NextRecoverAt.Equal(before.Quota.NextRecoverAt) {
					t.Fatal("outdated or ineligible snapshot changed quota state")
				}
				return
			}
			if updated == nil || after.Unavailable || after.Quota.Exceeded || after.Status != StatusActive {
				t.Fatal("matching quota snapshot did not recover")
			}
			if count := reg.GetModelCount(model); count != 1 {
				t.Fatalf("registry model count = %d, want 1", count)
			}
			picked, errPick := manager.scheduler.pickSingle(ctx, "codex", model, cliproxyexecutor.Options{}, nil)
			if errPick != nil || picked == nil || picked.ID != authID {
				t.Fatalf("scheduler selection after recovery = %v, error = %v", picked, errPick)
			}
		})
	}
}
