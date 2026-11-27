package quota

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestParseCodexUsageClassifiesWindowLengths(t *testing.T) {
	tests := []struct {
		name    string
		seconds int
		kind    Kind
	}{
		{name: "five hour", seconds: 18000, kind: FiveHour},
		{name: "weekly", seconds: 604800, kind: Weekly},
		{name: "28 day month", seconds: 2419200, kind: Monthly},
		{name: "31 day month", seconds: 2678400, kind: Monthly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secondary := `,"secondary_window":{"used_percent":20,"limit_window_seconds":604800,"reset_after_seconds":60}`
			if tt.kind == Weekly {
				secondary = ""
			}
			raw := `{"rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":` +
				strconv.Itoa(tt.seconds) + `,"reset_after_seconds":60}` + secondary + `}}`
			snap, err := ParseCodexUsage([]byte(raw), testNow)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := snap.Window(string(tt.kind)); !ok {
				t.Fatalf("missing %s window: %+v", tt.kind, snap.Windows)
			}
		})
	}
}

func TestParseCodexUsageSkipsUnknownWindowLengths(t *testing.T) {
	raw := []byte(`{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":12345,"reset_after_seconds":60},"secondary_window":{"used_percent":25,"limit_window_seconds":604800,"reset_after_seconds":120}}}`)
	snap, err := ParseCodexUsage(raw, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Windows) != 1 || snap.Windows[0].Kind != Weekly || snap.Windows[0].UsedPercent != 25 {
		t.Fatalf("unknown window should be skipped while known data is retained: %+v", snap.Windows)
	}

	raw = []byte(`{"rate_limit":{"primary_window":{"used_percent":45,"limit_window_seconds":18000,"reset_after_seconds":60},"secondary_window":{"used_percent":25,"limit_window_seconds":12345,"reset_after_seconds":120}}}`)
	snap, err = ParseCodexUsage(raw, testNow)
	if err != nil {
		t.Fatalf("unknown long window should not fail the poll: %v", err)
	}
	if len(snap.Windows) != 1 || snap.Windows[0].Kind != FiveHour {
		t.Fatalf("recognized window should remain available for assessment: %+v", snap.Windows)
	}
	if got := Assess(snap, "gpt-5", testNow, time.Hour); got.State != Unknown || got.Reason != "no_long_window" {
		t.Fatalf("snapshot without a recognized long window should remain unknown: %+v", got)
	}

	snap, err = ParseCodexUsage([]byte(`{"rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":12345,"reset_after_seconds":60}}}`), testNow)
	if err != nil || len(snap.Windows) != 0 {
		t.Fatalf("unknown-only poll should succeed without adding an availability window: %+v %v", snap, err)
	}
}

func TestParseCodexUsageDefaultsMissingLengthsByPosition(t *testing.T) {
	raw := []byte(`{"rate_limit":{"primary_window":{"used_percent":10,"reset_after_seconds":60},"secondary_window":{"used_percent":20,"reset_after_seconds":120}}}`)
	snap, err := ParseCodexUsage(raw, testNow)
	if err != nil {
		t.Fatal(err)
	}
	five, okFive := snap.Window(string(FiveHour))
	weekly, okWeekly := snap.Window(string(Weekly))
	if !okFive || !okWeekly || five.UsedPercent != 10 || weekly.UsedPercent != 20 {
		t.Fatalf("missing lengths should use their slot defaults: %+v", snap.Windows)
	}
}

func TestParseCodexHeadersClassifiesAndDefaultsWindowLengths(t *testing.T) {
	now := testNow
	reset := now.Add(time.Hour).Unix()
	h := http.Header{
		"X-Codex-Primary-Used-Percent":          {"12"},
		"X-Codex-Primary-Reset-At":              {strconv.FormatInt(reset, 10)},
		"X-Codex-Secondary-Used-Percent":        {"34"},
		"X-Codex-Secondary-Window-Minutes":      {"12345"},
		"X-Codex-Secondary-Reset-After-Seconds": {"120"},
	}
	got := ParseCodexHeaders(h, now)
	if len(got) != 1 || got[0].Kind != FiveHour || got[0].UsedPercent != 12 {
		t.Fatalf("missing primary length should default to five-hour and unknown secondary be skipped: %+v", got)
	}

	h = http.Header{
		"X-Codex-Primary-Used-Percent":          {"12"},
		"X-Codex-Primary-Window-Minutes":        {"300"},
		"X-Codex-Primary-Reset-At":              {strconv.FormatInt(reset, 10)},
		"X-Codex-Secondary-Used-Percent":        {"34"},
		"X-Codex-Secondary-Reset-After-Seconds": {"120"},
	}
	got = ParseCodexHeaders(h, now)
	if len(got) != 2 || got[0].Kind != FiveHour || got[1].Kind != Weekly {
		t.Fatalf("missing secondary length should default to weekly: %+v", got)
	}
}

func TestParseCodexHeadersClassifiesWeeklyAndMonthlyLengths(t *testing.T) {
	tests := []struct {
		minutes string
		kind    Kind
	}{
		{minutes: "10080", kind: Weekly},
		{minutes: "40320", kind: Monthly},
		{minutes: "44640", kind: Monthly},
	}
	for _, tt := range tests {
		t.Run(tt.minutes, func(t *testing.T) {
			h := http.Header{
				"X-Codex-Primary-Used-Percent":        {"12"},
				"X-Codex-Primary-Window-Minutes":      {tt.minutes},
				"X-Codex-Primary-Reset-After-Seconds": {"60"},
			}
			got := ParseCodexHeaders(h, testNow)
			if len(got) != 1 || got[0].Kind != tt.kind {
				t.Fatalf("length %s should classify as %s: %+v", tt.minutes, tt.kind, got)
			}
		})
	}
}
