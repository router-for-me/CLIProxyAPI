// Package sse provides bounded, JSON-aware Server-Sent Events framing helpers.
package sse

import (
	"bufio"
	"bytes"
	"io"

	"github.com/tidwall/gjson"
)

// LineScanner scans SSE fields and separates safely glued JSON data frames.
type LineScanner struct {
	scanner *bufio.Scanner
	pending [][]byte
	current []byte
}

// NewLineScanner creates a scanner with a bounded pending token size.
func NewLineScanner(reader io.Reader, maxTokenBytes int) *LineScanner {
	scanner := bufio.NewScanner(reader)
	if maxTokenBytes > 0 {
		scanner.Buffer(nil, maxTokenBytes)
	}
	return &LineScanner{scanner: scanner}
}

// Scan advances to the next SSE field line.
func (s *LineScanner) Scan() bool {
	for len(s.pending) == 0 {
		if !s.scanner.Scan() {
			return false
		}
		normalized := NormalizeGluedFrames(s.scanner.Bytes())
		for _, line := range bytes.Split(normalized, []byte("\n")) {
			line = bytes.TrimSuffix(line, []byte("\r"))
			if len(line) > 0 {
				s.pending = append(s.pending, bytes.Clone(line))
			}
		}
	}
	s.current = s.pending[0]
	s.pending = s.pending[1:]
	return true
}

// Bytes returns the current field line.
func (s *LineScanner) Bytes() []byte { return s.current }

// Err returns the underlying scanner error.
func (s *LineScanner) Err() error { return s.scanner.Err() }

// NormalizeGluedFrames safely separates adjacent SSE fields when the preceding
// data field contains complete JSON. It iterates so three or more glued frames
// are normalized without splitting marker-like text inside JSON strings.
func NormalizeGluedFrames(chunk []byte) []byte {
	if len(chunk) == 0 {
		return chunk
	}
	for {
		normalized := normalizeGluedPass(chunk)
		if bytes.Equal(normalized, chunk) {
			return normalized
		}
		chunk = normalized
	}
}

func normalizeGluedPass(chunk []byte) []byte {
	chunk = safeReplaceGlued(chunk, []byte("}event:"), []byte("}\n\nevent:"))
	chunk = safeReplaceGlued(chunk, []byte("}\r\nevent:"), []byte("}\r\n\r\nevent:"))
	chunk = safeReplaceGlued(chunk, []byte("}data:"), []byte("}\ndata:"))
	chunk = safeReplaceGlued(chunk, []byte("}\r\ndata:"), []byte("}\r\ndata:"))
	return chunk
}

func safeReplaceGlued(chunk, old, replacement []byte) []byte {
	for searchFrom := 0; searchFrom < len(chunk); {
		relative := bytes.Index(chunk[searchFrom:], old)
		if relative < 0 {
			return chunk
		}
		idx := searchFrom + relative
		lineStart := bytes.LastIndexByte(chunk[:idx], '\n') + 1
		part := bytes.TrimSuffix(chunk[lineStart:idx+1], []byte("\r"))
		jsonData, ok := extractData(part)
		if ok && len(jsonData) > 0 && gjson.ValidBytes(jsonData) {
			out := make([]byte, 0, len(chunk)-len(old)+len(replacement))
			out = append(out, chunk[:idx]...)
			out = append(out, replacement...)
			out = append(out, chunk[idx+len(old):]...)
			return out
		}
		searchFrom = idx + len(old)
	}
	return chunk
}

func extractData(line []byte) ([]byte, bool) {
	if data, ok := bytes.CutPrefix(line, []byte("data: ")); ok {
		return data, true
	}
	if data, ok := bytes.CutPrefix(line, []byte("data:")); ok {
		return data, true
	}
	return nil, false
}
