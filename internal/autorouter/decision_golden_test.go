package autorouter

import (
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

// TestDecisionSnapshotJSONShape pins the persistence contract: the snapshot is
// stored as jsonb in usage_events.auto_router_decision and its keys are read
// back by the stats/replay queries. Any change to these keys is a breaking
// change for stored rows and must be a deliberate, migrated one.
func TestDecisionSnapshotJSONShape(t *testing.T) {
	snap := DecisionSnapshot{
		ProfileVersion:   1,
		ProfileHash:      "sha256:abc",
		ProfileSnapshot:  DefaultProfileConfig(),
		ScoreTotal:       0.42,
		ScoreFields:      map[ScoreField]float64{FieldTokens: 0.1},
		ReasoningMarkers: 1,
		ScoredTier:       TierMedium,
		EffectiveTier:    TierComplex,
		DecisionCause:    DecisionCauseKeywordMatch,
		MatchedRules:     []MatchedKeywordRule{{ID: "r", Tier: TierComplex, Keywords: []string{"k"}}},
		MappingTier:      TierComplex,
		FallbackChain:    []Tier{TierReasoning, TierComplex},
		TargetModel:      "claude-sonnet-4-5",
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"profile_version", "profile_hash", "profile_snapshot", "score_total",
		"score_fields", "reasoning_markers", "scored_tier", "effective_tier",
		"decision_cause", "matched_rules", "mapping_tier", "fallback_chain", "target_model",
	} {
		if !gjson.GetBytes(raw, key).Exists() {
			t.Errorf("decision snapshot JSON missing key %q (got %s)", key, raw)
		}
	}
}
