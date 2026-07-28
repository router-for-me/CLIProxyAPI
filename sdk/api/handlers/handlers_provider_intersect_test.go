package handlers

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

func TestOrderProvidersByPriority(t *testing.T) {
	cases := []struct {
		name       string
		providers  []string
		priorities []store.ProviderPriority
		want       []string
	}{
		{
			name:       "descending_priority_orders_primary_first",
			providers:  []string{"opencode", "cometapi", "semutssh"},
			priorities: []store.ProviderPriority{{Provider: "opencode", Priority: 10}, {Provider: "semutssh", Priority: 5}, {Provider: "cometapi", Priority: 1}},
			want:       []string{"opencode", "semutssh", "cometapi"},
		},
		{
			name:       "unlisted_defaults_to_zero_so_stays_last",
			providers:  []string{"opencode", "cometapi"},
			priorities: []store.ProviderPriority{{Provider: "cometapi", Priority: 7}},
			want:       []string{"cometapi", "opencode"},
		},
		{
			name:       "same_priority_preserves_registry_order",
			providers:  []string{"opencode", "cometapi", "semutssh"},
			priorities: []store.ProviderPriority{{Provider: "opencode", Priority: 5}, {Provider: "semutssh", Priority: 5}},
			want:       []string{"opencode", "semutssh", "cometapi"},
		},
		{
			name:       "case_insensitive_provider_match",
			providers:  []string{"OpenCode", "CometAPI"},
			priorities: []store.ProviderPriority{{Provider: "opencode", Priority: 1}, {Provider: "cometapi", Priority: 9}},
			want:       []string{"CometAPI", "OpenCode"},
		},
		{
			name:       "no_priorities_returns_input_unchanged",
			providers:  []string{"opencode", "cometapi"},
			priorities: nil,
			want:       []string{"opencode", "cometapi"},
		},
		{
			name:       "single_provider_returns_unchanged",
			providers:  []string{"opencode"},
			priorities: []store.ProviderPriority{{Provider: "opencode", Priority: 99}},
			want:       []string{"opencode"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := orderProvidersByPriority(tc.providers, tc.priorities)
			if len(got) != len(tc.want) {
				t.Fatalf("orderProvidersByPriority = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("orderProvidersByPriority[%d] = %q, want %q (got=%v)", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

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
