package autorouter

import (
	"strings"
	"testing"
)

// TestExtractRequestStructure guards the structured-extraction contract: the
// latest user turn, history size, and fenced code payload must all be captured
// from a single pass over the body.
func TestExtractRequestStructure(t *testing.T) {
	// The fence payload is embedded with JSON \n escapes (a real newline inside
	// a JSON string literal would truncate parsing at the fence marker).
	body := `{"model":"x","messages":[
		{"role":"system","content":"You are a coding agent. Follow instructions."},
		{"role":"user","content":"hi there"},
		{"role":"assistant","content":"sure"},
		{"role":"user","content":"please summarize this:\\n` + "```go\\nfunc main() { a := 1 }\\n```\\n" + `thanks"}
	]}`
	ext := extractRequest([]byte(body), "openai")
	if len(ext.LatestUserText) == 0 {
		t.Fatal("expected latest user text to be captured")
	}
	if !strings.Contains(ext.LatestUserText, "summarize") {
		t.Fatalf("latest user text = %q", ext.LatestUserText)
	}
	if ext.CodeFenceTokens == 0 {
		t.Fatal("expected code fence tokens > 0")
	}
	if ext.FlatText == "" {
		t.Fatal("expected flat text to be populated")
	}
	// Flat text covers system + history + latest turn.
	if !strings.Contains(ext.FlatText, "coding agent") {
		t.Fatal("expected flat text to include system prompt")
	}
}

func TestTierFor(t *testing.T) {
	cases := []struct {
		total   float64
		markers int
		want    Tier
	}{
		{0.05, 0, TierSimple},
		{0.1499, 0, TierSimple},
		{0.15, 0, TierMedium},
		{0.25, 0, TierMedium},
		{0.35, 0, TierComplex},
		{0.40, 0, TierComplex},
		{0.60, 0, TierComplex},
		{0.61, 0, TierReasoning},
		{0.80, 0, TierReasoning},
		// 2+ reasoning markers force REASONING even at a low score.
		{0.05, 2, TierReasoning},
		{0.05, 3, TierReasoning},
		{0.40, 1, TierComplex},
	}
	for _, c := range cases {
		if got := tierFor(c.total, c.markers); got != c.want {
			t.Errorf("tierFor(%v, %d) = %q, want %q", c.total, c.markers, got, c.want)
		}
	}
}

func TestScoreSimple(t *testing.T) {
	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi, what is your name?"}]}`
	s := Score([]byte(body), "openai")
	if s.Tier != TierSimple {
		t.Fatalf("expected SIMPLE, got %q (total=%v)", s.Tier, s.Total)
	}
}

func TestScoreMedium(t *testing.T) {
	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"please summarize the main points of this meeting notes document"}]}`
	s := Score([]byte(body), "openai")
	if s.Tier != TierMedium {
		t.Fatalf("expected MEDIUM, got %q (total=%v)", s.Tier, s.Total)
	}
}

func TestScoreComplexViaCode(t *testing.T) {
	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"refactor this function to use goroutines and handle errors: func load(id string) ([]byte, error) { return ioutil.ReadFile(id) } implement retry logic with backoff and tests"}]}`
	s := Score([]byte(body), "openai")
	if s.Tier == TierSimple {
		t.Fatalf("expected not SIMPLE for a code/refactor request, got %q (total=%v)", s.Tier, s.Total)
	}
	if s.Tier == TierReasoning && s.ReasoningMarkers < ReasonerMarkerThreshold {
		t.Fatalf("expected REASONING only via high score or markers, got markers=%d", s.ReasoningMarkers)
	}
}

func TestScoreReasoningMarkers(t *testing.T) {
	body := `{"model":"claude","messages":[{"role":"user","content":"explain why the algorithm is correct and analyze its time complexity. prove the trade-off between memory and speed."}]}`
	s := Score([]byte(body), "claude")
	if s.Tier != TierReasoning {
		t.Fatalf("expected REASONING from markers, got %q (markers=%d)", s.Tier, s.ReasoningMarkers)
	}
	if s.ReasoningMarkers < ReasonerMarkerThreshold {
		t.Fatalf("expected >= %d reasoning markers, got %d", ReasonerMarkerThreshold, s.ReasoningMarkers)
	}
}

