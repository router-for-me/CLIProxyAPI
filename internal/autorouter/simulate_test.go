package autorouter

import (
	"strings"
	"testing"
)

// storedEventFor builds a StoredDecision from a raw body scored with the
// built-in profile — guaranteeing the simulation inputs match what the live
// scorer would have persisted.
func storedEventFor(t *testing.T, requestID, body string) StoredDecision {
	t.Helper()
	res := ScoreWithProfile([]byte(body), "openai", nil)
	return StoredDecision{
		RequestID:        requestID,
		ScoreFields:      res.Score.Fields,
		ReasoningMarkers: res.Score.ReasoningMarkers,
		ScoredTier:       res.EffectiveTier,
		EffectiveTier:    res.EffectiveTier,
		MappingTier:      res.EffectiveTier,
		DecisionCause:    res.DecisionCause,
		MatchedRules:     res.MatchedRules,
	}
}

// TestSimulateNoMovesWhenCandidateMatchesStored pins the parity guarantee:
// simulating with the same thresholds/weights the events were scored with must
// move nothing.
func TestSimulateNoMovesWhenCandidateMatchesStored(t *testing.T) {
	events := []StoredDecision{
		storedEventFor(t, "r1", `{"messages":[{"role":"user","content":"hi, what is your name?"}]}`),
		storedEventFor(t, "r2", `{"messages":[{"role":"user","content":"please summarize the main points of this meeting notes document"}]}`),
		storedEventFor(t, "r3", `{"messages":[{"role":"user","content":"explain why the algorithm is correct and analyze its time complexity. prove the trade-off between memory and speed."}]}`),
	}
	candidate := DefaultProfileConfig()
	result := SimulateProfile(events, candidate, nil)
	if result.MovedCount() != 0 {
		t.Fatalf("expected zero moves, got %+v", result.Moves)
	}
	if result.Events != int64(len(events)) {
		t.Fatalf("event count = %d, want %d", result.Events, len(events))
	}
	if result.UnsimulableCount != 0 {
		t.Fatalf("unsimulable = %d, want 0", result.UnsimulableCount)
	}
}

// TestSimulateThresholdShiftMovesEvents pins that a looser SIMPLE threshold
// (SimpleMax raised past MEDIUM's lower bound) reclassifies stored events and
// records sample request ids.
func TestSimulateThresholdShiftMovesEvents(t *testing.T) {
	events := []StoredDecision{
		storedEventFor(t, "r1", `{"messages":[{"role":"user","content":"please summarize the main points of this meeting notes document"}]}`),
	}
	candidate := DefaultProfileConfig()
	candidate.Thresholds = TierThresholds{SimpleMax: 0.60, MediumMax: 0.70, ComplexMax: 0.90}
	result := SimulateProfile(events, candidate, nil)
	moved := result.MovedCount()
	if moved != 1 {
		t.Fatalf("expected 1 move, got %d (%+v)", moved, result.Moves)
	}
	if len(result.Moves) == 0 || len(result.Moves[0].SampleRequestIDs) == 0 {
		t.Fatalf("expected sample request ids, got %+v", result.Moves)
	}
	if !strings.Contains(result.Moves[0].SampleRequestIDs[0], "r1") {
		t.Fatalf("sample id = %q", result.Moves[0].SampleRequestIDs[0])
	}
	if len(result.Confusion) == 0 {
		t.Fatal("expected confusion cells")
	}
}

// TestSimulateUnsimulableRules pins the flagging contract: candidate keyword
// rules whose IDs never appear in the stored matched-rules set are reported as
// unsimulable, never silently applied.
func TestSimulateUnsimulableRules(t *testing.T) {
	events := []StoredDecision{storedEventFor(t, "r1", `{"messages":[{"role":"user","content":"hi"}]}`)}
	candidate := DefaultProfileConfig()
	candidate.KeywordTierRules = []KeywordTierRule{
		{ID: "brand-new-rule", Tier: TierComplex, Keywords: []string{"special-phrase"}},
	}
	result := SimulateProfile(events, candidate, nil)
	if result.UnsimulableCount != 1 {
		t.Fatalf("unsimulable count = %d, want 1", result.UnsimulableCount)
	}
	if len(result.UnsimulableRules) != 1 || result.UnsimulableRules[0] != "brand-new-rule" {
		t.Fatalf("unsimulable rules = %+v", result.UnsimulableRules)
	}
}

