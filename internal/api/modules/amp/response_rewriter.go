package amp

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ResponseRewriter wraps a gin.ResponseWriter to intercept and modify the response body
// It is used to rewrite model names in responses when model mapping is used
// and to keep Amp-compatible response shapes.
type ResponseRewriter struct {
	gin.ResponseWriter
	body                   *bytes.Buffer
	originalModel          string
	isStreaming            bool
	suppressedContentBlock map[int]struct{}
}

// NewResponseRewriter creates a new response rewriter for model name substitution.
func NewResponseRewriter(w gin.ResponseWriter, originalModel string) *ResponseRewriter {
	return &ResponseRewriter{
		ResponseWriter:         w,
		body:                   &bytes.Buffer{},
		originalModel:          originalModel,
		suppressedContentBlock: make(map[int]struct{}),
	}
}

const maxBufferedResponseBytes = 2 * 1024 * 1024 // 2MB safety cap

func looksLikeSSEChunk(data []byte) bool {
	for _, line := range bytes.Split(data, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if bytes.HasPrefix(trimmed, []byte("data:")) ||
			bytes.HasPrefix(trimmed, []byte("event:")) {
			return true
		}
	}
	return false
}

func (rw *ResponseRewriter) enableStreaming(reason string) error {
	if rw.isStreaming {
		return nil
	}
	rw.isStreaming = true

	if rw.body != nil && rw.body.Len() > 0 {
		buf := rw.body.Bytes()
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

func (rw *ResponseRewriter) Write(data []byte) (int, error) {
	if !rw.isStreaming && rw.body.Len() == 0 {
		contentType := rw.Header().Get("Content-Type")
		rw.isStreaming = strings.Contains(contentType, "text/event-stream") ||
			strings.Contains(contentType, "stream")
	}

	if !rw.isStreaming {
		if looksLikeSSEChunk(data) {
			if err := rw.enableStreaming("sse heuristic"); err != nil {
				return 0, err
			}
		} else if rw.body.Len()+len(data) > maxBufferedResponseBytes {
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

func (rw *ResponseRewriter) Flush() {
	if rw.isStreaming {
		if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
			flusher.Flush()
		}
		return
	}
	if rw.body.Len() > 0 {
		rewritten := rw.rewriteModelInResponse(rw.body.Bytes())
		// Update Content-Length to match the rewritten body size, since
		// signature injection and model name changes alter the payload length.
		rw.ResponseWriter.Header().Set("Content-Length", fmt.Sprintf("%d", len(rewritten)))
		if _, err := rw.ResponseWriter.Write(rewritten); err != nil {
			log.Warnf("amp response rewriter: failed to write rewritten response: %v", err)
		}
	}
}

var modelFieldPaths = []string{"message.model", "model", "modelVersion", "response.model", "response.modelVersion"}

// ensureAmpSignature injects empty signature fields into tool_use/thinking blocks
// in API responses so that the Amp TUI does not crash on P.signature.length.
func ensureAmpSignature(data []byte) []byte {
	for index, block := range gjson.GetBytes(data, "content").Array() {
		blockType := block.Get("type").String()
		if blockType != "tool_use" && blockType != "thinking" {
			continue
		}
		signaturePath := fmt.Sprintf("content.%d.signature", index)
		if gjson.GetBytes(data, signaturePath).Exists() {
			continue
		}
		var err error
		data, err = sjson.SetBytes(data, signaturePath, "")
		if err != nil {
			log.Warnf("Amp ResponseRewriter: failed to add empty signature to %s block: %v", blockType, err)
			break
		}
	}

	contentBlockType := gjson.GetBytes(data, "content_block.type").String()
	if (contentBlockType == "tool_use" || contentBlockType == "thinking") && !gjson.GetBytes(data, "content_block.signature").Exists() {
		var err error
		data, err = sjson.SetBytes(data, "content_block.signature", "")
		if err != nil {
			log.Warnf("Amp ResponseRewriter: failed to add empty signature to streaming %s block: %v", contentBlockType, err)
		}
	}

	return data
}

func (rw *ResponseRewriter) markSuppressedContentBlock(index int) {
	if rw.suppressedContentBlock == nil {
		rw.suppressedContentBlock = make(map[int]struct{})
	}
	rw.suppressedContentBlock[index] = struct{}{}
}

func (rw *ResponseRewriter) isSuppressedContentBlock(index int) bool {
	_, ok := rw.suppressedContentBlock[index]
	return ok
}

func (rw *ResponseRewriter) suppressAmpThinking(data []byte) []byte {
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
				}
			}
		}
	}

	eventType := gjson.GetBytes(data, "type").String()
	indexResult := gjson.GetBytes(data, "index")
	if eventType == "content_block_start" && gjson.GetBytes(data, "content_block.type").String() == "thinking" && indexResult.Exists() {
		rw.markSuppressedContentBlock(int(indexResult.Int()))
		return nil
	}
	if gjson.GetBytes(data, "delta.type").String() == "thinking_delta" {
		if indexResult.Exists() {
			rw.markSuppressedContentBlock(int(indexResult.Int()))
		}
		return nil
	}
	if eventType == "content_block_stop" && indexResult.Exists() {
		index := int(indexResult.Int())
		if rw.isSuppressedContentBlock(index) {
			delete(rw.suppressedContentBlock, index)
			return nil
		}
	}

	return data
}

