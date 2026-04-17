package helps

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestReadStreamLinesReadsBlankAndTrailingLines(t *testing.T) {
	input := "data: one\n\ndata: two"
	var lines [][]byte

	err := ReadStreamLines(strings.NewReader(input), func(line []byte) error {
		lines = append(lines, append([]byte(nil), line...))
		return nil
	})
	if err != nil {
		t.Fatalf("ReadStreamLines error: %v", err)
	}

	if got := len(lines); got != 3 {
		t.Fatalf("line count = %d, want 3", got)
	}
	if !bytes.Equal(lines[0], []byte("data: one")) {
		t.Fatalf("line[0] = %q", lines[0])
	}
	if len(lines[1]) != 0 {
		t.Fatalf("line[1] = %q, want blank", lines[1])
	}
	if !bytes.Equal(lines[2], []byte("data: two")) {
		t.Fatalf("line[2] = %q", lines[2])
	}
}

func TestReadStreamLinesRejectsOversizedLine(t *testing.T) {
	oversized := strings.Repeat("a", streamLineMaxSizeBytes+1)
	err := ReadStreamLines(strings.NewReader(oversized), func([]byte) error { return nil })
	if !errors.Is(err, ErrStreamLineTooLong) {
		t.Fatalf("ReadStreamLines error = %v, want ErrStreamLineTooLong", err)
	}
}
