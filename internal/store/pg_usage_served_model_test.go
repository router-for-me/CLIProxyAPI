package store

import (
	"strings"
	"testing"
	"time"

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
	// Positional coupling: served_model must sit right after model's entry
	// attribution (entry_provider_key) in both lists. The batch builders bind
	// args positionally against this order, so the whole served-model feature
	// rests on it. The lists wrap across lines with tabs, so compare on
	// whitespace-normalized fields.
	for _, tc := range []struct {
		name string
		list string
	}{
		{"usageEventColumnList", usageEventColumnList},
		{"usageErrorColumnList", usageErrorColumnList},
	} {
		normalized := strings.Join(strings.Fields(tc.list), " ")
		if !strings.Contains(normalized, "model, entry_provider_key, served_model,") {
			t.Fatalf("%s: expected served_model right after model/entry_provider_key, got: %s", tc.name, normalized)
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

// TestUsageStore_ListSubstitutions covers the alert detector's aggregate query:
// only rows where a non-empty served_model differs (case-insensitively) from
// the requested model count, and they group per provider/model/served triple
// with a descending-count order. An equal served model and an empty served
// model must be excluded, and a case-only difference must NOT count as a
// substitution — matching DetectSubstitution's EqualFold semantics upstream.
func TestUsageStore_ListSubstitutions(t *testing.T) {
	ps := newTestPostgresStore(t, "usage_substitutions_test")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(ps)

	events := []UsageEvent{
		// Two substituted rows for the same triple → one aggregate, Count=2.
		{Provider: "anthropic", Model: "claude-opus-5", ServedModel: "claude-haiku-4-5", RequestedAt: now()},
		{Provider: "anthropic", Model: "claude-opus-5", ServedModel: "claude-haiku-4-5", RequestedAt: now()},
		// Loud triple (3 rows) must sort before the 2-row triple.
		{Provider: "google", Model: "gemini-3-pro", ServedModel: "gemini-3-flash", RequestedAt: now()},
		{Provider: "google", Model: "gemini-3-pro", ServedModel: "gemini-3-flash", RequestedAt: now()},
		{Provider: "google", Model: "gemini-3-pro", ServedModel: "gemini-3-flash", RequestedAt: now()},
		// Not substitutions: equal model, empty served, and a case-only
		// difference (EqualFold semantics → treated as the same model).
		{Provider: "anthropic", Model: "claude-opus-5", ServedModel: "claude-opus-5", RequestedAt: now()},
		{Provider: "anthropic", Model: "claude-opus-5", ServedModel: "", RequestedAt: now()},
		{Provider: "anthropic", Model: "Claude-Haiku", ServedModel: "claude-haiku", RequestedAt: now()},
	}
	if err := us.BatchInsertEvents(ctx, events); err != nil {
		t.Fatalf("BatchInsertEvents: %v", err)
	}

	rows, err := us.ListSubstitutions(ctx, time.Now().UTC().Add(-time.Hour))
	if err != nil {
		t.Fatalf("ListSubstitutions: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("ListSubstitutions returned %d rows, want 2: %+v", len(rows), rows)
	}
	// Descending count: the 3-row google triple first, then anthropic.
	if rows[0].Provider != "google" || rows[0].Count != 3 {
		t.Fatalf("rows[0] = %+v, want google/gemini-3-pro→gemini-3-flash count 3", rows[0])
	}
	want := SubstitutionRow{Provider: "anthropic", Model: "claude-opus-5", ServedModel: "claude-haiku-4-5", Count: 2}
	if rows[1] != want {
		t.Fatalf("rows[1] = %+v, want %+v", rows[1], want)
	}

	// The window bound must exclude older events: a far-future `since` finds
	// nothing.
	rows, err = us.ListSubstitutions(ctx, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatalf("ListSubstitutions(future): %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("ListSubstitutions(future) returned %d rows, want 0", len(rows))
	}
}