func (rw *ResponseRewriter) rewriteModelInResponse(data []byte) []byte {
	data = ensureAmpSignature(data)
	data = rw.suppressAmpThinking(data)
	if len(data) == 0 {
		return data
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

func (rw *ResponseRewriter) rewriteStreamChunk(chunk []byte) []byte {
	lines := bytes.Split(chunk, []byte("\n"))
	var out [][]byte

	i := 0
	for i < len(lines) {
		line := lines[i]
		trimmed := bytes.TrimSpace(line)

		// Case 1: "event:" line - look ahead for its "data:" line
		if bytes.HasPrefix(trimmed, []byte("event: ")) {
			// Scan forward past blank lines to find the data: line
			dataIdx := -1
			for j := i + 1; j < len(lines); j++ {
				t := bytes.TrimSpace(lines[j])
				if len(t) == 0 {
					continue
				}
				if bytes.HasPrefix(t, []byte("data: ")) {
					dataIdx = j
				}
				break
			}

			if dataIdx >= 0 {
				// Found event+data pair - process through rewriter
				jsonData := bytes.TrimPrefix(bytes.TrimSpace(lines[dataIdx]), []byte("data: "))
				if len(jsonData) > 0 && jsonData[0] == '{' {
					rewritten := rw.rewriteStreamEvent(jsonData)
					if rewritten == nil {
						// Event suppressed (e.g. thinking block), skip event+data pair
						i = dataIdx + 1
						continue
					}
					// Emit event line
					out = append(out, line)
					// Emit blank lines between event and data
					for k := i + 1; k < dataIdx; k++ {
						out = append(out, lines[k])
					}
					// Emit rewritten data
					out = append(out, append([]byte("data: "), rewritten...))
					i = dataIdx + 1
					continue
				}
			}

			// No data line found (orphan event from cross-chunk split)
			// Pass it through as-is - the data will arrive in the next chunk
			out = append(out, line)
			i++
			continue
		}

		// Case 2: standalone "data:" line (no preceding event: in this chunk)
		if bytes.HasPrefix(trimmed, []byte("data: ")) {
			jsonData := bytes.TrimPrefix(trimmed, []byte("data: "))
			if len(jsonData) > 0 && jsonData[0] == '{' {
				rewritten := rw.rewriteStreamEvent(jsonData)
				if rewritten != nil {
					out = append(out, append([]byte("data: "), rewritten...))
				}
				i++
				continue
			}
		}

		// Case 3: everything else
		out = append(out, line)
		i++
	}

	return bytes.Join(out, []byte("\n"))
}

// rewriteStreamEvent processes a single JSON event in the SSE stream.
// It rewrites model names and ensures signature fields exist.
func (rw *ResponseRewriter) rewriteStreamEvent(data []byte) []byte {
	// Suppress thinking blocks before any other processing.
	data = rw.suppressAmpThinking(data)
	if len(data) == 0 {
		return nil
	}

	// Inject empty signature where needed
	data = ensureAmpSignature(data)

	// Rewrite model name
	if rw.originalModel != "" {
		for _, path := range modelFieldPaths {
			if gjson.GetBytes(data, path).Exists() {
				data, _ = sjson.SetBytes(data, path, rw.originalModel)
			}
		}
	}

	return data
}

// SanitizeAmpRequestBody removes thinking blocks with empty/missing/invalid signatures
// from the messages array in a request body before forwarding to the upstream API.
// This prevents 400 errors from the API which requires valid signatures on thinking blocks.
func SanitizeAmpRequestBody(body []byte) []byte {
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body
	}

	modified := false
	for msgIdx, msg := range messages.Array() {
		if msg.Get("role").String() != "assistant" {
			continue
		}
		content := msg.Get("content")
		if !content.Exists() || !content.IsArray() {
			continue
		}

		var keepBlocks []interface{}
		removedCount := 0

		for _, block := range content.Array() {
			blockType := block.Get("type").String()
			if blockType == "thinking" {
				sig := block.Get("signature")
				if !sig.Exists() || sig.Type != gjson.String || strings.TrimSpace(sig.String()) == "" {
					removedCount++
					continue
				}
			}
			keepBlocks = append(keepBlocks, block.Value())
		}

		if removedCount > 0 {
			contentPath := fmt.Sprintf("messages.%d.content", msgIdx)
			var err error
			if len(keepBlocks) == 0 {
				body, err = sjson.SetBytes(body, contentPath, []interface{}{})
			} else {
				body, err = sjson.SetBytes(body, contentPath, keepBlocks)
			}
			if err != nil {
				log.Warnf("Amp RequestSanitizer: failed to remove thinking blocks from message %d: %v", msgIdx, err)
				continue
			}
			modified = true
			log.Debugf("Amp RequestSanitizer: removed %d thinking blocks with invalid signatures from message %d", removedCount, msgIdx)
		}
	}

	if modified {
		log.Debugf("Amp RequestSanitizer: sanitized request body")
	}
	return body
}
