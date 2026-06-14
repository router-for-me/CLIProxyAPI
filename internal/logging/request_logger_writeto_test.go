package logging

import (
	"bytes"
	"os"
	"testing"
)

// TestFileBodySource_WriteToReturnsBytesWritten verifies that WriteTo returns the
// number of bytes actually written to the destination writer.
//
// AppendPart stores data with a trailing newline (via writeLogPart), so the
// content read back by WriteTo includes that newline.
func TestFileBodySource_WriteToReturnsBytesWritten(t *testing.T) {
	logsDir := t.TempDir()
	source, errSource := NewFileBodySourceInDir(logsDir, "writeto-bytes-test")
	if errSource != nil {
		t.Fatalf("NewFileBodySourceInDir: %v", errSource)
	}
	defer source.Cleanup() //nolint:errcheck

	data := []byte("hello world")
	if errAppend := source.AppendPart(data); errAppend != nil {
		t.Fatalf("AppendPart: %v", errAppend)
	}

	var buf bytes.Buffer
	n, err := source.WriteTo(&buf)
	if err != nil {
		t.Fatalf("WriteTo() error = %v", err)
	}
	// AppendPart appends a trailing "\n" to the file, so the output is "hello world\n".
	wantContent := "hello world\n"
	if buf.String() != wantContent {
		t.Errorf("WriteTo() content = %q, want %q", buf.String(), wantContent)
	}
	if n != int64(len(wantContent)) {
		t.Errorf("WriteTo() n = %d, want %d", n, int64(len(wantContent)))
	}
}

// TestFileBodySource_WriteToNilReceiverReturnsZero verifies that a nil receiver
// returns (0, nil) without panicking.
func TestFileBodySource_WriteToNilReceiverReturnsZero(t *testing.T) {
	var source *FileBodySource
	var buf bytes.Buffer
	n, err := source.WriteTo(&buf)
	if err != nil {
		t.Fatalf("WriteTo() on nil receiver error = %v", err)
	}
	if n != 0 {
		t.Errorf("WriteTo() on nil receiver n = %d, want 0", n)
	}
}

// TestFileBodySource_WriteToNilWriterReturnsZero verifies that a nil writer
// returns (0, nil) without panicking.
func TestFileBodySource_WriteToNilWriterReturnsZero(t *testing.T) {
	logsDir := t.TempDir()
	source, errSource := NewFileBodySourceInDir(logsDir, "writeto-nil-writer-test")
	if errSource != nil {
		t.Fatalf("NewFileBodySourceInDir: %v", errSource)
	}
	defer source.Cleanup() //nolint:errcheck

	n, err := source.WriteTo(nil)
	if err != nil {
		t.Fatalf("WriteTo(nil) error = %v", err)
	}
	if n != 0 {
		t.Errorf("WriteTo(nil) n = %d, want 0", n)
	}
}

// TestFileBodySource_WriteToMultiplePartsInsertsSeparator verifies that multiple
// parts are separated by a newline and that the total byte count includes the
// separator.
func TestFileBodySource_WriteToMultiplePartsInsertsSeparator(t *testing.T) {
	logsDir := t.TempDir()
	source, errSource := NewFileBodySourceInDir(logsDir, "writeto-multipart-test")
	if errSource != nil {
		t.Fatalf("NewFileBodySourceInDir: %v", errSource)
	}
	defer source.Cleanup() //nolint:errcheck

	part1 := []byte("first")
	part2 := []byte("second")
	if errAppend := source.AppendPart(part1); errAppend != nil {
		t.Fatalf("AppendPart(part1): %v", errAppend)
	}
	if errAppend := source.AppendPart(part2); errAppend != nil {
		t.Fatalf("AppendPart(part2): %v", errAppend)
	}

	var buf bytes.Buffer
	n, err := source.WriteTo(&buf)
	if err != nil {
		t.Fatalf("WriteTo() error = %v", err)
	}

	// Each part file has a trailing "\n" added by AppendPart (via writeLogPart).
	// WriteTo inserts an additional "\n" separator between parts.
	// So two parts "first" and "second" produce: "first\n\nsecond\n".
	expected := "first\n\nsecond\n"
	if buf.String() != expected {
		t.Errorf("WriteTo() content = %q, want %q", buf.String(), expected)
	}

	wantN := int64(len(expected))
	if n != wantN {
		t.Errorf("WriteTo() n = %d, want %d", n, wantN)
	}
}

