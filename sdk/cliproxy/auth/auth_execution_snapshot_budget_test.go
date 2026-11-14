package auth

import (
	"context"
	"strings"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestAuthPolicySnapshotAttributeBudgetBoundary(t *testing.T) {
	for _, shape := range []string{"ascii", "escaped"} {
		for _, over := range []bool{false, true} {
			name := shape + "/at-limit"
			if over {
				name = shape + "/over-limit"
			}
			t.Run(name, func(t *testing.T) {
				// The documented string charge includes four bytes per string.
				// "catalog" and its value therefore have fifteen bytes of overhead.
				remaining := (640 << 10) - 15
				value := strings.Repeat("a", remaining)
				if shape == "escaped" {
					// Each quote/backslash pair costs four charged bytes.
					value = strings.Repeat("\"\\", remaining/4) + strings.Repeat("a", remaining%4)
				}
				if over {
					value += "a"
				}
				snapshot, err := snapshotAuthForPolicy(&Auth{Attributes: map[string]string{"catalog": value}})
				if over {
					if err == nil || snapshot != nil {
						t.Fatal("policy attribute budget accepted excess data")
					}
					return
				}
				if err != nil || snapshot == nil || snapshot.Attributes["catalog"] != value {
					t.Fatal("policy attribute budget rejected its boundary")
				}
			})
		}
	}
}

func TestAuthPolicySnapshotBudgetsAreIndependent(t *testing.T) {
	for _, attributes := range []string{"empty", "large"} {
		for _, over := range []bool{false, true} {
			name := attributes + "/metadata-at-limit"
			if over {
				name = attributes + "/metadata-over-limit"
			}
			t.Run(name, func(t *testing.T) {
				// One map and string node, two string allowances, and the ten
				// bytes in "credential" cost thirty-four bytes before its value.
				length := (64 << 10) - 34
				if over {
					length++
				}
				auth := &Auth{Metadata: map[string]any{"credential": strings.Repeat("m", length)}}
				if attributes == "large" {
					auth.Attributes = map[string]string{"catalog": strings.Repeat("a", (640<<10)-15)}
				}
				snapshot, err := snapshotAuthForPolicy(auth)
				if over {
					if err == nil || snapshot != nil {
						t.Fatal("policy attributes enlarged the metadata allowance")
					}
					return
				}
				if err != nil || snapshot == nil || snapshot.Metadata["credential"] != auth.Metadata["credential"] ||
					snapshot.Attributes["catalog"] != auth.Attributes["catalog"] {
					t.Fatal("policy budgets consumed one another")
				}
			})
		}
	}
}

func TestAuthPolicySnapshotLargeAttributeOwnership(t *testing.T) {
	for _, kind := range authUsePaths {
		t.Run(string(kind), func(t *testing.T) {
			f := newAuthUseFixture(t)
			value := strings.Repeat("synthetic-catalog", 8192)
			_, committed, err := f.manager.Modify(f.ctx, f.auth.ID, 0, func(a *Auth) error {
				a.Attributes["catalog"] = value
				return nil
			})
			if err != nil || !committed {
				t.Fatal("policy large attribute setup failed")
			}
			checks := 0
			f.manager.SetAuthExecutionPolicy(authUsePolicyFunc(func(_ context.Context, use AuthUse) error {
				checks++
				if use.Auth == nil || use.Auth.ID != f.auth.ID || use.Auth.Attributes["catalog"] != value {
					t.Error("policy large attribute lost selected input")
					return authExecutionRefusal{}
				}
				use.Auth.Attributes["catalog"] = "changed-copy"
				use.Auth.Metadata["nested"].(map[string]any)["value"] = "changed-copy"
				return nil
			}))
			if err := f.invoke(kind, cliproxyexecutor.Options{}); err != nil {
				t.Fatal("policy large attribute prevented execution")
			}
			prepares, inputs := f.exec.observed()
			current, found := f.manager.GetByID(f.auth.ID)
			if !found || checks != 1 || prepares != 0 || len(inputs) != 1 ||
				inputs[0].Attributes["catalog"] != value || current.Attributes["catalog"] != value ||
				inputs[0].Metadata["nested"].(map[string]any)["value"] != "original" ||
				current.Metadata["nested"].(map[string]any)["value"] != "original" {
				t.Fatal("policy large attribute mutation escaped its snapshot")
			}
		})
	}
}

func TestAuthPolicySnapshotOversizedAttributePreventsDispatch(t *testing.T) {
	for _, kind := range authUsePaths {
		t.Run(string(kind), func(t *testing.T) {
			f := newAuthUseFixture(t)
			_, committed, err := f.manager.Modify(f.ctx, f.auth.ID, 0, func(a *Auth) error {
				a.Attributes["catalog"] = strings.Repeat("a", 640<<10)
				return nil
			})
			if err != nil || !committed {
				t.Fatal("policy oversized attribute setup failed")
			}
			checks := 0
			f.manager.SetAuthExecutionPolicy(authUsePolicyFunc(func(context.Context, AuthUse) error { checks++; return nil }))
			requireAuthUseRefusal(t, f.invoke(kind, cliproxyexecutor.Options{}))
			prepares, inputs := f.exec.observed()
			if checks != 0 || prepares != 0 || len(inputs) != 0 {
				t.Fatal("policy oversized attribute reached callback or provider")
			}
			f.requireNoHealthResult(t)
		})
	}
}
