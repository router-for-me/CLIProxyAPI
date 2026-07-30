package store

import (
	"reflect"
	"testing"
)

// TestFilterExcludedSnapshots covers the read-time filter used by the dashboard
// latest-status endpoint and the public uptime endpoint. Matching mirrors the
// probe runner's write-time exclusion (case-insensitive, trimmed), so an
// excluded "GPT-4" hides a snapshot stored as "gpt-4".
func TestFilterExcludedSnapshots(t *testing.T) {
	rows := []ModelHealthRow{
		{ModelID: "claude-3", Status: ModelHealthStatusOperational},
		{ModelID: "gpt-4", Status: ModelHealthStatusDegraded},
		{ModelID: "gemini-1.5", Status: ModelHealthStatusUnavailable},
		{ModelID: "llama-3", Status: ModelHealthStatusOperational},
	}

	cases := []struct {
		name        string
		rows        []ModelHealthRow
		excluded    []string
		wantModelID []string
	}{
		{
			name:        "no exclusions returns input unchanged",
			rows:        rows,
			excluded:    nil,
			wantModelID: []string{"claude-3", "gpt-4", "gemini-1.5", "llama-3"},
		},
		{
			name:        "empty exclusion slice is a no-op",
			rows:        rows,
			excluded:    []string{},
			wantModelID: []string{"claude-3", "gpt-4", "gemini-1.5", "llama-3"},
		},
		{
			name:        "single exclusion removed",
			rows:        rows,
			excluded:    []string{"gpt-4"},
			wantModelID: []string{"claude-3", "gemini-1.5", "llama-3"},
		},
		{
			name:        "case-insensitive match",
			rows:        rows,
			excluded:    []string{"GPT-4"},
			wantModelID: []string{"claude-3", "gemini-1.5", "llama-3"},
		},
		{
			name:        "excluded entry is trimmed before matching",
			rows:        rows,
			excluded:    []string{"  gpt-4  "},
			wantModelID: []string{"claude-3", "gemini-1.5", "llama-3"},
		},
		{
			name:        "multiple exclusions preserve order of survivors",
			rows:        rows,
			excluded:    []string{"gemini-1.5", "llama-3"},
			wantModelID: []string{"claude-3", "gpt-4"},
		},
		{
			name:        "all excluded yields empty slice (sorted-input order preserved)",
			rows:        rows,
			excluded:    []string{"claude-3", "gpt-4", "gemini-1.5", "llama-3"},
			wantModelID: []string{},
		},
		{
			name:        "duplicate excluded entries collapse",
			rows:        rows,
			excluded:    []string{"gpt-4", "GPT-4", " gpt-4 "},
			wantModelID: []string{"claude-3", "gemini-1.5", "llama-3"},
		},
		{
			name:        "unknown excluded model has no effect",
			rows:        rows,
			excluded:    []string{"does-not-exist"},
			wantModelID: []string{"claude-3", "gpt-4", "gemini-1.5", "llama-3"},
		},
		{
			name:        "empty and whitespace-only entries ignored",
			rows:        rows,
			excluded:    []string{"", "   ", "gpt-4"},
			wantModelID: []string{"claude-3", "gemini-1.5", "llama-3"},
		},
		{
			name:        "empty input rows with exclusions still safe",
			rows:        []ModelHealthRow{},
			excluded:    []string{"gpt-4"},
			wantModelID: []string{},
		},
		{
			name:        "nil input rows returns nil",
			rows:        nil,
			excluded:    nil,
			wantModelID: []string{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FilterExcludedSnapshots(tc.rows, tc.excluded)
			ids := make([]string, 0, len(got))
			for _, r := range got {
				ids = append(ids, r.ModelID)
			}
			if !reflect.DeepEqual(ids, tc.wantModelID) {
				t.Fatalf("FilterExcludedSnapshots model_ids = %v, want %v", ids, tc.wantModelID)
			}
		})
	}
}

// TestFilterExcludedSnapshots_ReturnsNewSlice ensures the filter does not mutate
// or alias the input slice — callers (the handlers) may keep referencing the
// original ListSnapshots result independently.
func TestFilterExcludedSnapshots_ReturnsNewSlice(t *testing.T) {
	rows := []ModelHealthRow{
		{ModelID: "gpt-4"},
		{ModelID: "claude-3"},
	}
	// No exclusions: the helper returns the input slice directly (optimized
	// no-op). Confirm it does not change length or contents.
	got := FilterExcludedSnapshots(rows, nil)
	if len(got) != 2 {
		t.Fatalf("expected 2 rows unchanged, got %d", len(got))
	}
	// With an exclusion, a brand-new slice is produced; mutating it must not
	// affect the original input.
	got = FilterExcludedSnapshots(rows, []string{"gpt-4"})
	if len(got) != 1 || got[0].ModelID != "claude-3" {
		t.Fatalf("expected only claude-3, got %+v", got)
	}
	got[0].Status = ModelHealthStatusDegraded
	if rows[1].Status == ModelHealthStatusDegraded {
		t.Fatalf("filter result aliased the input slice; mutating output changed input")
	}
}
