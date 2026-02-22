package management

import "testing"

func TestNormalizeRoutingStrategy_AcceptsFillFirstAliases(t *testing.T) {
	tests := []string{
		"fill-first",
		"fill_first",
		"fillfirst",
		"ff",
		"  Fill_First  ",
	}

	for _, input := range tests {
		got, ok := normalizeRoutingStrategy(input)
		if !ok {
			t.Fatalf("normalizeRoutingStrategy(%q) was rejected", input)
		}
		if got != "fill-first" {
			t.Fatalf("normalizeRoutingStrategy(%q) = %q, want %q", input, got, "fill-first")
		}
	}
}

func TestNormalizeRoutingStrategy_RejectsUnknownAlias(t *testing.T) {
	if got, ok := normalizeRoutingStrategy("fill-first-v2"); ok || got != "" {
		t.Fatalf("normalizeRoutingStrategy() expected rejection, got=%q ok=%v", got, ok)
	}
}