// TestFileBodySource_WriteToEmptySourceReturnsZero verifies that a source with
// no parts written returns (0, nil).
func TestFileBodySource_WriteToEmptySourceReturnsZero(t *testing.T) {
	logsDir := t.TempDir()
	source, errSource := NewFileBodySourceInDir(logsDir, "writeto-empty-test")
	if errSource != nil {
		t.Fatalf("NewFileBodySourceInDir: %v", errSource)
	}
	defer source.Cleanup() //nolint:errcheck

	var buf bytes.Buffer
	n, err := source.WriteTo(&buf)
	if err != nil {
		t.Fatalf("WriteTo() error = %v", err)
	}
	if n != 0 {
		t.Errorf("WriteTo() n = %d, want 0", n)
	}
	if buf.Len() != 0 {
		t.Errorf("WriteTo() wrote %d bytes to buffer, want 0", buf.Len())
	}
}

// TestFileBodySource_WriteToSkipsMissingParts verifies that if a part file has
// been removed externally (e.g. already cleaned up), WriteTo skips it silently
// and still accounts for bytes from existing parts.
func TestFileBodySource_WriteToSkipsMissingParts(t *testing.T) {
	logsDir := t.TempDir()
	source, errSource := NewFileBodySourceInDir(logsDir, "writeto-missing-test")
	if errSource != nil {
		t.Fatalf("NewFileBodySourceInDir: %v", errSource)
	}

	if errAppend := source.AppendPart([]byte("present")); errAppend != nil {
		t.Fatalf("AppendPart: %v", errAppend)
	}

	// Remove all part files before calling WriteTo to simulate missing files.
	paths := source.Paths()
	for _, p := range paths {
		if err := os.Remove(p); err != nil {
			t.Fatalf("remove %s: %v", p, err)
		}
	}

	var buf bytes.Buffer
	n, err := source.WriteTo(&buf)
	if err != nil {
		t.Fatalf("WriteTo() on missing files error = %v", err)
	}
	if n != 0 {
		t.Errorf("WriteTo() n = %d, want 0 when all files are missing", n)
	}
}

// TestFileBodySource_WriteToReturnsTotalBytesForThreeParts verifies byte counting
// across three parts with two separating newlines.
func TestFileBodySource_WriteToReturnsTotalBytesForThreeParts(t *testing.T) {
	logsDir := t.TempDir()
	source, errSource := NewFileBodySourceInDir(logsDir, "writeto-three-parts-test")
	if errSource != nil {
		t.Fatalf("NewFileBodySourceInDir: %v", errSource)
	}
	defer source.Cleanup() //nolint:errcheck

	parts := []string{"alpha", "beta", "gamma"}
	for _, p := range parts {
		if errAppend := source.AppendPart([]byte(p)); errAppend != nil {
			t.Fatalf("AppendPart(%q): %v", p, errAppend)
		}
	}

	var buf bytes.Buffer
	n, err := source.WriteTo(&buf)
	if err != nil {
		t.Fatalf("WriteTo() error = %v", err)
	}

	// Each part file has a trailing "\n" (appended by writeLogPart), and WriteTo
	// inserts a "\n" separator between consecutive parts.
	// Three parts "alpha", "beta", "gamma" produce: "alpha\n\nbeta\n\ngamma\n".
	expected := "alpha\n\nbeta\n\ngamma\n"
	if buf.String() != expected {
		t.Errorf("WriteTo() content = %q, want %q", buf.String(), expected)
	}
	if n != int64(len(expected)) {
		t.Errorf("WriteTo() n = %d, want %d", n, int64(len(expected)))
	}
}