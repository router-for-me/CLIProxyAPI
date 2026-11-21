package auth

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestModelCooldownError_ConfigurableStatusCode(t *testing.T) {
	t.Cleanup(func() { SetModelCooldownStatusCode(0) })

	if got := newModelCooldownError("m", "p", time.Minute).StatusCode(); got != http.StatusTooManyRequests {
		t.Fatalf("default status = %d, want 429", got)
	}

	SetModelCooldownStatusCode(529)
	if got := newModelCooldownError("m", "p", time.Minute).StatusCode(); got != 529 {
		t.Fatalf("configured status = %d, want 529", got)
	}

	SetModelCooldownStatusCode(599)
	if got := newModelCooldownError("m", "p", time.Minute).StatusCode(); got != 599 {
		t.Fatalf("status = %d, want 599", got)
	}

	SetModelCooldownStatusCode(399)
	if got := newModelCooldownError("m", "p", time.Minute).StatusCode(); got != http.StatusTooManyRequests {
		t.Fatalf("out-of-range status = %d, want 429 fallback", got)
	}

	SetModelCooldownStatusCode(600)
	if got := newModelCooldownError("m", "p", time.Minute).StatusCode(); got != http.StatusTooManyRequests {
		t.Fatalf("out-of-range status = %d, want 429 fallback", got)
	}
}

func TestConfiguredCooldownStatusPreservesInternalRetryDecisions(t *testing.T) {
	withQuotaCooldownEnabled(t)
	t.Cleanup(func() { SetModelCooldownStatusCode(0) })
	for _, code := range []int{0, 529} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			SetModelCooldownStatusCode(code)
			manager := NewManager(nil, nil, nil)
			manager.SetRetryConfig(1, 5*time.Second, 0)
			manager.SetConfig(&internalconfig.Config{QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true}})
			const model = "cooldown-status-internal-retry"
			const authID = "cooldown-status-internal-auth"
			next := time.Now().Add(2 * time.Second)
			_, err := manager.Register(context.Background(), &Auth{
				ID: authID, Provider: "claude", Status: StatusActive,
				ModelStates: map[string]*ModelState{model: {
					Unavailable: true, Status: StatusError, NextRetryAfter: next,
					LastError: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			registry.GetGlobalRegistry().RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
			for _, cooldown := range []error{
				newModelCooldownError(model, "claude", 2*time.Second),
				fmt.Errorf("wrapped: %w", newModelCooldownError(model, "claude", 2*time.Second)),
				&poolCooldownTerminalError{cause: &Error{HTTPStatus: http.StatusTooManyRequests}, deadline: next},
			} {
				wait, retry := manager.shouldRetryAfterError(cooldown, 0, []string{"claude"}, model, 5*time.Second)
				if !retry || wait <= 0 || wait > 2*time.Second {
					t.Fatalf("%T: retry=(%v, %t), want a short internal cooldown wait", cooldown, wait, retry)
				}
				if !shouldAttemptAntigravityCreditsFallback(manager, cooldown, []string{"antigravity"}) {
					t.Fatalf("%T: configured client status suppressed credits fallback", cooldown)
				}
				if _, retry := manager.shouldRetryAfterError(cooldown, 1, []string{"claude"}, model, 5*time.Second); retry {
					t.Fatal("client status bypassed the configured internal retry limit")
				}
			}
		})
	}
}
