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

type codexSSEQueuedItem struct {
	payload  []byte
	rawLines int
	rawBytes int
}

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
// delivered as the same multi-line data field, so it still fails downstream. The first such event
// in a stream gets one warning that describes its framing without any of its bytes.
//
// The scanner merges data lines and nothing else. It holds only the data lines of an event being
// collected and delivers every other line the moment it reads it, in the order read. A comment,
// id: or retry: line inside such an event therefore comes before the event's data instead of
// between its pieces, which SSE treats alike: an event's fields take effect when it is dispatched.
// The blank line or over-limit line that ends a collection follows the event's data.
//
// SetAssembly(false) makes the scanner read exactly as bufio.Scanner does, for a caller that
// budgets every upstream line while it holds a stream.
type CodexSSEScanner struct {
	ctx      context.Context
	lines    *bufio.Scanner
	maxBytes int
	// stock is set while assembly is off.
	stock bool
	// held is the event being collected: its data lines as read, joined with "\n" in one buffer, so
	// its memory follows its bytes, not its line count. count is how many lines it holds and size
	// their bytes. joined is their joined value, and nesting follows it.
	held    []byte
	count   int
	size    int
	joined  []byte
	nesting codexJSONNesting
	queue   []codexSSEQueuedItem
	current codexSSEQueuedItem
	// warned is set once the stream has had its invalid-event warning.
	warned      bool
	onLine      func(line []byte)
	budgetCheck func(line []byte, heldLines int, heldBytes int) bool
}

// NewCodexSSEScanner returns a CodexSSEScanner over r, with assembly on. maxBytes bounds one line,
// as bufio.Scanner.Buffer does, and also the data lines collected for one event.
func NewCodexSSEScanner(ctx context.Context, r io.Reader, maxBytes int) *CodexSSEScanner {
	lines := bufio.NewScanner(r)
	lines.Buffer(nil, maxBytes)
	return &CodexSSEScanner{ctx: ctx, lines: lines, maxBytes: maxBytes}
}

// SetOnLine sets a callback invoked for every physical line read from upstream.
func (s *CodexSSEScanner) SetOnLine(fn func(line []byte)) {
	s.onLine = fn
}

// HeldLines returns how many data lines are currently held in an incomplete event.
func (s *CodexSSEScanner) HeldLines() int {
	return s.count
}

// HeldBytes returns how many bytes are currently held in an incomplete event.
func (s *CodexSSEScanner) HeldBytes() int {
	return s.size
}

// SetBudgetChecker sets an optional callback invoked on each raw line read while an event is being held.
// If it returns true, the scanner yields from Scan() with an empty payload to signal that the
// caller should release buffering, while preserving the held assembly state for subsequent Scan() calls.
func (s *CodexSSEScanner) SetBudgetChecker(fn func(line []byte, heldLines int, heldBytes int) bool) {
	s.budgetCheck = fn
}

// SetAssembly turns event assembly on or off. While it is off, no data line starts a collection and
// every line is delivered as read, exactly as bufio.Scanner delivers it. An event already being
// collected is still completed.
func (s *CodexSSEScanner) SetAssembly(on bool) { s.stock = !on }

// Scan advances to the next line, or to the next assembled event data line.
func (s *CodexSSEScanner) Scan() bool {
	if len(s.queue) > 0 {
		s.current, s.queue = s.queue[0], s.queue[1:]
		return true
	}
	for s.lines.Scan() {
		line := s.lines.Bytes()
		if s.onLine != nil {
			s.onLine(line)
		}
		if !bytes.HasPrefix(line, codexSSEDataPrefix) {
			if len(line) == 0 && s.count > 0 {
				s.release([]byte{})
				return s.Scan()
			}
			s.current = codexSSEQueuedItem{payload: line, rawLines: 1, rawBytes: len(line)}
			return true
		}
		if s.count == 0 {
			if s.stock || !codexSSEIncompleteJSON(bytes.TrimSpace(line[len(codexSSEDataPrefix):])) {
				s.current = codexSSEQueuedItem{payload: line, rawLines: 1, rawBytes: len(line)}
				return true
			}
		} else if s.size+len(line) > s.maxBytes {
			// Bounded like a single line: stop collecting and let this line be read on its own.
			s.release(bytes.Clone(line))
			return s.Scan()
		}

		closed := s.hold(line)
		if !closed && s.budgetCheck != nil && s.budgetCheck(line, s.count, s.size) {
			s.budgetCheck = nil
			s.current = codexSSEQueuedItem{payload: nil, rawLines: 0, rawBytes: 0}
			return true
		}
		if closed {
			s.release()
			return s.Scan()
		}
	}
	if s.count > 0 {
		s.release()
		return s.Scan()
	}
	return false
}

