package management

import (
	"os"
	"path/filepath"
	"testing"
)

// --- parseLogLine tests ---

func TestParseLogLine_WithCaller(t *testing.T) {
	line := "[2025-12-23 20:14:04] [a1b2c3d4] [debug ] [manager.go:524] Use API key sk-9abc for model gpt-5.2 provider=openai model=gpt-5.2"
	parsed := parseLogLine(line)

	if parsed.level != "debug" {
		t.Fatalf("expected level 'debug', got %q", parsed.level)
	}
	if parsed.requestID != "a1b2c3d4" {
		t.Fatalf("expected request_id 'a1b2c3d4', got %q", parsed.requestID)
	}
	if parsed.model != "gpt-5.2" {
		t.Fatalf("expected model 'gpt-5.2', got %q", parsed.model)
	}
	if parsed.provider != "openai" {
		t.Fatalf("expected provider 'openai', got %q", parsed.provider)
	}
}

func TestParseLogLine_WithoutCaller(t *testing.T) {
	line := "[2025-12-23 20:14:04] [--------] [info  ] Started server provider=openai model=gpt-4o"
	parsed := parseLogLine(line)

	if parsed.level != "info" {
		t.Fatalf("expected level 'info', got %q", parsed.level)
	}
	if parsed.requestID != "--------" {
		t.Fatalf("expected request_id '--------', got %q", parsed.requestID)
	}
	if parsed.model != "gpt-4o" {
		t.Fatalf("expected model 'gpt-4o', got %q", parsed.model)
	}
}

func TestParseLogLine_WarnLevel(t *testing.T) {
	line := "[2025-12-23 20:14:04] [--------] [warn  ] Deprecated config option used"
	parsed := parseLogLine(line)

	if parsed.level != "warn" {
		t.Fatalf("expected level 'warn', got %q", parsed.level)
	}
	if parsed.model != "" {
		t.Fatalf("expected empty model, got %q", parsed.model)
	}
}

func TestParseLogLine_ErrorLevel(t *testing.T) {
	line := "[2025-12-23 20:14:04] [b2c3d4e5] [error] [handler.go:88] Failed to process request model=claude-sonnet-4 error=context deadline exceeded"
	parsed := parseLogLine(line)

	if parsed.level != "error" {
		t.Fatalf("expected level 'error', got %q", parsed.level)
	}
	if parsed.model != "claude-sonnet-4" {
		t.Fatalf("expected model 'claude-sonnet-4', got %q", parsed.model)
	}
}

func TestParseLogLine_EmptyLine(t *testing.T) {
	parsed := parseLogLine("")
	if parsed.level != "" {
		t.Fatalf("expected empty level, got %q", parsed.level)
	}
}

func TestParseLogLine_NonConformingLine(t *testing.T) {
	parsed := parseLogLine("just a plain text line")
	if parsed.level != "" {
		t.Fatalf("expected empty level for non-conforming line, got %q", parsed.level)
	}
}

// --- logFilters.matches tests ---

func TestLogFilters_Matches_LevelOnly(t *testing.T) {
	f := logFilters{level: "error"}
	parsed := logLineFields{level: "error", raw: "[2025-12-23 20:14:04] [--------] [error] something bad"}
	if !f.matches(parsed) {
		t.Fatal("expected match for error level")
	}

	parsed2 := logLineFields{level: "info", raw: "[2025-12-23 20:14:04] [--------] [info  ] something good"}
	if f.matches(parsed2) {
		t.Fatal("expected no match for info level when filtering by error")
	}
}

func TestLogFilters_Matches_KeywordOnly(t *testing.T) {
	f := logFilters{keyword: "timeout"}
	parsed := logLineFields{level: "error", raw: "[2025-12-23 20:14:04] [--------] [error] connection timeout occurred"}
	if !f.matches(parsed) {
		t.Fatal("expected match for keyword 'timeout'")
	}

	parsed2 := logLineFields{level: "error", raw: "[2025-12-23 20:14:04] [--------] [error] connection refused"}
	if f.matches(parsed2) {
		t.Fatal("expected no match when keyword 'timeout' not present")
	}
}

func TestLogFilters_Matches_KeywordCaseInsensitive(t *testing.T) {
	f := logFilters{keyword: "Timeout"}
	parsed := logLineFields{level: "error", raw: "[2025-12-23 20:14:04] [--------] [error] connection TIMEOUT occurred"}
	if !f.matches(parsed) {
		t.Fatal("expected case-insensitive keyword match")
	}
}

