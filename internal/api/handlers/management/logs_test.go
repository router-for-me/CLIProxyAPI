package management

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestDecodeLogCursorRejectsUnsafeFiles(t *testing.T) {
	unsafeNames := []string{
		"",
		".",
		"..",
		"../secret",
		"nested/main.log",
		`nested\main.log`,
		"error.log",
	}

	for _, name := range unsafeNames {
		t.Run(name, func(t *testing.T) {
			raw := mustEncodeRawCursor(t, logCursor{
				Version:     logCursorVersion,
				File:        name,
				Fingerprint: "fingerprint",
			})
			if _, err := decodeLogCursor(raw); err == nil {
				t.Fatalf("decodeLogCursor(%q) succeeded, want error", name)
			}
		})
	}

	for _, name := range []string{defaultLogFileName, defaultLogFileName + ".1", "main-2026-06-15T10-00-00.log"} {
		t.Run("allowed_"+name, func(t *testing.T) {
			raw := mustEncodeRawCursor(t, logCursor{
				Version:     logCursorVersion,
				File:        name,
				Fingerprint: "fingerprint",
			})
			if _, err := decodeLogCursor(raw); err != nil {
				t.Fatalf("decodeLogCursor(%q) error = %v", name, err)
			}
		})
	}
}

func TestLogCursorRoundTripOmitsAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, defaultLogFileName)
	if err := os.WriteFile(path, []byte("line one\nline two\n"), 0o644); err != nil {
		t.Fatalf("write log file: %v", err)
	}

	boundary, errBoundary := completeLogBoundary(path)
	if errBoundary != nil {
		t.Fatalf("completeLogBoundary() error = %v", errBoundary)
	}
	raw, errCursor := newLogCursor(path, boundary, 123)
	if errCursor != nil {
		t.Fatalf("newLogCursor() error = %v", errCursor)
	}
	decoded, errDecode := decodeLogCursor(raw)
	if errDecode != nil {
		t.Fatalf("decodeLogCursor() error = %v", errDecode)
	}
	if decoded.File != defaultLogFileName {
		t.Fatalf("cursor file = %q, want %q", decoded.File, defaultLogFileName)
	}
	if decoded.Offset != boundary {
		t.Fatalf("cursor offset = %d, want %d", decoded.Offset, boundary)
	}
	if decoded.LatestTimestamp != 123 {
		t.Fatalf("cursor latest timestamp = %d, want 123", decoded.LatestTimestamp)
	}
	if strings.Contains(raw, dir) {
		t.Fatalf("encoded cursor contains log directory %q: %q", dir, raw)
	}
}

func TestReadCompleteLogLinesSkipsTrailingPartial(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, defaultLogFileName)
	initial := "first\nsecond\r\npartial"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatalf("write log file: %v", err)
	}

	read, errRead := readCompleteLogLines(path, 0, -1, 0)
	if errRead != nil {
		t.Fatalf("readCompleteLogLines() error = %v", errRead)
	}
	wantLines := []string{"first", "second"}
	if !reflect.DeepEqual(read.lines, wantLines) {
		t.Fatalf("lines = %#v, want %#v", read.lines, wantLines)
	}
	wantOffset := int64(len("first\nsecond\r\n"))
	if read.endOffset != wantOffset {
		t.Fatalf("endOffset = %d, want %d", read.endOffset, wantOffset)
	}

	file, errOpen := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if errOpen != nil {
		t.Fatalf("open log file: %v", errOpen)
	}
	if _, errWrite := file.WriteString("\n"); errWrite != nil {
		_ = file.Close()
		t.Fatalf("append newline: %v", errWrite)
	}
	if errClose := file.Close(); errClose != nil {
		t.Fatalf("close log file: %v", errClose)
	}

	next, errNext := readCompleteLogLines(path, read.endOffset, -1, 0)
	if errNext != nil {
		t.Fatalf("readCompleteLogLines() after append error = %v", errNext)
	}
	if !reflect.DeepEqual(next.lines, []string{"partial"}) {
		t.Fatalf("next lines = %#v, want partial", next.lines)
	}
	if next.endOffset != int64(len(initial)+1) {
		t.Fatalf("next endOffset = %d, want %d", next.endOffset, len(initial)+1)
	}
}

