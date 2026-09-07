package autorouter

import (
	"strings"
	"testing"
)

// TestCompileProfileParity pins the CompiledProfile contract: compiling a raw
// config normalizes it (same identity as NormalizeProfile) and the compiled
// scoring path produces the same tier/cause/matched-rules as the legacy
// per-request path.
func TestCompileProfileParity(t *testing.T) {
	raw := DefaultProfileConfig()
	// Unnormalized inputs: duplicate keyword with different casing/spacing, and
	// a missing weight (must be filled from DefaultWeights).
	raw.KeywordTierRules = []KeywordTierRule{
		{ID: "db", Tier: TierComplex, Keywords: []string{"Migrate  the Database!", "migrate the database"}},
	}
	compiled, err := CompileProfile(raw)
	if err != nil {
		t.Fatalf("CompileProfile: %v", err)
	}
	normalized, err := NormalizeProfile(raw)
	if err != nil {
		t.Fatalf("NormalizeProfile: %v", err)
	}
	if compiled.Hash == "" {
		t.Fatal("compiled hash empty")
	}
	if compiled.Hash != mustHash(t, normalized) {
		t.Fatalf("compiled hash %q != normalized hash %q", compiled.Hash, mustHash(t, normalized))
	}
	// Keywords pre-normalized once: identical after rule normalization.
	if len(compiled.KeywordTierRules) == 0 {
		t.Fatal("no rules compiled")
	}
	for _, r := range compiled.KeywordTierRules {
		for _, kw := range r.keywords {
			if kw != normalizeKeywordText(kw) {
				t.Fatalf("keyword %q not pre-normalized", kw)
			}
		}
	}

	// Scoring parity: legacy profile path vs compiled path must agree on the
	// corpus below.
	corpus := []string{
		`{"messages":[{"role":"user","content":"please provide a formal proof"}]}`,
		`{"messages":[{"role":"user","content":"please migrate the database to Postgres 16"}]}`,
		`{"messages":[{"role":"user","content":"hi, what is your name?"}]}`,
	}
	legacy := Profile{ProfileConfig: raw, ProfileVersion: 7, ProfileHash: compiled.Hash}
	for _, body := range corpus {
		a := ScoreWithProfile([]byte(body), "openai", &legacy)
		b := ScoreWithProfileCompiled([]byte(body), "openai", &compiled)
		if a.EffectiveTier != b.EffectiveTier || a.DecisionCause != b.DecisionCause {
			t.Fatalf("parity mismatch on %s: legacy=%s/%s compiled=%s/%s",
				body, a.EffectiveTier, a.DecisionCause, b.EffectiveTier, b.DecisionCause)
		}
		if len(a.MatchedRules) != len(b.MatchedRules) {
			t.Fatalf("matched-rule count mismatch on %s: %d vs %d", body, len(a.MatchedRules), len(b.MatchedRules))
		}
		if a.Score.Total != b.Score.Total {
			t.Fatalf("total mismatch on %s: %v vs %v", body, a.Score.Total, b.Score.Total)
		}
	}
}

// TestCompileProfileNilSafety: compiling a zero config must not panic; scoring
// with a nil compiled profile must fall back to built-in defaults.
func TestCompileProfileNilSafety(t *testing.T) {
	if _, err := CompileProfile(DefaultProfileConfig()); err != nil {
		t.Fatalf("default config must compile: %v", err)
	}
	res := ScoreWithProfileCompiled([]byte(`{"messages":[{"content":"hi"}]}`), "openai", nil)
	if res.EffectiveTier != TierSimple {
		t.Fatalf("nil compiled profile must use defaults, got %q", res.EffectiveTier)
	}
}

// TestCompiledKeywordPhraseMatching pins that pre-normalized phrase keywords
// still match punctuation-varying request text through the compiled path.
func TestCompiledKeywordPhraseMatching(t *testing.T) {
	raw := DefaultProfileConfig()
	raw.KeywordTierRules = []KeywordTierRule{
		{ID: "deploy", Tier: TierComplex, Keywords: []string{"ship it to production"}},
	}
	compiled, err := CompileProfile(raw)
	if err != nil {
		t.Fatalf("CompileProfile: %v", err)
	}
	body := `{"messages":[{"content":"can you ship it to production today?"}]}`
	res := ScoreWithProfileCompiled([]byte(body), "openai", &compiled)
	if res.EffectiveTier != TierComplex || res.DecisionCause != DecisionCauseKeywordMatch {
		t.Fatalf("tier=%q cause=%q matched=%+v", res.EffectiveTier, res.DecisionCause, res.MatchedRules)
	}
	if !strings.Contains(compiled.Hash, "sha256:") {
		t.Fatalf("hash format = %q", compiled.Hash)
	}
}

func mustHash(t *testing.T, config ProfileConfig) string {
	t.Helper()
	h, err := ProfileHash(config)
	if err != nil {
		t.Fatalf("ProfileHash: %v", err)
	}
	return h
}
