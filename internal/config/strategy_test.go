package config

import "testing"

func TestNormalizePoolRoutingStrategy(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"   ", ""},
		{"round-robin", "round-robin"},
		{" Round-Robin ", "round-robin"},
		{"weighted-round-robin", "weighted-round-robin"},
		{"wrr", "weighted-round-robin"},
		{"fill-first", "fill-first"},
		{"ff", "fill-first"},
		{"priority", "fill-first"},  // alias, canonicalized
		{"failover", "round-robin"}, // alias, canonicalized
		{"bogus", ""},
	}
	for _, tc := range cases {
		if got := NormalizePoolRoutingStrategy(tc.in); got != tc.want {
			t.Fatalf("NormalizePoolRoutingStrategy(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestValidatePoolRoutingStrategy(t *testing.T) {
	for _, ok := range []string{"", "round-robin", "weighted-round-robin", "fill-first"} {
		if err := ValidatePoolRoutingStrategy(ok); err != nil {
			t.Fatalf("ValidatePoolRoutingStrategy(%q) = %v, want nil", ok, err)
		}
	}
	if err := ValidatePoolRoutingStrategy("priority"); err == nil {
		t.Fatal("ValidatePoolRoutingStrategy(priority) = nil, want error (raw aliases rejected at input boundaries)")
	}
	if err := ValidatePoolRoutingStrategy("nope"); err == nil {
		t.Fatal("ValidatePoolRoutingStrategy(nope) = nil, want error")
	}
}
