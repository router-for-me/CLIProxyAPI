package metrics

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/routing"
)

func TestNewStore(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	if store.dir != dir {
		t.Errorf("expected dir=%s, got %s", dir, store.dir)
	}
}

func TestNewStoreCreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "metrics")
	_, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Error("NewStore should create the directory")
	}
}

func TestStoreWriteRecords(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	records := []RequestRecord{
		{
			Timestamp:   time.Now().UTC(),
			Provider:    "claude",
			Model:       "claude-3-opus",
			Profile:     "default",
			RequestType: routing.RequestTypeChat,
			LatencyMs:   150,
			Success:     true,
		},
		{
			Timestamp:   time.Now().UTC(),
			Provider:    "openai",
			Model:       "gpt-4",
			Profile:     "work",
			RequestType: routing.RequestTypeCompletion,
			LatencyMs:   200,
			ErrorType:   "rate_limit",
			Success:     false,
		},
	}

	if err := store.WriteRecords(records); err != nil {
		t.Fatalf("WriteRecords failed: %v", err)
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("expected 1 file, got %d", len(entries))
	}
}

func TestStoreWriteRecordsEmpty(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	if err := store.WriteRecords(nil); err != nil {
		t.Errorf("WriteRecords(nil) should not error: %v", err)
	}
	if err := store.WriteRecords([]RequestRecord{}); err != nil {
		t.Errorf("WriteRecords([]) should not error: %v", err)
	}
}

func TestStoreLoadMetrics(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	now := time.Now().UTC()
	records := []RequestRecord{
		{
			Timestamp:   now,
			Provider:    "claude",
			Model:       "claude-3-opus",
			Profile:     "default",
			RequestType: routing.RequestTypeChat,
			LatencyMs:   100,
			Success:     true,
		},
		{
			Timestamp:   now,
			Provider:    "claude",
			Model:       "claude-3-sonnet",
			Profile:     "default",
			RequestType: routing.RequestTypeChat,
			LatencyMs:   200,
			Success:     true,
		},
		{
			Timestamp:   now,
			Provider:    "openai",
			Model:       "gpt-4",
			Profile:     "work",
			RequestType: routing.RequestTypeCompletion,
			LatencyMs:   300,
			ErrorType:   "timeout",
			Success:     false,
		},
	}
	store.WriteRecords(records)

	from := now.AddDate(0, 0, -1)
	to := now.AddDate(0, 0, 1)
	resp, err := store.LoadMetrics(from, to)
	if err != nil {
		t.Fatalf("LoadMetrics failed: %v", err)
	}

	if resp.Summary.TotalRequests != 3 {
		t.Errorf("expected TotalRequests=3, got %d", resp.Summary.TotalRequests)
	}
	if resp.Summary.TotalFailures != 1 {
		t.Errorf("expected TotalFailures=1, got %d", resp.Summary.TotalFailures)
	}
	expectedAvg := 200.0
	if resp.Summary.AvgLatencyMs != expectedAvg {
		t.Errorf("expected AvgLatencyMs=%.2f, got %.2f", expectedAvg, resp.Summary.AvgLatencyMs)
	}

	claude, ok := resp.ByProvider["claude"]
	if !ok {
		t.Fatal("expected claude in ByProvider")
	}
	if claude.Requests != 2 {
		t.Errorf("expected claude requests=2, got %d", claude.Requests)
	}

	openai, ok := resp.ByProvider["openai"]
	if !ok {
		t.Fatal("expected openai in ByProvider")
	}
	if openai.Failures != 1 {
		t.Errorf("expected openai failures=1, got %d", openai.Failures)
	}

	chat, ok := resp.ByType["chat"]
	if !ok {
		t.Fatal("expected chat in ByType")
	}
	if chat.Requests != 2 {
		t.Errorf("expected chat requests=2, got %d", chat.Requests)
	}

	defaultProfile, ok := resp.ByProfile["default"]
	if !ok {
		t.Fatal("expected default in ByProfile")
	}
	if defaultProfile.Requests != 2 {
		t.Errorf("expected default profile requests=2, got %d", defaultProfile.Requests)
	}

	if len(resp.Daily) != 1 {
		t.Errorf("expected 1 daily summary, got %d", len(resp.Daily))
	}
}

