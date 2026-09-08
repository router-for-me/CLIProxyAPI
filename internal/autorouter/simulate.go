package autorouter

// StoredDecision is one persisted decision snapshot reduced to the fields the
// simulation needs. It never carries request text — simulation works purely
// from stored signals (score fields, markers, tiers, matched rules).
type StoredDecision struct {
	RequestID        string
	ScoreFields      map[ScoreField]float64
	ScoreTotal       float64
	ReasoningMarkers int
	ScoredTier       Tier
	EffectiveTier    Tier
	MappingTier      Tier
	DecisionCause    string
	MatchedRules     []MatchedKeywordRule
}

// TierMove counts events whose tier changes from one tier to another under the
// candidate profile, with up to simulateSampleIDCap sample request ids.
type TierMove struct {
	From             Tier     `json:"from"`
	To               Tier     `json:"to"`
	Count            int64    `json:"count"`
	SampleRequestIDs []string `json:"sample_request_ids"`
}

// ConfusionCell is one from→to cell of the classification confusion matrix
// (including the diagonal — events whose tier stays put).
type ConfusionCell struct {
	From  Tier  `json:"from"`
	To    Tier  `json:"to"`
	Count int64 `json:"count"`
}

// SimulationResult is the outcome of replaying stored decisions against a
// candidate scoring profile.
type SimulationResult struct {
	// Events is the number of stored decisions simulated.
	Events int64 `json:"events"`
	// Moves lists every from→to tier change with samples, ordered by count desc.
	Moves []TierMove `json:"moves"`
	// Confusion is the full from→to matrix (moves plus unchanged cells).
	Confusion []ConfusionCell `json:"confusion"`
	// UnsimulableRules lists candidate keyword-rule IDs that never appeared in
	// the stored matched sets and therefore could not be evaluated from stored
	// signals alone. Their tier raise is NOT applied to the recompute.
	UnsimulableRules []string `json:"unsimulable_rules"`
	// UnsimulableCount mirrors len(UnsimulableRules) as a convenience.
	UnsimulableCount int `json:"unsimulable_count"`
}

// MovedCount totals the events whose tier changes under the candidate.
func (r *SimulationResult) MovedCount() int64 {
	var total int64
	for _, m := range r.Moves {
		total += m.Count
	}
	return total
}

// simulateSampleIDCap bounds the sample request ids kept per tier move.
const simulateSampleIDCap = 10

// SimulateProfile recomputes the tier for every stored decision under the
// candidate profile and reports how classifications would move. Recompute
// parity: when candidate == the profile the events were scored with, the
// weighted-total math below reproduces the stored tiers exactly (same formula
// as ScoreWithProfile, minus text extraction which is not persisted).
//
// Keyword rules are handled conservatively: a candidate rule whose ID appears
// in the event's stored matched set replays with its stored effect; any other
// candidate rule is counted as unsimulable (reported, not applied) because
// request text is not persisted and matching cannot be reproduced.
//
// knownRuleTiers may be nil; when supplied it maps rule ID → tier and is used
// instead of deriving tiers from the candidate config (used by tests).
func SimulateProfile(events []StoredDecision, candidate ProfileConfig, knownRuleTiers map[string]Tier) *SimulationResult {
	result := &SimulationResult{Events: int64(len(events)), UnsimulableRules: []string{}}

	// Candidate weights: fill missing from defaults, exactly like scoring.
	weights := candidate.Weights
	if weights == nil {
		weights = DefaultWeights
	}
	weightFor := func(f ScoreField) float64 {
		if w, ok := weights[f]; ok {
			return w
		}
		return DefaultWeights[f]
	}

	// Determine which candidate rules are simulable: rule IDs that appear in
	// at least one stored matched set have provenance; others do not.
	seenRuleIDs := map[string]bool{}
	for _, ev := range events {
		for _, r := range ev.MatchedRules {
			seenRuleIDs[r.ID] = true
		}
	}
	candidateRuleTiers := map[string]Tier{}
	for _, rule := range candidate.KeywordTierRules {
		if knownRuleTiers != nil {
			if t, ok := knownRuleTiers[rule.ID]; ok {
				candidateRuleTiers[rule.ID] = t
			}
		} else {
			candidateRuleTiers[rule.ID] = rule.Tier
		}
	}
	for _, rule := range candidate.KeywordTierRules {
		if !seenRuleIDs[rule.ID] {
			result.UnsimulableRules = append(result.UnsimulableRules, rule.ID)
		}
	}
	result.UnsimulableCount = len(result.UnsimulableRules)

	moveCounts := map[[2]Tier]int64{}
	moveSamples := map[[2]Tier][]string{}
	confusion := map[[2]Tier]int64{}

	for _, ev := range events {
		// 1. Recompute the weighted total from stored sub-scores.
		total := 0.0
		norm := 0.0
		for _, f := range []ScoreField{
			FieldTokens, FieldCode, FieldReasoningMark, FieldTechnicalTerms,
			FieldMultiStep, FieldQuestion,
		} {
			total += ev.ScoreFields[f] * weightFor(f)
			norm += weightFor(f)
		}
		total -= ev.ScoreFields[FieldSimpleIndic] * weightFor(FieldSimpleIndic)
		if norm > 0 {
			total /= norm
		}
		total = clamp01(total)

		// 2. Threshold-only tier, then apply the stored rule raises that the
		// candidate still configures (replay), skipping unsimulable rules.
		newTier := tierFor(total, ev.ReasoningMarkers, candidate.Thresholds)
		for _, stored := range ev.MatchedRules {
			tier, configured := candidateRuleTiers[stored.ID]
			if !configured {
				continue // rule dropped by candidate — its raise disappears
			}
			if tierIndex(tier) > tierIndex(newTier) {
				newTier = tier
			}
		}

		// 3. Record the from→to outcome.
		from := ev.EffectiveTier
		confusion[[2]Tier{from, newTier}]++
		if from != newTier {
			key := [2]Tier{from, newTier}
			moveCounts[key]++
			if len(moveSamples[key]) < simulateSampleIDCap {
				moveSamples[key] = append(moveSamples[key], ev.RequestID)
			}
		}
	}

	for key, count := range moveCounts {
		result.Moves = append(result.Moves, TierMove{
			From: key[0], To: key[1], Count: count, SampleRequestIDs: moveSamples[key],
		})
	}
	sortMoves(result.Moves)
	for key, count := range confusion {
		result.Confusion = append(result.Confusion, ConfusionCell{From: key[0], To: key[1], Count: count})
	}
	sortConfusion(result.Confusion)
	return result
}

// sortMoves orders moves by count descending then tier names (deterministic).
func sortMoves(moves []TierMove) {
	for i := 1; i < len(moves); i++ {
		for j := i; j > 0; j-- {
			a, b := moves[j-1], moves[j]
			if a.Count < b.Count || (a.Count == b.Count && (string(a.From)+string(a.To)) > (string(b.From)+string(b.To))) {
				moves[j-1], moves[j] = b, a
			} else {
				break
			}
		}
	}
}

// sortConfusion orders confusion cells by from tier order then to tier order
// (deterministic, matching TierOrder).
func sortConfusion(cells []ConfusionCell) {
	rank := func(t Tier) int { return tierIndex(t) }
	for i := 1; i < len(cells); i++ {
		for j := i; j > 0; j-- {
			a, b := cells[j-1], cells[j]
			if rank(a.From) > rank(b.From) || (rank(a.From) == rank(b.From) && rank(a.To) > rank(b.To)) {
				cells[j-1], cells[j] = b, a
			} else {
				break
			}
		}
	}
}
