package store

import (
	"strings"
	"testing"
	"time"
)

// TestBuildAutoRouterDecisionWhere verifies the router-scoped WHERE clause and
// the AND-stacking of every AutoRouterDecisionFilter field. Empty fields
// must not contribute positional parameters so unrelated queries still pass.
func TestBuildAutoRouterDecisionWhere(t *testing.T) {
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	cases := []struct {
		name     string
		routerID string
		filter   AutoRouterDecisionFilter
		wantArgs int
		mustHave []string
		mustMiss []string
	}{
		{
			name:     "router only",
			routerID: "r-1",
			filter:   AutoRouterDecisionFilter{},
			wantArgs: 1,
			mustHave: []string{"router_id = $1"},
		},
		{
			name:     "all fields stack",
			routerID: "r-1",
			filter: AutoRouterDecisionFilter{
				APIKeyID:      "k-1",
				ScoredTier:    "complex",
				EffectiveTier: "complex",
				MappingTier:   "complex",
				DecisionCause: "literal_keyword_match",
				TargetModel:   "gpt-4o",
				ProfileHash:   "sha256:abc",
				From:          from,
				To:            to,
			},
			wantArgs: 10,
			mustHave: []string{
				"router_id = $1",
				"api_key_id = $2",
				"scored_tier = $3",
				"effective_tier = $4",
				"mapping_tier = $5",
				"decision_cause = $6",
				"model = $7",
				"profile_hash = $8",
				"requested_at >= $9",
				"requested_at < $10",
			},
			mustMiss: []string{},
		},
		{
			name:     "empty fields skip params",
			routerID: "r-1",
			filter:   AutoRouterDecisionFilter{DecisionCause: "literal_keyword_match"},
			wantArgs: 2,
			mustHave: []string{"decision_cause = $2"},
			mustMiss: []string{"scored_tier"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, args := buildAutoRouterDecisionWhere(tc.routerID, tc.filter)
			if len(args) != tc.wantArgs {
				t.Fatalf("args count = %d, want %d (clause=%q)", len(args), tc.wantArgs, got)
			}
			if args[0] != tc.routerID {
				t.Fatalf("args[0] = %v, want router id", args[0])
			}
			for _, want := range tc.mustHave {
				if !strings.Contains(got, want) {
					t.Errorf("clause missing %q (got %q)", want, got)
				}
			}
			for _, miss := range tc.mustMiss {
				if strings.Contains(got, miss) {
					t.Errorf("clause unexpectedly contains %q (got %q)", miss, got)
				}
			}
		})
	}
}