func TestStoreLoadMetricsMultipleDays(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	now := time.Now().UTC()
	yesterday := now.AddDate(0, 0, -1)

	recordsToday := []RequestRecord{
		{Timestamp: now, Provider: "claude", LatencyMs: 100, Success: true, RequestType: routing.RequestTypeChat},
	}
	recordsYesterday := []RequestRecord{
		{Timestamp: yesterday, Provider: "openai", LatencyMs: 200, Success: true, RequestType: routing.RequestTypeChat},
	}

	store.WriteRecords(recordsToday)
	store.WriteRecords(recordsYesterday)

	resp, _ := store.LoadMetrics(yesterday.AddDate(0, 0, -1), now.AddDate(0, 0, 1))
	if resp.Summary.TotalRequests != 2 {
		t.Errorf("expected 2 total requests across days, got %d", resp.Summary.TotalRequests)
	}
	if len(resp.Daily) != 2 {
		t.Errorf("expected 2 daily summaries, got %d", len(resp.Daily))
	}
}

func TestStorePruneOldMetrics(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	old := time.Now().UTC().AddDate(0, 0, -10)
	recent := time.Now().UTC()

	store.WriteRecords([]RequestRecord{
		{Timestamp: old, Provider: "claude", LatencyMs: 100, Success: true},
	})
	store.WriteRecords([]RequestRecord{
		{Timestamp: recent, Provider: "openai", LatencyMs: 200, Success: true},
	})

	entriesBefore, _ := os.ReadDir(dir)
	if len(entriesBefore) != 2 {
		t.Fatalf("expected 2 files before prune, got %d", len(entriesBefore))
	}

	store.PruneOldMetrics(7)

	entriesAfter, _ := os.ReadDir(dir)
	if len(entriesAfter) != 1 {
		t.Errorf("expected 1 file after prune, got %d", len(entriesAfter))
	}
}

func TestStoreGetDirectory(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)
	if store.GetDirectory() != dir {
		t.Errorf("expected dir=%s, got %s", dir, store.GetDirectory())
	}
}

func TestStorePercentiles(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	now := time.Now().UTC()
	records := make([]RequestRecord, 100)
	for i := 0; i < 100; i++ {
		records[i] = RequestRecord{
			Timestamp:   now,
			Provider:    "claude",
			LatencyMs:   int64(i + 1),
			Success:     true,
			RequestType: routing.RequestTypeChat,
		}
	}
	store.WriteRecords(records)

	resp, _ := store.LoadMetrics(now.AddDate(0, 0, -1), now.AddDate(0, 0, 1))

	claude := resp.ByProvider["claude"]
	if claude.P50Ms < 45 || claude.P50Ms > 55 {
		t.Errorf("expected P50 around 50, got %.2f", claude.P50Ms)
	}
	if claude.P90Ms < 85 || claude.P90Ms > 95 {
		t.Errorf("expected P90 around 90, got %.2f", claude.P90Ms)
	}
}

func BenchmarkStoreWrite(b *testing.B) {
	dir := b.TempDir()
	store, _ := NewStore(dir)

	records := make([]RequestRecord, 100)
	for i := range records {
		records[i] = RequestRecord{
			Timestamp:   time.Now().UTC(),
			Provider:    "claude",
			Model:       "claude-3-opus",
			Profile:     "default",
			RequestType: routing.RequestTypeChat,
			LatencyMs:   int64(i * 10),
			Success:     true,
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store.WriteRecords(records)
	}
}

func BenchmarkStoreLoad(b *testing.B) {
	dir := b.TempDir()
	store, _ := NewStore(dir)

	now := time.Now().UTC()
	records := make([]RequestRecord, 1000)
	for i := range records {
		records[i] = RequestRecord{
			Timestamp:   now,
			Provider:    "claude",
			Model:       "claude-3-opus",
			Profile:     "default",
			RequestType: routing.RequestTypeChat,
			LatencyMs:   int64(i),
			Success:     i%10 != 0,
		}
	}
	store.WriteRecords(records)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store.LoadMetrics(now.AddDate(0, 0, -1), now.AddDate(0, 0, 1))
	}
}

// TG5: 7-Day Retention Tests

func TestStoreMetricsWithTimestamps(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	now := time.Now().UTC()
	records := []RequestRecord{
		{
			Timestamp:   now,
			Provider:    "claude",
			Model:       "claude-3-opus",
			LatencyMs:   100,
			Success:     true,
			RequestType: routing.RequestTypeChat,
		},
		{
			Timestamp:   now.Add(-1 * time.Hour),
			Provider:    "openai",
			Model:       "gpt-4",
			LatencyMs:   200,
			Success:     true,
			RequestType: routing.RequestTypeChat,
		},
	}
	store.WriteRecords(records)

	resp, err := store.LoadMetrics(now.AddDate(0, 0, -1), now.AddDate(0, 0, 1))
	if err != nil {
		t.Fatalf("LoadMetrics failed: %v", err)
	}

	if resp.Summary.TotalRequests != 2 {
		t.Errorf("expected 2 requests with timestamps, got %d", resp.Summary.TotalRequests)
	}
}

