package thinking

import "testing"

func TestMapToClaudeEffortPreservesAdvertisedLevels(t *testing.T) {
	tests := []struct {
		name, input string
		levels      []string
		want        string
		valid       bool
	}{
		{"native xhigh", "xhigh", []string{"low", "medium", "high", "xhigh", "max"}, "xhigh", true},
		{"native xhigh normalized", " XHIGH ", []string{"xhigh", "max"}, "xhigh", true},
		{"legacy xhigh maps to max", "xhigh", []string{"low", "medium", "high", "max"}, "max", true},
		{"legacy xhigh maps to high", "xhigh", []string{"low", "medium", "high"}, "high", true},
		{"native max", "max", []string{"high", "xhigh", "max"}, "max", true},
		{"legacy minimal", "minimal", []string{"low", "medium", "high"}, "low", true},
		{"empty", "", []string{"high"}, "", false},
		{"invalid", "bogus", []string{"high"}, "", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, valid := MapToClaudeEffort(test.input, test.levels)
			if got != test.want || valid != test.valid {
				t.Fatalf("got (%q,%v), want (%q,%v)", got, valid, test.want, test.valid)
			}
		})
	}
}
