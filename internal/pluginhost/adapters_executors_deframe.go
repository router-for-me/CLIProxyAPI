package pluginhost

import (
	"bytes"
	"context"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// This file normalizes plugin executor SSE streams before stream translation.
//
// Same-format stream translators assume each incoming payload is exactly one
// complete event. A passthrough plugin relaying raw host HTTP stream bytes
// does not guarantee that: chunks may hold several coalesced events or split
// one event at an arbitrary byte. deframeExecutorSSEChunks rebuilds event
// boundaries so the translators always receive exactly one event at a time.
//
// Payloads that are not SSE at all (bare JSON objects, the stock example's
// unterminated "plugin-example" text) are never parsed: they are forwarded
// with their original chunk boundary intact, so consecutive bare objects
// without line breaks are not concatenated, and multi-line bare JSON is not
// split per line.

// executorSSEDeframer accumulates incoming plugin stream chunks only while
// they can still belong to an incomplete SSE event. Once an event boundary
// (a blank line) is reached the event is emitted whole. Chunk bytes that can
// no longer belong to an SSE event are released verbatim with their chunk
// boundary intact instead of being buffered or split. A partial prefix such
// as "da" is buffered because later bytes may complete a "data:" line.
//
// The deframer also remembers whether the stream has ever produced an SSE
// event (sawSSE). Established SSE streams treat blank lines between events
// as framing even when a blank line arrives in a later chunk, and bare
// lines are emitted one line at a time so a following SSE event can still
// be recognized. Streams that have never produced an SSE event stay in
// bare mode, where payloads are forwarded with their chunk boundaries
// intact and never split or merged.
type executorSSEDeframer struct {
	buffer []byte
	sawSSE bool
}

func (d *executorSSEDeframer) feed(payload []byte) [][]byte {
	if len(payload) == 0 {
		return nil
	}
	// Fast path: nothing pending, no SSE event seen yet, and the chunk
	// cannot begin an SSE event. Emit the chunk untouched so bare payloads
	// keep their boundaries, including bare content that opens with a
	// newline.
	if len(d.buffer) == 0 && !d.sawSSE && !canBeginSSEEvent(payload) {
		return [][]byte{payload}
	}
	d.buffer = append(d.buffer, payload...)

	var chunks [][]byte
	for {
		if len(d.buffer) == 0 {
			break
		}
		// Leading blank lines are the unmatched second half of an earlier
		// event boundary and are dropped. In bare mode this only runs after
		// an SSE event started in an earlier chunk (the fast path above
		// keeps newline-led bare payloads untouched).
		dropBlankSSELine := false
		if idx := bytes.IndexByte(d.buffer, '\n'); idx >= 0 && isBlankSSELine(d.buffer[:idx]) {
			d.advance(idx + 1)
			dropBlankSSELine = true
		}
		if dropBlankSSELine {
			continue
		}
		if len(d.buffer) == 0 {
			break
		}
		if !sseDataLinePrefix(d.buffer) && !bytes.HasPrefix(d.buffer, []byte("event:")) && !bytes.HasPrefix(d.buffer, []byte(":")) {
			// Leftover bytes do not begin a new SSE event. A partial
			// "data:"/"event:" prefix stays buffered. In SSE mode only the
			// first complete line is released so an SSE event that follows
			// in a later chunk is still deframed; in bare mode the whole
			// leftover is released so bare content keeps its chunk
			// boundary (no line-based splitting or merging).
			if isSSEFieldPrefixPartial(d.buffer) {
				break
			}
			if d.sawSSE {
				idx := bytes.IndexByte(d.buffer, '\n')
				if idx < 0 {
					break
				}
				chunks = append(chunks, bytes.Clone(d.buffer[:idx+1]))
				d.advance(idx + 1)
				continue
			}
			chunks = append(chunks, bytes.Clone(d.buffer))
			d.buffer = nil
			break
		}
		event, complete := d.takeEvent()
		if !complete {
			break
		}
		d.sawSSE = true
		if len(event) > 0 {
			chunks = append(chunks, event)
		}
	}
	return chunks
}

// flush returns any buffered partial event at end of stream.
func (d *executorSSEDeframer) flush() []byte {
	pending := d.buffer
	d.buffer = nil
	// Trailing CR/LF bytes are line framing only: the partial content before
	// them is what remains to be delivered, so a final "data: [DONE]\r" still
	// reads as the terminal marker. Interior whitespace (including a
	// leading newline) belongs to the payload and is preserved.
	pending = bytes.TrimRight(pending, "\r\n")
	if len(pending) == 0 {
		return nil
	}
	return pending
}

// canBeginSSEEvent reports whether payload could start an SSE event: a data
// line, an event line, a comment line, or a strict prefix of the "data:" or
// "event:" field names that later bytes may complete. A leading newline
// alone is not enough: bare JSON may open with a newline and must stay one
// untouched payload.
func canBeginSSEEvent(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	switch b[0] {
	case ':':
		return true
	case 'd':
		return sseFieldPrefixCandidate(b, "data:")
	case 'e':
		return sseFieldPrefixCandidate(b, "event:")
	}
	return false
}

func sseFieldPrefixCandidate(b []byte, field string) bool {
	if len(b) <= len(field) {
		return bytes.HasPrefix([]byte(field), b)
	}
	return bytes.HasPrefix(b, []byte(field))
}

// isSSEFieldPrefixPartial reports whether b is a strict prefix of "data:" or
// "event:": the field name may be completed by a later chunk, so these bytes
// must stay buffered instead of being released.
func isSSEFieldPrefixPartial(b []byte) bool {
	if len(b) == 0 || len(b) >= len("event:") {
		return false
	}
	return bytes.HasPrefix([]byte("data:"), b) || bytes.HasPrefix([]byte("event:"), b)
}

// sseDataLinePrefix reports whether b begins with an SSE data line. The
// space after the colon is optional in SSE, so any payload bytes after
// "data:" count as a data line.
func sseDataLinePrefix(b []byte) bool {
	return bytes.HasPrefix(b, []byte("data:"))
}

// takeEvent extracts the next complete SSE event when one is available.
// Comment-only blocks are consumed but not returned: they are SSE
// keep-alives with no data semantics, matching how built-in executors ignore
// ":" lines instead of forwarding them as payloads.
func (d *executorSSEDeframer) takeEvent() ([]byte, bool) {
	if len(d.buffer) == 0 {
		return nil, false
	}
	idx := indexSSEFrameBoundary(d.buffer)
	if idx < 0 {
		return nil, false
	}
	end := idx + 1
	event := bytes.Clone(d.buffer[:end])
	d.advance(end)
	if isSSECommentOnlyEvent(event) {
		return nil, true
	}
	return event, true
}

func (d *executorSSEDeframer) advance(n int) {
	rest := d.buffer[n:]
	if len(rest) == 0 {
		d.buffer = nil
		return
	}
	d.buffer = append(d.buffer[:0], rest...)
}

// deframeExecutorSSEChunks reassembles full SSE events from arbitrary chunk
// boundaries and passes bare payloads through with their chunk boundaries
// intact. It only runs for same-format streams with a registered native
// stream translator.
//
// It never synthesizes a terminal marker: emitTranslatedExecutorStreamTail
// in the translation goroutine owns that responsibility (it feeds one
// [DONE] through TranslateStream at channel close for [DONE]-terminated
// formats). Emitting a second synthetic marker here would run plugin
// response hooks twice and could surface two terminal frames.
func deframeExecutorSSEChunks(ctx context.Context, in <-chan pluginapi.ExecutorStreamChunk) <-chan pluginapi.ExecutorStreamChunk {
	if in == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	out := make(chan pluginapi.ExecutorStreamChunk)
	go func() {
		defer close(out)
		deframer := &executorSSEDeframer{}

		emit := func(payloads [][]byte) bool {
			for _, payload := range payloads {
				if len(payload) == 0 {
					continue
				}
				if !sendExecutorPluginStreamChunk(ctx, out, pluginapi.ExecutorStreamChunk{Payload: payload}) {
					return false
				}
			}
			return true
		}

		for {
			select {
			case <-ctx.Done():
				return
			case chunk, ok := <-in:
				if !ok {
					if tail := deframer.flush(); len(tail) > 0 {
						_ = emit([][]byte{tail})
					}
					return
				}
				if chunk.Err != nil {
					// Buffered partial events belong to the failed prefix;
					// emit them before the error is forwarded.
					if tail := deframer.flush(); len(tail) > 0 {
						if !emit([][]byte{tail}) {
							return
						}
					}
					_ = sendExecutorPluginStreamChunk(ctx, out, chunk)
					continue
				}
				if !emit(deframer.feed(chunk.Payload)) {
					return
				}
			}
		}
	}()
	return out
}

// indexSSEFrameBoundary scans complete lines and returns the index of the LF
// ending the blank line that terminates an SSE event, or -1 while the event
// is incomplete. Handling line by line keeps LF, CRLF, and mixed endings
// correct, including a CRLF blank line split across chunk boundaries.
func indexSSEFrameBoundary(b []byte) int {
	pos := 0
	for {
		idx := bytes.IndexByte(b[pos:], '\n')
		if idx < 0 {
			return -1
		}
		lineEnd := pos + idx
		if isBlankSSELine(b[pos:lineEnd]) {
			return lineEnd
		}
		pos = lineEnd + 1
	}
}

func isBlankSSELine(line []byte) bool {
	line = bytes.TrimSuffix(line, []byte{'\r'})
	return len(line) == 0
}

func isSSECommentOnlyEvent(event []byte) bool {
	sawComment := false
	for _, line := range bytes.Split(event, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		if !bytes.HasPrefix(trimmed, []byte(":")) {
			return false
		}
		sawComment = true
	}
	return sawComment
}
