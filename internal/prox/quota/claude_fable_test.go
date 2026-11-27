package quota

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

var claudeFableNow = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func TestParseClaudeFableWeeklyLimit(t *testing.T) {
	raw := []byte(`{"seven_day":{"utilization":20,"resets_at":"2026-10-26T12:00:00Z"},"limits":[{"kind":"weekly_scoped","scope":{"model":{"display_name":"Fable"}},"percent":24,"resets_at":"2026-10-25T12:00:00Z"},{"kind":"weekly_scoped","scope":{"model":{"display_name":"FABLE 5"}},"percent":73,"resets_at":"2026-10-24T12:00:00Z","is_active":true},{"kind":"weekly_scoped","scope":{"model":{"display_name":"Fable next"}},"percent":99,"resets_at":"2026-10-23T12:00:00Z","is_active":true},{"kind":"weekly","scope":{"model":{"display_name":"Fable"}},"percent":98,"resets_at":"2026-10-22T12:00:00Z"}]}`)
	snap, err := ParseClaudeUsage(raw, claudeFableNow)
	if err != nil {
		t.Fatal(err)
	}
	fable, ok := snap.Window("model_weekly:fable")
	if !ok || fable.UsedPercent != 73 || fable.Kind != ModelWeekly || fable.Family != "fable" || fable.Source != SourcePoll || !fable.ObservedAt.Equal(claudeFableNow) {
		t.Fatalf("Fable window: %+v, present=%v", fable, ok)
	}
	wantReset := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	if fable.ResetsAt == nil || !fable.ResetsAt.Equal(wantReset) {
		t.Fatalf("Fable reset: %v, want %v", fable.ResetsAt, wantReset)
	}
	if len(snap.Windows) != 2 {
		t.Fatalf("unexpected windows: %+v", snap.Windows)
	}
}

func TestParseClaudeFablePollPrefersTopLevelWindow(t *testing.T) {
	raw := []byte(`{"seven_day":{"utilization":20,"resets_at":"2026-10-26T12:00:00Z"},"seven_day_fable":{"utilization":35,"resets_at":"2026-10-25T12:00:00Z"},"limits":[{"kind":"weekly_scoped","scope":{"model":{"display_name":"Fable 5"}},"percent":99,"resets_at":"2026-10-24T12:00:00Z","is_active":true}]}`)
	snap, err := ParseClaudeUsage(raw, claudeFableNow)
	if err != nil {
		t.Fatal(err)
	}
	fable, ok := snap.Window("model_weekly:fable")
	if !ok || fable.UsedPercent != 35 || len(snap.Windows) != 2 {
		t.Fatalf("top-level Fable window was not preferred: %+v", snap.Windows)
	}
}

func TestParseClaudeFablePassiveHeaders(t *testing.T) {
	reset := strconv.FormatInt(claudeFableNow.Add(48*time.Hour).Unix(), 10)
	headers := http.Header{
		"Anthropic-Ratelimit-Unified-7d_oi-Utilization": {"0.64"},
		"Anthropic-Ratelimit-Unified-7d_oi-Reset":       {reset},
	}
	windows := ParseClaudeHeaders(headers, claudeFableNow)
	if len(windows) != 1 {
		t.Fatalf("Fable header windows: %+v", windows)
	}
	fable := windows[0]
	if fable.Key() != "model_weekly:fable" || fable.UsedPercent != 64 || fable.Source != SourcePassive || !fable.ObservedAt.Equal(claudeFableNow) {
		t.Fatalf("Fable header window: %+v", fable)
	}
	wantReset := claudeFableNow.Add(48 * time.Hour)
	if fable.ResetsAt == nil || !fable.ResetsAt.Equal(wantReset) {
		t.Fatalf("Fable header reset: %v, want %v", fable.ResetsAt, wantReset)
	}
	for _, bad := range []http.Header{
		{"Anthropic-Ratelimit-Unified-7d_oi-Utilization": {"0.64"}},
		{"Anthropic-Ratelimit-Unified-7d_oi-Utilization": {"1.2"}, "Anthropic-Ratelimit-Unified-7d_oi-Reset": {reset}},
	} {
		if got := ParseClaudeHeaders(bad, claudeFableNow); len(got) != 0 {
			t.Fatalf("accepted invalid Fable headers %v: %+v", bad, got)
		}
	}
}

func TestClaudeFableWindowExhaustsOnlyFableRequests(t *testing.T) {
	raw := []byte(`{"seven_day":{"utilization":20,"resets_at":"2026-10-26T12:00:00Z"},"limits":[{"kind":"weekly_scoped","scope":{"model":{"display_name":"Fable 5"}},"percent":100,"resets_at":"2026-10-24T12:00:00Z","is_active":true}]}`)
	snap, err := ParseClaudeUsage(raw, claudeFableNow)
	if err != nil {
		t.Fatal(err)
	}
	fable := Assess(snap, "claude-fable-5", claudeFableNow, time.Hour)
	if fable.State != Exhausted || fable.Reason != "model_weekly:fable_exhausted" {
		t.Fatalf("Fable request assessment: %+v", fable)
	}
	sonnet := Assess(snap, "claude-sonnet-4", claudeFableNow, time.Hour)
	if sonnet.State != Ready {
		t.Fatalf("unrelated model assessment: %+v", sonnet)
	}
}

func TestParseClaudeFableNullTopLevelUsesLimits(t *testing.T) {
	raw := []byte(`{"seven_day":{"utilization":20,"resets_at":"2026-10-26T12:00:00Z"},"seven_day_fable":null,"limits":[{"kind":"weekly_scoped","scope":{"model":{"display_name":"Fable"}},"percent":100,"resets_at":"2026-10-24T12:00:00Z","is_active":true}]}`)
	snap, err := ParseClaudeUsage(raw, claudeFableNow)
	if err != nil {
		t.Fatal(err)
	}
	if fable, ok := snap.Window("model_weekly:fable"); !ok || fable.UsedPercent != 100 {
		t.Fatalf("null seven_day_fable hid the limits[] window: %+v", snap.Windows)
	}
}
