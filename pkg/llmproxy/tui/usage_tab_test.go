package tui

import "testing"

func TestResolveUsageTotalTokens_PrefersTopLevelValue(t *testing.T) {
	usageMap := map[string]any{
		"total_tokens": float64(123),
		"apis": map[string]any{
			"kimi": map[string]any{
				"models": map[string]any{
					"kimi-k2.5": map[string]any{"total_tokens": float64(999)},
				},
			},
		},
	}

	if got := resolveUsageTotalTokens(usageMap); got != 123 {
		t.Fatalf("resolveUsageTotalTokens() = %d, want 123", got)
	}
}

func TestResolveUsageTotalTokens_FallsBackToModelTotals(t *testing.T) {
	usageMap := map[string]any{
		"total_tokens": float64(0),
		"apis": map[string]any{
			"kimi": map[string]any{
				"models": map[string]any{
					"kimi-k2.5": map[string]any{"total_tokens": float64(40)},
					"kimi-k2.6": map[string]any{"total_tokens": float64(60)},
				},
			},
		},
	}

	if got := resolveUsageTotalTokens(usageMap); got != 100 {
		t.Fatalf("resolveUsageTotalTokens() = %d, want 100", got)
	}
}

func TestResolveUsageTotalTokens_FallsBackToDetailBreakdown(t *testing.T) {
	usageMap := map[string]any{
		"total_tokens": float64(0),
		"apis": map[string]any{
			"kimi": map[string]any{
				"models": map[string]any{
					"kimi-k2.5": map[string]any{
						"details": []any{
							map[string]any{
								"prompt_tokens":     float64(10),
								"completion_tokens": float64(15),
								"cached_tokens":     float64(5),
								"reasoning_tokens":  float64(3),
							},
							map[string]any{
								"tokens": map[string]any{
									"input_tokens":     float64(7),
									"output_tokens":    float64(8),
									"cached_tokens":    float64(1),
									"reasoning_tokens": float64(1),
								},
							},
						},
					},
				},
			},
		},
	}

	// 10+15+5+3 + 7+8+1+1
	if got := resolveUsageTotalTokens(usageMap); got != 50 {
		t.Fatalf("resolveUsageTotalTokens() = %d, want 50", got)
	}
}

func TestUsageTokenBreakdown_CombinesNestedAndFlatFields(t *testing.T) {
	detail := map[string]any{
		"prompt_tokens":     float64(11),
		"completion_tokens": float64(12),
		"tokens": map[string]any{
			"input_tokens":     float64(1),
			"output_tokens":    float64(2),
			"cached_tokens":    float64(3),
			"reasoning_tokens": float64(4),
		},
	}

	input, output, cached, reasoning := usageTokenBreakdown(detail)
	if input != 12 || output != 14 || cached != 3 || reasoning != 4 {
		t.Fatalf("usageTokenBreakdown() = (%d,%d,%d,%d), want (12,14,3,4)", input, output, cached, reasoning)
	}
}
