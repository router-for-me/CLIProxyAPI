package thinking

import (
	"testing"
)

func TestConvertLevelToBudget(t *testing.T) {
	cases := []struct {
		level  string
		want   int
		wantOk bool
	}{
		{"none", 0, true},
		{"auto", -1, true},
		{"minimal", 512, true},
		{"low", 1024, true},
		{"medium", 8192, true},
		{"high", 24576, true},
		{"xhigh", 32768, true},
		{"UNKNOWN", 0, false},
	}

	for _, tc := range cases {
		got, ok := ConvertLevelToBudget(tc.level)
		if got != tc.want || ok != tc.wantOk {
			t.Errorf("ConvertLevelToBudget(%q) = (%d, %v), want (%d, %v)", tc.level, got, ok, tc.want, tc.wantOk)
		}
	}
}

func TestConvertBudgetToLevel(t *testing.T) {
	cases := []struct {
		budget int
		want   string
		wantOk bool
	}{
		{-2, "", false},
		{-1, "auto", true},
		{0, "none", true},
		{100, "minimal", true},
		{600, "low", true},
		{2000, "medium", true},
		{10000, "high", true},
		{30000, "xhigh", true},
	}

	for _, tc := range cases {
		got, ok := ConvertBudgetToLevel(tc.budget)
		if got != tc.want || ok != tc.wantOk {
			t.Errorf("ConvertBudgetToLevel(%d) = (%q, %v), want (%q, %v)", tc.budget, got, ok, tc.want, tc.wantOk)
		}
	}
}
