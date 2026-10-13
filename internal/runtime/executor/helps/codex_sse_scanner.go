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
//   - a data line that is a complete JSON object on its own starts the next event;
//   - the collected bytes would exceed the line limit.
//
// A completed event is delivered compacted onto one line. One that never becomes valid JSON is
// delivered as the same multi-line data field, so it still fails downstream, and one warning
// describes its framing without any of its bytes. Other lines pass through in order.
type CodexSSEScanner struct {
	ctx      context.Context
	lines    *bufio.Scanner
	maxBytes int
	queue    [][]byte
	current  []byte
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
		return true
	}
	if !s.lines.Scan() {
		return false
	}
	line := s.lines.Bytes()
	if !bytes.HasPrefix(line, codexSSEDataPrefix) || !codexSSEIncompleteJSON(bytes.TrimSpace(line[len(codexSSEDataPrefix):])) {
		s.current = line
		return true
	}
	// An incomplete event: collect its continuation. The underlying scanner reuses its buffer, so keep copies.
	data := [][]byte{bytes.Clone(line)}
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
		size += len(next)
		if !bytes.HasPrefix(next, codexSSEDataPrefix) {
			rest = append(rest, bytes.Clone(next))
			continue
		}
		if value := bytes.TrimSpace(next[len(codexSSEDataPrefix):]); bytes.HasPrefix(value, []byte("{")) && json.Valid(value) {
			// A complete object of its own is the next event of an upstream that sends no blank lines.
			rest = append(rest, bytes.Clone(next))
			break
		}
		data = append(data, bytes.Clone(next))
		value := codexSSEValue(next)
		joined = append(append(joined, '\n'), value...)
		closed = nesting.feed([]byte{'\n'}) || nesting.feed(value)
	}
	s.current = s.assemble(data, joined)
	s.queue = rest
	return true
}

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
