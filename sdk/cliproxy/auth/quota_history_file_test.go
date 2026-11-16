package auth

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
)

func openTestQuotaHistory(t *testing.T, path string, now time.Time) *QuotaHistory {
	t.Helper()
	h, errOpen := OpenQuotaHistory(path, now)
	if errOpen != nil {
		t.Fatalf("OpenQuotaHistory: %v", errOpen)
	}
	t.Cleanup(func() {
		if errClose := h.Close(); errClose != nil {
			t.Errorf("Close: %v", errClose)
		}
	})
	return h
}

func readQuotaHistoryLines(t *testing.T, path string) []quotaHistoryLine {
	t.Helper()
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read history file: %v", errRead)
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		t.Fatalf("history file does not end with a newline: %q", data)
	}
	var lines []quotaHistoryLine
	for _, raw := range bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n")) {
		if len(raw) == 0 {
			continue
		}
		var line quotaHistoryLine
		if errUnmarshal := json.Unmarshal(raw, &line); errUnmarshal != nil {
			t.Fatalf("history line %q: %v", raw, errUnmarshal)
		}
		lines = append(lines, line)
	}
	return lines
}

func assertSamePoints(t *testing.T, got, want []QuotaHistoryPoint) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d points, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if !got[i].At.Equal(want[i].At) || got[i].Used != want[i].Used || !got[i].ResetAt.Equal(want[i].ResetAt) {
			t.Fatalf("point %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestQuotaHistoryAppendReloadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), QuotaHistoryFileName)
	h := openTestQuotaHistory(t, path, quotaHistoryNow)
	if _, errStat := os.Stat(path); !os.IsNotExist(errStat) {
		t.Fatalf("opening without history created the file: %v", errStat)
	}
	reset := quotaHistoryNow.Add(24 * time.Hour)
	observeWeekly(h, "claude-a@example.com.json", quotaHistoryNow, 0.415, reset)
	observeWeekly(h, "claude-a@example.com.json", quotaHistoryNow.Add(time.Hour), 0.5, time.Time{})
	if errFlush := h.Flush(); errFlush != nil {
		t.Fatalf("Flush: %v", errFlush)
	}

	lines := readQuotaHistoryLines(t, path)
	want := quotaHistoryLine{V: 1, ID: "claude-a@example.com.json", K: QuotaWindowWeekly, T: quotaHistoryNow.Unix(), U: 0.415, R: reset.Unix(), P: 604800}
	if len(lines) != 2 || lines[0] != want || lines[1].R != 0 {
		t.Fatalf("lines = %+v, want first %+v and an unknown reset second", lines, want)
	}

	reloaded := openTestQuotaHistory(t, path, quotaHistoryNow.Add(2*time.Hour))
	series := reloaded.Series("claude-a@example.com.json", time.Time{})
	if len(series) != 1 || series[0].Period != quotaHistoryWeek {
		t.Fatalf("reloaded series = %+v", series)
	}
	assertSamePoints(t, series[0].Points, weeklyPoints(t, h, "claude-a@example.com.json"))
}

func TestQuotaHistoryLoadSkipsTornTailAndBadLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), QuotaHistoryFileName)
	resetAt := quotaHistoryNow.Add(24 * time.Hour)
	unix := func(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }
	at := func(offset time.Duration) string { return unix(quotaHistoryNow.Add(offset)) }
	content := strings.Join([]string{
		`{"v":1,"id":"a","k":"weekly","t":` + at(-2*time.Hour) + `,"u":0.2,"r":` + unix(resetAt) + `,"p":604800}`,
		`not json`,
		`{"v":2,"id":"a","k":"weekly","t":` + at(-90*time.Minute) + `,"u":0.3}`,
		`{"v":1,"id":"","k":"weekly","t":` + at(-90*time.Minute) + `,"u":0.3}`,
		`{"v":1,"id":"a","k":"","t":` + at(-90*time.Minute) + `,"u":0.3}`,
		`{"v":1,"id":"a","k":"weekly","t":` + at(time.Hour) + `,"u":0.3}`,
		`{"v":1,"id":"a","k":"weekly","t":` + at(-31*24*time.Hour) + `,"u":0.3}`,
		`{"v":1,"id":"a","k":"weekly","t":` + at(-time.Hour) + `,"u":0.4,"r":` + unix(resetAt) + `}`,
		`{"v":1,"id":"a","k":"weekly","t":` + at(-time.Hour) + `,"u":0.9}`,
		`{"v":1,"id":"a","k":"weekly","t":` + at(-30*time.Minute),
	}, "\n")
	if errWrite := os.WriteFile(path, []byte(content), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}

	h := openTestQuotaHistory(t, path, quotaHistoryNow)
	points := weeklyPoints(t, h, "a")
	assertSamePoints(t, points, []QuotaHistoryPoint{
		{At: quotaHistoryNow.Add(-2 * time.Hour), Used: 0.2, ResetAt: resetAt},
		{At: quotaHistoryNow.Add(-time.Hour), Used: 0.4, ResetAt: resetAt},
	})

	observeWeekly(h, "a", quotaHistoryNow, 0.6, resetAt)
	if errFlush := h.Flush(); errFlush != nil {
		t.Fatalf("Flush: %v", errFlush)
	}
	lines := readQuotaHistoryLines(t, path)
	if len(lines) != 3 || lines[2].T != quotaHistoryNow.Unix() || lines[2].U != 0.6 {
		t.Fatalf("lines after append = %+v, want the two kept points and the new one", lines)
	}
}

