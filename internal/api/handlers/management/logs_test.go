package management

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	if resp.LineCount != len(wantLines) {
		t.Fatalf("line-count = %d, want returned line count %d", resp.LineCount, len(wantLines))
	}
	if resp.NextCursor == "" {
		t.Fatal("next-cursor is empty")
	}
	wantLatest := time.Date(2026, 6, 15, 10, 0, 3, 0, time.Local).Unix()
	if resp.LatestTimestamp != wantLatest {
		t.Fatalf("latest-timestamp = %d, want %d", resp.LatestTimestamp, wantLatest)
	}
}

func TestGetLogsTailLimitDoesNotScanOlderFilesForLineCount(t *testing.T) {
	dir := t.TempDir()
	rotatedPath := filepath.Join(dir, defaultLogFileName+".1")
	if err := os.WriteFile(rotatedPath, []byte(strings.Repeat("x", logScannerMaxBuffer+1)+"\n"), 0o644); err != nil {
		t.Fatalf("write rotated log: %v", err)
	}
	writeMainLog(t, dir, "[2026-06-15 10:00:00] current\n")

	resp := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?limit=1")
	wantLines := []string{"[2026-06-15 10:00:00] current"}
	if !reflect.DeepEqual(resp.Lines, wantLines) {
		t.Fatalf("lines = %#v, want %#v", resp.Lines, wantLines)
	}
	if resp.LineCount != len(wantLines) {
		t.Fatalf("line-count = %d, want returned line count %d", resp.LineCount, len(wantLines))
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

func TestGetLogsCursorReturnsOnlyNewCompleteLines(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		"[2026-06-15 10:00:00] first",
		"[2026-06-15 10:00:01] second",
		"[2026-06-15 10:00:02] third",
	}
	writeMainLog(t, dir, strings.Join(lines, "\n")+"\n")
	initial := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?limit=2")
	if initial.NextCursor == "" {
		t.Fatal("initial next-cursor is empty")
	}

	appendMainLog(t, dir, "[2026-06-15 10:00:03] fourth\n")
	resp := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape(initial.NextCursor)+"&limit=10")
	wantLines := []string{"[2026-06-15 10:00:03] fourth"}
	if !reflect.DeepEqual(resp.Lines, wantLines) {
		t.Fatalf("lines = %#v, want %#v", resp.Lines, wantLines)
	}
	if resp.LineCount != 1 {
		t.Fatalf("line-count = %d, want 1", resp.LineCount)
	}
	if resp.CursorReset {
		t.Fatal("cursor-reset = true, want false")
	}
	wantLatest := time.Date(2026, 6, 15, 10, 0, 3, 0, time.Local).Unix()
	if resp.LatestTimestamp != wantLatest {
		t.Fatalf("latest-timestamp = %d, want %d", resp.LatestTimestamp, wantLatest)
	}
}

func TestGetLogsCursorRejectsOversizedLine(t *testing.T) {
	dir := t.TempDir()
	writeMainLog(t, dir, "[2026-06-15 10:00:00] first\n")
	initial := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?limit=1")
	if initial.NextCursor == "" {
		t.Fatal("initial next-cursor is empty")
	}

	appendMainLog(t, dir, strings.Repeat("x", logScannerMaxBuffer+1)+"\n")
	status, body := performGetLogsRaw(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape(initial.NextCursor)+"&limit=1")
	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", status, http.StatusInternalServerError)
	}
	if !strings.Contains(body, "log line exceeds") {
		t.Fatalf("body = %s, want oversized line error", body)
	}
}

func TestGetLogsCursorNoNewLinesKeepsCursorStable(t *testing.T) {
	dir := t.TempDir()
	line := "[2026-06-15 10:00:00] first"
	writeMainLog(t, dir, line+"\n")
	initial := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?limit=1")

	resp := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape(initial.NextCursor)+"&limit=10")
	if len(resp.Lines) != 0 {
		t.Fatalf("lines = %#v, want empty", resp.Lines)
	}
	if resp.LineCount != 0 {
		t.Fatalf("line-count = %d, want 0", resp.LineCount)
	}
	if resp.NextCursor != initial.NextCursor {
		t.Fatalf("next-cursor changed with no complete lines")
	}
	if resp.LatestTimestamp != initial.LatestTimestamp {
		t.Fatalf("latest-timestamp = %d, want %d", resp.LatestTimestamp, initial.LatestTimestamp)
	}
}

