package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestManagerNonWeightedPinnedSelection(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		for _, affinity := range []bool{false, true} {
			for _, strategy := range []string{"round-robin", "fill-first"} {
				t.Run(fmt.Sprintf("mixed=%t/affinity=%t/%s", mixed, affinity, strategy), func(t *testing.T) {
					var selector Selector = &RoundRobinSelector{}
					if strategy == "fill-first" {
						selector = &FillFirstSelector{}
					}
					if affinity {
						sticky := NewSessionAffinitySelector(selector)
						t.Cleanup(sticky.Stop)
						selector = sticky
					}
					manager := NewManager(nil, selector, nil)
					const provider, model = "pinned-non-weighted", "pinned-model"
					manager.RegisterExecutor(schedulerTestExecutor{provider: provider})
					pinnedID := t.Name() + "-pinned"
					for i := range 32 {
						auth := &Auth{
							ID: fmt.Sprintf("%s-other-%02d", t.Name(), i), Provider: provider, Status: StatusActive,
							Attributes: map[string]string{AttributeAuthKind: AuthKindAPIKey, "priority": "10"},
						}
						if i == 0 {
							auth.ID = pinnedID
							auth.Attributes["priority"] = "0"
						}
						registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
						t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
						if _, errRegister := manager.Register(WithSkipPersist(t.Context()), auth); errRegister != nil {
							t.Fatal(errRegister)
						}
					}
					// Normalize the legacy selection signatures for the same pinning checks.
					pick := func(ctx context.Context, id string, tried map[string]struct{}) (*Auth, error) {
						opts := cliproxyexecutor.Options{
							Headers:  http.Header{"Session_id": {"non-weighted-pinned"}},
							Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: id},
						}
						if mixed {
							auth, _, _, errPick := manager.pickNextMixedLegacy(ctx, []string{provider}, model, opts, tried)
							return auth, errPick
						}
						auth, _, errPick := manager.pickNextLegacy(ctx, provider, model, opts, tried)
						return auth, errPick
					}
					ctx := withRequiredAuthKind(t.Context(), AuthKindAPIKey)
					for range 3 {
						if auth, errPick := pick(ctx, pinnedID, nil); errPick != nil || auth == nil || auth.ID != pinnedID {
							t.Fatalf("lower-priority pin not enforced: auth=%v err=%v", auth, errPick)
						}
					}
					for _, tc := range []struct {
						name  string
						ctx   context.Context
						id    string
						tried map[string]struct{}
					}{
						{name: "missing pin", ctx: ctx, id: "missing"},
						{name: "already tried", ctx: ctx, id: pinnedID, tried: map[string]struct{}{pinnedID: {}}},
						{name: "ineligible kind", ctx: withRequiredAuthKind(t.Context(), AuthKindOAuth), id: pinnedID},
					} {
						auth, errPick := pick(tc.ctx, tc.id, tc.tried)
						var authErr *Error
						if auth != nil || !errors.As(errPick, &authErr) || authErr.Code != "auth_not_found" {
							t.Fatalf("%s: auth=%v err=%v, want auth_not_found", tc.name, auth, errPick)
						}
					}
					if sticky, ok := selector.(*SessionAffinitySelector); ok {
						namespace := provider
						if mixed {
							namespace = "mixed"
						}
						id := CanonicalSessionID(http.Header{"Session_id": {"non-weighted-pinned"}}, nil, nil)
						if binding, found := sticky.cache.Get(namespace + "::" + id + "::" + model); !found || binding != pinnedID {
							t.Fatalf("pinning checks changed the sticky binding: %q, found=%t", binding, found)
						}
					}
				})
			}
		}
	}
}