func TestStoreOlderThan7DaysAutoPurged(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	now := time.Now().UTC()
	oldRecord := []RequestRecord{
		{
			Timestamp: now.AddDate(0, 0, -10),
			Provider:  "claude",
			LatencyMs: 100,
			Success:   true,
		},
	}
	recentRecord := []RequestRecord{
		{
			Timestamp: now,
			Provider:  "openai",
			LatencyMs: 200,
			Success:   true,
		},
	}

	store.WriteRecords(oldRecord)
	store.WriteRecords(recentRecord)

	entriesBefore, _ := os.ReadDir(dir)
	if len(entriesBefore) != 2 {
		t.Fatalf("expected 2 files before purge, got %d", len(entriesBefore))
	}

	// Run purge with 7-day retention
	store.PruneOldMetrics(7)

	entriesAfter, _ := os.ReadDir(dir)
	if len(entriesAfter) != 1 {
		t.Errorf("expected 1 file after 7-day purge, got %d", len(entriesAfter))
	}
}

func TestStorePurgeOldMetricsRemovesOnlyOldData(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	now := time.Now().UTC()

	// Create records for different days
	days := []int{-10, -8, -6, -4, -2, 0} // 6 days total
	for _, dayOffset := range days {
		records := []RequestRecord{
			{
				Timestamp: now.AddDate(0, 0, dayOffset),
				Provider:  "claude",
				LatencyMs: 100,
				Success:   true,
			},
		}
		store.WriteRecords(records)
	}

	entriesBefore, _ := os.ReadDir(dir)
	if len(entriesBefore) != 6 {
		t.Fatalf("expected 6 files before purge, got %d", len(entriesBefore))
	}

	// Purge with 7-day retention (should keep -6, -4, -2, 0 = 4 files)
	store.PruneOldMetrics(7)

	entriesAfter, _ := os.ReadDir(dir)
	if len(entriesAfter) != 4 {
		t.Errorf("expected 4 files after 7-day purge (days -6,-4,-2,0), got %d", len(entriesAfter))
	}
}

func TestStoreGetMetricsSinceReturnsCorrectTimeWindow(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	now := time.Now().UTC()

	// Create records for last 10 days
	for i := 0; i <= 10; i++ {
		records := []RequestRecord{
			{
				Timestamp:   now.AddDate(0, 0, -i),
				Provider:    "claude",
				LatencyMs:   int64(100 + i*10),
				Success:     true,
				RequestType: routing.RequestTypeChat,
			},
		}
		store.WriteRecords(records)
	}

	// Get metrics for last 3 days
	resp, err := store.GetMetricsSince(3 * 24 * time.Hour)
	if err != nil {
		t.Fatalf("GetMetricsSince failed: %v", err)
	}

	// Should have days 0, -1, -2, -3 = 4 days
	if resp.Summary.TotalRequests != 4 {
		t.Errorf("expected 4 requests in 3-day window, got %d", resp.Summary.TotalRequests)
	}

	// Get metrics for last 7 days
	resp7d, err := store.GetMetricsSince(7 * 24 * time.Hour)
	if err != nil {
		t.Fatalf("GetMetricsSince 7d failed: %v", err)
	}

	// Should have days 0 through -7 = 8 days
	if resp7d.Summary.TotalRequests != 8 {
		t.Errorf("expected 8 requests in 7-day window, got %d", resp7d.Summary.TotalRequests)
	}
}

func TestStoreGetLatencyPercentilesP99(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	now := time.Now().UTC()
	records := make([]RequestRecord, 100)
	for i := 0; i < 100; i++ {
		records[i] = RequestRecord{
			Timestamp:   now,
			Provider:    "claude",
			LatencyMs:   int64(i + 1),
			Success:     true,
			RequestType: routing.RequestTypeChat,
		}
	}
	store.WriteRecords(records)

	resp, _ := store.LoadMetrics(now.AddDate(0, 0, -1), now.AddDate(0, 0, 1))
	claude := resp.ByProvider["claude"]

	// P99 should be around 99
	if claude.P99Ms < 95 || claude.P99Ms > 100 {
		t.Errorf("expected P99 around 99, got %.2f", claude.P99Ms)
	}
}

func TestStoreRetentionConfigDefault(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	// Default retention should be 7 days
	if store.GetRetentionDays() != 7 {
		t.Errorf("expected default retention of 7 days, got %d", store.GetRetentionDays())
	}
}

func TestStoreSetRetentionDays(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	store.SetRetentionDays(14)
	if store.GetRetentionDays() != 14 {
		t.Errorf("expected retention of 14 days, got %d", store.GetRetentionDays())
	}

	// Minimum retention is 1 day
	store.SetRetentionDays(0)
	if store.GetRetentionDays() != 1 {
		t.Errorf("expected minimum retention of 1 day, got %d", store.GetRetentionDays())
	}
}
