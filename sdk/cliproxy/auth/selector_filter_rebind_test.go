package auth

import (
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type excludingPolicy struct {
	FillFirstSelector
	excluded string
}

func (p *excludingPolicy) FilterCandidates(_ context.Context, _, _ string, _ cliproxyexecutor.Options, auths []*Auth) ([]*Auth, error) {
	// Compact in place, as a filter is allowed to.
	result := auths[:0]
	for _, a := range auths {
		if a.ID != p.excluded {
			result = append(result, a)
		}
	}
	return result, nil
}

func TestAffinityRebindsFilteredAccountUnderQuotaOnlyRebind(t *testing.T) {
	policy := &excludingPolicy{}
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: policy, RebindOn: SessionAffinityRebindOnQuota})
	defer selector.Stop()
	// The manager marks both accounts as only transiently unavailable before the filter runs.
	ctx := selectorContextForAvailableAuths(context.Background(), selector, "model")
	ctx = withSessionAffinityTransientAuthIDs(ctx, map[string]struct{}{"a": {}, "z": {}})
	opts := cliproxyexecutor.Options{Headers: map[string][]string{"Session_id": {"filter-rebind-session"}}}
	pick := func() string {
		t.Helper()
		selected, err := selector.Pick(ctx, "codex", "model", opts, []*Auth{{ID: "a"}, {ID: "z"}})
		if err != nil || selected == nil {
			t.Fatalf("pick: %v %v", selected, err)
		}
		return selected.ID
	}
	if got := pick(); got != "a" {
		t.Fatalf("initial pick = %s, want a", got)
	}
	policy.excluded = "a"
	if got := pick(); got != "z" {
		t.Fatalf("excluded account reused: %s", got)
	}
	policy.excluded = ""
	if got := pick(); got != "z" {
		t.Fatalf("session returned to filtered account %s instead of keeping the rebinding to z", got)
	}
}