func TestScoreGeminiFormat(t *testing.T) {
	body := `{"contents":[{"parts":[{"text":"summarize this short article in a few bullet points"}]}]}`
	s := Score([]byte(body), "gemini")
	if s.Tier != TierSimple && s.Tier != TierMedium {
		t.Fatalf("expected low tier for a simple summarize request, got %q", s.Tier)
	}
}

func TestScoreEmpty(t *testing.T) {
	s := Score([]byte(`{"model":"x"}`), "openai")
	if s.Tier != TierSimple {
		t.Fatalf("expected SIMPLE for empty body, got %q", s.Tier)
	}
}

func TestResolveFallback(t *testing.T) {
	c := &Config{
		Mappings: []TierMapping{
			{Tier: TierSimple, Model: "gpt-4o-mini"},
			{Tier: TierMedium, Model: "gpt-4o"},
			{Tier: TierComplex, Model: "claude-sonnet-4-5"},
		},
	}
	// REASONING unmapped -> falls back to COMPLEX.
	resolved, ok := Resolve(TierReasoning, c)
	if !ok || resolved == nil || resolved.Model != "claude-sonnet-4-5" {
		t.Fatalf("expected fallback to COMPLEX, got %+v ok=%v", resolved, ok)
	}
	// COMPLEX -> itself.
	resolved, _ = Resolve(TierComplex, c)
	if resolved == nil || resolved.Model != "claude-sonnet-4-5" {
		t.Fatalf("expected claude-sonnet-4-5, got %+v", resolved)
	}
}

func TestResolveAllUnmapped(t *testing.T) {
	c := &Config{Mappings: nil}
	if _, ok := Resolve(TierSimple, c); ok {
		t.Fatal("expected no resolution when no mappings present")
	}
}

func TestResolveDefaultSuffixPreserved(t *testing.T) {
	c := &Config{
		Mappings: []TierMapping{
			{Tier: TierSimple, Model: "gpt-4o-mini(medium)"},
			{Tier: TierReasoning, Model: "claude-opus-4-5(high)"},
		},
	}
	resolved, ok := Resolve(TierReasoning, c)
	if !ok || resolved == nil || resolved.Model != "claude-opus-4-5(high)" {
		t.Fatalf("expected exact reasoner model with suffix, got %+v ok=%v", resolved, ok)
	}
}

func TestResolveCarriesRouting(t *testing.T) {
	c := &Config{
		Mappings: []TierMapping{
			{
				Tier:      TierComplex,
				Model:     "claude-sonnet-4-5",
				Providers: []string{"anthropic", "openai"},
				Strategy:  "priority",
				Priorities: []ProviderPriority{
					{Provider: "anthropic", Priority: 10},
					{Provider: "openai", Priority: 5},
				},
			},
		},
	}
	resolved, ok := Resolve(TierComplex, c)
	if !ok || resolved == nil {
		t.Fatal("expected resolution")
	}
	if len(resolved.Providers) != 2 || resolved.Strategy != "priority" || len(resolved.Priorities) != 2 {
		t.Fatalf("routing not carried through, got %+v", resolved)
	}
	if resolved.Priorities[0].Provider != "anthropic" || resolved.Priorities[0].Priority != 10 {
		t.Fatalf("priorities not preserved, got %+v", resolved.Priorities)
	}
}

// Ensure every dimension produces a sane sub-score in [0,1] and the total is in
// [0,1] for a realistic mixed request, so a regression in one scorer branch
// cannot skew the classification.
func TestScoreDimensionsInRange(t *testing.T) {
	body := `{"model":"x","messages":[{"role":"user","content":"explain and analyze the complexity trade-offs in this HTTP retry algorithm: for i:=0;i<3;i++ { if err := do(); err != nil { continue } }"}]}`
	s := Score([]byte(body), "openai")
	if s.Total < 0 || s.Total > 1 {
		t.Fatalf("total out of range: %v", s.Total)
	}
	for _, f := range []ScoreField{
		FieldTokens, FieldCode, FieldReasoningMark, FieldTechnicalTerms,
		FieldSimpleIndic, FieldMultiStep, FieldQuestion,
	} {
		if v := s.Fields[f]; v < 0 || v > 1 {
			t.Errorf("field %s out of range: %v", f, v)
		}
	}
}

