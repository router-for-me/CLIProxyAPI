package openai

import (
	"bytes"
	"io"
)

// rawImageSSEState tracks only the trailing line endings, without buffering or
// rewriting upstream events. CRLF counts as one line ending even across chunks.
type rawImageSSEState struct {
	trailingNewlines uint8
	lastCR           bool
}

func (s *rawImageSSEState) observe(chunk []byte) {
	prefix := bytes.TrimRight(chunk, "\r\n")
	if len(prefix) > 0 {
		s.trailingNewlines = 0
		s.lastCR = false
	}
	for _, b := range chunk[len(prefix):] {
		if b == '\n' && s.lastCR {
			s.lastCR = false
			continue
		}
		if s.trailingNewlines < 2 {
			s.trailingNewlines++
		}
		s.lastCR = b == '\r'
	}
}

func (s *rawImageSSEState) atBoundary() bool {
	return s.trailingNewlines == 2
}

// Separate a terminal error from an unfinished upstream event. Successful streams
// remain byte-for-byte passthrough; this does not try to repair truncated JSON.
func (s *rawImageSSEState) ensureBoundary(w io.Writer) {
	if s.atBoundary() {
		return
	}
	separator := "\n\n"
	if s.trailingNewlines == 1 && !s.lastCR {
		separator = "\n"
	}
	_, _ = io.WriteString(w, separator)
	s.trailingNewlines = 2
	s.lastCR = false
}
