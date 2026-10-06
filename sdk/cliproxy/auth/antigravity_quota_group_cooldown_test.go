package auth

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// antigravityGroupQuotaResult builds the result an Antigravity executor produces for an
// exhausted quota window that upstream attributes to a quota group.
func antigravityGroupQuotaResult(authID, model, group string, resetAfter time.Duration) Result {
	return Result{
		AuthID:     authID,
		Provider:   "antigravity",
		Model:      model,
		RouteModel: model,
		Success:    false,
		Error:      &Error{HTTPStatus: 429, Message: "Individual quota reached"},
		RetryAfter: &resetAfter,
		QuotaGroup: group,
	}
}

// antigravityGroupAuth registers an Antigravity credential that serves all three test
// models, so the group cooldown also covers a model that has no ModelState yet.
func antigravityGroupAuth(t *testing.T, manager *Manager, authID string) {
	t.Helper()
	registry.GetGlobalRegistry().RegisterClient(authID, "antigravity", []*registry.ModelInfo{
		{ID: "gemini-3.8-flash-high"},
		{ID: "gemini-3.1-pro-high"},
		{ID: "claude-opus-5-5-high"},
	})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{
		ID: authID, Provider: "antigravity", Status: StatusActive,
	}); errRegister != nil {
		t.Fatalf("Register() returned error: %v", errRegister)
	}
}

func TestMarkResult_QuotaExhaustedCoolsWholeAntigravityQuotaGroup(t *testing.T) {
	const (
		authID       = "antigravity-group-1"
		failingModel = "gemini-3.8-flash-high"
		siblingModel = "gemini-3.1-pro-high"
		otherGroup   = "claude-opus-5-5-high"
	)
	resetAfter := 69*time.Hour + 2*time.Minute + 37*time.Second

	manager := NewManager(nil, nil, nil)
	antigravityGroupAuth(t, manager, authID)

	before := time.Now()
	manager.MarkResult(context.Background(), antigravityGroupQuotaResult(
		authID, failingModel, AntigravityQuotaGroup(failingModel), resetAfter))

	stored, ok := manager.GetByID(authID)
	if !ok || stored == nil {
		t.Fatal("auth was not found after MarkResult")
	}
	sibling := stored.ModelStates[siblingModel]
	if sibling == nil {
		t.Fatalf("ModelStates[%q] = nil, want a group-wide cooldown", siblingModel)
	}
	if !sibling.Unavailable || !sibling.Quota.Exceeded {
		t.Fatalf("ModelStates[%q] = %+v, want an unavailable quota cooldown", siblingModel, sibling)
	}
	wantRecover := before.Add(resetAfter)
	if delta := sibling.Quota.NextRecoverAt.Sub(wantRecover); delta < -time.Minute || delta > time.Minute {
		t.Fatalf("ModelStates[%q].Quota.NextRecoverAt = %v, want about %v", siblingModel, sibling.Quota.NextRecoverAt, wantRecover)
	}
	if other := stored.ModelStates[otherGroup]; other != nil {
		t.Fatalf("ModelStates[%q] = %+v, want the sibling quota group untouched", otherGroup, other)
	}

	// Selection: the exhausted group is skipped, the other group stays selectable.
	cooling := stored.Clone()
	healthy := &Auth{ID: "antigravity-group-healthy", Provider: "antigravity", Status: StatusActive}
	selector := &RoundRobinSelector{}
	selected, errPick := selector.Pick(context.Background(), "antigravity", siblingModel, cliproxyexecutor.Options{}, []*Auth{cooling, healthy})
	if errPick != nil {
		t.Fatalf("Pick(%q) error = %v, want the healthy credential", siblingModel, errPick)
	}
	if selected.ID != healthy.ID {
		t.Fatalf("Pick(%q) = %q, want %q", siblingModel, selected.ID, healthy.ID)
	}
	selectedOther, errPickOther := selector.Pick(context.Background(), "antigravity", otherGroup, cliproxyexecutor.Options{}, []*Auth{cooling, healthy})
	if errPickOther != nil {
		t.Fatalf("Pick(%q) error = %v, want a credential from the other quota group", otherGroup, errPickOther)
	}
	if selectedOther == nil {
		t.Fatalf("Pick(%q) returned no credential", otherGroup)
	}
}

// TestMarkResult_QuotaExhaustedUnknownGroupCoolsOnlyThatModel pins the fallback: a model
// name matching no known Antigravity group keeps the pre-group per-model behavior.
func TestMarkResult_QuotaExhaustedUnknownGroupCoolsOnlyThatModel(t *testing.T) {
	const (
		authID       = "antigravity-group-unknown"
		failingModel = "mystery-model-1"
		siblingModel = "gemini-3.1-pro-high"
	)
	resetAfter := 48 * time.Hour

	manager := NewManager(nil, nil, nil)
	antigravityGroupAuth(t, manager, authID)

	manager.MarkResult(context.Background(), antigravityGroupQuotaResult(authID, failingModel, "", resetAfter))

	stored, ok := manager.GetByID(authID)
	if !ok || stored == nil {
		t.Fatal("auth was not found after MarkResult")
	}
	if state := stored.ModelStates[failingModel]; state == nil || !state.Quota.Exceeded {
		t.Fatalf("ModelStates[%q] = %+v, want the failing model cooled", failingModel, state)
	}
	if other := stored.ModelStates[siblingModel]; other != nil {
		t.Fatalf("ModelStates[%q] = %+v, want no cooldown when the group is unknown", siblingModel, other)
	}
}

func TestAntigravityQuotaGroup(t *testing.T) {
	cases := map[string]string{
		"gemini-3.8-flash-high": AntigravityQuotaGroupGemini,
		"gemini-3.1-pro-high":   AntigravityQuotaGroupGemini,
		"claude-opus-5-5-high":  AntigravityQuotaGroupClaudeGPT,
		"claude-sonnet-4-6":     AntigravityQuotaGroupClaudeGPT,
		"gpt-oss-120b-high":     AntigravityQuotaGroupClaudeGPT,
		"gemini-3-pro(high)":    AntigravityQuotaGroupGemini,
		"some-other-model":      "",
		"":                      "",
	}
	for model, want := range cases {
		if got := AntigravityQuotaGroup(model); got != want {
			t.Errorf("AntigravityQuotaGroup(%q) = %q, want %q", model, got, want)
		}
	}
}