func TestGetLogsCursorDoesNotAdvancePastTrailingPartial(t *testing.T) {
	dir := t.TempDir()
	line := "[2026-06-15 10:00:00] first"
	writeMainLog(t, dir, line+"\n")
	initial := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?limit=1")

	appendMainLog(t, dir, "partial")
	partial := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape(initial.NextCursor)+"&limit=10")
	if len(partial.Lines) != 0 {
		t.Fatalf("partial lines = %#v, want empty", partial.Lines)
	}
	if partial.NextCursor != initial.NextCursor {
		t.Fatalf("cursor advanced past partial line")
	}

	appendMainLog(t, dir, "\n")
	complete := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape(initial.NextCursor)+"&limit=10")
	if !reflect.DeepEqual(complete.Lines, []string{"partial"}) {
		t.Fatalf("complete lines = %#v, want partial", complete.Lines)
	}
	if complete.LatestTimestamp != initial.LatestTimestamp {
		t.Fatalf("latest-timestamp = %d, want %d", complete.LatestTimestamp, initial.LatestTimestamp)
	}
}

func TestGetLogsCursorResetAfterTruncateTailsLimit(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		"[2026-06-15 10:00:00] first",
		"[2026-06-15 10:00:01] second",
		"[2026-06-15 10:00:02] third",
	}
	writeMainLog(t, dir, strings.Join(lines, "\n")+"\n")
	initial := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?limit=3")

	resetLine := "[2026-06-15 10:00:03] reset"
	writeMainLog(t, dir, resetLine+"\n")
	resp := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape(initial.NextCursor)+"&limit=1")
	if !resp.CursorReset {
		t.Fatal("cursor-reset = false, want true")
	}
	if !reflect.DeepEqual(resp.Lines, []string{resetLine}) {
		t.Fatalf("lines = %#v, want reset tail", resp.Lines)
	}
	if resp.LineCount != 1 {
		t.Fatalf("line-count = %d, want 1", resp.LineCount)
	}
}

func TestGetLogsCursorReadsAcrossRotation(t *testing.T) {
	dir := t.TempDir()
	line1 := "[2026-06-15 10:00:00] first"
	line2 := "[2026-06-15 10:00:01] second"
	line3 := "[2026-06-15 10:00:02] third"
	writeMainLog(t, dir, line1+"\n")
	initial := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?limit=1")

	appendMainLog(t, dir, line2+"\n")
	if err := os.Rename(filepath.Join(dir, defaultLogFileName), filepath.Join(dir, defaultLogFileName+".1")); err != nil {
		t.Fatalf("rotate main log: %v", err)
	}
	writeMainLog(t, dir, line3+"\n")

	resp := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape(initial.NextCursor)+"&limit=10")
	wantLines := []string{line2, line3}
	if !reflect.DeepEqual(resp.Lines, wantLines) {
		t.Fatalf("lines = %#v, want %#v", resp.Lines, wantLines)
	}
	if resp.CursorReset {
		t.Fatal("cursor-reset = true, want false")
	}
}

func TestGetLogsCursorReadsRotatedFileWhenNewMainIsSmaller(t *testing.T) {
	dir := t.TempDir()
	line1 := "[2026-06-15 10:00:00] first line with enough bytes"
	line2 := "[2026-06-15 10:00:01] second"
	line3 := "new"
	writeMainLog(t, dir, line1+"\n")
	initial := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?limit=1")

	appendMainLog(t, dir, line2+"\n")
	if err := os.Rename(filepath.Join(dir, defaultLogFileName), filepath.Join(dir, defaultLogFileName+".1")); err != nil {
		t.Fatalf("rotate main log: %v", err)
	}
	writeMainLog(t, dir, line3+"\n")

	resp := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape(initial.NextCursor)+"&limit=1")
	if !reflect.DeepEqual(resp.Lines, []string{line2}) {
		t.Fatalf("lines = %#v, want rotated unread line", resp.Lines)
	}
	if resp.CursorReset {
		t.Fatal("cursor-reset = true, want false")
	}

	next := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape(resp.NextCursor)+"&limit=1")
	if !reflect.DeepEqual(next.Lines, []string{line3}) {
		t.Fatalf("next lines = %#v, want new main line", next.Lines)
	}
	if next.CursorReset {
		t.Fatal("next cursor-reset = true, want false")
	}
}

