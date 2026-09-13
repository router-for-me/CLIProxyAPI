package config

import "testing"

func TestNormalizeRoutingStrategy(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "", want: "round-robin"},
		{input: " round-robin ", want: "round-robin"},
		{input: "weighted-round-robin", want: "weighted-round-robin"},
		{input: "weightedroundrobin", want: "weighted-round-robin"},
		{input: "WRR", want: "weighted-round-robin"},
		{input: "fill-first", want: "fill-first"},
		{input: "fillfirst", want: "fill-first"},
		{input: "FF", want: "fill-first"},
		{input: "unknown", want: "round-robin"},
	}

	for _, testCase := range tests {
		if got := NormalizeRoutingStrategy(testCase.input); got != testCase.want {
			t.Errorf("NormalizeRoutingStrategy(%q) = %q, want %q", testCase.input, got, testCase.want)
		}
	}
}
