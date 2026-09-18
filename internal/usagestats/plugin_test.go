package usagestats

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if line := scanner.Text(); line != "" {
			lines = append(lines, line)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return lines
}

func TestHandleUsageDisabledWritesNothing(t *testing.T) {
	dir := t.TempDir()
	Configure(false, dir)

	(&recordPlugin{}).HandleUsage(context.Background(), coreusage.Record{
		Provider:    "antigravity",
		Model:       "gemini-3.8-flash-high",
		RequestedAt: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC),
	})

	entries, err := os.ReadDir(Dir(dir))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("unexpected error reading dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no files written while disabled, found %d", len(entries))
	}
}

func TestHandleUsageEnabledWritesOneLineWithExpectedFields(t *testing.T) {
	dir := t.TempDir()
	Configure(true, dir)

	rec := coreusage.Record{
		Provider:     "antigravity",
		ExecutorType: "antigravity",
		Model:        "gemini-3.8-flash-high",
		Alias:        "gemini-3.8-flash-high",
		AuthID:       "antigravity-kevin.lee.rowan@gmail.com.json",
		AuthIndex:    "0",
		AuthType:     "oauth",
		APIKey:       "super-secret-key-should-not-be-persisted",
		RequestedAt:  time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC),
		Latency:      250 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:  100,
			OutputTokens: 50,
			TotalTokens:  150,
		},
	}
	(&recordPlugin{}).HandleUsage(context.Background(), rec)

	path := filepath.Join(Dir(dir), "usage-2026-09-18.jsonl")
	lines := readLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 line, got %d", len(lines))
	}

	var e entry
	if err := json.Unmarshal([]byte(lines[0]), &e); err != nil {
		t.Fatalf("unmarshal line: %v", err)
	}
	if e.Provider != "antigravity" {
		t.Errorf("provider = %q, want antigravity", e.Provider)
	}
	if e.Model != "gemini-3.8-flash-high" {
		t.Errorf("model = %q, want gemini-3.8-flash-high", e.Model)
	}
	if e.Account != "antigravity-kevin.lee.rowan@gmail.com.json" {
		t.Errorf("account = %q, want auth id", e.Account)
	}
	if e.Failed {
		t.Errorf("failed = true, want false")
	}
	if e.StatusCode != 200 {
		t.Errorf("status_code = %d, want 200", e.StatusCode)
	}
	if e.LatencyMs != 250 {
		t.Errorf("latency_ms = %d, want 250", e.LatencyMs)
	}
	if e.AccountingVersion != coreusage.TokenAccountingSchemaVersion {
		t.Errorf("accounting_version = %d, want %d", e.AccountingVersion, coreusage.TokenAccountingSchemaVersion)
	}

	if strings.Contains(lines[0], "super-secret-key-should-not-be-persisted") {
		t.Fatalf("persisted line leaked raw api key: %s", lines[0])
	}
}

func TestHandleUsageRoutesDifferentDatesToDifferentFiles(t *testing.T) {
	dir := t.TempDir()
	Configure(true, dir)

	plugin := &recordPlugin{}
	plugin.HandleUsage(context.Background(), coreusage.Record{
		Provider:    "antigravity",
		Model:       "gemini-3.8-flash-high",
		RequestedAt: time.Date(2026, 9, 17, 23, 59, 0, 0, time.UTC),
	})
	plugin.HandleUsage(context.Background(), coreusage.Record{
		Provider:    "antigravity",
		Model:       "gemini-3.8-flash-high",
		RequestedAt: time.Date(2026, 9, 18, 0, 1, 0, 0, time.UTC),
	})

	day1 := readLines(t, filepath.Join(Dir(dir), "usage-2026-09-17.jsonl"))
	day2 := readLines(t, filepath.Join(Dir(dir), "usage-2026-09-18.jsonl"))
	if len(day1) != 1 {
		t.Fatalf("day1 lines = %d, want 1", len(day1))
	}
	if len(day2) != 1 {
		t.Fatalf("day2 lines = %d, want 1", len(day2))
	}
}

func TestHandleUsageZeroRequestedAtFallsBackToNow(t *testing.T) {
	dir := t.TempDir()
	Configure(true, dir)

	(&recordPlugin{}).HandleUsage(context.Background(), coreusage.Record{
		Provider: "antigravity",
		Model:    "gemini-3.8-flash-high",
	})

	today := time.Now().UTC().Format("2006-01-02")
	path := filepath.Join(Dir(dir), "usage-"+today+".jsonl")
	lines := readLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("expected the zero-RequestedAt record to land in today's file, got %d lines", len(lines))
	}
}

func TestHandleUsageFailedRecordCapturesStatusCode(t *testing.T) {
	dir := t.TempDir()
	Configure(true, dir)

	(&recordPlugin{}).HandleUsage(context.Background(), coreusage.Record{
		Provider:    "antigravity",
		Model:       "gemini-3.8-flash-high",
		RequestedAt: time.Date(2026, 9, 18, 14, 15, 0, 0, time.UTC),
		Failed:      true,
		Fail:        coreusage.Failure{StatusCode: 429, Body: "RESOURCE_EXHAUSTED"},
	})

	path := filepath.Join(Dir(dir), "usage-2026-09-18.jsonl")
	lines := readLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	var e entry
	if err := json.Unmarshal([]byte(lines[0]), &e); err != nil {
		t.Fatalf("unmarshal line: %v", err)
	}
	if !e.Failed {
		t.Errorf("failed = false, want true")
	}
	if e.StatusCode != 429 {
		t.Errorf("status_code = %d, want 429", e.StatusCode)
	}
}

func TestConfigureDisabledThenEnabledResumesWriting(t *testing.T) {
	dir := t.TempDir()
	Configure(false, dir)
	Configure(true, dir)

	(&recordPlugin{}).HandleUsage(context.Background(), coreusage.Record{
		Provider:    "antigravity",
		Model:       "gemini-3.8-flash-high",
		RequestedAt: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC),
	})

	path := filepath.Join(Dir(dir), "usage-2026-09-18.jsonl")
	lines := readLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line after re-enabling, got %d", len(lines))
	}
}

func TestHandleUsageCapturesStreamTtftAndErrorMessage(t *testing.T) {
	dir := t.TempDir()
	Configure(true, dir)

	(&recordPlugin{}).HandleUsage(context.Background(), coreusage.Record{
		Provider:    "antigravity",
		Model:       "gemini-3.8-flash-high",
		RequestedAt: time.Date(2026, 9, 18, 15, 0, 0, 0, time.UTC),
		Stream:      true,
		TTFT:        120 * time.Millisecond,
		Latency:     450 * time.Millisecond,
		Failed:      true,
		Fail: coreusage.Failure{
			StatusCode: 429,
			Body:       "RESOURCE_EXHAUSTED: quota exceeded",
		},
	})

	path := filepath.Join(Dir(dir), "usage-2026-09-18.jsonl")
	lines := readLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	var e entry
	if err := json.Unmarshal([]byte(lines[0]), &e); err != nil {
		t.Fatalf("unmarshal line: %v", err)
	}
	if !e.Stream {
		t.Errorf("stream = false, want true")
	}
	if e.TtftMs != 120 {
		t.Errorf("ttft_ms = %d, want 120", e.TtftMs)
	}
	if e.ErrorMessage != "RESOURCE_EXHAUSTED: quota exceeded" {
		t.Errorf("error_message = %q, want quota exceeded", e.ErrorMessage)
	}
}
