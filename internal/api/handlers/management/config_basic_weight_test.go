package management

import "testing"

func TestNormalizeRoutingStrategyWeightedRoundRobin(t *testing.T) {
	for _, input := range []string{"weighted-round-robin", "weightedroundrobin", "wrr"} {
		got, ok := normalizeRoutingStrategy(input)
		if !ok || got != "weighted-round-robin" {
			t.Fatalf("normalizeRoutingStrategy(%q) = %q, %v; want weighted-round-robin, true", input, got, ok)
		}
	}
}

func TestNormalizeRoutingStrategySequential(t *testing.T) {
	for _, input := range []string{"sequential", "Sequential-Fill"} {
		got, ok := normalizeRoutingStrategy(input)
		if !ok || got != "sequential" {
			t.Fatalf("normalizeRoutingStrategy(%q) = %q, %v; want sequential, true", input, got, ok)
		}
	}
}
