package amp

import (
	"bytes"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ResponseRewriter wraps a gin.ResponseWriter to intercept and modify the response body
// It's used to rewrite model names in responses when model mapping is used
type ResponseRewriter struct {
	gin.ResponseWriter
	body          *bytes.Buffer
	originalModel string
	isStreaming   bool
}

// NewResponseRewriter creates a new response rewriter for model name substitution
func NewResponseRewriter(w gin.ResponseWriter, originalModel string) *ResponseRewriter {
	return &ResponseRewriter{
		ResponseWriter: w,
		body:           &bytes.Buffer{},
		originalModel:  originalModel,
	}
}

const maxBufferedResponseBytes = 2 * 1024 * 1024 // 2MB safety cap

func looksLikeSSEChunk(data []byte) bool {
	// Fallback detection: some upstreams may omit/lie about Content-Type, causing SSE to be buffered.
	// Heuristics are intentionally simple and cheap.
	return bytes.Contains(data, []byte("data:")) ||
		bytes.Contains(data, []byte("event:")) ||
		bytes.Contains(data, []byte("message_start")) ||
		bytes.Contains(data, []byte("message_delta")) ||
		bytes.Contains(data, []byte("content_block_start")) ||
		bytes.Contains(data, []byte("content_block_delta")) ||
		bytes.Contains(data, []byte("content_block_stop")) ||
		bytes.Contains(data, []byte("\n\n"))
}

func (rw *ResponseRewriter) enableStreaming(reason string) error {
	if rw.isStreaming {
		return nil
	}
	rw.isStreaming = true

	// Flush any previously buffered data to avoid reordering or data loss.
	if rw.body != nil && rw.body.Len() > 0 {
		buf := rw.body.Bytes()
		// Copy before Reset() to keep bytes stable.
		toFlush := make([]byte, len(buf))
		copy(toFlush, buf)
		rw.body.Reset()

		if _, err := rw.ResponseWriter.Write(rw.rewriteStreamChunk(toFlush)); err != nil {
			return err
		}
		if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
			flusher.Flush()
		}
	}

	log.Debugf("amp response rewriter: switched to streaming (%s)", reason)
	return nil
}

// Write intercepts response writes and buffers them for model name replacement
func (rw *ResponseRewriter) Write(data []byte) (int, error) {
	// Detect streaming on first write (header-based)
	if !rw.isStreaming && rw.body.Len() == 0 {
		contentType := rw.Header().Get("Content-Type")
		rw.isStreaming = strings.Contains(contentType, "text/event-stream") ||
			strings.Contains(contentType, "stream")
	}

	if !rw.isStreaming {
		// Content-based fallback: detect SSE-like chunks even if Content-Type is missing/wrong.
		if looksLikeSSEChunk(data) {
			if err := rw.enableStreaming("sse heuristic"); err != nil {
				return 0, err
			}
		} else if rw.body.Len()+len(data) > maxBufferedResponseBytes {
			// Safety cap: avoid unbounded buffering on large responses.
			log.Warnf("amp response rewriter: buffer exceeded %d bytes, switching to streaming", maxBufferedResponseBytes)
			if err := rw.enableStreaming("buffer limit"); err != nil {
				return 0, err
			}
		}
	}

	if rw.isStreaming {
		n, err := rw.ResponseWriter.Write(rw.rewriteStreamChunk(data))
		if err == nil {
			if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		return n, err
	}
	return rw.body.Write(data)
}

// Flush writes the buffered response with model names rewritten
func (rw *ResponseRewriter) Flush() {
	if rw.isStreaming {
		if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
			flusher.Flush()
		}
		return
	}
	if rw.body.Len() > 0 {
		if _, err := rw.ResponseWriter.Write(rw.rewriteModelInResponse(rw.body.Bytes())); err != nil {
			log.Warnf("amp response rewriter: failed to write rewritten response: %v", err)
		}
	}
}

// modelFieldPaths lists all JSON paths where model name may appear
var modelFieldPaths = []string{"message.model", "model", "modelVersion", "response.model", "response.modelVersion"}

// rewriteModelInResponse replaces all occurrences of the mapped model with the original model in JSON
// It also suppresses "thinking" blocks if "tool_use" is present to ensure Amp client compatibility
func (rw *ResponseRewriter) rewriteModelInResponse(data []byte) []byte {
	// 1. Amp Compatibility: Suppress thinking blocks if tool use is detected
	// The Amp client struggles when both thinking and tool_use blocks are present
	if gjson.GetBytes(data, `content.#(type=="tool_use")`).Exists() {
		filtered := gjson.GetBytes(data, `content.#(type!="thinking")#`)
		if filtered.Exists() {
			originalCount := gjson.GetBytes(data, "content.#").Int()
			filteredCount := filtered.Get("#").Int()

			if originalCount > filteredCount {
				var err error
				data, err = sjson.SetBytes(data, "content", filtered.Value())
				if err != nil {
					log.Warnf("Amp ResponseRewriter: failed to suppress thinking blocks: %v", err)
				} else {
					log.Debugf("Amp ResponseRewriter: Suppressed %d thinking blocks due to tool usage", originalCount-filteredCount)
					// Log the result for verification
					log.Debugf("Amp ResponseRewriter: Resulting content: %s", gjson.GetBytes(data, "content").String())
				}
			}
		}
	}

	if rw.originalModel == "" {
		return data
	}
	for _, path := range modelFieldPaths {
		if gjson.GetBytes(data, path).Exists() {
			data, _ = sjson.SetBytes(data, path, rw.originalModel)
		}
	}
	return data
}

// rewriteStreamChunk rewrites model names in SSE stream chunks
func (rw *ResponseRewriter) rewriteStreamChunk(chunk []byte) []byte {
	if rw.originalModel == "" {
		return chunk
	}

	// SSE format: "data: {json}\n\n"
	lines := bytes.Split(chunk, []byte("\n"))
	for i, line := range lines {
		if bytes.HasPrefix(line, []byte("data: ")) {
			jsonData := bytes.TrimPrefix(line, []byte("data: "))
			if len(jsonData) > 0 && jsonData[0] == '{' {
				// Rewrite JSON in the data line
				rewritten := rw.rewriteModelInResponse(jsonData)
				lines[i] = append([]byte("data: "), rewritten...)
			}
		}
	}

	return bytes.Join(lines, []byte("\n"))
}