func TestQuotaHistoryCompactAtomicAndMode0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, QuotaHistoryFileName)
	h := openTestQuotaHistory(t, path, quotaHistoryNow)
	reset := quotaHistoryNow.Add(24 * time.Hour)
	observeWeekly(h, "a", quotaHistoryNow, 0.1, reset)
	if errFlush := h.Flush(); errFlush != nil {
		t.Fatalf("Flush: %v", errFlush)
	}
	if errCompact := h.Compact(quotaHistoryNow); errCompact != nil {
		t.Fatalf("Compact: %v", errCompact)
	}
	observeWeekly(h, "a", quotaHistoryNow.Add(time.Hour), 0.2, reset)
	if errFlush := h.Flush(); errFlush != nil {
		t.Fatalf("Flush after Compact: %v", errFlush)
	}

	if lines := readQuotaHistoryLines(t, path); len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %+v", len(lines), lines)
	}
	info, errStat := os.Stat(path)
	if errStat != nil {
		t.Fatal(errStat)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	entries, errRead := os.ReadDir(dir)
	if errRead != nil {
		t.Fatal(errRead)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("temp file left behind: %s", entry.Name())
		}
	}
}

func TestQuotaHistoryFileNameIsNotJSON(t *testing.T) {
	if strings.HasSuffix(strings.ToLower(QuotaHistoryFileName), ".json") {
		t.Fatalf("%s would be loaded as a credential by the file store", QuotaHistoryFileName)
	}
	if filepath.Ext(QuotaHistoryFileName) != ".jsonl" {
		t.Fatalf("extension of %s = %s, want .jsonl", QuotaHistoryFileName, filepath.Ext(QuotaHistoryFileName))
	}
}

func TestQuotaHistoryWriteErrorKeepsMemoryAndCompactHeals(t *testing.T) {
	path := filepath.Join(t.TempDir(), QuotaHistoryFileName)
	h := openTestQuotaHistory(t, path, quotaHistoryNow)
	reset := quotaHistoryNow.Add(24 * time.Hour)
	observeWeekly(h, "a", quotaHistoryNow, 0.1, reset)
	if errFlush := h.Flush(); errFlush != nil {
		t.Fatalf("Flush: %v", errFlush)
	}

	// Break the append handle so the next write fails.
	if errClose := h.file.out.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	observeWeekly(h, "a", quotaHistoryNow.Add(time.Hour), 0.2, reset)
	if errFlush := h.Flush(); errFlush == nil {
		t.Fatal("Flush on a broken handle succeeded")
	}
	if len(h.pending) != 0 || !h.Dirty() {
		t.Fatalf("after a failed write: pending=%d dirty=%v, want 0 and true", len(h.pending), h.Dirty())
	}

	observeWeekly(h, "a", quotaHistoryNow.Add(2*time.Hour), 0.3, reset)
	if errFlush := h.Flush(); errFlush != nil {
		t.Fatalf("Flush while dirty: %v", errFlush)
	}
	if lines := readQuotaHistoryLines(t, path); len(lines) != 1 {
		t.Fatalf("a dirty store appended: %+v", lines)
	}

	if errCompact := h.Compact(quotaHistoryNow.Add(2 * time.Hour)); errCompact != nil {
		t.Fatalf("Compact: %v", errCompact)
	}
	if h.Dirty() {
		t.Fatal("a successful Compact left the store dirty")
	}
	reloaded := openTestQuotaHistory(t, path, quotaHistoryNow.Add(2*time.Hour))
	assertSamePoints(t, weeklyPoints(t, reloaded, "a"), weeklyPoints(t, h, "a"))
	if got := len(weeklyPoints(t, reloaded, "a")); got != 3 {
		t.Fatalf("reloaded %d points, want 3", got)
	}
}

func TestQuotaHistoryConcurrentUse(t *testing.T) {
	path := filepath.Join(t.TempDir(), QuotaHistoryFileName)
	h := openTestQuotaHistory(t, path, quotaHistoryNow)
	reset := quotaHistoryNow.Add(24 * time.Hour)
	const writers, readings = 4, 50
	var wg sync.WaitGroup
	for writer := range writers {
		authID := string(rune('a' + writer))
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := range readings {
				observeWeekly(h, authID, quotaHistoryNow.Add(time.Duration(i)*time.Hour), 0.1, reset)
				if errFlush := h.Flush(); errFlush != nil {
					t.Errorf("Flush: %v", errFlush)
				}
			}
		}()
		go func() {
			defer wg.Done()
			for range readings {
				h.Series(authID, time.Time{})
				h.Dirty()
			}
		}()
	}
	wg.Wait()
	if errCompact := h.Compact(quotaHistoryNow.Add(readings * time.Hour)); errCompact != nil {
		t.Fatalf("Compact: %v", errCompact)
	}
	if lines := readQuotaHistoryLines(t, path); len(lines) != writers*readings {
		t.Fatalf("got %d lines, want %d", len(lines), writers*readings)
	}
}
