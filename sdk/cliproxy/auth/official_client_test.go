package auth

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestOfficialClientRequired(t *testing.T) {
	restricted := map[string]bool{"claude": true, "codex": false, "gemini": true}
	setOfficialClientRequirementForTest(t, func(provider string) bool { return restricted[provider] })

	tests := []struct {
		name string
		auth *Auth
		want bool
	}{
		{name: "restricted provider", auth: &Auth{Provider: "claude"}, want: true},
		{name: "provider casing", auth: &Auth{Provider: " Claude "}, want: true},
		{name: "provider switched off", auth: &Auth{Provider: "codex"}, want: false},
		{name: "unsupported provider ignored", auth: &Auth{Provider: "gemini"}, want: false},
		{name: "nil auth", auth: nil, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := officialClientRequired(tc.auth); got != tc.want {
				t.Fatalf("officialClientRequired() = %v, want %v", got, tc.want)
			}
		})
	}

	SetOfficialClientRequirement(nil)
	if officialClientRequired(&Auth{Provider: "claude"}) {
		t.Fatal("no lookup installed must leave providers unrestricted")
	}
}

func registerOfficialClientTestAuth(t *testing.T, manager *Manager, id, provider string) {
	t.Helper()
	auth := &Auth{ID: id, Provider: provider, Status: StatusActive, Metadata: map[string]any{}}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register %s: %v", id, errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(id, provider, []*registry.ModelInfo{{ID: "official-client-model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
}

func setOfficialClientRequirementForTest(t *testing.T, requirement OfficialClientRequirement) {
	t.Helper()
	SetOfficialClientRequirement(requirement)
	t.Cleanup(func() { SetOfficialClientRequirement(nil) })
}

func setOfficialClientVerifierForTest(t *testing.T, verifier OfficialClientVerifier) {
	t.Helper()
	SetOfficialClientVerifier(verifier)
	t.Cleanup(func() { SetOfficialClientVerifier(nil) })
}