func TestGetLogsTailLimitReturnsRecentLinesWithCursor(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		"[2026-06-15 10:00:00] first",
		"[2026-06-15 10:00:01] second",
		"[2026-06-15 10:00:02] third",
		"[2026-06-15 10:00:03] fourth",
	}
	writeMainLog(t, dir, strings.Join(lines, "\n")+"\n")

	resp := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?limit=2")
	wantLines := []string{lines[2], lines[3]}
	if !reflect.DeepEqual(resp.Lines, wantLines) {
		t.Fatalf("lines = %#v, want %#v", resp.Lines, wantLines)
	}
	if resp.LineCount != 2 {
		t.Fatalf("line-count = %d, want 2", resp.LineCount)
	}
	if resp.NextCursor == "" {
		t.Fatal("next-cursor is empty")
	}
	wantLatest := time.Date(2026, 6, 15, 10, 0, 3, 0, time.Local).Unix()
	if resp.LatestTimestamp != wantLatest {
		t.Fatalf("latest-timestamp = %d, want %d", resp.LatestTimestamp, wantLatest)
	}
}

func TestGetLogsNoLimitKeepsFullScanBehavior(t *testing.T) {
	dir := t.TempDir()
	writeMainLog(t, dir, "complete\npartial")

	resp := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs")
	wantLines := []string{"complete", "partial"}
	if !reflect.DeepEqual(resp.Lines, wantLines) {
		t.Fatalf("lines = %#v, want %#v", resp.Lines, wantLines)
	}
	if resp.LineCount != 2 {
		t.Fatalf("line-count = %d, want full scan count 2", resp.LineCount)
	}
	if resp.NextCursor == "" {
		t.Fatal("next-cursor is empty")
	}
	cursor, errCursor := decodeLogCursor(resp.NextCursor)
	if errCursor != nil {
		t.Fatalf("decode next-cursor: %v", errCursor)
	}
	if cursor.Offset != int64(len("complete\n")) {
		t.Fatalf("cursor offset = %d, want complete-line boundary", cursor.Offset)
	}
}

func TestGetLogsAfterKeepsTimestampScanAndReturnsCursor(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		"[2026-06-15 10:00:00] first",
		"[2026-06-15 10:00:01] second",
		"[2026-06-15 10:00:02] third",
	}
	writeMainLog(t, dir, strings.Join(lines, "\n")+"\n")

	cutoff := time.Date(2026, 6, 15, 10, 0, 0, 0, time.Local).Unix()
	resp := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?after="+strconv.FormatInt(cutoff, 10))
	wantLines := []string{lines[1], lines[2]}
	if !reflect.DeepEqual(resp.Lines, wantLines) {
		t.Fatalf("lines = %#v, want %#v", resp.Lines, wantLines)
	}
	if resp.LineCount != 3 {
		t.Fatalf("line-count = %d, want full scan count 3", resp.LineCount)
	}
	if resp.NextCursor == "" {
		t.Fatal("next-cursor is empty")
	}
}

func mustEncodeRawCursor(t *testing.T, cursor logCursor) string {
	t.Helper()
	raw, err := json.Marshal(cursor)
	if err != nil {
		t.Fatalf("json.Marshal cursor: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

type logsAPIResponse struct {
	Lines           []string `json:"lines"`
	LineCount       int      `json:"line-count"`
	LatestTimestamp int64    `json:"latest-timestamp"`
	NextCursor      string   `json:"next-cursor"`
	CursorReset     bool     `json:"cursor-reset"`
}

func newLogsTestHandler(dir string, loggingToFile bool) *Handler {
	h := NewHandlerWithoutConfigFilePath(&config.Config{LoggingToFile: loggingToFile}, nil)
	h.SetLogDirectory(dir)
	return h
}

func performGetLogs(t *testing.T, h *Handler, target string) logsAPIResponse {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	h.GetLogs(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("GetLogs status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp logsAPIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Lines == nil {
		resp.Lines = []string{}
	}
	return resp
}

func writeMainLog(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, defaultLogFileName), []byte(content), 0o644); err != nil {
		t.Fatalf("write main log: %v", err)
	}
}
