package tui

import (
	"strings"
	"testing"
)

func TestDashboardRenderShowsFailoverCard(t *testing.T) {
	restoreLocale := CurrentLocale()
	SetLocale("en")
	t.Cleanup(func() {
		SetLocale(restoreLocale)
	})

	dashboard := dashboardModel{
		client: &Client{baseURL: "http://127.0.0.1:8317"},
		width:  160,
	}
	content := dashboard.renderDashboard(nil, map[string]any{
		"usage": map[string]any{
			"total_requests":  float64(12),
			"success_count":   float64(11),
			"failure_count":   float64(1),
			"total_failovers": float64(4),
			"total_tokens":    float64(256),
		},
	}, nil, nil)

	if !strings.Contains(content, T("total_failovers")) {
		t.Fatalf("expected dashboard content to contain %q: %s", T("total_failovers"), content)
	}
	if !strings.Contains(content, "4") {
		t.Fatalf("expected dashboard content to contain failover count: %s", content)
	}
}
