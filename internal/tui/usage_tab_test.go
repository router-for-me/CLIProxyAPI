package tui

import (
	"strings"
	"testing"
)

func TestRenderLatencyBreakdown(t *testing.T) {
	tests := []struct {
		name         string
		modelStats   map[string]any
		wantEmpty    bool
		wantContains string
	}{
		{
			name:       "no details",
			modelStats: map[string]any{},
			wantEmpty:  true,
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
