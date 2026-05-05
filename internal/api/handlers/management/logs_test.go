package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

type logsResponse struct {
	Lines           []string `json:"lines"`
	LineCount       int      `json:"line-count"`
	LatestTimestamp int64    `json:"latest-timestamp"`
}

func TestGetLogsDefaultsToRecentLines(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()

	oldBase := time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local)
	newBase := time.Date(2026, 1, 2, 0, 0, 0, 0, time.Local)
	writeLogFile(t, filepath.Join(dir, "main-2026-01-01T00-00-00.log"), makeLogLines(oldBase, 10, "old"))
	writeLogFile(t, filepath.Join(dir, defaultLogFileName), makeLogLines(newBase, 250, "new"))

	resp := performGetLogs(t, dir, "/v0/management/logs")

	if len(resp.Lines) != defaultLogLimit {
		t.Fatalf("expected %d lines, got %d", defaultLogLimit, len(resp.Lines))
	}
	if resp.LineCount != defaultLogLimit {
		t.Fatalf("expected line-count %d, got %d", defaultLogLimit, resp.LineCount)
	}
	if !strings.Contains(resp.Lines[0], "new-051") {
		t.Fatalf("expected first returned line to be new-051, got %q", resp.Lines[0])
	}
	if !strings.Contains(resp.Lines[len(resp.Lines)-1], "new-250") {
		t.Fatalf("expected last returned line to be new-250, got %q", resp.Lines[len(resp.Lines)-1])
	}
	wantLatest := newBase.Add(249 * time.Second).Unix()
	if resp.LatestTimestamp != wantLatest {
		t.Fatalf("expected latest timestamp %d, got %d", wantLatest, resp.LatestTimestamp)
	}
}

func TestGetLogsClampsLargeLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()

	base := time.Date(2026, 1, 2, 0, 0, 0, 0, time.Local)
	writeLogFile(t, filepath.Join(dir, defaultLogFileName), makeLogLines(base, maxLogLimit+25, "line"))

	resp := performGetLogs(t, dir, "/v0/management/logs?limit=999999")

	if len(resp.Lines) != maxLogLimit {
		t.Fatalf("expected %d lines, got %d", maxLogLimit, len(resp.Lines))
	}
	if !strings.Contains(resp.Lines[0], "line-026") {
		t.Fatalf("expected first returned line to be line-026, got %q", resp.Lines[0])
	}
	if !strings.Contains(resp.Lines[len(resp.Lines)-1], "line-5025") {
		t.Fatalf("expected last returned line to be line-5025, got %q", resp.Lines[len(resp.Lines)-1])
	}
}

func TestGetLogsSkipsOldFilesForIncrementalReads(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()

	oldPath := filepath.Join(dir, "main-2026-01-01T00-00-00.log")
	writeLogFile(t, oldPath, []string{strings.Repeat("x", logScannerMaxBuffer+1)})
	oldTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatalf("failed to set old log mtime: %v", err)
	}

	newBase := time.Date(2026, 1, 2, 0, 0, 0, 0, time.Local)
	newPath := filepath.Join(dir, defaultLogFileName)
	writeLogFile(t, newPath, makeLogLines(newBase, 3, "new"))
	newTime := newBase.Add(time.Minute)
	if err := os.Chtimes(newPath, newTime, newTime); err != nil {
		t.Fatalf("failed to set active log mtime: %v", err)
	}

	cutoff := newBase.Add(-time.Second).Unix()
	resp := performGetLogs(t, dir, fmt.Sprintf("/v0/management/logs?after=%d", cutoff))

	if len(resp.Lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(resp.Lines))
	}
	if !strings.Contains(resp.Lines[0], "new-001") || !strings.Contains(resp.Lines[2], "new-003") {
		t.Fatalf("unexpected lines: %#v", resp.Lines)
	}
}

func TestGetLogsRejectsInvalidLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	writeLogFile(t, filepath.Join(dir, defaultLogFileName), []string{"[2026-01-01 00:00:00] hello"})

	h := &Handler{cfg: &config.Config{LoggingToFile: true}, logDir: dir}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/logs?limit=0", nil)

	h.GetLogs(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d: %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func performGetLogs(t *testing.T, dir string, target string) logsResponse {
	t.Helper()

	h := &Handler{cfg: &config.Config{LoggingToFile: true}, logDir: dir}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, target, nil)

	h.GetLogs(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var resp logsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	return resp
}

func writeLogFile(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("failed to write log file %s: %v", path, err)
	}
}

func makeLogLines(base time.Time, count int, prefix string) []string {
	lines := make([]string, 0, count)
	for i := 0; i < count; i++ {
		lines = append(lines, fmt.Sprintf("[%s] [info ] %s-%03d", base.Add(time.Duration(i)*time.Second).Format("2006-01-02 15:04:05"), prefix, i+1))
	}
	return lines
}