func TestGetLogsZeroOffsetCursorWithPartialLineReadsAcrossRotation(t *testing.T) {
	dir := t.TempDir()
	writeMainLog(t, dir, "partial")
	initial := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?limit=1")
	if initial.NextCursor == "" {
		t.Fatal("initial next-cursor is empty")
	}
	cursor, errCursor := decodeLogCursor(initial.NextCursor)
	if errCursor != nil {
		t.Fatalf("decode initial cursor: %v", errCursor)
	}
	if cursor.Offset != 0 || cursor.Size == 0 {
		t.Fatalf("cursor offset/size = %d/%d, want zero offset with partial size", cursor.Offset, cursor.Size)
	}

	appendMainLog(t, dir, " complete\n")
	if err := os.Rename(filepath.Join(dir, defaultLogFileName), filepath.Join(dir, defaultLogFileName+".1")); err != nil {
		t.Fatalf("rotate main log: %v", err)
	}
	writeMainLog(t, dir, "new\n")

	resp := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape(initial.NextCursor)+"&limit=10")
	wantLines := []string{"partial complete", "new"}
	if !reflect.DeepEqual(resp.Lines, wantLines) {
		t.Fatalf("lines = %#v, want %#v", resp.Lines, wantLines)
	}
	if resp.CursorReset {
		t.Fatal("cursor-reset = true, want false")
	}
}

func TestGetLogsZeroOffsetCursorWithEmptyFileReadsAcrossRotation(t *testing.T) {
	dir := t.TempDir()
	writeMainLog(t, dir, "")
	initial := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?limit=1")
	if initial.NextCursor == "" {
		t.Fatal("initial next-cursor is empty")
	}
	cursor, errCursor := decodeLogCursor(initial.NextCursor)
	if errCursor != nil {
		t.Fatalf("decode initial cursor: %v", errCursor)
	}
	if cursor.Offset != 0 || cursor.Size != 0 {
		t.Fatalf("cursor offset/size = %d/%d, want empty zero offset", cursor.Offset, cursor.Size)
	}

	appendMainLog(t, dir, "first\n")
	mainPath := filepath.Join(dir, defaultLogFileName)
	nextModTime := time.Unix(0, cursorModTimeUnixNano(cursor)+int64(time.Second))
	if err := os.Chtimes(mainPath, nextModTime, nextModTime); err != nil {
		t.Fatalf("update main log mtime: %v", err)
	}
	if err := os.Rename(mainPath, filepath.Join(dir, defaultLogFileName+".1")); err != nil {
		t.Fatalf("rotate main log: %v", err)
	}
	writeMainLog(t, dir, "second\n")

	resp := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape(initial.NextCursor)+"&limit=1")
	if !reflect.DeepEqual(resp.Lines, []string{"first"}) {
		t.Fatalf("lines = %#v, want first rotated line", resp.Lines)
	}
	if resp.CursorReset {
		t.Fatal("cursor-reset = true, want false")
	}

	next := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape(resp.NextCursor)+"&limit=1")
	if !reflect.DeepEqual(next.Lines, []string{"second"}) {
		t.Fatalf("next lines = %#v, want second main line", next.Lines)
	}
	if next.CursorReset {
		t.Fatal("next cursor-reset = true, want false")
	}
}

