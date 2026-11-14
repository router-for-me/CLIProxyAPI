package auth

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func authPolicyConfiguredRules() []config.RequestScopedErrorRule {
	return []config.RequestScopedErrorRule{{
		Status: 400, Action: "stop",
		Match: []string{"fixture-match"}, MatchRegexr: []string{"fixture-regex"},
	}}
}

func setAuthPolicyConfiguredRules(t *testing.T, f *authUseFixture) {
	t.Helper()
	_, changed, err := f.manager.Modify(f.ctx, f.auth.ID, 0, func(a *Auth) error {
		// ConfigSynthesizer stores this concrete type in credential metadata.
		a.Metadata["request_scoped_errors"] = authPolicyConfiguredRules()
		return nil
	})
	if err != nil || !changed {
		t.Fatal("configured rule fixture failed")
	}
}

func mutateAuthPolicyConfiguredRules(t *testing.T, a *Auth) {
	t.Helper()
	if a == nil {
		t.Error("configured rule snapshot missing")
		return
	}
	rules, ok := a.Metadata["request_scoped_errors"].([]config.RequestScopedErrorRule)
	if !ok || !reflect.DeepEqual(rules, authPolicyConfiguredRules()) {
		t.Error("configured rule snapshot changed its type or fields")
		return
	}
	rules[0].Status = 500
	rules[0].Action = "continue"
	rules[0].Match[0] = "changed-match"
	rules[0].MatchRegexr[0] = "changed-regex"
}

func TestAuthExecutionPolicyConfiguredErrorRules(t *testing.T) {
	for _, policy := range []string{"nil", "allow"} {
		for _, kind := range authUsePaths {
			t.Run(policy+"/"+string(kind), func(t *testing.T) {
				f := newAuthUseFixture(t)
				setAuthPolicyConfiguredRules(t, f)
				checks := 0
				if policy == "allow" {
					f.manager.SetAuthExecutionPolicy(authUsePolicyFunc(func(_ context.Context, use AuthUse) error {
						checks++
						mutateAuthPolicyConfiguredRules(t, use.Auth)
						return nil
					}))
				}
				if err := f.invoke(kind, cliproxyexecutor.Options{}); err != nil {
					t.Fatal("configured rules prevented execution")
				}
				wantChecks := 0
				if policy == "allow" {
					wantChecks = 1
				}
				prepares, inputs := f.exec.observed()
				current, found := f.manager.GetByID(f.auth.ID)
				if !found || checks != wantChecks || prepares != 0 || len(inputs) != 1 {
					t.Fatal("configured rule dispatch count changed")
				}
				for _, a := range []*Auth{current, inputs[0]} {
					if !reflect.DeepEqual(a.Metadata["request_scoped_errors"], authPolicyConfiguredRules()) {
						t.Fatal("configured rule mutation escaped its snapshot")
					}
				}
			})
		}
	}
}

func TestAuthPolicySnapshotConfiguredErrorRuleCollections(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rules []config.RequestScopedErrorRule
	}{
		{"nil_outer", nil},
		{"empty_outer", []config.RequestScopedErrorRule{}},
		{"nil_nested", []config.RequestScopedErrorRule{{Status: 400, Action: "stop"}}},
		{"empty_nested", []config.RequestScopedErrorRule{{Status: 400, Action: "stop", Match: []string{}, MatchRegexr: []string{}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Auth{Metadata: map[string]any{"request_scoped_errors": tc.rules}}
			snapshot, err := snapshotAuthForPolicy(a)
			if err != nil || snapshot == nil || !reflect.DeepEqual(snapshot.Metadata["request_scoped_errors"], tc.rules) {
				t.Fatal("configured rule collection shape changed")
			}
		})
	}
}

func TestAuthPolicySnapshotConfiguredErrorRuleLimits(t *testing.T) {
	for _, shape := range []string{"action", "match", "regex", "many_rules", "depth"} {
		t.Run(shape, func(t *testing.T) {
			rules := authPolicyConfiguredRules()
			switch shape {
			case "action":
				rules[0].Action = strings.Repeat("x", policyMetadataBudget)
			case "match":
				rules[0].Match = []string{strings.Repeat("x", policyMetadataBudget)}
			case "regex":
				rules[0].MatchRegexr = []string{strings.Repeat("x", policyMetadataBudget)}
			case "many_rules":
				rules = make([]config.RequestScopedErrorRule, policyMetadataBudget)
			}
			var value any = rules
			if shape == "depth" {
				for range 63 {
					value = map[string]any{"child": value}
				}
			}
			snapshot, err := snapshotAuthForPolicy(&Auth{Metadata: map[string]any{"rules": value}})
			if err == nil || snapshot != nil {
				t.Fatal("configured rule limit was not refused")
			}
		})
	}
}

func TestAuthTransitionConfiguredErrorRules(t *testing.T) {
	f := newAuthUseFixture(t)
	setAuthPolicyConfiguredRules(t, f)
	e := &authTransitionExecutor{authUseExecutor: f.exec, refresh: rotatedTransitionAuth}
	f.manager.RegisterExecutor(e)
	events := 0
	f.manager.SetAuthExecutionPolicy(&authTransitionPolicy{observe: func(_ context.Context, event AuthTransition) {
		events++
		for _, snapshot := range []*Auth{event.Before, event.CommitBase, event.Merged, event.Committed} {
			mutateAuthPolicyConfiguredRules(t, snapshot)
		}
	}})
	if _, err := f.manager.ForceRefreshAuth(f.ctx, f.auth.ID); err != nil || events != 1 {
		t.Fatal("configured rules blocked trusted transition")
	}
	current, found := f.manager.GetByID(f.auth.ID)
	if !found || !reflect.DeepEqual(current.Metadata["request_scoped_errors"], authPolicyConfiguredRules()) {
		t.Fatal("configured rule transition mutation escaped its snapshot")
	}
}
