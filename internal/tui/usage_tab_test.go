package tui

import (
	"strings"
	"testing"
)

func TestRenderTokenBreakdown(t *testing.T) {
	tests := []struct {
		name         string
		modelStats   map[string]any
		wantEmpty    bool
		wantContains string
	}{
		{
			name:       "no summary or details",
			modelStats: map[string]any{},
			wantEmpty:  true,
		},
		{
			name: "aggregated token breakdown",
			modelStats: map[string]any{
				"token_breakdown": map[string]any{
					"input_tokens":     float64(10),
					"output_tokens":    float64(20),
					"cached_tokens":    float64(3),
					"reasoning_tokens": float64(4),
				},
			},
			wantContains: "Input:10  Output:20  Cached:3  Reasoning:4",
		},
		{
			name: "fallback to request details",
			modelStats: map[string]any{
				"details": []any{
					map[string]any{
						"tokens": map[string]any{
							"input_tokens":     float64(7),
							"output_tokens":    float64(9),
							"cached_tokens":    float64(1),
							"reasoning_tokens": float64(2),
						},
					},
				},
			},
			wantContains: "Input:7  Output:9  Cached:1  Reasoning:2",
		},
	}

	prevLocale := CurrentLocale()
	SetLocale("en")
	t.Cleanup(func() {
		SetLocale(prevLocale)
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := usageTabModel{}
			result := m.renderTokenBreakdown(tt.modelStats)

			if tt.wantEmpty {
				if result != "" {
					t.Fatalf("renderTokenBreakdown() = %q, want empty string", result)
				}
				return
			}

			if result == "" {
				t.Fatal("renderTokenBreakdown() = empty, want non-empty string")
			}
			if tt.wantContains != "" && !strings.Contains(result, tt.wantContains) {
				t.Fatalf("renderTokenBreakdown() = %q, want to contain %q", result, tt.wantContains)
			}
		})
	}
}

func TestRenderLatencyBreakdown(t *testing.T) {
	tests := []struct {
		name         string
		modelStats   map[string]any
		wantEmpty    bool
		wantContains string
	}{
		{
			name:       "no summary or details",
			modelStats: map[string]any{},
			wantEmpty:  true,
		},
		{
			name: "aggregated latency summary",
			modelStats: map[string]any{
				"latency": map[string]any{
					"count":    float64(3),
					"total_ms": float64(900),
					"min_ms":   float64(200),
					"max_ms":   float64(400),
				},
			},
			wantEmpty:    false,
			wantContains: "avg 300ms  min 200ms  max 400ms",
		},
		{
			name: "empty details",
			modelStats: map[string]any{
				"details": []any{},
			},
			wantEmpty: true,
		},
		{
			name: "details with zero latency",
			modelStats: map[string]any{
				"details": []any{
					map[string]any{
						"latency_ms": float64(0),
					},
				},
			},
			wantEmpty: true,
		},
		{
			name: "single request with latency",
			modelStats: map[string]any{
				"details": []any{
					map[string]any{
						"latency_ms": float64(1500),
					},
				},
			},
			wantEmpty:    false,
			wantContains: "avg 1500ms  min 1500ms  max 1500ms",
		},
		{
			name: "multiple requests with varying latency",
			modelStats: map[string]any{
				"details": []any{
					map[string]any{
						"latency_ms": float64(100),
					},
					map[string]any{
						"latency_ms": float64(200),
					},
					map[string]any{
						"latency_ms": float64(300),
					},
				},
			},
			wantEmpty:    false,
			wantContains: "avg 200ms  min 100ms  max 300ms",
		},
		{
			name: "mixed valid and invalid latency values",
			modelStats: map[string]any{
				"details": []any{
					map[string]any{
						"latency_ms": float64(500),
					},
					map[string]any{
						"latency_ms": float64(0),
					},
					map[string]any{
						"latency_ms": float64(1500),
					},
				},
			},
			wantEmpty:    false,
			wantContains: "avg 1000ms  min 500ms  max 1500ms",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := usageTabModel{}
			result := m.renderLatencyBreakdown(tt.modelStats)

			if tt.wantEmpty {
				if result != "" {
					t.Errorf("renderLatencyBreakdown() = %q, want empty string", result)
				}
				return
			}

			if result == "" {
				t.Errorf("renderLatencyBreakdown() = empty, want non-empty string")
				return
			}

			if tt.wantContains != "" && !strings.Contains(result, tt.wantContains) {
				t.Errorf("renderLatencyBreakdown() = %q, want to contain %q", result, tt.wantContains)
			}
		})
	}
}

func TestUsageTimeTranslations(t *testing.T) {
	prevLocale := CurrentLocale()
	t.Cleanup(func() {
		SetLocale(prevLocale)
	})

	tests := []struct {
		locale string
		want   string
	}{
		{locale: "en", want: "Time"},
		{locale: "zh", want: "时间"},
	}

	for _, tt := range tests {
		t.Run(tt.locale, func(t *testing.T) {
			SetLocale(tt.locale)
			if got := T("usage_time"); got != tt.want {
				t.Fatalf("T(usage_time) = %q, want %q", got, tt.want)
			}
		})
	}
}
