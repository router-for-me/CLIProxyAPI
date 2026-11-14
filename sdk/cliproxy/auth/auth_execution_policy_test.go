package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestAuthExecutionPolicyFirstDispatch(t *testing.T) {
	for _, kind := range authUsePaths {
		for _, behavior := range []string{"nil", "allow", "refuse", "mutate-snapshot"} {
			t.Run(string(kind)+"/"+behavior, func(t *testing.T) {
				f := newAuthUseFixture(t)
				if behavior == "refuse" {
					other := f.auth.Clone()
					other.ID += "-alternate"
					other.Metadata["access_token"] = "synthetic-alternate"
					if _, err := f.manager.Register(f.ctx, other); err != nil {
						t.Fatal("alternative fixture registration failed")
					}
					registry.GetGlobalRegistry().RegisterClient(other.ID, other.Provider, []*registry.ModelInfo{{ID: f.model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(other.ID) })
				}
				checks := 0
				if behavior != "nil" {
					f.manager.SetAuthExecutionPolicy(authUsePolicyFunc(func(ctx context.Context, use AuthUse) error {
						checks++
						if ctx.Value(authUseContextKey{}) != "fixture-context" || use.Kind != kind || use.Auth == nil ||
							use.Model != f.model || use.RequestedModel != "public-fixture-model" {
							t.Error("policy received incorrect execution context")
						}
						if behavior == "refuse" {
							return errors.New("private fixture error must not escape")
						}
						if use.Auth == nil || authAccessToken(use.Auth) != "synthetic-a" {
							t.Error("policy did not receive actual execution credential")
							return errors.New("fixture credential mismatch")
						}
						if behavior == "mutate-snapshot" {
							use.Auth.ID = "mutated-snapshot"
							use.Auth.Attributes["source"] = "mutated"
							use.Auth.Metadata["access_token"] = "synthetic-mutated"
							use.Auth.Metadata["nested"].(map[string]any)["value"] = "mutated"
						}
						return nil
					}))
				}
				err := f.invoke(kind, cliproxyexecutor.Options{})
				prepares, inputs := f.exec.observed()
				if behavior == "refuse" {
					requireAuthUseRefusal(t, err)
					if prepares != 0 || len(inputs) != 0 || checks != 1 {
						t.Fatal("policy refusal dispatched or selected another credential")
					}
					f.requireNoHealthResult(t)
					return
				}
				if err != nil || prepares != 0 || len(inputs) != 1 {
					t.Fatal("permitted fixture execution failed")
				}
				if behavior != "nil" && checks != 1 {
					t.Fatal("first provider dispatch was not checked once")
				}
				current, ok := f.manager.GetByID(f.auth.ID)
				if !ok || inputs[0].ID != f.auth.ID || authAccessToken(inputs[0]) != "synthetic-a" ||
					inputs[0].Attributes["source"] != "fixture" || inputs[0].Metadata["nested"].(map[string]any)["value"] != "original" ||
					authAccessToken(current) != "synthetic-a" || current.Metadata["nested"].(map[string]any)["value"] != "original" {
					t.Fatal("policy snapshot mutation escaped into execution or manager")
				}
			})
		}
	}
}

func TestAuthExecutionPolicyPreparationInput(t *testing.T) {
	for _, kind := range authUsePaths {
		for _, replace := range []bool{false, true} {
			name := "unchanged"
			if replace {
				name = "replaced-after-selection"
			}
			t.Run(string(kind)+"/"+name, func(t *testing.T) {
				f := newAuthUseFixture(t)
				f.exec.prepare = true
				// This public preparer callback runs after selection, before Manager
				// rereads the current credential for the actual preparation call.
				if replace {
					f.exec.onShould = func() { f.replace(t, "synthetic-b") }
				}
				var seen []AuthUseKind
				f.manager.SetAuthExecutionPolicy(authUsePolicyFunc(func(_ context.Context, use AuthUse) error {
					seen = append(seen, use.Kind)
					if use.Auth == nil || authAccessToken(use.Auth) != "synthetic-a" {
						return errors.New("fixture identity mismatch")
					}
					return nil
				}))
				err := f.invoke(kind, cliproxyexecutor.Options{})
				prepares, inputs := f.exec.observed()
				if replace {
					requireAuthUseRefusal(t, err)
					if prepares != 0 || len(inputs) != 0 || len(seen) != 1 || seen[0] != AuthUsePrepare {
						t.Fatal("replacement reached provider preparation")
					}
					f.requireNoHealthResult(t)
					return
				}
				if err != nil || prepares != 1 || len(inputs) != 1 || len(seen) != 2 || seen[0] != AuthUsePrepare || seen[1] != kind {
					t.Fatal("unchanged preparation and dispatch were not checked")
				}
			})
		}
	}
}

func TestAuthExecutionPolicyPreparedResult(t *testing.T) {
	for _, kind := range authUsePaths {
		for _, changed := range []bool{false, true} {
			name := "unchanged"
			if changed {
				name = "unapproved-prepared-credential"
			}
			t.Run(string(kind)+"/"+name, func(t *testing.T) {
				f := newAuthUseFixture(t)
				f.exec.prepare = true
				if changed {
					f.exec.prepared = func(a *Auth) *Auth {
						a.Metadata["access_token"] = "synthetic-b"
						return a
					}
				}
				checks := 0
				f.manager.SetAuthExecutionPolicy(authUsePolicyFunc(func(_ context.Context, use AuthUse) error {
					checks++
					if use.Auth == nil || authAccessToken(use.Auth) != "synthetic-a" {
						return errors.New("fixture identity mismatch")
					}
					return nil
				}))
				err := f.invoke(kind, cliproxyexecutor.Options{})
				prepares, inputs := f.exec.observed()
				if prepares != 1 || checks != 2 {
					t.Fatal("prepared actual credential was not checked")
				}
				if changed {
					requireAuthUseRefusal(t, err)
					if len(inputs) != 0 {
						t.Fatal("unapproved preparation result reached execution")
					}
					f.requireNoHealthResult(t)
				} else if err != nil || len(inputs) != 1 {
					t.Fatal("unchanged preparation result was refused")
				}
			})
		}
	}
}

func TestAuthExecutionPolicyActualSelectedSnapshot(t *testing.T) {
	for _, kind := range authUsePaths {
		t.Run(string(kind), func(t *testing.T) {
			f := newAuthUseFixture(t)
			checks := 0
			f.manager.SetAuthExecutionPolicy(authUsePolicyFunc(func(_ context.Context, use AuthUse) error {
				checks++
				if use.Auth == nil || authAccessToken(use.Auth) != "synthetic-a" {
					return errors.New("fixture selected snapshot changed")
				}
				return nil
			}))
			intercepts := 0
			err := f.invoke(kind, cliproxyexecutor.Options{RequestAfterAuthInterceptor: func(context.Context, cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
				intercepts++
				f.replace(t, "synthetic-b")
				return cliproxyexecutor.RequestAfterAuthInterceptResponse{}
			}})
			_, inputs := f.exec.observed()
			current, ok := f.manager.GetByID(f.auth.ID)
			if err != nil || !ok || checks != 1 || intercepts != 1 || len(inputs) != 1 ||
				authAccessToken(inputs[0]) != "synthetic-a" || authAccessToken(current) != "synthetic-b" {
				t.Fatal("policy checked manager replacement instead of actual executor input")
			}
		})
	}
}

func TestAuthExecutionPolicyDirectPreparation(t *testing.T) {
	for _, anonymous := range []bool{false, true} {
		for _, refuse := range []bool{false, true} {
			name := "registered"
			if anonymous {
				name = "anonymous"
			}
			if refuse {
				name += "/refuse"
			} else {
				name += "/allow"
			}
			t.Run(name, func(t *testing.T) {
				f := newAuthUseFixture(t)
				f.exec.prepare = true
				a := f.auth.Clone()
				if anonymous {
					a.ID = ""
				}
				checks := 0
				f.manager.SetAuthExecutionPolicy(authUsePolicyFunc(func(ctx context.Context, use AuthUse) error {
					checks++
					if use.Kind != AuthUsePrepare || use.Auth == nil || use.Auth.ID != a.ID || ctx.Value(authUseContextKey{}) != "fixture-context" {
						t.Error("direct preparation check lost actual input or context")
					}
					if refuse {
						return errors.New("private preparation refusal")
					}
					return nil
				}))
				_, err := f.manager.PrepareRequestAuth(f.ctx, f.exec, a)
				prepares, inputs := f.exec.observed()
				if checks != 1 || len(inputs) != 0 {
					t.Fatal("direct preparation did not enforce policy")
				}
				if refuse {
					requireAuthUseRefusal(t, err)
					if prepares != 0 {
						t.Fatal("refused direct preparation reached provider")
					}
				} else if err != nil || prepares != 1 {
					t.Fatal("allowed direct preparation failed")
				}
			})
		}
	}
}
