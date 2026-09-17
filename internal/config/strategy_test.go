package config

import (
	"strings"
	"testing"
)

func TestNormalizePoolRoutingStrategy(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"   ", ""},
		{"round-robin", "round-robin"},
		{" Round-Robin ", "round-robin"},
		{"roundrobin", "round-robin"}, // global-normalizer shorthand
		{"rr", "round-robin"},         // global-normalizer shorthand
		{"weighted-round-robin", "weighted-round-robin"},
		{"weightedroundrobin", "weighted-round-robin"}, // global-normalizer shorthand
		{"wrr", "weighted-round-robin"},
		{"fill-first", "fill-first"},
		{"fillfirst", "fill-first"}, // global-normalizer shorthand
		{"ff", "fill-first"},
		{"priority", "fill-first"},  // alias, canonicalized
		{"failover", "round-robin"}, // alias, canonicalized
		{"power-of-two-choices", "power-of-two-choices"},
		{"poweroftwochoices", "power-of-two-choices"},  // global-normalizer shorthand
		{"p2c", "power-of-two-choices"},                // alias, canonicalized
		{"two-random-choices", "power-of-two-choices"}, // alias, canonicalized
		{"least-used", "least-used"},
		{"leastused", "least-used"},  // global-normalizer shorthand
		{"least-busy", "least-used"}, // alias, canonicalized
		{"bogus", ""},
	}
	for _, tc := range cases {
		if got := NormalizePoolRoutingStrategy(tc.in); got != tc.want {
			t.Fatalf("NormalizePoolRoutingStrategy(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestValidatePoolRoutingStrategy(t *testing.T) {
	for _, ok := range []string{"", "round-robin", "weighted-round-robin", "fill-first", "power-of-two-choices", "least-used"} {
		if err := ValidatePoolRoutingStrategy(ok); err != nil {
			t.Fatalf("ValidatePoolRoutingStrategy(%q) = %v, want nil", ok, err)
		}
	}
	if err := ValidatePoolRoutingStrategy("priority"); err == nil {
		t.Fatal("ValidatePoolRoutingStrategy(priority) = nil, want error (raw aliases rejected at input boundaries)")
	}
	if err := ValidatePoolRoutingStrategy("p2c"); err == nil {
		t.Fatal("ValidatePoolRoutingStrategy(p2c) = nil, want error (raw aliases rejected at input boundaries)")
	}
	if err := ValidatePoolRoutingStrategy("nope"); err == nil {
		t.Fatal("ValidatePoolRoutingStrategy(nope) = nil, want error")
	}
	// The error message lists every canonical value so operators see the
	// full accepted set without opening the source.
	err := ValidatePoolRoutingStrategy("nope")
	for _, canonical := range []string{"round-robin", "weighted-round-robin", "fill-first", "power-of-two-choices", "least-used"} {
		if !strings.Contains(err.Error(), canonical) {
			t.Fatalf("ValidatePoolRoutingStrategy(nope) error = %v, want it to list %q", err, canonical)
		}
	}
}

func TestNormalizeGlobalRoutingStrategy(t *testing.T) {
	cases := map[string]string{
		"round-robin": GlobalStrategyRoundRobin,
		"rr":          GlobalStrategyRoundRobin,
		"fill-first":  GlobalStrategyFillFirst,
		"ff":          GlobalStrategyFillFirst,
		"weighted":    GlobalStrategyWeighted,
		"w":           GlobalStrategyWeighted,
		"headroom":    GlobalStrategyHeadroom,
		"hr":          GlobalStrategyHeadroom,
		"  weighted ": GlobalStrategyWeighted,
		"unknown":     "",
		"":            "",
		"   ":         "",
		"Round-Robin": GlobalStrategyRoundRobin,
		"FILL-FIRST":  GlobalStrategyFillFirst,
	}
	for in, want := range cases {
		if got := NormalizeGlobalRoutingStrategy(in); got != want {
			t.Errorf("NormalizeGlobalRoutingStrategy(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateGlobalRoutingStrategy(t *testing.T) {
	if err := ValidateGlobalRoutingStrategy(GlobalStrategyHeadroom); err != nil {
		t.Errorf("headroom should validate: %v", err)
	}
	if err := ValidateGlobalRoutingStrategy(GlobalStrategyFillFirst); err != nil {
		t.Errorf("fill-first should validate: %v", err)
	}
	if err := ValidateGlobalRoutingStrategy(GlobalStrategyWeighted); err != nil {
		t.Errorf("weighted should validate: %v", err)
	}
	if err := ValidateGlobalRoutingStrategy(GlobalStrategyRoundRobin); err != nil {
		t.Errorf("round-robin should validate: %v", err)
	}
	if err := ValidateGlobalRoutingStrategy("bogus"); err == nil {
		t.Errorf("bogus should fail validation")
	}
	if err := ValidateGlobalRoutingStrategy(""); err != nil {
		t.Errorf("empty should validate (unset is allowed): %v", err)
	}
}

func TestPoolStrategyFillFirstUnchanged(t *testing.T) {
	// Ensure we did not break the existing pool-row canonical value.
	if NormalizePoolRoutingStrategy("ff") != PoolStrategyFillFirst {
		t.Errorf("pool fill-first alias regressed")
	}
}
