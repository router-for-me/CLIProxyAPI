package helps

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
)

// codexSSEDiagnosticLines caps how many data lines one invalid-event diagnostic describes.
const codexSSEDiagnosticLines = 8

var codexSSEDataPrefix = []byte("data:")

// CodexSSEScanner reads an upstream Responses SSE stream line by line, as bufio.Scanner does, but
// delivers an event whose data the upstream split across several "data:" lines as one "data:" line.
// In SSE an event's data may span several data lines, joined with "\n". Taking each data line as a
// whole event instead cuts such an event: on a split response.completed the stream ends at the
// first fragment and the rest is never read.
//
// Every event on this path is one JSON value. A data line that is complete JSON on its own, or
// cannot start JSON at all (an empty value, [DONE]), is delivered byte-for-byte as read with no
// lookahead, which keeps upstreams that omit the blank line between events working as before. A
// data line that opens a JSON object or array without completing it starts an event that collects
// the following data lines until one of these:
//   - their joined value closes its outermost object or array;
//   - a blank line ends the event;
//   - the stream ends;
//   - the collected bytes would exceed the line limit.
//
// A completed event is delivered compacted onto one line. One that never becomes valid JSON is
// delivered as the same multi-line data field, so it still fails downstream, and one warning
// describes its framing without any of its bytes. Other lines pass through in order.
type CodexSSEScanner struct {
	ctx      context.Context
	lines    *bufio.Scanner
	maxBytes int
	guard    func(lines, bytes int) bool
	queue    [][]byte
	current  []byte
	// consumedLines and consumedBytes are the upstream lines and bytes behind current.
	consumedLines int
	consumedBytes int
}

// NewCodexSSEScanner returns a CodexSSEScanner over r. maxBytes bounds one line, as
// bufio.Scanner.Buffer does, and also the data lines collected for one event.
func NewCodexSSEScanner(ctx context.Context, r io.Reader, maxBytes int) *CodexSSEScanner {
	lines := bufio.NewScanner(r)
	lines.Buffer(nil, maxBytes)
	return &CodexSSEScanner{ctx: ctx, lines: lines, maxBytes: maxBytes}
}

// Scan advances to the next line, or to the next assembled event data line.
func (s *CodexSSEScanner) Scan() bool {
	if len(s.queue) > 0 {
		s.current, s.queue = s.queue[0], s.queue[1:]
		s.consumedLines, s.consumedBytes = 1, len(s.current)
		return true
	}
	if !s.lines.Scan() {
		return false
	}
	line := s.lines.Bytes()
	if !bytes.HasPrefix(line, codexSSEDataPrefix) || !codexSSEIncompleteJSON(bytes.TrimSpace(line[len(codexSSEDataPrefix):])) {
		s.current = line
		s.consumedLines, s.consumedBytes = 1, len(line)
		return true
	}
	// An incomplete event: collect its continuation. The underlying scanner reuses its buffer, so keep copies.
	first := bytes.Clone(line)
	data := [][]byte{first}
	read := [][]byte{first}
	joined := bytes.Clone(codexSSEValue(line))
	var nesting codexJSONNesting
	closed := nesting.feed(joined)
	var rest [][]byte
	size := len(line)
	for !closed && s.lines.Scan() {
		next := s.lines.Bytes()
		if len(next) == 0 {
			rest = append(rest, []byte{})
			break
		}
		if size+len(next) > s.maxBytes {
			// Bounded like a single line: stop collecting and let the next line be read on its own.
			rest = append(rest, bytes.Clone(next))
			break
		}
		if s.guard != nil && !s.guard(len(read)+1, size+len(next)) {
			// The caller's budget does not cover this event: deliver its lines one by one, as read.
			s.current = first
			s.consumedLines, s.consumedBytes = 1, len(first)
			s.queue = append(read[1:], bytes.Clone(next))
			return true
		}
		size += len(next)
		kept := bytes.Clone(next)
		read = append(read, kept)
		if !bytes.HasPrefix(next, codexSSEDataPrefix) {
			rest = append(rest, kept)
			continue
		}
		data = append(data, kept)
		value := codexSSEValue(next)
		joined = append(append(joined, '\n'), value...)
		closed = nesting.feed([]byte{'\n'}) || nesting.feed(value)
	}
	s.current = s.assemble(data, joined)
	s.consumedLines, s.consumedBytes = len(data), 0
	for _, line := range data {
		s.consumedBytes += len(line)
	}
	s.queue = rest
	return true
}

// Consumed reports how many upstream lines and bytes the current line stands for: one line and its
// own length, or, for an event assembled from several data lines, all of those lines. Lines that
// arrived inside such an event but are not data are delivered, and counted, on their own.
func (s *CodexSSEScanner) Consumed() (lines, bytes int) { return s.consumedLines, s.consumedBytes }

