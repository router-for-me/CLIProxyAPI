package thinking

import "testing"

func TestThinkingModeString(t *testing.T) {
	tests := []struct {
		mode ThinkingMode
		want string
	}{
		{ModeBudget, "budget"},
		{ModeLevel, "level"},
		{ModeNone, "none"},
		{ModeAuto, "auto"},
		{ThinkingMode(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.mode.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}
