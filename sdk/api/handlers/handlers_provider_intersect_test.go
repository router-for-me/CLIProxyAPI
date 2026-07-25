package handlers

import (
	"testing"
)

func TestIntersectProviders(t *testing.T) {
	cases := []struct {
		name      string
		providers []string
		pinned    []string
		want      []string
	}{
		{
			name:      "subset_intersection_preserves_order",
			providers: []string{"semutssh", "opencode", "cometapi"},
			pinned:    []string{"opencode", "cometapi"},
			want:      []string{"opencode", "cometapi"},
		},
		{
			name:      "single_pin_no_failover_set",
			providers: []string{"semutssh", "opencode", "cometapi"},
			pinned:    []string{"semutssh"},
			want:      []string{"semutssh"},
		},
		{
			name:      "case_insensitive",
			providers: []string{"OpenCode", "CometAPI"},
			pinned:    []string{"opencode", "cometapi"},
			want:      []string{"OpenCode", "CometAPI"},
		},
		{
			name:      "empty_intersection_returns_nil",
			providers: []string{"opencode", "cometapi"},
			pinned:    []string{"semutssh"},
			want:      nil,
		},
		{
			name:      "no_pin_returns_all",
			providers: []string{"opencode", "cometapi"},
			pinned:    nil,
			want:      []string{"opencode", "cometapi"},
		},
		{
			name:      "dedupes_provider_duplicates",
			providers: []string{"opencode", "opencode", "cometapi"},
			pinned:    []string{"opencode", "cometapi"},
			want:      []string{"opencode", "cometapi"},
		},
		{
			name:      "trims_whitespace",
			providers: []string{" opencode ", "cometapi"},
			pinned:    []string{"opencode"},
			want:      []string{" opencode "},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := intersectProviders(tc.providers, tc.pinned)
			if len(got) != len(tc.want) {
				t.Fatalf("intersectProviders(%v, %v) = %v, want %v", tc.providers, tc.pinned, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("intersectProviders(%v, %v)[%d] = %q, want %q", tc.providers, tc.pinned, i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestIntersectProvidersEmptyProviders(t *testing.T) {
	if got := intersectProviders(nil, []string{"opencode"}); got != nil {
		t.Fatalf("intersectProviders(nil, pinned) = %v, want nil", got)
	}
}
