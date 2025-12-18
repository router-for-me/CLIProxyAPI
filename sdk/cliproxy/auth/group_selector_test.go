package auth

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/routing"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func TestGroupSelector_PickFromGroup_RoundRobin(t *testing.T) {
	gs := NewGroupSelector()
	ctx := context.Background()

	group := &routing.ProviderGroup{
		ID:                "test-group",
		Name:              "Test Group",
		AccountIDs:        []string{"a", "b", "c"},
		SelectionStrategy: routing.SelectionRoundRobin,
	}

	authsByID := map[string]*Auth{
		"a": {ID: "a", Provider: "claude"},
		"b": {ID: "b", Provider: "claude"},
		"c": {ID: "c", Provider: "claude"},
	}

	// Pick multiple times and verify round-robin behavior
	picked := make(map[string]int)
	for i := 0; i < 9; i++ {
		auth, err := gs.PickFromGroup(ctx, group, "claude-sonnet", cliproxyexecutor.Options{}, authsByID)
		if err != nil {
			t.Fatalf("PickFromGroup() error = %v", err)
		}
		picked[auth.ID]++
	}

	// Each should be picked 3 times
	for id, count := range picked {
		if count != 3 {
			t.Errorf("Account %s picked %d times, want 3", id, count)
		}
	}
}

func TestGroupSelector_PickFromGroup_Priority(t *testing.T) {
	gs := NewGroupSelector()
	ctx := context.Background()

	group := &routing.ProviderGroup{
		ID:                "test-group",
		Name:              "Test Group",
		AccountIDs:        []string{"a", "b", "c"},
		SelectionStrategy: routing.SelectionPriority,
	}

	authsByID := map[string]*Auth{
		"a": {ID: "a", Provider: "claude"},
		"b": {ID: "b", Provider: "claude"},
		"c": {ID: "c", Provider: "claude"},
	}

	// Priority should always pick the first available
	for i := 0; i < 5; i++ {
		auth, err := gs.PickFromGroup(ctx, group, "claude-sonnet", cliproxyexecutor.Options{}, authsByID)
		if err != nil {
			t.Fatalf("PickFromGroup() error = %v", err)
		}
		if auth.ID != "a" {
			t.Errorf("PickFromGroup() picked %s, want a (first priority)", auth.ID)
		}
	}
}

func TestGroupSelector_PickFromGroup_SkipsRateLimited(t *testing.T) {
	gs := NewGroupSelector()
	ctx := context.Background()

	group := &routing.ProviderGroup{
		ID:                "test-group",
		Name:              "Test Group",
		AccountIDs:        []string{"a", "b", "c"},
		SelectionStrategy: routing.SelectionRoundRobin,
	}

	authsByID := map[string]*Auth{
		"a": {ID: "a", Provider: "claude"},
		"b": {ID: "b", Provider: "claude"},
		"c": {ID: "c", Provider: "claude"},
	}

	// Mark "a" as rate limited
	gs.GetHealthTracker().MarkRateLimited("a", nil)

	picked := make(map[string]int)
	for i := 0; i < 6; i++ {
		auth, err := gs.PickFromGroup(ctx, group, "claude-sonnet", cliproxyexecutor.Options{}, authsByID)
		if err != nil {
			t.Fatalf("PickFromGroup() error = %v", err)
		}
		picked[auth.ID]++
	}

	if picked["a"] != 0 {
		t.Errorf("Rate-limited account 'a' was picked %d times, want 0", picked["a"])
	}
}

func TestGroupSelector_PickFromGroup_FallbackWhenAllLimited(t *testing.T) {
	gs := NewGroupSelector()
	ctx := context.Background()

	group := &routing.ProviderGroup{
		ID:                "test-group",
		Name:              "Test Group",
		AccountIDs:        []string{"a", "b"},
		SelectionStrategy: routing.SelectionRoundRobin,
	}

	authsByID := map[string]*Auth{
		"a": {ID: "a", Provider: "claude"},
		"b": {ID: "b", Provider: "claude"},
	}

	// Mark all as rate limited
	gs.GetHealthTracker().MarkRateLimited("a", nil)
	gs.GetHealthTracker().MarkRateLimited("b", nil)

	// Should still return something (least recently limited)
	auth, err := gs.PickFromGroup(ctx, group, "claude-sonnet", cliproxyexecutor.Options{}, authsByID)
	if err != nil {
		t.Fatalf("PickFromGroup() error = %v, should fallback", err)
	}
	if auth == nil {
		t.Error("PickFromGroup() returned nil, should fallback to least recently limited")
	}
}

func TestGroupSelector_PickFromGroup_EmptyGroup(t *testing.T) {
	gs := NewGroupSelector()
	ctx := context.Background()

	group := &routing.ProviderGroup{
		ID:                "test-group",
		Name:              "Test Group",
		AccountIDs:        []string{},
		SelectionStrategy: routing.SelectionRoundRobin,
	}

	_, err := gs.PickFromGroup(ctx, group, "model", cliproxyexecutor.Options{}, nil)
	if err == nil {
		t.Error("PickFromGroup() should return error for empty group")
	}
}

func TestGroupSelector_PickFromGroup_NilGroup(t *testing.T) {
	gs := NewGroupSelector()
	ctx := context.Background()

	_, err := gs.PickFromGroup(ctx, nil, "model", cliproxyexecutor.Options{}, nil)
	if err == nil {
		t.Error("PickFromGroup() should return error for nil group")
	}
}

func TestGroupSelector_RecordResult_Success(t *testing.T) {
	gs := NewGroupSelector()

	gs.GetHealthTracker().MarkRateLimited("test", nil)

	gs.RecordResult(Result{
		AuthID:  "test",
		Success: true,
	})

	if !gs.GetHealthTracker().IsAvailable("test") {
		t.Error("Account should be available after success")
	}
}

func TestGroupSelector_RecordResult_RateLimit(t *testing.T) {
	gs := NewGroupSelector()

	gs.RecordResult(Result{
		AuthID:  "test",
		Success: false,
		Error:   &Error{HTTPStatus: 429, Message: "rate limited"},
	})

	h := gs.GetHealthTracker().GetHealth("test")
	if h == nil || h.Status != HealthRateLimited {
		t.Error("Account should be rate limited after 429")
	}
}

func TestGroupSelector_RecordResult_ServerError(t *testing.T) {
	gs := NewGroupSelector()

	for i := 0; i < ConsecutiveFailures; i++ {
		gs.RecordResult(Result{
			AuthID:  "test",
			Success: false,
			Error:   &Error{HTTPStatus: 500, Message: "server error"},
		})
	}

	h := gs.GetHealthTracker().GetHealth("test")
	if h == nil || h.Status != HealthErroring {
		t.Errorf("Account should be erroring after %d failures", ConsecutiveFailures)
	}
}
