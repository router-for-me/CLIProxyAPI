package auth

import "testing"

func TestParsePriority_DefaultAndClamp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want int
	}{
		{name: "empty", raw: "", want: PriorityDefault},
		{name: "whitespace", raw: "  ", want: PriorityDefault},
		{name: "invalid", raw: "nope", want: PriorityDefault},
		{name: "zero", raw: "0", want: PriorityDefault},
		{name: "low", raw: "-1", want: PriorityFallback},
		{name: "fallback", raw: "1", want: PriorityFallback},
		{name: "default", raw: "2", want: PriorityDefault},
		{name: "preferred", raw: "3", want: PriorityPreferred},
		{name: "high", raw: "10", want: PriorityPreferred},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ParsePriority(tt.raw); got != tt.want {
				t.Fatalf("ParsePriority(%q) = %d, want %d", tt.raw, got, tt.want)
			}
		})
	}
}