func TestLogFilters_Matches_ModelOnly(t *testing.T) {
	f := logFilters{model: "gpt-5.2"}
	parsed := logLineFields{level: "info", model: "gpt-5.2", raw: "[2025-12-23 20:14:04] [--------] [info  ] request model=gpt-5.2"}
	if !f.matches(parsed) {
		t.Fatal("expected match for model 'gpt-5.2'")
	}

	parsed2 := logLineFields{level: "info", model: "claude-sonnet-4", raw: "[2025-12-23 20:14:04] [--------] [info  ] request model=claude-sonnet-4"}
	if f.matches(parsed2) {
		t.Fatal("expected no match for different model")
	}
}

func TestLogFilters_Matches_APIKeyOnly(t *testing.T) {
	f := logFilters{apiKey: "sk-9abc"}
	parsed := logLineFields{level: "info", raw: "[2025-12-23 20:14:04] [--------] [info  ] Use API key sk-9abc0RHO for model gpt-5.2"}
	if !f.matches(parsed) {
		t.Fatal("expected match for api_key 'sk-9abc' in log message")
	}

	parsed2 := logLineFields{level: "info", raw: "[2025-12-23 20:14:04] [--------] [info  ] Use API key sk-XXXX for model gpt-5.2"}
	if f.matches(parsed2) {
		t.Fatal("expected no match for different api_key")
	}
}

func TestLogFilters_Matches_CombinedFilters(t *testing.T) {
	f := logFilters{level: "error", model: "gpt-5.2", keyword: "timeout"}
	parsed := logLineFields{level: "error", model: "gpt-5.2", raw: "[2025-12-23 20:14:04] [--------] [error] connection timeout model=gpt-5.2"}
	if !f.matches(parsed) {
		t.Fatal("expected match when all filters pass")
	}

	// model doesn't match
	parsed2 := logLineFields{level: "error", model: "claude-sonnet-4", raw: "[2025-12-23 20:14:04] [--------] [error] connection timeout model=claude-sonnet-4"}
	if f.matches(parsed2) {
		t.Fatal("expected no match when model filter fails")
	}
}

func TestLogFilters_Matches_EmptyFilterMatchesAll(t *testing.T) {
	f := logFilters{}
	parsed := logLineFields{level: "info", raw: "[2025-12-23 20:14:04] [--------] [info  ] anything"}
	if !f.matches(parsed) {
		t.Fatal("empty filter should match all lines")
	}
}

// --- logAccumulator with filters ---

func TestLogAccumulator_WithLevelFilter(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "test.log")
	content := "[2025-12-23 20:14:04] [--------] [info  ] Starting server\n" +
		"[2025-12-23 20:14:05] [a1b2c3d4] [error] Connection failed\n" +
		"[2025-12-23 20:14:06] [b2c3d4e5] [info  ] Request processed\n" +
		"[2025-12-23 20:14:07] [c3d4e5f6] [warn  ] Slow query\n"
	if err := os.WriteFile(logFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	acc := newLogAccumulatorWithFilters(0, 0, logFilters{level: "error"})
	if err := acc.consumeFile(logFile); err != nil {
		t.Fatal(err)
	}
	lines, total, _ := acc.result()
	if total != 4 {
		t.Fatalf("expected total 4, got %d", total)
	}
	if len(lines) != 1 {
		t.Fatalf("expected 1 filtered line, got %d", len(lines))
	}
	if !anyLineContains(lines, "Connection failed") {
		t.Fatalf("expected 'Connection failed' in filtered results, got %v", lines)
	}
}

func TestLogAccumulator_WithKeywordFilter(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "test.log")
	content := "[2025-12-23 20:14:04] [--------] [info  ] Starting server\n" +
		"[2025-12-23 20:14:05] [a1b2c3d4] [error] Connection timeout\n" +
		"[2025-12-23 20:14:06] [b2c3d4e5] [info  ] Request processed\n"
	if err := os.WriteFile(logFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	acc := newLogAccumulatorWithFilters(0, 0, logFilters{keyword: "timeout"})
	if err := acc.consumeFile(logFile); err != nil {
		t.Fatal(err)
	}
	lines, _, _ := acc.result()
	if len(lines) != 1 {
		t.Fatalf("expected 1 line with 'timeout', got %d", len(lines))
	}
}

func TestLogAccumulator_WithModelFilter(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "test.log")
	content := "[2025-12-23 20:14:04] [--------] [info  ] Request model=gpt-5.2\n" +
		"[2025-12-23 20:14:05] [--------] [info  ] Request model=claude-sonnet-4\n" +
		"[2025-12-23 20:14:06] [--------] [info  ] Request model=gpt-5.2\n"
	if err := os.WriteFile(logFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	acc := newLogAccumulatorWithFilters(0, 0, logFilters{model: "gpt-5.2"})
	if err := acc.consumeFile(logFile); err != nil {
		t.Fatal(err)
	}
	lines, _, _ := acc.result()
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines for model 'gpt-5.2', got %d", len(lines))
	}
}

