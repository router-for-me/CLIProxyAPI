package helps

import "bytes"

// RelaySSEBuffer reassembles SSE frames across arbitrary relay chunk boundaries.
// Bare JSON relay messages remain whole, including pretty-printed JSON.
type RelaySSEBuffer struct {
	pending   []byte
	sse       bool
	scan      int
	lineStart int
}

// Feed returns complete frames and retains an unfinished SSE frame for the next chunk.
func (b *RelaySSEBuffer) Feed(chunk []byte) [][]byte {
	b.pending = append(b.pending, chunk...)
	if !b.sse {
		trimmed := bytes.TrimLeft(b.pending, " \t\r\n")
		if len(trimmed) == 0 {
			return nil
		}
		partialPrefix := false
		for _, prefix := range []string{"data:", "event:", "id:", "retry:", ":"} {
			if bytes.HasPrefix(trimmed, []byte(prefix)) {
				b.sse = true
				break
			}
			partialPrefix = partialPrefix || bytes.HasPrefix([]byte(prefix), trimmed)
		}
		if !b.sse {
			if partialPrefix {
				return nil
			}
			return b.Finish()
		}
	}

	var frames [][]byte
	for b.scan < len(b.pending) {
		offset := bytes.IndexByte(b.pending[b.scan:], '\n')
		if offset < 0 {
			b.scan = len(b.pending)
			break
		}
		end := b.scan + offset
		blank := end == b.lineStart || (end == b.lineStart+1 && b.pending[b.lineStart] == '\r')
		b.scan, b.lineStart = end+1, end+1
		if blank {
			frames = append(frames, bytes.Clone(b.pending[:end+1]))
			b.pending = b.pending[end+1:]
			b.scan, b.lineStart = 0, 0
		}
	}
	if len(b.pending) == 0 {
		b.pending = nil
	}
	return frames
}

// Finish flushes a final frame from relays that omit its trailing blank line.
func (b *RelaySSEBuffer) Finish() [][]byte {
	if len(b.pending) == 0 {
		return nil
	}
	frame := b.pending
	b.pending = nil
	b.scan, b.lineStart = 0, 0
	return [][]byte{frame}
}
