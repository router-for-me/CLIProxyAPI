package autorouter

import (
	"strings"
	"testing"
)

// TestScorerParityOnCorpus guards against accidental tier regressions: for the
// historic corpus (the requests the scorer already classified correctly), the
// tier must not move. New v2 signals must only affect bodies the old scorer
// mishandled (system-prompt-heavy and fenced payloads).
func TestScorerParityOnCorpus(t *testing.T) {
	cases := []struct {
		name string
		body string
		want Tier
	}{
		{"greeting", `{"messages":[{"role":"user","content":"hi, what is your name?"}]}`, TierSimple},
		{"summarize", `{"messages":[{"role":"user","content":"please summarize the main points of this meeting notes document"}]}`, TierMedium},
		{"proof", `{"messages":[{"role":"user","content":"explain why the algorithm is correct and analyze its time complexity. prove the trade-off between memory and speed."}]}`, TierReasoning},
		{"gemini", `{"contents":[{"parts":[{"text":"summarize this short article in a few bullet points"}]}]}`, TierSimple},
		{"empty", `{"model":"x"}`, TierSimple},
	}
	for _, c := range cases {
		if got := Score([]byte(c.body), "openai").Tier; got != c.want {
			t.Errorf("%s: tier = %q, want %q", c.name, got, c.want)
		}
	}
}

// buildLargeAgentBody builds a worst-case body: a 40k-line agent system prompt
// wrapping a short user turn — the shape F1's role-aware split exists for.
func buildLargeAgentBody() []byte {
	var sb strings.Builder
	sb.WriteString(`{"model":"x","messages":[{"role":"system","content":"`)
	for i := 0; i < 40000; i++ {
		sb.WriteString("agent system instruction line with some technical words api json cache\n")
	}
	sb.WriteString(`"},{"role":"user","content":"add a retry with backoff to the fetch helper, then implement tests for it"}]}`)
	return []byte(sb.String())
}

func BenchmarkScoreLargeAgentBody(b *testing.B) {
	raw := buildLargeAgentBody()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		Score(raw, "openai")
	}
}

// BenchmarkScoreSmallBody measures the common case: a short chat request.
func BenchmarkScoreSmallBody(b *testing.B) {
	raw := []byte(`{"model":"x","messages":[{"role":"user","content":"hi, what is your name?"}]}`)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		Score(raw, "openai")
	}
}