// hold adds a data line to the event being collected and reports whether its data has closed.
func (s *CodexSSEScanner) hold(line []byte) bool {
	value := codexSSEValue(line)
	if s.count > 0 {
		s.held = append(s.held, '\n')
		s.joined = append(s.joined, '\n')
		s.nesting.feed([]byte{'\n'})
	}
	s.held = append(s.held, line...)
	s.joined = append(s.joined, value...)
	s.count++
	s.size += len(line)
	return s.nesting.feed(value)
}

// release ends the collection and queues the event as one line, followed by the line that ended
// it, if any.
func (s *CodexSSEScanner) release(end ...[]byte) {
	assembled := s.assemble(s.held, s.count, s.joined)
	s.queue = append(s.queue, codexSSEQueuedItem{
		payload:  assembled,
		rawLines: s.count,
		rawBytes: s.size,
	})
	for _, e := range end {
		s.queue = append(s.queue, codexSSEQueuedItem{
			payload:  e,
			rawLines: 1,
			rawBytes: len(e),
		})
	}
	s.held, s.count, s.size, s.joined, s.nesting = nil, 0, 0, nil, codexJSONNesting{}
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
func (s *CodexSSEScanner) Bytes() []byte { return s.current.payload }

// RawLines returns the count of physical upstream lines consumed to produce the current event.
func (s *CodexSSEScanner) RawLines() int {
	if s.current.rawLines == 0 {
		return 1
	}
	return s.current.rawLines
}

// RawBytes returns the total bytes of physical upstream lines consumed to produce the current event.
func (s *CodexSSEScanner) RawBytes() int {
	if s.current.rawBytes == 0 {
		return len(s.current.payload)
	}
	return s.current.rawBytes
}

// Err returns the error that stopped the scan, as bufio.Scanner does.
func (s *CodexSSEScanner) Err() error { return s.lines.Err() }

// assemble returns the event held as lines data lines: compacted onto one line when its data is
// valid JSON, otherwise as held.
func (s *CodexSSEScanner) assemble(held []byte, lines int, joined []byte) []byte {
	if lines == 1 {
		s.diagnose(held, lines, bytes.TrimSpace(joined))
		return held
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
	s.diagnose(held, lines, joined)
	return held
}

// diagnose logs one bounded warning for the first event in the stream whose data is not valid
// JSON, so a broken upstream cannot turn every event into a log line. It describes how many data
// lines carried it, each line's length and whether it is valid on its own, and where the joined
// data fails to parse. It never logs data bytes.
func (s *CodexSSEScanner) diagnose(held []byte, lines int, data []byte) {
	if s.warned || len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) || json.Valid(data) {
		return
	}
	s.warned = true
	offset := int64(-1)
	var syntaxErr *json.SyntaxError
	if errors.As(json.Unmarshal(data, new(json.RawMessage)), &syntaxErr) {
		offset = syntaxErr.Offset
	}
	shown := min(lines, codexSSEDiagnosticLines)
	lengths := make([]string, shown)
	valid := make([]string, shown)
	for i := range shown {
		var line []byte
		line, held, _ = bytes.Cut(held, []byte("\n"))
		value := bytes.TrimSpace(line[len(codexSSEDataPrefix):])
		lengths[i] = strconv.Itoa(len(value))
		valid[i] = strconv.FormatBool(json.Valid(value))
	}
	more := ""
	if lines > shown {
		more = ",+" + strconv.Itoa(lines-shown)
	}
	LogWithRequestID(s.ctx).WithFields(log.Fields{
		"data_lines":        lines,
		"line_bytes":        strings.Join(lengths, ",") + more,
		"lines_valid":       strings.Join(valid, ",") + more,
		"data_bytes":        len(data),
		"json_error_offset": offset,
	}).Warn("codex executor: upstream SSE event data is not valid JSON")
}
