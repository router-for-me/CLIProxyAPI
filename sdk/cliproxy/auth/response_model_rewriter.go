package auth

import (
	"bytes"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var modelFieldPaths = []string{"model", "modelVersion", "response.model", "response.modelVersion", "message.model"}

const maxPendingBufSize = 1 << 20 // 1MB limit for pending buffer

func rewriteSSEPayloadLines(payload []byte, targetModel string) []byte {
	if targetModel == "" || len(payload) == 0 {
		return payload
	}
	lines := bytes.Split(payload, []byte("\n"))
	out := make([][]byte, 0, len(lines))
	for _, line := range lines {
		prefix, jsonData, ok := extractSSEDataLine(line)
		if ok && len(jsonData) > 0 && jsonData[0] == '{' && gjson.ValidBytes(jsonData) {
			rewritten := rewriteModelInResponse(jsonData, targetModel)
			line = append(append([]byte{}, prefix...), rewritten...)
		}
		out = append(out, line)
	}
	joined := bytes.Join(out, []byte("\n"))
	if len(payload) > 0 && payload[len(payload)-1] == '\n' && (len(joined) == 0 || joined[len(joined)-1] != '\n') {
		joined = append(joined, '\n')
	}
	return joined
}

func rewriteModelInResponse(data []byte, targetModel string) []byte {
	if targetModel == "" || len(data) == 0 {
		return data
	}
	for _, path := range modelFieldPaths {
		if gjson.GetBytes(data, path).Exists() {
			data, _ = sjson.SetBytes(data, path, targetModel)
			log.Debugf("response rewriter: rewrote model at path %s to %s", path, targetModel)
		}
	}
	return data
}

// StreamRewriteOptions configures the stream rewriter.
type StreamRewriteOptions struct {
	RewriteModel string
}

// StreamRewriter rewrites model names in streaming SSE responses.
type StreamRewriter struct {
	options    StreamRewriteOptions
	pendingBuf []byte
}

// NewStreamRewriter creates a new stream rewriter.
func NewStreamRewriter(options StreamRewriteOptions) *StreamRewriter {
	return &StreamRewriter{
		options:    options,
		pendingBuf: nil,
	}
}

// RewriteChunk rewrites model names in a single SSE chunk.
func (r *StreamRewriter) RewriteChunk(chunk []byte) []byte {
	if r.options.RewriteModel == "" {
		return chunk
	}

	if len(r.pendingBuf) > 0 {
		combined := make([]byte, 0, len(r.pendingBuf)+1+len(chunk))
		combined = append(combined, r.pendingBuf...)
		if combined[len(combined)-1] != '\n' {
			combined = append(combined, '\n')
		}
		combined = append(combined, chunk...)
		chunk = combined
		r.pendingBuf = nil
	}
	chunk = normalizeGluedSSEEvents(chunk)

	if len(chunk) > maxPendingBufSize {
		return chunk
	}

	// Handle raw JSON chunks (Gemini/OpenAI format without SSE "data:" prefix)
	trimmed := bytes.TrimSpace(chunk)
	if len(trimmed) > 0 && trimmed[0] == '{' && gjson.ValidBytes(trimmed) {
		rewritten := trimmed
		if r.options.RewriteModel != "" {
			rewritten = rewriteModelInResponse(rewritten, r.options.RewriteModel)
		}
		return rewritten
	}

	lastDoubleNewline := bytes.LastIndex(chunk, []byte("\n\n"))

	var processChunk []byte
	if lastDoubleNewline >= 0 {
		afterComplete := chunk[lastDoubleNewline+2:]
		if len(afterComplete) > 0 && !bytes.Equal(afterComplete, []byte("\n")) {
			processChunk = chunk[:lastDoubleNewline+2]
			r.pendingBuf = make([]byte, len(afterComplete))
			copy(r.pendingBuf, afterComplete)
		} else {
			processChunk = chunk
		}
	} else if gjson.ValidBytes(extractLastDataPayload(chunk)) {
		processChunk = chunk
	} else if len(bytes.TrimSpace(chunk)) == 0 {
		return chunk
	} else if len(chunk) > 0 {
		r.pendingBuf = make([]byte, len(chunk))
		copy(r.pendingBuf, chunk)
		return nil
	} else {
		return chunk
	}

	lines := bytes.Split(processChunk, []byte("\n"))
	var result [][]byte
	var pendingEvent []byte
	skipBlanks := false

	for _, line := range lines {
		if len(line) == 0 && skipBlanks {
			continue
		}
		if len(line) != 0 && skipBlanks {
			skipBlanks = false
		}

		if bytes.HasPrefix(line, []byte("event:")) {
			pendingEvent = line
			continue
		}

		dataPrefix, jsonData, found := extractSSEDataLine(line)
		if found && len(jsonData) > 0 && jsonData[0] == '{' {
			if !gjson.ValidBytes(jsonData) {
				if pendingEvent != nil {
					r.pendingBuf = append(pendingEvent, '\n')
					r.pendingBuf = append(r.pendingBuf, line...)
					pendingEvent = nil
				} else {
					r.pendingBuf = append(r.pendingBuf, line...)
				}
				continue
			}

			if pendingEvent != nil {
				result = append(result, pendingEvent)
				pendingEvent = nil
			}

			rewritten := jsonData
			if r.options.RewriteModel != "" {
				rewritten = rewriteModelInResponse(jsonData, r.options.RewriteModel)
			}
			result = append(result, append(dataPrefix, rewritten...))
			continue
		}

		if pendingEvent != nil {
			result = append(result, pendingEvent)
			pendingEvent = nil
		}
		result = append(result, line)
	}

	if pendingEvent != nil {
		result = append(result, pendingEvent)
	}

	joined := bytes.Join(result, []byte("\n"))
	if len(joined) == 0 && len(chunk) > 0 {
		return rewriteSSEPayloadLines(chunk, r.options.RewriteModel)
	}
	return joined
}

func extractLastDataPayload(chunk []byte) []byte {
	lines := bytes.Split(chunk, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		if _, jsonData, found := extractSSEDataLine(lines[i]); found && len(jsonData) > 0 {
			return jsonData
		}
	}
	return nil
}

func extractSSEDataLine(line []byte) (prefix []byte, jsonData []byte, ok bool) {
	if jsonData, found := bytes.CutPrefix(line, []byte("data: ")); found {
		return []byte("data: "), jsonData, true
	}
	if jsonData, found := bytes.CutPrefix(line, []byte("data:")); found {
		return []byte("data:"), jsonData, true
	}
	return nil, nil, false
}

func normalizeGluedSSEEvents(chunk []byte) []byte {
	if len(chunk) == 0 {
		return chunk
	}
	// Antigravity/Gemini translators emit event frames without trailing blank lines.
	// When multiple frames are buffered back-to-back they can glue as "...}event:...".
	// Only split when the bytes before the glue close a valid SSE data JSON object.
	chunk = safeReplaceGlued(chunk, []byte("}event:"), []byte("}\n\nevent:"))
	chunk = safeReplaceGlued(chunk, []byte("}\r\nevent:"), []byte("}\r\n\r\nevent:"))
	// Codex executor emits one "data: {json}" chunk per SSE line without trailing newlines.
	// Buffered chunks can glue as "...}data:...".
	chunk = safeReplaceGlued(chunk, []byte("}data:"), []byte("}\ndata:"))
	chunk = safeReplaceGlued(chunk, []byte("}\r\ndata:"), []byte("}\r\ndata:"))
	return chunk
}

func safeReplaceGlued(chunk []byte, old, new []byte) []byte {
	if len(old) == 0 || len(chunk) == 0 {
		return chunk
	}
	if !bytes.Contains(chunk, old) {
		return chunk
	}
	var result []byte
	remaining := chunk
	for {
		idx := bytes.Index(remaining, old)
		if idx == -1 {
			result = append(result, remaining...)
			break
		}
		lineStart := bytes.LastIndexByte(remaining[:idx], '\n')
		var part []byte
		if lineStart == -1 {
			part = remaining[:idx+1]
		} else {
			part = remaining[lineStart+1 : idx+1]
		}
		_, jsonData, ok := extractSSEDataLine(part)
		if ok && len(jsonData) > 0 && gjson.ValidBytes(jsonData) {
			result = append(result, remaining[:idx]...)
			result = append(result, new...)
			remaining = remaining[idx+len(old):]
			continue
		}
		result = append(result, remaining[:idx+len(old)]...)
		remaining = remaining[idx+len(old):]
	}
	return result
}

// Finish flushes any buffered partial SSE data at the end of a stream.
func (r *StreamRewriter) Finish() []byte {
	if len(r.pendingBuf) == 0 {
		return nil
	}
	buf := make([]byte, len(r.pendingBuf)+2)
	copy(buf, r.pendingBuf)
	buf[len(r.pendingBuf)] = '\n'
	buf[len(r.pendingBuf)+1] = '\n'
	buf = normalizeGluedSSEEvents(buf)
	r.pendingBuf = nil
	out := r.RewriteChunk(buf)
	if len(r.pendingBuf) > 0 {
		tail := rewriteSSEPayloadLines(r.pendingBuf, r.options.RewriteModel)
		r.pendingBuf = nil
		if len(tail) > 0 {
			if len(out) > 0 {
				out = append(out, tail...)
			} else {
				out = tail
			}
		}
	}
	return out
}
