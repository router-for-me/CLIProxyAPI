// Package sse provides bounded, JSON-aware Server-Sent Events framing helpers.
package sse

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
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
// data field contains complete JSON. It scans each data value once so inputs
// containing many glued frames are processed linearly.
func NormalizeGluedFrames(chunk []byte) []byte {
	if len(chunk) == 0 {
		return chunk
	}

	var out []byte
	cursor := 0
	changed := false
	for cursor < len(chunk) {
		dataStart := nextDataFieldStart(chunk, cursor)
		if dataStart < 0 {
			if !changed {
				return chunk
			}
			out = append(out, chunk[cursor:]...)
			break
		}
		out = append(out, chunk[cursor:dataStart]...)

		jsonStart := dataStart + len("data:")
		for jsonStart < len(chunk) && (chunk[jsonStart] == ' ' || chunk[jsonStart] == '\t') {
			jsonStart++
		}
		jsonEnd, ok := completeJSONValueEnd(chunk, jsonStart)
		if !ok {
			if !changed {
				return chunk
			}
			out = append(out, chunk[dataStart:]...)
			break
		}
		out = append(out, chunk[dataStart:jsonEnd]...)
		cursor = jsonEnd

		switch {
		case bytes.HasPrefix(chunk[cursor:], []byte("data:")):
			out = append(out, '\n')
			changed = true
		case bytes.HasPrefix(chunk[cursor:], []byte("event:")):
			out = append(out, '\n', '\n')
			changed = true
		case bytes.HasPrefix(chunk[cursor:], []byte("\r\ndata:")):
			out = append(out, '\r', '\n')
			cursor += 2
		case bytes.HasPrefix(chunk[cursor:], []byte("\r\nevent:")):
			out = append(out, '\r', '\n', '\r', '\n')
			cursor += 2
			changed = true
		default:
			if cursor < len(chunk) {
				out = append(out, chunk[cursor])
				cursor++
			}
		}
	}
	if !changed {
		return chunk
	}
	return out
}

func nextDataFieldStart(chunk []byte, from int) int {
	if from < len(chunk) && bytes.HasPrefix(chunk[from:], []byte("data:")) {
		return from
	}
	if relative := bytes.Index(chunk[from:], []byte("\ndata:")); relative >= 0 {
		return from + relative + 1
	}
	return -1
}

func completeJSONValueEnd(data []byte, start int) (int, bool) {
	if start >= len(data) || (data[start] != '{' && data[start] != '[') {
		return 0, false
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(data); i++ {
		current := data[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch current {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch current {
		case '"':
			inString = true
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				end := i + 1
				return end, json.Valid(data[start:end])
			}
			if depth < 0 {
				return 0, false
			}
		}
	}
	return 0, false
}
