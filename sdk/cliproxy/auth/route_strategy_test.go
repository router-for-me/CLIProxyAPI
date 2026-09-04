package auth

import (
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestRouteStrategyFromMetadata(t *testing.T) {
	cases := []struct {
		name string
		meta map[string]any
		want schedulerStrategy
	}{
		{
			name: "nil_meta_inherits_global",
			meta: nil,
			want: schedulerStrategyCurrent,
		},
		{
			name: "no_key_inherits_global",
			meta: map[string]any{"other": "x"},
			want: schedulerStrategyCurrent,
		},
		{
			name: "priority_maps_to_fill_first",
			meta: map[string]any{cliproxyexecutor.RouteStrategyMetadataKey: "priority"},
			want: schedulerStrategyFillFirst,
		},
		{
			name: "failover_maps_to_round_robin",
			meta: map[string]any{cliproxyexecutor.RouteStrategyMetadataKey: "failover"},
			want: schedulerStrategyRoundRobin,
		},
		{
			name: "case_insensitive_priority",
			meta: map[string]any{cliproxyexecutor.RouteStrategyMetadataKey: "PRIORITY"},
			want: schedulerStrategyFillFirst,
		},
		{
			name: "whitespace_trimmed_failover",
			meta: map[string]any{cliproxyexecutor.RouteStrategyMetadataKey: " failover "},
			want: schedulerStrategyRoundRobin,
		},
		{
			name: "bytes_payload_supported",
			meta: map[string]any{cliproxyexecutor.RouteStrategyMetadataKey: []byte("priority")},
			want: schedulerStrategyFillFirst,
		},
		{
			name: "unknown_strategy_inherits_global",
			meta: map[string]any{cliproxyexecutor.RouteStrategyMetadataKey: "weighted"},
			want: schedulerStrategyCurrent,
		},
		{
			name: "empty_string_inherits_global",
			meta: map[string]any{cliproxyexecutor.RouteStrategyMetadataKey: ""},
			want: schedulerStrategyCurrent,
		},
		{
			name: "canonical_round_robin_maps_to_round_robin",
			meta: map[string]any{cliproxyexecutor.RouteStrategyMetadataKey: "round-robin"},
			want: schedulerStrategyRoundRobin,
		},
		{
			name: "canonical_fill_first_maps_to_fill_first",
			meta: map[string]any{cliproxyexecutor.RouteStrategyMetadataKey: "fill-first"},
			want: schedulerStrategyFillFirst,
		},
		{
			name: "canonical_weighted_round_robin_maps_to_weighted",
			meta: map[string]any{cliproxyexecutor.RouteStrategyMetadataKey: "weighted-round-robin"},
			want: schedulerStrategyWeightedRoundRobin,
		},
		{
			name: "canonical_values_case_insensitive",
			meta: map[string]any{cliproxyexecutor.RouteStrategyMetadataKey: " Fill-First "},
			want: schedulerStrategyFillFirst,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := routeStrategyFromMetadata(tc.meta); got != tc.want {
				t.Fatalf("routeStrategyFromMetadata(%v) = %d, want %d", tc.meta, got, tc.want)
			}
		})
	}
}
