package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestAuthOfficialClientOnly(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		metadata map[string]any
		want     bool
	}{
		{name: "missing", provider: "claude", metadata: map[string]any{}, want: false},
		{name: "nil metadata", provider: "claude", metadata: nil, want: false},
		{name: "explicit null", provider: "codex", metadata: map[string]any{MetadataOfficialClientOnly: nil}, want: false},
		{name: "true", provider: "claude", metadata: map[string]any{MetadataOfficialClientOnly: true}, want: true},
		{name: "false", provider: "codex", metadata: map[string]any{MetadataOfficialClientOnly: false}, want: false},
		{name: "string true", provider: "codex", metadata: map[string]any{MetadataOfficialClientOnly: " TRUE "}, want: true},
		{name: "string false", provider: "codex", metadata: map[string]any{MetadataOfficialClientOnly: "false"}, want: false},
		{name: "malformed string fails closed", provider: "claude", metadata: map[string]any{MetadataOfficialClientOnly: "yes please"}, want: true},
		{name: "unexpected type fails closed", provider: "claude", metadata: map[string]any{MetadataOfficialClientOnly: float64(0)}, want: true},
		{name: "provider casing", provider: " Claude ", metadata: map[string]any{MetadataOfficialClientOnly: true}, want: true},
		{name: "unsupported provider ignored", provider: "gemini", metadata: map[string]any{MetadataOfficialClientOnly: true}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			auth := &Auth{Provider: tc.provider, Metadata: tc.metadata}
			if got := auth.OfficialClientOnly(); got != tc.want {
				t.Fatalf("OfficialClientOnly() = %v, want %v", got, tc.want)
			}
		})
	}
	var nilAuth *Auth
	if nilAuth.OfficialClientOnly() {
		t.Fatal("nil auth must not be restricted")
	}
}

func registerOfficialClientTestAuth(t *testing.T, manager *Manager, id, provider string, restricted bool) {
	t.Helper()
	auth := &Auth{ID: id, Provider: provider, Status: StatusActive, Metadata: map[string]any{}}
	if restricted {
		auth.Metadata[MetadataOfficialClientOnly] = true
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register %s: %v", id, errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(id, provider, []*registry.ModelInfo{{ID: "official-client-model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
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
			newManager := func(t *testing.T, provider string) *Manager {
				manager := NewManager(nil, selectorCase.selector, nil)
				manager.RegisterExecutor(schedulerProviderTestExecutor{provider: provider})
				return manager
			}

			t.Run("rejects non official client with 403", func(t *testing.T) {
				manager := newManager(t, "codex")
				registerOfficialClientTestAuth(t, manager, "oc-only-"+selectorCase.name, "codex", true)
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
				registerOfficialClientTestAuth(t, manager, "oc-allowed-"+selectorCase.name, "codex", true)
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
				registerOfficialClientTestAuth(t, manager, "oc-noverifier-"+selectorCase.name, "claude", true)
				SetOfficialClientVerifier(nil)

				_, _, _, errPick := manager.pickNextMixed(ctx, []string{"claude"}, "official-client-model", cliproxyexecutor.Options{}, nil)
				var authErr *Error
				if !errors.As(errPick, &authErr) || authErr == nil || authErr.Code != OfficialClientRequiredCode {
					t.Fatalf("expected %s error, got %T: %v", OfficialClientRequiredCode, errPick, errPick)
				}
			})

			t.Run("unrestricted credential still serves non official client", func(t *testing.T) {
				manager := newManager(t, "claude")
				registerOfficialClientTestAuth(t, manager, "oc-restricted-"+selectorCase.name, "claude", true)
				registerOfficialClientTestAuth(t, manager, "oc-open-"+selectorCase.name, "claude", false)
				setOfficialClientVerifierForTest(t, func(context.Context, string, cliproxyexecutor.Options) bool { return false })

				for i := 0; i < 4; i++ {
					picked, _, _, errPick := manager.pickNextMixed(ctx, []string{"claude"}, "official-client-model", cliproxyexecutor.Options{}, nil)
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
				registerOfficialClientTestAuth(t, manager, "oc-a-"+selectorCase.name, "codex", true)
				registerOfficialClientTestAuth(t, manager, "oc-b-"+selectorCase.name, "codex", true)
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

			t.Run("toggle takes effect on the live credential", func(t *testing.T) {
				manager := newManager(t, "codex")
				id := "oc-toggle-" + selectorCase.name
				registerOfficialClientTestAuth(t, manager, id, "codex", false)
				setOfficialClientVerifierForTest(t, func(context.Context, string, cliproxyexecutor.Options) bool { return false })

				if _, _, _, errPick := manager.pickNextMixed(ctx, []string{"codex"}, "official-client-model", cliproxyexecutor.Options{}, nil); errPick != nil {
					t.Fatalf("unrestricted pick failed: %v", errPick)
				}

				current, _ := manager.GetByID(id)
				current.Metadata[MetadataOfficialClientOnly] = true
				if _, errUpdate := manager.Update(ctx, current); errUpdate != nil {
					t.Fatalf("enable restriction: %v", errUpdate)
				}
				if _, _, _, errPick := manager.pickNextMixed(ctx, []string{"codex"}, "official-client-model", cliproxyexecutor.Options{}, nil); errPick == nil {
					t.Fatal("restricted credential was selected for a non official client")
				}

				current, _ = manager.GetByID(id)
				current.Metadata[MetadataOfficialClientOnly] = false
				if _, errUpdate := manager.Update(ctx, current); errUpdate != nil {
					t.Fatalf("disable restriction: %v", errUpdate)
				}
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
	if check.allows(&Auth{Provider: "codex", Metadata: map[string]any{MetadataOfficialClientOnly: true}}) {
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