func TestGetLogsInvalidCursorResetsToTail(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		"[2026-06-15 10:00:00] first",
		"[2026-06-15 10:00:01] second",
	}
	writeMainLog(t, dir, strings.Join(lines, "\n")+"\n")

	cases := []string{
		"not-base64",
		mustEncodeRawCursor(t, logCursor{
			Version:     logCursorVersion,
			File:        "../secret",
			Fingerprint: "fingerprint",
		}),
	}
	for _, raw := range cases {
		resp := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape(raw)+"&limit=1")
		if !resp.CursorReset {
			t.Fatalf("cursor-reset = false for cursor %q", raw)
		}
		if !reflect.DeepEqual(resp.Lines, []string{lines[1]}) {
			t.Fatalf("lines = %#v, want latest line", resp.Lines)
		}
		if resp.LineCount != 1 {
			t.Fatalf("line-count = %d, want 1", resp.LineCount)
		}
	}
}

func TestGetLogsMissingRotatedCursorFileResetsToTail(t *testing.T) {
	dir := t.TempDir()
	current := "[2026-06-15 10:00:01] current"
	writeMainLog(t, dir, current+"\n")
	rotatedPath := filepath.Join(dir, defaultLogFileName+".1")
	if err := os.WriteFile(rotatedPath, []byte("[2026-06-15 10:00:00] old\n"), 0o644); err != nil {
		t.Fatalf("write rotated log: %v", err)
	}
	cursor, errCursor := newLogCursor(rotatedPath, int64(len("[2026-06-15 10:00:00] old\n")), 0)
	if errCursor != nil {
		t.Fatalf("newLogCursor() error = %v", errCursor)
	}
	if errRemove := os.Remove(rotatedPath); errRemove != nil {
		t.Fatalf("remove rotated log: %v", errRemove)
	}

	resp := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape(cursor)+"&limit=1")
	if !resp.CursorReset {
		t.Fatal("cursor-reset = false, want true")
	}
	if !reflect.DeepEqual(resp.Lines, []string{current}) {
		t.Fatalf("lines = %#v, want current tail", resp.Lines)
	}
}

func TestGetLogsMissingLogDirKeepsOKEmptyResponse(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	resp := performGetLogs(t, newLogsTestHandler(dir, true), "/v0/management/logs?cursor="+url.QueryEscape("not-base64")+"&limit=1")
	if len(resp.Lines) != 0 {
		t.Fatalf("lines = %#v, want empty", resp.Lines)
	}
	if resp.LineCount != 0 {
		t.Fatalf("line-count = %d, want 0", resp.LineCount)
	}
	if !resp.CursorReset {
		t.Fatal("cursor-reset = false, want true for cursor against missing log dir")
	}
}

func TestGetLogsLoggingDisabledKeepsBadRequest(t *testing.T) {
	status, body := performGetLogsRaw(t, newLogsTestHandler(t.TempDir(), false), "/v0/management/logs?cursor=not-base64&limit=1")
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
	}
	if !strings.Contains(body, "logging to file disabled") {
		t.Fatalf("body = %s, want logging disabled error", body)
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
	status, body := performGetLogsRaw(t, h, target)
	if status != http.StatusOK {
		t.Fatalf("GetLogs status = %d, body = %s", status, body)
	}
	var resp logsAPIResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Lines == nil {
		resp.Lines = []string{}
	}
	return resp
}

func performGetLogsRaw(t *testing.T, h *Handler, target string) (int, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	h.GetLogs(c)
	return rec.Code, rec.Body.String()
}

func writeMainLog(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, defaultLogFileName), []byte(content), 0o644); err != nil {
		t.Fatalf("write main log: %v", err)
	}
}

func appendMainLog(t *testing.T, dir, content string) {
	t.Helper()
	file, errOpen := os.OpenFile(filepath.Join(dir, defaultLogFileName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if errOpen != nil {
		t.Fatalf("open main log: %v", errOpen)
	}
	if _, errWrite := file.WriteString(content); errWrite != nil {
		_ = file.Close()
		t.Fatalf("append main log: %v", errWrite)
	}
	if errClose := file.Close(); errClose != nil {
		t.Fatalf("close main log: %v", errClose)
	}
}