// TestScoreResponsesInputTextNoRole guards against a regression where
// Responses-format messages that carry their text via a top-level `text` field
// (and lack an explicit `role`) get silently dropped from scoring. Without the
// fix the body scores as SIMPLE; with the fix the heavy engineering prompt
// scores above SIMPLE.
func TestScoreResponsesInputTextNoRole(t *testing.T) {
	body := `{"model":"x","input":[{"text":"refactor this retry loop to use goroutines and implement exponential backoff with jitter and add tests"},{"content":[{"type":"text","text":"and handle timeout errors"}]}]}`
	s := Score([]byte(body), "openai")
	if s.Tier == TierSimple {
		t.Fatalf("expected non-SIMPLE tier when input[].text carries a heavy engineering prompt, got %q (total=%v)", s.Tier, s.Total)
	}
}

// TestScoreResponsesInputRoleOnlyText guards the case where a Responses-format
// message carries only `role` + `text` (no `content` block). The score must
// reflect the `text` payload, and a prompt that hits >= the reasoning-marker
// threshold must be classified REASONING (not silently downgraded).
func TestScoreWithCustomProfileAndKeywordOverride(t *testing.T) {
	config := DefaultProfileConfig()
	config.Thresholds = TierThresholds{SimpleMax: 0.15, MediumMax: 0.35, ComplexMax: 0.60}
	config.KeywordTierRules = []KeywordTierRule{{ID: "proof", Tier: TierReasoning, Keywords: []string{"formal proof"}}}
	hash, err := ProfileHash(config)
	if err != nil {
		t.Fatalf("ProfileHash: %v", err)
	}
	result := ScoreWithProfile([]byte(`{"messages":[{"role":"user","content":"please provide a formal proof"}]}`), "openai", &Profile{ProfileConfig: config, ProfileVersion: 7, ProfileHash: hash})
	if result.EffectiveTier != TierReasoning || result.DecisionCause != DecisionCauseKeywordMatch {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(result.MatchedRules) != 1 || result.MatchedRules[0].ID != "proof" {
		t.Fatalf("matched rules = %+v", result.MatchedRules)
	}
	if result.ProfileVersion != 7 || result.ProfileHash != hash {
		t.Fatalf("profile metadata = %d/%q", result.ProfileVersion, result.ProfileHash)
	}
}

func TestKeywordRulesChooseHighestTierRegardlessOfOrder(t *testing.T) {
	config := DefaultProfileConfig()
	config.KeywordTierRules = []KeywordTierRule{
		{ID: "complex", Tier: TierComplex, Keywords: []string{"production"}},
		{ID: "reasoning", Tier: TierReasoning, Keywords: []string{"theorem"}},
	}
	result := ScoreWithProfile([]byte(`{"messages":[{"content":"production theorem"}]}`), "openai", &Profile{ProfileConfig: config})
	if result.EffectiveTier != TierReasoning {
		t.Fatalf("tier = %q, want reasoning; matches=%+v", result.EffectiveTier, result.MatchedRules)
	}
}

func TestProfileHashDeterministicAfterNormalization(t *testing.T) {
	base := DefaultProfileConfig()
	base.KeywordTierRules = []KeywordTierRule{{ID: "b", Tier: TierComplex, Keywords: []string{"Race Condition", "deadlock"}}, {ID: "a", Tier: TierReasoning, Keywords: []string{"proof"}}}
	reversed := base
	reversed.KeywordTierRules = []KeywordTierRule{{ID: "a", Tier: TierReasoning, Keywords: []string{"proof"}}, {ID: "b", Tier: TierComplex, Keywords: []string{"deadlock", "race condition"}}}
	first, err := ProfileHash(base)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ProfileHash(reversed)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("hashes differ: %q != %q", first, second)
	}
}
