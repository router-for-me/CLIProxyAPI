package usageimport

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

func TestMergePayloadsDeduplicatesAcrossOverlappingExports(t *testing.T) {
	t1 := time.Date(2026, 3, 24, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 3, 24, 10, 30, 0, 0, time.UTC)
	t3 := time.Date(2026, 3, 24, 11, 0, 0, 0, time.UTC)

	payloads := []ExportPayload{
		{Version: 1, Usage: snapshotWithDetails("api-1", "gpt-5", detailAt(t1), detailAt(t2))},
		{Version: 1, Usage: snapshotWithDetails("api-1", "gpt-5", detailAt(t1), detailAt(t2), detailAt(t3))},
		{Version: 1, Usage: snapshotWithDetails("api-1", "gpt-5", detailAt(t3))},
	}

	merged, summary, err := MergePayloads(context.Background(), payloads)
	if err != nil {
		t.Fatalf("MergePayloads failed: %v", err)
	}
	if summary.Files != 3 {
		t.Fatalf("summary files = %d, want 3", summary.Files)
	}
	if summary.SourceRequests != 6 {
		t.Fatalf("source requests = %d, want 6", summary.SourceRequests)
	}
	if summary.MergedRequests != 3 {
		t.Fatalf("merged requests = %d, want 3", summary.MergedRequests)
	}
	if summary.DeduplicatedRecords != 3 {
		t.Fatalf("deduplicated records = %d, want 3", summary.DeduplicatedRecords)
	}
	if summary.Added != 3 || summary.Skipped != 3 {
		t.Fatalf("merge result added/skipped = %d/%d, want 3/3", summary.Added, summary.Skipped)
	}
	if merged.TotalRequests != 3 {
		t.Fatalf("merged total requests = %d, want 3", merged.TotalRequests)
	}
}

func TestResolveInputPathsSupportsFilesDirectoriesAndGlobs(t *testing.T) {
	tempDir := t.TempDir()
	first := filepath.Join(tempDir, "a.json")
	second := filepath.Join(tempDir, "b.json")
	third := filepath.Join(tempDir, "ignore.txt")

	writeTestFile(t, first)
	writeTestFile(t, second)
	writeTestFile(t, third)

	paths, err := ResolveInputPaths([]string{first, filepath.Join(tempDir, "*.json"), tempDir})
	if err != nil {
		t.Fatalf("ResolveInputPaths failed: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("resolved paths = %d, want 2 (%v)", len(paths), paths)
	}
	if filepath.Base(paths[0]) != "a.json" || filepath.Base(paths[1]) != "b.json" {
		t.Fatalf("unexpected resolved paths: %v", paths)
	}
}

func TestReadPayloadFileRejectsUnsupportedVersion(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "usage.json")
	if err := os.WriteFile(path, []byte(`{"version":2,"usage":{}}`), 0o644); err != nil {
		t.Fatalf("write test payload: %v", err)
	}
	if _, err := ReadPayloadFile(path); err == nil {
		t.Fatal("expected unsupported version error")
	}
}

func snapshotWithDetails(apiName, modelName string, details ...usage.RequestDetail) usage.StatisticsSnapshot {
	return usage.StatisticsSnapshot{
		TotalRequests: int64(len(details)),
		APIs: map[string]usage.APISnapshot{
			apiName: {
				TotalRequests: int64(len(details)),
				Models: map[string]usage.ModelSnapshot{
					modelName: {
						TotalRequests: int64(len(details)),
						Details:       details,
					},
				},
			},
		},
		RequestsByDay:  map[string]int64{},
		RequestsByHour: map[string]int64{},
		TokensByDay:    map[string]int64{},
		TokensByHour:   map[string]int64{},
	}
}

func detailAt(ts time.Time) usage.RequestDetail {
	return usage.RequestDetail{
		Timestamp: ts,
		Source:    "legacy",
		AuthIndex: "1",
		Tokens: usage.TokenStats{
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
		},
	}
}

func writeTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write test file %s: %v", path, err)
	}
}
