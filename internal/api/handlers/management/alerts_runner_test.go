package management

import "testing"

func TestShouldWarnDrops(t *testing.T) {
	tests := []struct {
		name string
		prev int64
		cur  int64
		want bool
	}{
		{
			// Steady state: the anti-spam guarantee — a quiescent drain must
			// NOT warn every sweep.
			name: "steady_state_no_warn",
			prev: 5,
			cur:  5,
			want: false,
		},
		{
			name: "increased_warns",
			prev: 5,
			cur:  7,
			want: true,
		},
		{
			// Counter reset/overflow: a decrease must not warn.
			name: "decreased_no_warn",
			prev: 7,
			cur:  5,
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldWarnDrops(tt.prev, tt.cur); got != tt.want {
				t.Errorf("shouldWarnDrops(%d, %d) = %v, want %v", tt.prev, tt.cur, got, tt.want)
			}
		})
	}
}
