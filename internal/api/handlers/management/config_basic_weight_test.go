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

func TestNormalizeRoutingStrategyPowerOfTwoChoicesAndLeastUsed(t *testing.T) {
	for input, want := range map[string]string{
		"power-of-two-choices": "power-of-two-choices",
		"poweroftwochoices":    "power-of-two-choices",
		"p2c":                  "power-of-two-choices",
		"two-random-choices":   "power-of-two-choices",
		" P2C ":                "power-of-two-choices",
		"least-used":           "least-used",
		"leastused":            "least-used",
		"least-busy":           "least-used",
		"Least-Busy":           "least-used",
	} {
		got, ok := normalizeRoutingStrategy(input)
		if !ok || got != want {
			t.Fatalf("normalizeRoutingStrategy(%q) = %q, %v; want %q, true", input, got, ok, want)
		}
	}
	for _, bogus := range []string{"bogus", "p3c", "leastused-"} {
		if got, ok := normalizeRoutingStrategy(bogus); ok {
			t.Fatalf("normalizeRoutingStrategy(%q) = %q, %v; want rejection", bogus, got, ok)
		}
	}
}
