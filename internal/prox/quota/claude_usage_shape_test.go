package quota

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Trimmed from a live oauth/usage response (2026-10-19). seven_day_breakdown
// is not a window and must not fail the poll.
const claudeUsageLive = `{
  "five_hour": {"utilization": 37.0, "resets_at": "2026-10-19T17:00:00.122005+00:00", "limit_dollars": null, "used_dollars": null, "remaining_dollars": null, "locked_reason": null},
  "seven_day": {"utilization": 100.0, "resets_at": "2026-10-20T11:00:00.122029+00:00", "limit_dollars": null, "used_dollars": null, "remaining_dollars": null, "locked_reason": null},
  "seven_day_oauth_apps": null, "seven_day_opus": null, "seven_day_sonnet": null, "seven_day_cowork": null, "seven_day_omelette": null,
  "limits": [
    {"kind": "session", "group": "session", "percent": 37, "severity": "normal", "resets_at": "2026-10-19T17:00:00.122005+00:00", "scope": null, "is_active": false},
    {"kind": "weekly_all", "group": "weekly", "percent": 100, "severity": "critical", "resets_at": "2026-10-20T11:00:00.122029+00:00", "scope": null, "is_active": true}
  ],
  "seven_day_breakdown": {"as_of": "2026-10-19T16:40:54.140729+00:00", "window_started_at": "2026-10-13T11:00:00.122029+00:00", "rows": [{"key": "claude_code", "display_name": "Claude Code", "percent": 100}]},
  "extra_usage": {"is_enabled": false}
}`

func TestParseClaudeUsageLiveShape(t *testing.T) {
	snap, err := ParseClaudeUsage([]byte(claudeUsageLive), claudeFableNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Windows) != 2 {
		t.Fatalf("want five-hour and weekly only, got %+v", snap.Windows)
	}
	if w, ok := snap.Window("weekly"); !ok || w.UsedPercent != 100 {
		t.Fatalf("weekly: %+v", w)
	}
	if w, ok := snap.Window("five_hour"); !ok || w.UsedPercent != 37 {
		t.Fatalf("five-hour: %+v", w)
	}
}

func TestClaudePollSendsUserAgent(t *testing.T) {
	var ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(claudeUsageLive))
	}))
	defer srv.Close()
	f := NewHTTPFetcher()
	defer f.Close()
	f.ClaudeURL = srv.URL
	if _, err := f.Fetch(context.Background(), Credential{Type: "claude", AccessToken: "t"}); err != nil {
		t.Fatal(err)
	}
	if ua != claudeUserAgent {
		t.Fatalf("User-Agent %q, want %q", ua, claudeUserAgent)
	}
}
