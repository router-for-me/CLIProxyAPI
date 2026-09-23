package store

import (
	"strings"
	"testing"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// TestUsageColumnCountsMatchLists guards the invariant that the INSERT column
// lists and the count constants cannot drift apart: a mismatch silently
// breaks every batch insert (6da813c7 fixed exactly that in
// BatchInsertEvents). DSN-free by design: it only inspects the constants.
func TestUsageColumnCountsMatchLists(t *testing.T) {
	eventCols := len(strings.Split(usageEventColumnList, ","))
	if eventCols != usageEventColumnCount {
		t.Fatalf("usageEventColumnList has %d columns, usageEventColumnCount = %d", eventCols, usageEventColumnCount)
	}
	errorCols := len(strings.Split(usageErrorColumnList, ","))
	if errorCols != usageErrorColumnCount {
		t.Fatalf("usageErrorColumnList has %d columns, usageErrorColumnCount = %d", errorCols, usageErrorColumnCount)
	}
	// Positional coupling: served_model must sit immediately after model in
	// both lists. The batch builders bind args positionally against this
	// order, so the whole served-model feature rests on it. The lists wrap
	// across lines with tabs, so compare on whitespace-normalized fields.
	for _, tc := range []struct {
		name string
		list string
	}{
		{"usageEventColumnList", usageEventColumnList},
		{"usageErrorColumnList", usageErrorColumnList},
	} {
		normalized := strings.Join(strings.Fields(tc.list), " ")
		if !strings.Contains(normalized, "model, served_model,") {
			t.Fatalf("%s: expected served_model immediately after model, got: %s", tc.name, normalized)
		}
	}
}

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

// TestUsageStore_BatchInsertEventsPersistsServedModel covers the batch path:
// BatchInsertEvents builds a multi-value INSERT with positional placeholders
// (the exact code path whose column/arg count drift 6da813c7 fixed), so
// served_model must round-trip there too, across more than one row.
func TestUsageStore_BatchInsertEventsPersistsServedModel(t *testing.T) {
	ps := newTestPostgresStore(t, "usage_served_model_batch_test")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(ps)

	events := []UsageEvent{
		{
			Provider:    "anthropic",
			Model:       "claude-opus-5",
			ServedModel: "claude-haiku-4-5",
			RequestedAt: now(),
		},
		{
			Provider:    "anthropic",
			Model:       "claude-sonnet-4-5",
			ServedModel: "claude-sonnet-4-5",
			RequestedAt: now(),
		},
	}
	if err := us.BatchInsertEvents(ctx, events); err != nil {
		t.Fatalf("BatchInsertEvents: %v", err)
	}

	want := map[string]string{
		"claude-opus-5":     "claude-haiku-4-5",
		"claude-sonnet-4-5": "claude-sonnet-4-5",
	}
	for model, wantServed := range want {
		var served string
		row := ps.db.QueryRowContext(ctx,
			"SELECT served_model FROM "+us.eventsTable+" WHERE model = $1", model)
		if err := row.Scan(&served); err != nil {
			t.Fatalf("scan served_model for %q: %v", model, err)
		}
		if served != wantServed {
			t.Fatalf("served_model for %q = %q, want %q", model, served, wantServed)
		}
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

// TestUsageStore_BatchInsertErrorsPersistsServedModel is the batch-path mirror
// for usage_errors, covering BatchInsertErrors' positional placeholder build.
func TestUsageStore_BatchInsertErrorsPersistsServedModel(t *testing.T) {
	ps := newTestPostgresStore(t, "usage_served_model_errors_batch_test")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(ps)

	failed := []UsageError{
		{
			Provider:     "anthropic",
			Model:        "claude-opus-5",
			ServedModel:  "claude-haiku-4-5",
			ErrorMessage: "stream aborted",
			RequestedAt:  now(),
		},
		{
			Provider:     "anthropic",
			Model:        "claude-sonnet-4-5",
			ServedModel:  "claude-sonnet-4-5",
			ErrorMessage: "upstream 500",
			RequestedAt:  now(),
		},
	}
	if err := us.BatchInsertErrors(ctx, failed); err != nil {
		t.Fatalf("BatchInsertErrors: %v", err)
	}

	want := map[string]string{
		"claude-opus-5":     "claude-haiku-4-5",
		"claude-sonnet-4-5": "claude-sonnet-4-5",
	}
	for model, wantServed := range want {
		var served string
		row := ps.db.QueryRowContext(ctx,
			"SELECT served_model FROM "+us.errorsTable+" WHERE model = $1", model)
		if err := row.Scan(&served); err != nil {
			t.Fatalf("scan served_model for %q: %v", model, err)
		}
		if served != wantServed {
			t.Fatalf("served_model for %q = %q, want %q", model, served, wantServed)
		}
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
