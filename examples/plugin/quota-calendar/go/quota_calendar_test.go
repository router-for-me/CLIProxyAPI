package main

import (
	"strings"
	"testing"
	"time"
)

func TestStableUIDDoesNotChangeWhenResetMoves(t *testing.T) {
	first := stableUID("claude", "claude-sonnet")
	second := stableUID("claude", "claude-sonnet")
	if first != second {
		t.Fatalf("UID changed: %q != %q", first, second)
	}
	if first == stableUID("claude", "claude-opus") {
		t.Fatal("different models share a UID")
	}
}

func TestEscapeICS(t *testing.T) {
	got := escapeICS("a,b;c\\d\ne")
	want := `a\,b\;c\\d\ne`
	if got != want {
		t.Fatalf("escapeICS() = %q, want %q", got, want)
	}
}

func TestCalendarUsesOneEventPerKeyAndLatestReset(t *testing.T) {
	items := []event{
		{Provider: "claude", Model: "sonnet", Reset: time.Date(2026, 8, 20, 11, 0, 0, 0, time.UTC)},
		{Provider: "claude", Model: "sonnet", Reset: time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)},
	}
	latest := make(map[string]event)
	for _, item := range items {
		key := item.Provider + "\x00" + item.Model
		if current, ok := latest[key]; !ok || item.Reset.After(current.Reset) {
			latest[key] = item
		}
	}
	if len(latest) != 1 || !latest["claude\x00sonnet"].Reset.Equal(items[1].Reset) {
		t.Fatalf("latest = %#v", latest)
	}
	if strings.Contains(stableUID("claude", "sonnet"), ":") {
		t.Fatal("UID should not depend on a delimiter that can enter model names")
	}
}

func TestWriteICSLineUsesCRLFAndFolding(t *testing.T) {
	var builder strings.Builder
	writeICSLine(&builder, "SUMMARY:"+strings.Repeat("x", 100))
	got := builder.String()
	if !strings.Contains(got, "\r\n ") {
		t.Fatalf("line was not folded: %q", got)
	}
	if strings.Contains(got, "\n\n") {
		t.Fatalf("unexpected bare/newline sequence: %q", got)
	}
}

func TestCalendarUsesSourceRevisionInsteadOfRequestTime(t *testing.T) {
	revision := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	reset := revision.Add(time.Hour)
	if got := formatICS(revision); got != "20260820T120000Z" {
		t.Fatalf("formatICS(revision) = %q", got)
	}
	if got := formatICS(reset); got == formatICS(time.Now()) {
		t.Fatal("test data unexpectedly used request time")
	}
}