// TestSimulateKnownRuleReplaysExactly pins that a rule identical to one
// recorded in the stored matched set replays deterministically: the stored
// effective tier already includes the rule's raise, and the candidate recompute
// (threshold tier + stored-rule replay) must land back on the same tier — net
// zero moves.
func TestSimulateKnownRuleReplaysExactly(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"please provide a formal proof"}]}`
	// Score with the profile that configured the proof rule, mirroring what the
	// live scorer persisted: effective tier raised to REASONING by the rule.
	storedCfg := DefaultProfileConfig()
	storedCfg.KeywordTierRules = []KeywordTierRule{
		{ID: "proof", Tier: TierReasoning, Keywords: []string{"formal proof"}},
	}
	res := ScoreWithProfile([]byte(body), "openai", &Profile{ProfileConfig: storedCfg})
	if res.EffectiveTier != TierReasoning {
		t.Fatalf("precondition: expected rule-raised REASONING, got %q", res.EffectiveTier)
	}
	event := StoredDecision{
		RequestID:        "r1",
		ScoreFields:      res.Score.Fields,
		ReasoningMarkers: res.Score.ReasoningMarkers,
		ScoredTier:       res.Score.Tier,
		EffectiveTier:    res.EffectiveTier,
		MappingTier:      res.EffectiveTier,
		DecisionCause:    res.DecisionCause,
		MatchedRules:     res.MatchedRules,
	}
	// Candidate keeps the identical rule: replay restores the raise → no move.
	candidate := DefaultProfileConfig()
	candidate.KeywordTierRules = []KeywordTierRule{
		{ID: "proof", Tier: TierReasoning, Keywords: []string{"formal proof"}},
	}
	result := SimulateProfile([]StoredDecision{event}, candidate, nil)
	if result.UnsimulableCount != 0 {
		t.Fatalf("stored rule must be simulable, got %+v", result.UnsimulableRules)
	}
	if result.MovedCount() != 0 {
		t.Fatalf("identical rule replay must not change the outcome, got %+v", result.Moves)
	}
	// Dropping the rule loses the raise: the event moves REASONING → its
	// threshold-only tier. Reported as a move, not silently preserved.
	dropped := DefaultProfileConfig()
	resultDropped := SimulateProfile([]StoredDecision{event}, dropped, nil)
	if resultDropped.MovedCount() != 1 {
		t.Fatalf("dropping the stored rule must move the event, got %+v", resultDropped.Moves)
	}
}

// TestSimulateSampleIDCap pins the ≤10 sample ids per move bound.
func TestSimulateSampleIDCap(t *testing.T) {
	events := make([]StoredDecision, 0, 30)
	for i := 0; i < 30; i++ {
		events = append(events, storedEventFor(t, "req-"+strings.Repeat("x", i)+string(rune('a'+i)),
			`{"messages":[{"role":"user","content":"please summarize the main points of this meeting notes document"}]}`))
	}
	candidate := DefaultProfileConfig()
	candidate.Thresholds = TierThresholds{SimpleMax: 0.60, MediumMax: 0.70, ComplexMax: 0.90}
	result := SimulateProfile(events, candidate, nil)
	for _, m := range result.Moves {
		if len(m.SampleRequestIDs) > 10 {
			t.Fatalf("sample ids = %d, want <= 10", len(m.SampleRequestIDs))
		}
	}
	if result.MovedCount() != 30 {
		t.Fatalf("expected all 30 events to move, got %d", result.MovedCount())
	}
}
