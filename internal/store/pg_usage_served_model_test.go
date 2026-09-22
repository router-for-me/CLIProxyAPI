package store

import (
	"testing"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// TestUsageStore_InsertEventPersistsServedModel verifies that the
// usage_events.served_model column is written by InsertEvent. Reads of the
// column via the row-select path are added later; this test queries the
// column directly.
func TestUsageStore_InsertEventPersistsServedModel(t *testing.T) {
	ps := newTestPostgresStore(t, "usage_served_model_test")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(ps)

	event := UsageEvent{
		Provider:    "anthropic",
		Model:       "claude-opus-5",
		ServedModel: "claude-haiku-4-5",
		RequestedAt: now(),
	}
	if err := us.InsertEvent(ctx, event); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}

	var served string
	row := ps.db.QueryRowContext(ctx,
		"SELECT served_model FROM "+us.eventsTable+" WHERE model = $1", "claude-opus-5")
	if err := row.Scan(&served); err != nil {
		t.Fatalf("scan served_model: %v", err)
	}
	if served != "claude-haiku-4-5" {
		t.Fatalf("served_model = %q, want %q", served, "claude-haiku-4-5")
	}
}

// TestUsageStore_InsertErrorPersistsServedModel is the usage_errors mirror:
// both tables must persist the upstream-served model.
func TestUsageStore_InsertErrorPersistsServedModel(t *testing.T) {
	ps := newTestPostgresStore(t, "usage_served_model_errors_test")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(ps)

	failed := UsageError{
		Provider:     "anthropic",
		Model:        "claude-opus-5",
		ServedModel:  "claude-haiku-4-5",
		ErrorMessage: "stream aborted",
		RequestedAt:  now(),
	}
	if err := us.InsertError(ctx, failed); err != nil {
		t.Fatalf("InsertError: %v", err)
	}

	var served string
	row := ps.db.QueryRowContext(ctx,
		"SELECT served_model FROM "+us.errorsTable+" WHERE model = $1", "claude-opus-5")
	if err := row.Scan(&served); err != nil {
		t.Fatalf("scan served_model: %v", err)
	}
	if served != "claude-haiku-4-5" {
		t.Fatalf("served_model = %q, want %q", served, "claude-haiku-4-5")
	}
}

// TestFlusherSubstitutionDetection documents the decision point the flusher
// uses to warn about silent upstream substitutions: empty served values must
// not raise a false alarm, and a differing served value must.
func TestFlusherSubstitutionDetection(t *testing.T) {
	if !coreusage.DetectSubstitution("claude-haiku-4-5", "claude-opus-5").Substituted {
		t.Fatalf("DetectSubstitution(haiku, opus).Substituted = false, want true")
	}
	if coreusage.DetectSubstitution("", "claude-opus-5").Substituted {
		t.Fatalf("DetectSubstitution(empty, opus).Substituted = true, want false")
	}
}
