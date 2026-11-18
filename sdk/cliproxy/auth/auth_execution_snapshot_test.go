package auth

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type authPolicyTokenStorage struct{}

func (*authPolicyTokenStorage) SaveTokenToFile(string) error {
	return errors.New("fixture token storage must not be called")
}

func TestAuthExecutionPolicyNestedSnapshotOwnership(t *testing.T) {
	for _, kind := range authUsePaths {
		t.Run(string(kind), func(t *testing.T) {
			f := newAuthUseFixture(t)
			_, _, err := f.manager.Modify(f.ctx, f.auth.ID, 0, func(a *Auth) error {
				a.Metadata["array"] = []any{map[string]any{"value": "original"}}
				a.Runtime = map[string]string{"value": "runtime"}
				a.Storage = &authPolicyTokenStorage{}
				return nil
			})
			if err != nil {
				t.Fatal("nested snapshot fixture failed")
			}
			checks := 0
			f.manager.SetAuthExecutionPolicy(authUsePolicyFunc(func(_ context.Context, use AuthUse) error {
				checks++
				if use.Auth == nil || use.Auth.Runtime != nil || use.Auth.Storage != nil || use.Auth.CodexRefreshPatch != nil {
					t.Error("policy exposed an opaque shared object")
					return nil
				}
				use.Auth.Metadata["array"].([]any)[0].(map[string]any)["value"] = "mutated"
				return nil
			}))
			if f.invoke(kind, cliproxyexecutor.Options{}) != nil || checks != 1 {
				t.Fatal("nested snapshot policy was not checked")
			}
			_, inputs := f.exec.observed()
			current, ok := f.manager.GetByID(f.auth.ID)
			if !ok || len(inputs) != 1 || current.Metadata["array"].([]any)[0].(map[string]any)["value"] != "original" ||
				inputs[0].Metadata["array"].([]any)[0].(map[string]any)["value"] != "original" || inputs[0].Runtime == nil || inputs[0].Storage == nil {
				t.Fatal("nested policy mutation escaped or removed executor runtime")
			}
		})
	}
}

func TestAuthPolicySnapshotBoundedJSON(t *testing.T) {
	for _, shape := range []string{"valid", "unsupported", "cycle", "oversized"} {
		t.Run(shape, func(t *testing.T) {
			a := &Auth{ID: "snapshot-fixture", Attributes: map[string]string{"source": "fixture"},
				Metadata: map[string]any{"array": []any{map[string]any{"value": "original"}}},
				Runtime:  map[string]string{"opaque": "fixture"}, CodexRefreshPatch: &codex.CredentialRefreshPatch{},
				Storage: &authPolicyTokenStorage{},
			}
			switch shape {
			case "unsupported":
				a.Metadata["unsupported"] = make(chan struct{})
			case "cycle":
				a.Metadata["self"] = a.Metadata
			case "oversized":
				a.Metadata["large"] = strings.Repeat("x", 64*1024+1)
			}
			// Cyclic input belongs to this helper contract, not Manager's Auth
			// registration contract, which uses the stock Auth.Clone.
			snapshot, err := snapshotAuthForPolicy(a)
			if shape != "valid" {
				if err == nil || snapshot != nil {
					t.Fatal("invalid policy snapshot was not refused")
				}
				return
			}
			if err != nil || snapshot == nil || snapshot.Runtime != nil || snapshot.Storage != nil || snapshot.CodexRefreshPatch != nil {
				t.Fatal("valid policy snapshot failed or exposed opaque state")
			}
			snapshot.Attributes["source"] = "mutated"
			snapshot.Metadata["array"].([]any)[0].(map[string]any)["value"] = "mutated"
			if a.Attributes["source"] != "fixture" || a.Metadata["array"].([]any)[0].(map[string]any)["value"] != "original" {
				t.Fatal("policy snapshot is not detached")
			}
		})
	}
}

func TestAuthTransitionSnapshotOwnership(t *testing.T) {
	f := newAuthUseFixture(t)
	_, _, err := f.manager.Modify(f.ctx, f.auth.ID, 0, func(a *Auth) error {
		a.Metadata["array"] = []any{map[string]any{"value": "original"}}
		a.Runtime = map[string]string{"opaque": "fixture"}
		a.Storage = &authPolicyTokenStorage{}
		return nil
	})
	if err != nil {
		t.Fatal("transition snapshot fixture failed")
	}
	e := &authTransitionExecutor{authUseExecutor: f.exec, refresh: rotatedTransitionAuth}
	f.manager.RegisterExecutor(e)
	events := 0
	f.manager.SetAuthExecutionPolicy(&authTransitionPolicy{observe: func(_ context.Context, event AuthTransition) {
		events++
		for _, snapshot := range []*Auth{event.Before, event.CommitBase, event.Merged, event.Committed} {
			if snapshot == nil || snapshot.Runtime != nil || snapshot.Storage != nil || snapshot.CodexRefreshPatch != nil {
				t.Error("transition exposed missing or opaque snapshot")
				continue
			}
			if snapshot.Metadata["array"].([]any)[0].(map[string]any)["value"] != "original" {
				t.Error("transition snapshots share mutable children")
			}
			snapshot.Metadata["array"].([]any)[0].(map[string]any)["value"] = "mutated"
		}
	}})
	if _, err := f.manager.ForceRefreshAuth(f.ctx, f.auth.ID); err != nil || events != 1 {
		t.Fatal("trusted transition snapshot was not observed")
	}
	current, ok := f.manager.GetByID(f.auth.ID)
	if !ok || current.Metadata["array"].([]any)[0].(map[string]any)["value"] != "original" || current.Runtime == nil || current.Storage == nil {
		t.Fatal("transition snapshot mutation changed current credential")
	}
}