func TestPickNextMixedOfficialClientOnly(t *testing.T) {
	ctx := context.Background()
	for _, selectorCase := range []struct {
		name     string
		selector Selector
	}{
		{name: "fast_path", selector: &RoundRobinSelector{}},
		{name: "legacy_path", selector: nil},
	} {
		t.Run(selectorCase.name, func(t *testing.T) {
			// newManager restricts only the given provider to its official client.
			newManager := func(t *testing.T, provider string) *Manager {
				manager := NewManager(nil, selectorCase.selector, nil)
				manager.RegisterExecutor(schedulerProviderTestExecutor{provider: provider})
				setOfficialClientRequirementForTest(t, func(p string) bool { return p == provider })
				return manager
			}

			t.Run("rejects non official client with 403", func(t *testing.T) {
				manager := newManager(t, "codex")
				registerOfficialClientTestAuth(t, manager, "oc-only-"+selectorCase.name, "codex")
				setOfficialClientVerifierForTest(t, func(context.Context, string, cliproxyexecutor.Options) bool { return false })

				_, _, _, errPick := manager.pickNextMixed(ctx, []string{"codex"}, "official-client-model", cliproxyexecutor.Options{}, nil)
				var authErr *Error
				if !errors.As(errPick, &authErr) || authErr == nil {
					t.Fatalf("expected *Error, got %T: %v", errPick, errPick)
				}
				if authErr.Code != OfficialClientRequiredCode || authErr.StatusCode() != http.StatusForbidden {
					t.Fatalf("got code=%q status=%d, want %q/%d", authErr.Code, authErr.StatusCode(), OfficialClientRequiredCode, http.StatusForbidden)
				}
			})

			t.Run("allows official client", func(t *testing.T) {
				manager := newManager(t, "codex")
				registerOfficialClientTestAuth(t, manager, "oc-allowed-"+selectorCase.name, "codex")
				var gotProvider string
				setOfficialClientVerifierForTest(t, func(_ context.Context, provider string, _ cliproxyexecutor.Options) bool {
					gotProvider = provider
					return true
				})

				picked, _, _, errPick := manager.pickNextMixed(ctx, []string{"codex"}, "official-client-model", cliproxyexecutor.Options{}, nil)
				if errPick != nil {
					t.Fatalf("pick failed: %v", errPick)
				}
				if picked == nil || picked.ID != "oc-allowed-"+selectorCase.name {
					t.Fatalf("picked = %v", picked)
				}
				if gotProvider != "codex" {
					t.Fatalf("verifier provider = %q, want codex", gotProvider)
				}
			})

			t.Run("fails closed without verifier", func(t *testing.T) {
				manager := newManager(t, "claude")
				registerOfficialClientTestAuth(t, manager, "oc-noverifier-"+selectorCase.name, "claude")
				SetOfficialClientVerifier(nil)

				_, _, _, errPick := manager.pickNextMixed(ctx, []string{"claude"}, "official-client-model", cliproxyexecutor.Options{}, nil)
				var authErr *Error
				if !errors.As(errPick, &authErr) || authErr == nil || authErr.Code != OfficialClientRequiredCode {
					t.Fatalf("expected %s error, got %T: %v", OfficialClientRequiredCode, errPick, errPick)
				}
			})

			t.Run("unrestricted provider still serves non official client", func(t *testing.T) {
				manager := newManager(t, "claude")
				manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "codex"})
				registerOfficialClientTestAuth(t, manager, "oc-restricted-"+selectorCase.name, "claude")
				registerOfficialClientTestAuth(t, manager, "oc-open-"+selectorCase.name, "codex")
				setOfficialClientVerifierForTest(t, func(context.Context, string, cliproxyexecutor.Options) bool { return false })

				for i := 0; i < 4; i++ {
					picked, _, _, errPick := manager.pickNextMixed(ctx, []string{"claude", "codex"}, "official-client-model", cliproxyexecutor.Options{}, nil)
					if errPick != nil {
						t.Fatalf("pick %d failed: %v", i, errPick)
					}
					if picked.ID != "oc-open-"+selectorCase.name {
						t.Fatalf("pick %d selected restricted credential %q", i, picked.ID)
					}
				}
			})

			t.Run("verifies once per request", func(t *testing.T) {
				manager := newManager(t, "codex")
				registerOfficialClientTestAuth(t, manager, "oc-a-"+selectorCase.name, "codex")
				registerOfficialClientTestAuth(t, manager, "oc-b-"+selectorCase.name, "codex")
				calls := 0
				setOfficialClientVerifierForTest(t, func(context.Context, string, cliproxyexecutor.Options) bool {
					calls++
					return false
				})

				_, _, _, _ = manager.pickNextMixed(ctx, []string{"codex"}, "official-client-model", cliproxyexecutor.Options{}, nil)
				if calls != 1 {
					t.Fatalf("verifier called %d times, want 1", calls)
				}
			})

			t.Run("config change takes effect without re-registering", func(t *testing.T) {
				manager := newManager(t, "codex")
				registerOfficialClientTestAuth(t, manager, "oc-toggle-"+selectorCase.name, "codex")
				setOfficialClientVerifierForTest(t, func(context.Context, string, cliproxyexecutor.Options) bool { return false })

				var enabled atomic.Bool
				setOfficialClientRequirementForTest(t, func(string) bool { return enabled.Load() })

				if _, _, _, errPick := manager.pickNextMixed(ctx, []string{"codex"}, "official-client-model", cliproxyexecutor.Options{}, nil); errPick != nil {
					t.Fatalf("unrestricted pick failed: %v", errPick)
				}
				enabled.Store(true)
				if _, _, _, errPick := manager.pickNextMixed(ctx, []string{"codex"}, "official-client-model", cliproxyexecutor.Options{}, nil); errPick == nil {
					t.Fatal("restricted provider was selected for a non official client")
				}
				enabled.Store(false)
				if _, _, _, errPick := manager.pickNextMixed(ctx, []string{"codex"}, "official-client-model", cliproxyexecutor.Options{}, nil); errPick != nil {
					t.Fatalf("pick after disabling restriction failed: %v", errPick)
				}
			})
		})
	}
}

func TestOfficialClientRejectionErrorLeavesOtherErrorsAlone(t *testing.T) {
	check := newOfficialClientCheck(context.Background(), cliproxyexecutor.Options{})
	setOfficialClientVerifierForTest(t, func(context.Context, string, cliproxyexecutor.Options) bool { return false })
	setOfficialClientRequirementForTest(t, func(string) bool { return true })
	if check.allows(&Auth{Provider: "codex"}) {
		t.Fatal("restricted auth allowed")
	}

	cooldown := &Error{Code: "auth_unavailable", Message: "all credentials cooling down"}
	if got := check.rejectionError(cooldown); got != error(cooldown) {
		t.Fatalf("non auth_not_found error was rewritten: %v", got)
	}
	if got := check.rejectionError(nil); got != nil {
		t.Fatalf("nil error rewritten to %v", got)
	}
	if got := newOfficialClientCheck(context.Background(), cliproxyexecutor.Options{}).rejectionError(&Error{Code: "auth_not_found"}); got.(*Error).Code != "auth_not_found" {
		t.Fatalf("error rewritten although nothing was rejected: %v", got)
	}
}
