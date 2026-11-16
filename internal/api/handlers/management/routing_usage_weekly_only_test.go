package management

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRoutingUsageWeeklyOnlySchedule(t *testing.T) {
	fixture, err := os.ReadFile("testdata/claude-weekly-only-usage.json")
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(fixture, &payload); err != nil {
		t.Fatal(err)
	}
	if string(payload["five_hour"]) != "null" || !strings.Contains(string(payload["seven_day"]), `"utilization": 0.0`) {
		t.Fatal("weekly-only fixture shape changed")
	}
	now := time.Date(2029, 12, 31, 2, 3, 4, 0, time.UTC)
	weekly := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	five := weekly.Add(2 * time.Hour)
	for _, tc := range []struct {
		name, old, replacement string
		valid                  bool
		five                   time.Time
	}{
		{name: "explicit null", valid: true},
		{name: "whitespace null", old: `"five_hour": null`, replacement: `"five_hour":  null `, valid: true},
		{name: "present session", old: `"five_hour": null`, replacement: `"five_hour": {"utilization":23,"resets_at":"2030-01-01T02:00:00Z"}`, valid: true, five: five},
		{name: "missing session", old: `"five_hour": null,`, replacement: ``},
		{name: "malformed session", old: `"five_hour": null`, replacement: `"five_hour": {}`},
		{name: "null session utilization", old: `"five_hour": null`, replacement: `"five_hour": {"utilization":null}`},
		{name: "negative session utilization", old: `"five_hour": null`, replacement: `"five_hour": {"utilization":-1}`},
		{name: "null weekly", old: `"seven_day": {`, replacement: `"seven_day": null, "ignored_weekly": {`},
		{name: "missing weekly", old: `"seven_day": {`, replacement: `"ignored_weekly": {`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := string(fixture)
			if tc.old != "" {
				body = strings.Replace(body, tc.old, tc.replacement, 1)
				if body == string(fixture) {
					t.Fatal("fixture mutation did not match")
				}
			}
			schedule, ok := routingUsageSchedule("claude", []byte(body), now)
			if ok != tc.valid {
				t.Fatalf("valid=%v want %v", ok, tc.valid)
			}
			if ok && (!schedule.WeeklyResetAt.Equal(weekly) || !schedule.FiveHourResetAt.Equal(tc.five) || !schedule.ObservedAt.Equal(now)) {
				t.Fatalf("incorrect weekly-only schedule: %+v", schedule)
			}
		})
	}
}
