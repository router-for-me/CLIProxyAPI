package management

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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

func mustEncodeRawCursor(t *testing.T, cursor logCursor) string {
	t.Helper()
	raw, err := json.Marshal(cursor)
	if err != nil {
		t.Fatalf("json.Marshal cursor: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}
