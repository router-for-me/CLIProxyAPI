package helps

import (
	"bytes"

	"github.com/MoeForge/moego/ojson"
)

// SSEJSONAssembler reassembles JSON payloads that may span multiple SSE data
// lines while isolating malformed events to their own SSE event boundary.
type SSEJSONAssembler struct {
	pending           []byte
	fromData          bool
	discardUntilBlank bool
	maxBytes          int
}

// NewSSEJSONAssembler creates an SSEJSONAssembler bounded by maxBytes.
// Non-positive limits default to StreamScannerBuffer.
func NewSSEJSONAssembler(maxBytes int) *SSEJSONAssembler {
	if maxBytes <= 0 {
		maxBytes = StreamScannerBuffer
	}
	return &SSEJSONAssembler{maxBytes: maxBytes}
}

// Reset clears any buffered partial JSON state.
func (a *SSEJSONAssembler) Reset() {
	if a == nil {
		return
	}
	a.pending = nil
	a.fromData = false
	a.discardUntilBlank = false
}

// Push feeds one scanned SSE line and returns a complete JSON payload when one
// becomes valid, or nil if more lines are needed or the line carries no JSON.
func (a *SSEJSONAssembler) Push(line []byte) []byte {
	if a == nil {
		return JSONPayload(line)
	}
	maxBytes := a.maxBytes
	if maxBytes <= 0 {
		maxBytes = StreamScannerBuffer
	}

	// Only a genuine empty line (after stripping at most one trailing CR)
	// terminates an SSE event; whitespace-only non-empty lines (including
	// multiple CRs) are ignored fields and must not discard in-progress state.
	if len(bytes.TrimSuffix(line, []byte("\r"))) == 0 {
		a.Reset()
		return nil
	}
	if a.discardUntilBlank {
		return nil
	}

	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil
	}

	if len(a.pending) > 0 {
		if isSSEControlLine(trimmed) {
			return nil
		}
		var chunk []byte
		if bytes.HasPrefix(trimmed, []byte("data:")) {
			chunk = bytes.TrimSpace(trimmed[len("data:"):])
		} else if bytes.Equal(trimmed, []byte("data")) {
			return nil
		} else if a.fromData {
			return nil
		} else {
			chunk = trimmed
		}
		if bytes.Equal(chunk, []byte("[DONE]")) {
			a.Reset()
			return nil
		}
		if len(chunk) == 0 {
			return nil
		}
		if len(a.pending)+1+len(chunk) > maxBytes {
			wasFromData := a.fromData
			a.Reset()
			if wasFromData {
				a.discardUntilBlank = true
			}
			return nil
		}
		a.pending = append(a.pending, '\n')
		a.pending = append(a.pending, chunk...)
		if !ojson.ValidBytes(a.pending) {
			return nil
		}
		payload := a.pending
		a.Reset()
		return payload
	}

	payload := JSONPayload(line)
	if payload == nil {
		return nil
	}
	if ojson.ValidBytes(payload) {
		return payload
	}
	isFromData := bytes.HasPrefix(trimmed, []byte("data:"))
	if len(payload) > maxBytes {
		if isFromData {
			a.discardUntilBlank = true
		}
		return nil
	}
	a.pending = append([]byte(nil), payload...)
	a.fromData = isFromData
	return nil
}

func isSSEControlLine(trimmed []byte) bool {
	if len(trimmed) == 0 {
		return false
	}
	if trimmed[0] == ':' {
		return true
	}
	return bytes.HasPrefix(trimmed, []byte("event:")) ||
		bytes.HasPrefix(trimmed, []byte("id:")) ||
		bytes.HasPrefix(trimmed, []byte("retry:")) ||
		bytes.Equal(trimmed, []byte("event")) ||
		bytes.Equal(trimmed, []byte("id")) ||
		bytes.Equal(trimmed, []byte("retry"))
}
