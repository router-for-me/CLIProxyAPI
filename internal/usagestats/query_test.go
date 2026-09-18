package usagestats

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFixtureFile(t *testing.T, dir, date string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, "usage-"+date+".jsonl")
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", path, err)
	}
}

func TestQueryDailyAggregatesAcrossDaysModelsAndAccounts(t *testing.T) {
	dir := t.TempDir()

	writeFixtureFile(t, dir, "2026-09-17", []string{
		`{"timestamp":"2026-09-17T19:00:00Z","provider":"antigravity","model":"gemini-3.8-flash-high","account":"kevin","failed":false,"status_code":200,"token_breakdown":{"total_tokens":150,"input":{"total_tokens":100},"output":{"total_tokens":50}}}`,
		`{"timestamp":"2026-09-17T19:05:00Z","provider":"antigravity","model":"gemini-3.8-flash-high","account":"kevin","failed":true,"status_code":429,"token_breakdown":{"total_tokens":0}}`,
		`{"timestamp":"2026-09-17T19:10:00Z","provider":"antigravity","model":"claude-sonnet-5","account":"tongzhipeng","failed":false,"status_code":200,"token_breakdown":{"total_tokens":300,"input":{"total_tokens":200},"output":{"total_tokens":100}}}`,
	})
	writeFixtureFile(t, dir, "2026-09-18", []string{
		`{"timestamp":"2026-09-18T14:15:00Z","provider":"antigravity","model":"gemini-3.8-flash-high","account":"kevin","failed":true,"status_code":429,"token_breakdown":{"total_tokens":0}}`,
	})

	from := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	stats, err := QueryDaily(dir, from, to)
	if err != nil {
		t.Fatalf("QueryDaily: %v", err)
	}

	byKey := make(map[string]DailyModelStats)
	for _, s := range stats {
		byKey[s.Date+"|"+s.Provider+"|"+s.Model+"|"+s.Account] = s
	}

	kevin17 := byKey["2026-09-17|antigravity|gemini-3.8-flash-high|kevin"]
	if kevin17.Records != 2 {
		t.Errorf("kevin 09-17 records = %d, want 2", kevin17.Records)
	}
	if kevin17.Failures != 1 {
		t.Errorf("kevin 09-17 failures = %d, want 1", kevin17.Failures)
	}
	if kevin17.TotalTokens != 150 {
		t.Errorf("kevin 09-17 total tokens = %d, want 150", kevin17.TotalTokens)
	}

	tong17 := byKey["2026-09-17|antigravity|claude-sonnet-5|tongzhipeng"]
	if tong17.Records != 1 || tong17.TotalTokens != 300 {
		t.Errorf("tongzhipeng 09-17 = %+v, want records=1 total=300", tong17)
	}

	kevin18 := byKey["2026-09-18|antigravity|gemini-3.8-flash-high|kevin"]
	if kevin18.Records != 1 || kevin18.Failures != 1 {
		t.Errorf("kevin 09-18 = %+v, want records=1 failures=1", kevin18)
	}
}

func TestQueryDailyEmptyRangeReturnsEmptyNotError(t *testing.T) {
	dir := t.TempDir()
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)

	stats, err := QueryDaily(dir, from, to)
	if err != nil {
		t.Fatalf("QueryDaily on empty/nonexistent dir returned error: %v", err)
	}
	if len(stats) != 0 {
		t.Fatalf("expected 0 stats for empty range, got %d", len(stats))
	}
}

func TestQueryDailySkipsMalformedLineButKeepsGoodOnes(t *testing.T) {
	dir := t.TempDir()
	writeFixtureFile(t, dir, "2026-09-18", []string{
		`{"timestamp":"2026-09-18T10:00:00Z","provider":"antigravity","model":"gemini-3.8-flash-high","account":"kevin","failed":false,"status_code":200,"token_breakdown":{"total_tokens":10}}`,
		`{this is not valid json`,
		`{"timestamp":"2026-09-18T11:00:00Z","provider":"antigravity","model":"gemini-3.8-flash-high","account":"kevin","failed":false,"status_code":200,"token_breakdown":{"total_tokens":20}}`,
	})

	from := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	to := from
	stats, err := QueryDaily(dir, from, to)
	if err != nil {
		t.Fatalf("QueryDaily: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("expected 1 aggregated row, got %d", len(stats))
	}
	if stats[0].Records != 2 {
		t.Errorf("records = %d, want 2 (garbage line skipped)", stats[0].Records)
	}
	if stats[0].TotalTokens != 30 {
		t.Errorf("total tokens = %d, want 30", stats[0].TotalTokens)
	}
}

func TestQueryDailyRejectsExcessiveRange(t *testing.T) {
	dir := t.TempDir()
	from := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if _, err := QueryDaily(dir, from, to); err == nil {
		t.Fatal("expected error for query range exceeding the cap, got nil")
	}
}
