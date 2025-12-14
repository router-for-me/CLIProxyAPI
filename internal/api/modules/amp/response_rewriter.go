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

// Write intercepts response writes and buffers them for model name replacement
func (rw *ResponseRewriter) Write(data []byte) (int, error) {
	// Detect streaming on first write
	if rw.body.Len() == 0 && !rw.isStreaming {
		contentType := rw.Header().Get("Content-Type")
		rw.isStreaming = strings.Contains(contentType, "text/event-stream") ||
			strings.Contains(contentType, "stream")
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
var modelFieldPaths = []string{"model", "modelVersion", "response.modelVersion", "message.model"}

// rewriteModelInResponse replaces all occurrences of the mapped model with the original model in JSON
func (rw *ResponseRewriter) rewriteModelInResponse(data []byte) []byte {
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