func TestLogAccumulator_WithAPIKeyFilter(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "test.log")
	content := "[2025-12-23 20:14:04] [--------] [info  ] Use API key sk-9abc0RHO for model gpt-5.2\n" +
		"[2025-12-23 20:14:05] [--------] [info  ] Use API key sk-XXXX0RHO for model gpt-5.2\n" +
		"[2025-12-23 20:14:06] [--------] [info  ] No key used\n"
	if err := os.WriteFile(logFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	acc := newLogAccumulatorWithFilters(0, 0, logFilters{apiKey: "sk-9abc"})
	if err := acc.consumeFile(logFile); err != nil {
		t.Fatal(err)
	}
	lines, _, _ := acc.result()
	if len(lines) != 1 {
		t.Fatalf("expected 1 line with api_key 'sk-9abc', got %d", len(lines))
	}
}

func TestLogAccumulator_WithCombinedFilters(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "test.log")
	content := "[2025-12-23 20:14:04] [--------] [info  ] Request model=gpt-5.2\n" +
		"[2025-12-23 20:14:05] [a1b2c3d4] [error] Timeout model=gpt-5.2\n" +
		"[2025-12-23 20:14:06] [b2c3d4e5] [error] Timeout model=claude-sonnet-4\n" +
		"[2025-12-23 20:14:07] [--------] [info  ] OK model=gpt-5.2\n"
	if err := os.WriteFile(logFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	acc := newLogAccumulatorWithFilters(0, 0, logFilters{level: "error", keyword: "Timeout", model: "gpt-5.2"})
	if err := acc.consumeFile(logFile); err != nil {
		t.Fatal(err)
	}
	lines, total, _ := acc.result()
	if total != 4 {
		t.Fatalf("expected total 4, got %d", total)
	}
	if len(lines) != 1 {
		t.Fatalf("expected 1 filtered line, got %d: %v", len(lines), lines)
	}
}

func TestLogAccumulator_FiltersWithCutoff(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "test.log")
	content := "[2025-12-23 20:14:04] [--------] [info  ] Before cutoff\n" +
		"[2025-12-23 20:14:06] [--------] [error] After cutoff error\n" +
		"[2025-12-23 20:14:07] [--------] [info  ] After cutoff info\n"
	if err := os.WriteFile(logFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	// cutoff at 20:14:05, filter level=error
	acc := newLogAccumulatorWithFilters(parseCutoff("1734977645"), 0, logFilters{level: "error"})
	if err := acc.consumeFile(logFile); err != nil {
		t.Fatal(err)
	}
	lines, _, _ := acc.result()
	if len(lines) != 1 {
		t.Fatalf("expected 1 line (after cutoff AND error level), got %d: %v", len(lines), lines)
	}
}

func TestLogAccumulator_FiltersWithLimit(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "test.log")
	content := "[2025-12-23 20:14:04] [--------] [error] Error 1\n" +
		"[2025-12-23 20:14:05] [--------] [error] Error 2\n" +
		"[2025-12-23 20:14:06] [--------] [error] Error 3\n"
	if err := os.WriteFile(logFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	acc := newLogAccumulatorWithFilters(0, 2, logFilters{level: "error"})
	if err := acc.consumeFile(logFile); err != nil {
		t.Fatal(err)
	}
	lines, _, _ := acc.result()
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines (limit=2), got %d", len(lines))
	}
	// Should keep the last 2
	if !contains(lines[0], "Error 2") {
		t.Fatalf("expected first line to contain 'Error 2', got %q", lines[0])
	}
}

func TestLogAccumulator_NoFiltersKeepAll(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "test.log")
	content := "[2025-12-23 20:14:04] [--------] [info  ] Line 1\n" +
		"[2025-12-23 20:14:05] [--------] [error] Line 2\n"
	if err := os.WriteFile(logFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	acc := newLogAccumulator(0, 0)
	if err := acc.consumeFile(logFile); err != nil {
		t.Fatal(err)
	}
	lines, _, _ := acc.result()
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines with no filters, got %d", len(lines))
	}
}

// anyLineContains checks if any line in the slice contains the substring.
func anyLineContains(lines []string, substr string) bool {
	for _, line := range lines {
		if contains(line, substr) {
			return true
		}
	}
	return false
}
