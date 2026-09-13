package claudemaster

import (
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestBackendSelectorAcceptsMixedDispatchButPinsActualCredential(t *testing.T) {
	selector := &backendSelector{authID: "selected", provider: "claude"}
	selected := &coreauth.Auth{ID: "selected", Provider: "claude", Status: coreauth.StatusActive}
	other := &coreauth.Auth{ID: "other", Provider: "claude", Status: coreauth.StatusActive}
	wrongProvider := &coreauth.Auth{ID: "selected", Provider: "codex", Status: coreauth.StatusActive}
	for _, dispatch := range []string{"claude", "mixed"} {
		t.Run(dispatch, func(t *testing.T) {
			got, err := selector.Pick(t.Context(), dispatch, "", coreexecutor.Options{}, []*coreauth.Auth{other, wrongProvider, selected})
			if err != nil || got == nil || got.ID != selected.ID || got.Provider != selected.Provider {
				t.Fatalf("pinned account rejected: %v", err)
			}
			for _, candidates := range [][]*coreauth.Auth{nil, {other}, {wrongProvider}, {nil, other, wrongProvider}} {
				if got, err := selector.Pick(t.Context(), dispatch, "", coreexecutor.Options{}, candidates); err == nil || got != nil {
					t.Fatal("unselected account or provider was accepted")
				}
			}
		})
	}
	if got, err := selector.Pick(t.Context(), "codex", "", coreexecutor.Options{}, []*coreauth.Auth{selected}); err == nil || got != nil {
		t.Fatal("unselected dispatcher provider was accepted")
	}
}