// SetCollectGuard installs a check made before an incomplete event collects each further line.
// It receives how many upstream lines and bytes this Scan would then have read in total, the same
// quantities Consumed and the queued lines will report. When it returns false, the scanner stops
// collecting and delivers the lines read so far one by one, as bufio.Scanner would, so a caller
// can keep its own per-line budgets while it holds a stream. A nil guard removes the check.
func (s *CodexSSEScanner) SetCollectGuard(guard func(lines, bytes int) bool) { s.guard = guard }

// codexSSEValue is one data line's value: the text after "data:" without the one leading space.
func codexSSEValue(line []byte) []byte {
	return bytes.TrimPrefix(line[len(codexSSEDataPrefix):], []byte(" "))
}

// codexSSEClosed reports whether joined event data is complete JSON. Only a value that now ends
// with a closing bracket can have become complete, so other values are not re-validated.
func codexSSEClosed(joined []byte) bool {
	trimmed := bytes.TrimRight(joined, " \t\r\n")
	if len(trimmed) == 0 || (trimmed[len(trimmed)-1] != '}' && trimmed[len(trimmed)-1] != ']') {
		return false
	}
	return json.Valid(joined)
}

// codexSSEIncompleteJSON reports whether one data line's value starts a JSON object or array that
// it does not complete. Values that cannot start JSON, such as an empty value or [DONE], are not.
func codexSSEIncompleteJSON(value []byte) bool {
	if len(value) == 0 || (value[0] != '{' && value[0] != '[') || bytes.Equal(value, []byte("[DONE]")) {
		return false
	}
	return !codexSSEClosed(value)
}

// codexJSONNesting follows the nesting of a JSON text fed in pieces, so an event split across many
// data lines is validated once, when its outermost object or array closes, not after every piece.
type codexJSONNesting struct {
	depth    int
	inString bool
	escaped  bool
}

// feed consumes p and reports whether the outermost object or array closed within it.
func (n *codexJSONNesting) feed(p []byte) bool {
	for _, c := range p {
		switch {
		case n.inString:
			switch {
			case n.escaped:
				n.escaped = false
			case c == '\\':
				n.escaped = true
			case c == '"':
				n.inString = false
			}
		case c == '"':
			n.inString = true
		case c == '{' || c == '[':
			n.depth++
		case c == '}' || c == ']':
			n.depth--
			if n.depth <= 0 {
				return true
			}
		}
	}
	return false
}

// Bytes returns the current line. It stays valid until the next call to Scan.
func (s *CodexSSEScanner) Bytes() []byte { return s.current }

// Err returns the error that stopped the scan, as bufio.Scanner does.
func (s *CodexSSEScanner) Err() error { return s.lines.Err() }

func (s *CodexSSEScanner) assemble(lines [][]byte, joined []byte) []byte {
	if len(lines) == 1 {
		s.diagnose(lines, bytes.TrimSpace(joined))
		return lines[0]
	}
	if json.Valid(joined) {
		var compacted bytes.Buffer
		compacted.Grow(len(codexSSEDataPrefix) + 1 + len(joined))
		compacted.Write(codexSSEDataPrefix)
		compacted.WriteByte(' ')
		if errCompact := json.Compact(&compacted, joined); errCompact == nil {
			return compacted.Bytes()
		}
	}
	s.diagnose(lines, joined)
	return bytes.Join(lines, []byte("\n"))
}

// diagnose logs one bounded warning when an event's data is not valid JSON: how many data lines
// carried it, each line's length and whether it is valid on its own, and where the joined data
// fails to parse. It never logs data bytes.
func (s *CodexSSEScanner) diagnose(lines [][]byte, data []byte) {
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) || json.Valid(data) {
		return
	}
	offset := int64(-1)
	var syntaxErr *json.SyntaxError
	if errors.As(json.Unmarshal(data, new(json.RawMessage)), &syntaxErr) {
		offset = syntaxErr.Offset
	}
	shown := min(len(lines), codexSSEDiagnosticLines)
	lengths := make([]string, shown)
	valid := make([]string, shown)
	for i := range shown {
		value := bytes.TrimSpace(lines[i][len(codexSSEDataPrefix):])
		lengths[i] = strconv.Itoa(len(value))
		valid[i] = strconv.FormatBool(json.Valid(value))
	}
	more := ""
	if len(lines) > shown {
		more = ",+" + strconv.Itoa(len(lines)-shown)
	}
	LogWithRequestID(s.ctx).WithFields(log.Fields{
		"data_lines":        len(lines),
		"line_bytes":        strings.Join(lengths, ",") + more,
		"lines_valid":       strings.Join(valid, ",") + more,
		"data_bytes":        len(data),
		"json_error_offset": offset,
	}).Warn("codex executor: upstream SSE event data is not valid JSON")
}
