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
