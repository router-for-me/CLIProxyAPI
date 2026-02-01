package routing

import (
	"bytes"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	log "github.com/sirupsen/logrus"
)

// ModelRewriter handles model name rewriting in requests and responses.
type ModelRewriter interface {
	// RewriteRequestBody rewrites the model field in a JSON request body.
	// Returns the modified body or the original if no rewrite was needed.
	RewriteRequestBody(body []byte, newModel string) ([]byte, error)

	// WrapResponseWriter wraps an http.ResponseWriter to rewrite model names in the response.
	// Returns the wrapped writer and a cleanup function that must be called after the response is complete.
	WrapResponseWriter(w http.ResponseWriter, requestedModel, resolvedModel string) (http.ResponseWriter, func())
}

// DefaultModelRewriter is the standard implementation of ModelRewriter.
type DefaultModelRewriter struct{}

// NewModelRewriter creates a new DefaultModelRewriter.
func NewModelRewriter() *DefaultModelRewriter {
	return &DefaultModelRewriter{}
}

// RewriteRequestBody replaces the model name in a JSON request body.
func (r *DefaultModelRewriter) RewriteRequestBody(body []byte, newModel string) ([]byte, error) {
	if !gjson.GetBytes(body, "model").Exists() {
		return body, nil
	}
	result, err := sjson.SetBytes(body, "model", newModel)
	if err != nil {
		return body, err
	}
	return result, nil
}

// WrapResponseWriter wraps a response writer to rewrite model names.
// The cleanup function must be called after the handler completes to flush any buffered data.
func (r *DefaultModelRewriter) WrapResponseWriter(w http.ResponseWriter, requestedModel, resolvedModel string) (http.ResponseWriter, func()) {
	rw := &responseRewriter{
		ResponseWriter:  w,
		body:            &bytes.Buffer{},
		requestedModel:  requestedModel,
		resolvedModel:   resolvedModel,
	}
	return rw, func() { rw.flush() }
}

// responseRewriter wraps http.ResponseWriter to intercept and modify the response body.
type responseRewriter struct {
	http.ResponseWriter
	body           *bytes.Buffer
	requestedModel string
	resolvedModel  string
	isStreaming    bool
	wroteHeader    bool
	flushed        bool
}

// Write intercepts response writes and buffers them for model name replacement.
func (rw *responseRewriter) Write(data []byte) (int, error) {
	// Ensure header is written
	if !rw.wroteHeader {
		rw.WriteHeader(http.StatusOK)
	}

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

// WriteHeader captures the status code and delegates to the underlying writer.
func (rw *responseRewriter) WriteHeader(code int) {
	if !rw.wroteHeader {
		rw.wroteHeader = true
		rw.ResponseWriter.WriteHeader(code)
	}
}

// flush writes the buffered response with model names rewritten.
func (rw *responseRewriter) flush() {
	if rw.flushed {
		return
	}
	rw.flushed = true

	if rw.isStreaming {
		if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
			flusher.Flush()
		}
		return
	}
	if rw.body.Len() > 0 {
		data := rw.rewriteModelInResponse(rw.body.Bytes())
		if _, err := rw.ResponseWriter.Write(data); err != nil {
			log.Warnf("response rewriter: failed to write rewritten response: %v", err)
		}
	}
}

// modelFieldPaths lists all JSON paths where model name may appear.
var modelFieldPaths = []string{"model", "modelVersion", "response.modelVersion", "message.model"}

// rewriteModelInResponse replaces all occurrences of the resolved model with the requested model.
func (rw *responseRewriter) rewriteModelInResponse(data []byte) []byte {
	if rw.requestedModel == "" || rw.resolvedModel == "" || rw.requestedModel == rw.resolvedModel {
		return data
	}

	for _, path := range modelFieldPaths {
		if gjson.GetBytes(data, path).Exists() {
			data, _ = sjson.SetBytes(data, path, rw.requestedModel)
		}
	}
	return data
}

// rewriteStreamChunk rewrites model names in SSE stream chunks.
func (rw *responseRewriter) rewriteStreamChunk(chunk []byte) []byte {
	if rw.requestedModel == "" || rw.resolvedModel == "" || rw.requestedModel == rw.resolvedModel {
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
