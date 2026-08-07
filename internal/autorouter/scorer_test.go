package autorouter

import (
	"testing"
)

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
