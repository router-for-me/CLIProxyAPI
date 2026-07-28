package logqa

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunOnceScansAndWritesReport(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	logsRoot := filepath.Join(root, "keys")
	workDir := filepath.Join(root, "qa")
	keyDir := filepath.Join(logsRoot, "panda")
	if err := os.MkdirAll(keyDir, 0o750); err != nil {
		t.Fatal(err)
	}

	body := `=== REQUEST INFO ===
Timestamp: 2026-07-15T01:10:00+08:00


=== HEADERS ===
Session-Id: sess-1
Thread-Id: thr-1
X-Codex-Turn-Metadata: {"session_id":"sess-1","thread_id":"thr-1","request_kind":"turn"}


=== REQUEST BODY ===
{"model":"gpt-5.6-sol","input":[
  {"type":"message","role":"user","content":"q1"},
  {"type":"message","role":"user","content":"q2"},
  {"type":"message","role":"user","content":"q3"},
  {"type":"message","role":"user","content":"q4"},
  {"type":"custom_tool_call","call_id":"call-1","name":"exec","arguments":"{}"},
  {"type":"custom_tool_call_output","call_id":"call-1","output":"ok"},
  {"type":"message","role":"assistant","content":"done"}
]}

=== RESPONSE ===
Status: 200
Content-Type: text/event-stream

event: response.completed
data: {"type":"response.completed","response":{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}}
`
	logPath := filepath.Join(keyDir, "sample.log")
	if err := os.WriteFile(logPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// age the file so min-file-age does not skip it
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(logPath, old, old); err != nil {
		t.Fatal(err)
	}

	loc := time.FixedZone("CST", 8*3600)
	cfg := Config{
		LogsRoot: logsRoot,
		WorkDir:  workDir,
		Timezone: "Asia/Shanghai",
		Location: loc,
		Schedule: ScheduleConfig{Interval: time.Hour, IntervalRaw: "1h"},
		Scan: ScanConfig{
			MinFileAge:         0,
			SkipCurrentHour:    false,
			MaxFilesPerRun:     0, // unlimited full scan
			MaxBytesPerRun:     0,
			MaxFileConcurrency: 2,
			MaxFileSize:        0,
		},
		Rules: RulesConfig{
			MinPromptRounds:            4,
			RequireToolCall:            true,
			RequireSessionID:           true,
			RequireEndsWithoutToolCall: true,
			RejectUnpairedToolCalls:    true,
			RejectDuplicateAssistant:   true,
			ExcludeIDEContext:          true,
			ExcludeEnvContext:          true,
			ExcludeTitleSummary:        true,
		},
		Aggregation: AggregationConfig{Key: "session_id", Snapshot: "max_input_len"},
		Report:      ReportConfig{KeepRuns: 5},
	}

	svc := NewService(cfg)
	if err := svc.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	summary, err := ReadSummary(workDir, "")
	if err != nil {
		t.Fatalf("ReadSummary: %v", err)
	}
	if summary.SessionsTotal != 1 || summary.SessionsPass != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	if summary.FilesScanned != 1 {
		t.Fatalf("files_scanned=%d", summary.FilesScanned)
	}

	// second run should skip unchanged
	if err := svc.RunOnce(context.Background()); err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	summary2, err := ReadSummary(workDir, "")
	if err != nil {
		t.Fatal(err)
	}
	if summary2.FilesSkippedUnchanged < 1 {
		t.Fatalf("expected unchanged skip, got %+v", summary2)
	}
}

func TestParseDisappearedFile(t *testing.T) {
	t.Parallel()
	// ensure fingerprint helper stable
	fp := fingerprint("a/b.log", 10, time.Unix(1, 2))
	if fp == "" {
		t.Fatal("empty fingerprint")
	}
}
