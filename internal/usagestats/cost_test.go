package usagestats

import (
	"math"
	"testing"
)

func TestPriceMatcherExactMatch(t *testing.T) {
	pm := NewPriceMatcher([]ModelPrice{
		{Provider: "openai", Model: "gpt-4.1-mini", InputCostPerToken: 0.0000004, OutputCostPerToken: 0.0000016},
		{Provider: "claude", Model: "claude-sonnet-4", InputCostPerToken: 0.000003, OutputCostPerToken: 0.000015},
	})

	p, ok := pm.Match("openai", "gpt-4.1-mini")
	if !ok {
		t.Fatal("expected match for openai/gpt-4.1-mini")
	}
	if p.InputCostPerToken != 0.0000004 {
		t.Errorf("input cost = %v, want 0.0000004", p.InputCostPerToken)
	}
	if p.OutputCostPerToken != 0.0000016 {
		t.Errorf("output cost = %v, want 0.0000016", p.OutputCostPerToken)
	}
}

func TestPriceMatcherCaseInsensitive(t *testing.T) {
	pm := NewPriceMatcher([]ModelPrice{
		{Provider: "OpenAI", Model: "GPT-4.1-Mini", InputCostPerToken: 0.0000004, OutputCostPerToken: 0.0000016},
	})

	_, ok := pm.Match("openai", "gpt-4.1-mini")
	if !ok {
		t.Fatal("expected case-insensitive match")
	}

	_, ok = pm.Match("OPENAI", "GPT-4.1-MINI")
	if !ok {
		t.Fatal("expected uppercase match")
	}
}

func TestPriceMatcherWhitespace(t *testing.T) {
	pm := NewPriceMatcher([]ModelPrice{
		{Provider: " openai ", Model: " gpt-4.1-mini ", InputCostPerToken: 0.0000004, OutputCostPerToken: 0.0000016},
	})

	_, ok := pm.Match("openai", "gpt-4.1-mini")
	if !ok {
		t.Fatal("expected whitespace-trimmed match")
	}
}

func TestPriceMatcherNoMatch(t *testing.T) {
	pm := NewPriceMatcher([]ModelPrice{
		{Provider: "openai", Model: "gpt-4.1-mini", InputCostPerToken: 0.0000004, OutputCostPerToken: 0.0000016},
	})

	_, ok := pm.Match("anthropic", "claude-sonnet-4")
	if ok {
		t.Fatal("expected no match for unknown provider/model")
	}
}

func TestPriceMatcherNil(t *testing.T) {
	var pm *PriceMatcher
	_, ok := pm.Match("openai", "gpt-4.1-mini")
	if ok {
		t.Fatal("expected no match on nil matcher")
	}
}

func TestPriceMatcherEmpty(t *testing.T) {
	pm := NewPriceMatcher(nil)
	_, ok := pm.Match("openai", "gpt-4.1-mini")
	if ok {
		t.Fatal("expected no match on empty matcher")
	}
}

func TestCalculateCostBasic(t *testing.T) {
	price := ModelPrice{
		InputCostPerToken:  0.0000004, // $0.40/1M tokens
		OutputCostPerToken: 0.0000016, // $1.60/1M tokens
	}
	inputTokens := int64(1000)
	outputTokens := int64(500)

	inputMicros, outputMicros, totalMicros := CalculateCost(price, inputTokens, outputTokens)

	// Expected: 1000 * 0.0000004 * 1e6 = 400 micros
	if inputMicros != 400 {
		t.Errorf("input cost micros = %d, want 400", inputMicros)
	}
	// Expected: 500 * 0.0000016 * 1e6 = 800 micros
	if outputMicros != 800 {
		t.Errorf("output cost micros = %d, want 800", outputMicros)
	}
	if totalMicros != 1200 {
		t.Errorf("total cost micros = %d, want 1200", totalMicros)
	}
}

func TestCalculateCostZeroTokens(t *testing.T) {
	price := ModelPrice{
		InputCostPerToken:  0.0000004,
		OutputCostPerToken: 0.0000016,
	}
	inputMicros, outputMicros, totalMicros := CalculateCost(price, 0, 0)
	if inputMicros != 0 || outputMicros != 0 || totalMicros != 0 {
		t.Errorf("expected all zeros for zero tokens, got input=%d output=%d total=%d", inputMicros, outputMicros, totalMicros)
	}
}

func TestCalculateCostRounding(t *testing.T) {
	price := ModelPrice{
		InputCostPerToken:  0.00000003,
		OutputCostPerToken: 0.00000007,
	}
	// 1 * 0.00000003 * 1e6 = 0.03 -> rounds to 0
	// 1 * 0.00000007 * 1e6 = 0.07 -> rounds to 0
	inputMicros, outputMicros, totalMicros := CalculateCost(price, 1, 1)
	if inputMicros != 0 {
		t.Errorf("input micros = %d, want 0 (rounding)", inputMicros)
	}
	if outputMicros != 0 {
		t.Errorf("output micros = %d, want 0 (rounding)", outputMicros)
	}
	if totalMicros != 0 {
		t.Errorf("total micros = %d, want 0", totalMicros)
	}
}

func TestMicrosToUSD(t *testing.T) {
	tests := []struct {
		micros int64
		want   float64
	}{
		{0, 0},
		{1200000, 1.2},
		{400, 0.0004},
		{1, 0.000001},
	}
	for _, tt := range tests {
		got := MicrosToUSD(tt.micros)
		if math.Abs(got-tt.want) > 1e-10 {
			t.Errorf("MicrosToUSD(%d) = %f, want %f", tt.micros, got, tt.want)
		}
	}
}
