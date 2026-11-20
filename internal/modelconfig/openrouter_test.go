package modelconfig

import (
	"slices"
	"testing"
)

func TestOpenRouterThinkingSupport(t *testing.T) {
	cases := []struct {
		name       string
		efforts    []string
		wantLevels []string
		wantZero   bool
		wantNil    bool
	}{
		{name: "claude haiku 5.5", efforts: []string{"max", "xhigh", "high", "medium", "low", "none"},
			wantLevels: []string{"low", "medium", "high", "xhigh", "max"}, wantZero: true},
		{name: "reasoning always on", efforts: []string{"max", "xhigh", "high", "medium", "low"},
			wantLevels: []string{"low", "medium", "high", "xhigh", "max"}},
		{name: "minimal and gaps", efforts: []string{"High", " minimal ", "none", "xhigh"},
			wantLevels: []string{"minimal", "high", "xhigh"}, wantZero: true},
		{name: "unknown effort sorts last", efforts: []string{"turbo", "high", "low"},
			wantLevels: []string{"low", "high", "turbo"}},
		{name: "only none", efforts: []string{"none"}, wantNil: true},
		{name: "nothing published", efforts: nil, wantNil: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := OpenRouterThinkingSupport(tc.efforts)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("got %+v, want nil so the caller keeps its default", got)
				}
				return
			}
			if got == nil {
				t.Fatal("got nil")
			}
			if !slices.Equal(got.Levels, tc.wantLevels) || got.ZeroAllowed != tc.wantZero {
				t.Fatalf("got levels %v zero %v, want %v zero %v", got.Levels, got.ZeroAllowed, tc.wantLevels, tc.wantZero)
			}
		})
	}
}
