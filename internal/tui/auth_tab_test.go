package tui

import (
	"strings"
	"testing"
)

func TestAuthTabRenderDetailShowsCodexPlanAndQuota(t *testing.T) {
	m := authTabModel{}
	out := m.renderDetail(map[string]any{
		"name":                    "codex-user.json",
		"type":                    "codex",
		"email":                   "user@example.com",
		"plan_type":               "plus",
		"quota_status":            "recovering",
		"quota_5h_amount":         "12 / 100 remaining",
		"quota_5h_recover_in":     "1h30m0s",
		"quota_weekly_amount":     "700 / 1000 remaining",
		"quota_weekly_recover_in": "72h0m0s",
		"quota_recover_in":        "1h30m0s",
		"quota_next_recover_at":   "2026-04-26T12:00:00Z",
		"quota_backoff_level":     2,
		"subscription_until":      "2026-05-01T00:00:00Z",
	})

	for _, want := range []string{
		"Plan Type",
		"plus",
		"Quota Status",
		"recovering",
		"5h Quota",
		"12 / 100 remaining",
		"5h Recover",
		"Weekly Quota",
		"700 / 1000 remaining",
		"Weekly Recover",
		"72h0m0s",
		"Last Recover",
		"1h30m0s",
		"Recover At",
		"2026-04-26T12:00:00Z",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("renderDetail() missing %q in:\n%s", want, out)
		}
	}
}

func TestAuthTabRenderContentShowsCodexQuotaSummaryInRows(t *testing.T) {
	m := authTabModel{
		width: 120,
		files: []map[string]any{
			{
				"name":                "codex-plus.json",
				"type":                "codex",
				"email":               "plus@example.com",
				"plan_type":           "plus",
				"quota_5h_amount":     "12 / 100 remaining",
				"quota_weekly_amount": "700 / 1000 remaining",
			},
		},
	}

	out := m.renderContent()
	for _, want := range []string{
		"Plus",
		"5h:12/100",
		"wk:700/1000",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("renderContent() missing %q in:\n%s", want, out)
		}
	}
}
