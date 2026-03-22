package tui

import (
	"strings"
	"testing"
)

func TestUsageTabRenderContentShowsFailovers(t *testing.T) {
	restoreLocale := CurrentLocale()
	SetLocale("en")
	t.Cleanup(func() {
		SetLocale(restoreLocale)
	})

	modelStats := map[string]any{
		"total_requests": float64(5),
		"total_tokens":   float64(100),
		"details": []any{
			map[string]any{
				"requested_model": "gemini-2.5-pro",
				"actual_model":    "gemini-2.0-flash",
				"tokens": map[string]any{
					"input_tokens":  float64(40),
					"output_tokens": float64(60),
				},
			},
		},
	}

	tab := usageTabModel{
		width: 160,
		usage: map[string]any{
			"usage": map[string]any{
				"total_requests":  float64(5),
				"success_count":   float64(5),
				"failure_count":   float64(0),
				"total_failovers": float64(1),
				"total_tokens":    float64(100),
				"apis": map[string]any{
					"test-key": map[string]any{
						"total_requests":  float64(5),
						"total_failovers": float64(1),
						"total_tokens":    float64(100),
						"models": map[string]any{
							"gemini-2.5-pro": modelStats,
						},
					},
				},
			},
		},
	}

	content := tab.renderContent()
	if !strings.Contains(content, T("usage_total_failovers")) {
		t.Fatalf("expected usage content to contain %q: %s", T("usage_total_failovers"), content)
	}
	if !strings.Contains(content, T("failovers")) {
		t.Fatalf("expected usage content to contain %q: %s", T("failovers"), content)
	}
	if !strings.Contains(content, "1") {
		t.Fatalf("expected usage content to contain failover count: %s", content)
	}
}
