// Package openai provides HTTP handlers for OpenAIResponses API endpoints.
// This package implements the OpenAIResponses-compatible API interface, including model listing
// and chat completion functionality. It supports both streaming and non-streaming responses,
// and manages a pool of clients to interact with backend services.
// The handlers translate OpenAIResponses API requests to the appropriate backend format and
// convert responses back to OpenAIResponses-compatible format.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	. "github.com/router-for-me/CLIProxyAPI/v6/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func writeResponsesSSEChunk(w io.Writer, chunk []byte) {
	if w == nil || len(chunk) == 0 {
		return
	}
	if _, err := w.Write(chunk); err != nil {
		return
	}
	if bytes.HasSuffix(chunk, []byte("\n\n")) || bytes.HasSuffix(chunk, []byte("\r\n\r\n")) {
		return
	}
	suffix := []byte("\n\n")
	if bytes.HasSuffix(chunk, []byte("\r\n")) {
		suffix = []byte("\r\n")
	} else if bytes.HasSuffix(chunk, []byte("\n")) {
		suffix = []byte("\n")
	}
	if _, err := w.Write(suffix); err != nil {
		return
	}
}

type responsesSSEFramer struct {
	pending []byte
}

func (f *responsesSSEFramer) WriteChunk(w io.Writer, chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	if responsesSSENeedsLineBreak(f.pending, chunk) {
		f.pending = append(f.pending, '\n')
	}
	f.pending = append(f.pending, chunk...)
	for {
		frameLen := responsesSSEFrameLen(f.pending)
		if frameLen == 0 {
			break
		}
		writeResponsesSSEChunk(w, f.pending[:frameLen])
		copy(f.pending, f.pending[frameLen:])
		f.pending = f.pending[:len(f.pending)-frameLen]
	}
	if len(bytes.TrimSpace(f.pending)) == 0 {
		f.pending = f.pending[:0]
		return
	}
	if len(f.pending) == 0 || !responsesSSECanEmitWithoutDelimiter(f.pending) {
		return
	}
	writeResponsesSSEChunk(w, f.pending)
	f.pending = f.pending[:0]
}

func (f *responsesSSEFramer) Flush(w io.Writer) {
	if len(f.pending) == 0 {
		return
	}
	if len(bytes.TrimSpace(f.pending)) == 0 {
		f.pending = f.pending[:0]
		return
	}
	if !responsesSSECanEmitWithoutDelimiter(f.pending) {
		f.pending = f.pending[:0]
		return
	}
	writeResponsesSSEChunk(w, f.pending)
	f.pending = f.pending[:0]
}

func responsesSSEFrameLen(chunk []byte) int {
	if len(chunk) == 0 {
		return 0
	}
	lf := bytes.Index(chunk, []byte("\n\n"))
	crlf := bytes.Index(chunk, []byte("\r\n\r\n"))
	switch {
	case lf < 0:
		if crlf < 0 {
			return 0
		}
		return crlf + 4
	case crlf < 0:
		return lf + 2
	case lf < crlf:
		return lf + 2
	default:
		return crlf + 4
	}
}

func responsesSSENeedsMoreData(chunk []byte) bool {
	trimmed := bytes.TrimSpace(chunk)
	if len(trimmed) == 0 {
		return false
	}
	return responsesSSEHasField(trimmed, []byte("event:")) && !responsesSSEHasField(trimmed, []byte("data:"))
}

func responsesSSEHasField(chunk []byte, prefix []byte) bool {
	s := chunk
	for len(s) > 0 {
		line := s
		if i := bytes.IndexByte(s, '\n'); i >= 0 {
			line = s[:i]
			s = s[i+1:]
		} else {
			s = nil
		}
		line = bytes.TrimSpace(line)
		if bytes.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func responsesSSECanEmitWithoutDelimiter(chunk []byte) bool {
	trimmed := bytes.TrimSpace(chunk)
	if len(trimmed) == 0 || responsesSSENeedsMoreData(trimmed) || !responsesSSEHasField(trimmed, []byte("data:")) {
		return false
	}
	return responsesSSEDataLinesValid(trimmed)
}

func responsesSSEDataLinesValid(chunk []byte) bool {
	s := chunk
	for len(s) > 0 {
		line := s
		if i := bytes.IndexByte(s, '\n'); i >= 0 {
			line = s[:i]
			s = s[i+1:]
		} else {
			s = nil
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 || !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[len("data:"):])
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		if !json.Valid(data) {
			return false
		}
	}
	return true
}

func responsesSSENeedsLineBreak(pending, chunk []byte) bool {
	if len(pending) == 0 || len(chunk) == 0 {
		return false
	}
	if bytes.HasSuffix(pending, []byte("\n")) || bytes.HasSuffix(pending, []byte("\r")) {
		return false
	}
	if chunk[0] == '\n' || chunk[0] == '\r' {
		return false
	}
	trimmed := bytes.TrimLeft(chunk, " \t")
	if len(trimmed) == 0 {
		return false
	}
	for _, prefix := range [][]byte{[]byte("data:"), []byte("event:"), []byte("id:"), []byte("retry:"), []byte(":")} {
		if bytes.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

// OpenAIResponsesAPIHandler contains the handlers for OpenAIResponses API endpoints.
// It holds a pool of clients to interact with the backend service.
type OpenAIResponsesAPIHandler struct {
	*handlers.BaseAPIHandler
}

// NewOpenAIResponsesAPIHandler creates a new OpenAIResponses API handlers instance.
// It takes an BaseAPIHandler instance as input and returns an OpenAIResponsesAPIHandler.
//
// Parameters:
//   - apiHandlers: The base API handlers instance
//
// Returns:
//   - *OpenAIResponsesAPIHandler: A new OpenAIResponses API handlers instance
func NewOpenAIResponsesAPIHandler(apiHandlers *handlers.BaseAPIHandler) *OpenAIResponsesAPIHandler {
	return &OpenAIResponsesAPIHandler{
		BaseAPIHandler: apiHandlers,
	}
}

// HandlerType returns the identifier for this handler implementation.
func (h *OpenAIResponsesAPIHandler) HandlerType() string {
	return OpenaiResponse
}

// Models returns the OpenAIResponses-compatible model metadata supported by this handler.
func (h *OpenAIResponsesAPIHandler) Models() []map[string]any {
	// Get dynamic models from the global registry
	modelRegistry := registry.GetGlobalRegistry()
	return modelRegistry.GetAvailableModels("openai")
}

// OpenAIResponsesModels handles the /v1/models endpoint.
// It returns a list of available AI models with their capabilities
// and specifications in OpenAIResponses-compatible format.
func (h *OpenAIResponsesAPIHandler) OpenAIResponsesModels(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data":   h.Models(),
	})
}

// Responses handles the /v1/responses endpoint.
// It determines whether the request is for a streaming or non-streaming response
// and calls the appropriate handler based on the model provider.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
func (h *OpenAIResponsesAPIHandler) Responses(c *gin.Context) {
	rawJSON, err := c.GetRawData()
	// If data retrieval fails, return a 400 Bad Request error.
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	// Check if the client requested a streaming response.
	streamResult := gjson.GetBytes(rawJSON, "stream")
	if streamResult.Type == gjson.True {
		h.handleStreamingResponse(c, rawJSON)
	} else {
		h.handleNonStreamingResponse(c, rawJSON)
	}

}

func (h *OpenAIResponsesAPIHandler) Compact(c *gin.Context) {
	rawJSON, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	streamResult := gjson.GetBytes(rawJSON, "stream")
	if streamResult.Type == gjson.True {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Streaming not supported for compact responses",
				Type:    "invalid_request_error",
			},
		})
		return
	}
	if streamResult.Exists() {
		if updated, err := sjson.DeleteBytes(rawJSON, "stream"); err == nil {
			rawJSON = updated
		}
	}

	c.Header("Content-Type", "application/json")
	modelName := gjson.GetBytes(rawJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)
	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, "responses/compact")
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	_, _ = c.Writer.Write(resp)
	cliCancel()
}

// handleNonStreamingResponse handles non-streaming chat completion responses
// for Gemini models. It selects a client from the pool, sends the request, and
// aggregates the response before sending it back to the client in OpenAIResponses format.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
//   - rawJSON: The raw JSON bytes of the OpenAIResponses-compatible request
func (h *OpenAIResponsesAPIHandler) handleNonStreamingResponse(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "application/json")

	resp, upstreamHeaders, errMsg, selectedAuthID, pinnedAuthID := h.executeNonStreamingWithAffinity(c, rawJSON, false)
	if errMsg != nil && hasEncryptedContentContext(rawJSON) {
		forceUnpinnedRetry := pinnedAuthID != ""
		invalidEncryptedRetry := isInvalidEncryptedContentError(errMsg)
		if forceUnpinnedRetry || invalidEncryptedRetry {
			// If a pinned auth fails for continuation input, retry once with encrypted reasoning removed
			// and without pinning so round-robin can select another healthy credential/provider.
			// Also recover explicit invalid_encrypted_content responses in unpinned mode.
			if sanitized, changed := stripEncryptedReasoningInput(rawJSON); changed {
				resp, upstreamHeaders, errMsg, selectedAuthID, _ = h.executeNonStreamingWithAffinity(c, sanitized, forceUnpinnedRetry)
			}
		}
	}
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		return
	}
	rememberResponsesAuthAffinity(selectedAuthID, resp)
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	_, _ = c.Writer.Write(resp)
}

// handleStreamingResponse handles streaming responses for Gemini models.
// It establishes a streaming connection with the backend service and forwards
// the response chunks to the client in real-time using Server-Sent Events.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
//   - rawJSON: The raw JSON bytes of the OpenAIResponses-compatible request
func (h *OpenAIResponsesAPIHandler) handleStreamingResponse(c *gin.Context, rawJSON []byte) {
	// Get the http.Flusher interface to manually flush the response.
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Streaming not supported",
				Type:    "server_error",
			},
		})
		return
	}

	startStream := func(payload []byte, forceUnpinned bool) (handlers.APIHandlerCancelFunc, <-chan []byte, http.Header, <-chan *interfaces.ErrorMessage, *string, string) {
		modelName := gjson.GetBytes(payload, "model").String()
		cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
		selectedAuthID := ""
		pinnedAuthID := ""
		if !forceUnpinned {
			pinnedAuthID = resolvePinnedAuthIDForResponses(payload)
		}
		if pinnedAuthID != "" {
			cliCtx = handlers.WithPinnedAuthID(cliCtx, pinnedAuthID)
			selectedAuthID = pinnedAuthID
		} else {
			cliCtx = handlers.WithSelectedAuthIDCallback(cliCtx, func(authID string) {
				selectedAuthID = strings.TrimSpace(authID)
			})
		}
		dataChan, upstreamHeaders, errChan := h.ExecuteStreamWithAuthManager(cliCtx, h.HandlerType(), modelName, payload, "")
		return cliCancel, dataChan, upstreamHeaders, errChan, &selectedAuthID, pinnedAuthID
	}

	cliCancel, dataChan, upstreamHeaders, errChan, selectedAuthIDPtr, pinnedAuthID := startStream(rawJSON, false)
	recoveryAttempted := false
	pendingChunks := make([][]byte, 0, 4)
	streamFlushed := false
	assistantContentStarted := false

	setSSEHeaders := func() {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("Access-Control-Allow-Origin", "*")
	}
	framer := &responsesSSEFramer{}

	flushPendingChunks := func() {
		if streamFlushed {
			return
		}
		setSSEHeaders()
		handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
		for _, chunk := range pendingChunks {
			framer.WriteChunk(c.Writer, chunk)
		}
		flusher.Flush()
		pendingChunks = pendingChunks[:0]
		streamFlushed = true
	}

	bufferOrWriteChunk := func(chunk []byte) {
		rememberResponsesAuthAffinityFromSSE(strings.TrimSpace(*selectedAuthIDPtr), chunk)
		if responsesStreamChunkHasVisibleAssistantContent(chunk) {
			assistantContentStarted = true
		}

		if streamFlushed {
			framer.WriteChunk(c.Writer, chunk)
			flusher.Flush()
			return
		}

		pendingChunks = append(pendingChunks, append([]byte(nil), chunk...))
		if assistantContentStarted || responsesStreamChunkCompletesResponse(chunk) {
			flushPendingChunks()
		}
	}

	resetBufferedStreamState := func() {
		pendingChunks = pendingChunks[:0]
		streamFlushed = false
		assistantContentStarted = false
	}

	tryRecover := func(errMsg *interfaces.ErrorMessage) bool {
		if recoveryAttempted || streamFlushed || !hasEncryptedContentContext(rawJSON) {
			return false
		}

		shouldRecover := false
		forceUnpinned := false
		if pinnedAuthID != "" {
			shouldRecover = true
			forceUnpinned = true
		} else if isInvalidEncryptedContentError(errMsg) {
			shouldRecover = true
			forceUnpinned = true
		}
		if !shouldRecover {
			return false
		}

		sanitized, changed := stripEncryptedReasoningInput(rawJSON)
		if !changed {
			return false
		}

		recoveryAttempted = true
		rawJSON = sanitized
		resetBufferedStreamState()
		if errMsg != nil {
			cliCancel(errMsg.Error)
		} else {
			cliCancel(nil)
		}
		cliCancel, dataChan, upstreamHeaders, errChan, selectedAuthIDPtr, pinnedAuthID = startStream(rawJSON, forceUnpinned)
		return true
	}

	finishStream := func() {
		if !streamFlushed {
			setSSEHeaders()
			handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
			if len(pendingChunks) > 0 {
				flushPendingChunks()
			}
		}
		framer.Flush(c.Writer)
		_, _ = c.Writer.Write([]byte("\n"))
		flusher.Flush()
		cliCancel(nil)
	}

	for {
		select {
		case <-c.Request.Context().Done():
			cliCancel(c.Request.Context().Err())
			return
		case errMsg, ok := <-errChan:
			if !ok {
				errChan = nil
				continue
			}
			if tryRecover(errMsg) {
				continue
			}

			if !streamFlushed {
				h.WriteErrorResponse(c, errMsg)
				if errMsg != nil {
					cliCancel(errMsg.Error)
				} else {
					cliCancel(nil)
				}
				return
			}

			recoverableHint := !assistantContentStarted && hasEncryptedContentContext(rawJSON) && isInvalidEncryptedContentError(errMsg)
			framer.Flush(c.Writer)
			h.writeResponsesStreamTerminalError(c, errMsg, recoverableHint)
			flusher.Flush()
			if errMsg != nil {
				cliCancel(errMsg.Error)
			} else {
				cliCancel(nil)
			}
			return
		case chunk, ok := <-dataChan:
			if !ok {
				if errChan != nil {
					select {
					case terminal, okErr := <-errChan:
						if okErr && terminal != nil {
							if tryRecover(terminal) {
								continue
							}
							if !streamFlushed {
								h.WriteErrorResponse(c, terminal)
								cliCancel(terminal.Error)
								return
							}
							recoverableHint := !assistantContentStarted && hasEncryptedContentContext(rawJSON) && isInvalidEncryptedContentError(terminal)
							framer.Flush(c.Writer)
							h.writeResponsesStreamTerminalError(c, terminal, recoverableHint)
							flusher.Flush()
							cliCancel(terminal.Error)
							return
						}
						if !okErr {
							errChan = nil
						}
					default:
					}
				}
				finishStream()
				return
			}
			bufferOrWriteChunk(chunk)
		}
	}
}
func responsesStreamChunkHasVisibleAssistantContent(chunk []byte) bool {
	for _, line := range bytes.Split(chunk, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[5:])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) || !gjson.ValidBytes(payload) {
			continue
		}
		eventType := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
		switch {
		case strings.HasPrefix(eventType, "response.output_text."):
			if strings.TrimSpace(gjson.GetBytes(payload, "delta").String()) != "" || strings.TrimSpace(gjson.GetBytes(payload, "text").String()) != "" {
				return true
			}
		case strings.HasPrefix(eventType, "response.refusal."):
			return true
		case strings.HasPrefix(eventType, "response.audio."):
			return true
		}
	}
	return false
}

func responsesStreamChunkCompletesResponse(chunk []byte) bool {
	for _, line := range bytes.Split(chunk, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[5:])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) || !gjson.ValidBytes(payload) {
			continue
		}
		if strings.TrimSpace(gjson.GetBytes(payload, "type").String()) == "response.completed" {
			return true
		}
	}
	return false
}

func (h *OpenAIResponsesAPIHandler) writeResponsesStreamTerminalError(c *gin.Context, errMsg *interfaces.ErrorMessage, recoverableHint bool) {
	if errMsg == nil {
		return
	}
	status := http.StatusInternalServerError
	if errMsg.StatusCode > 0 {
		status = errMsg.StatusCode
	}
	errText := http.StatusText(status)
	if errMsg.Error != nil && errMsg.Error.Error() != "" {
		errText = errMsg.Error.Error()
	}
	if recoverableHint {
		errText = "invalid_encrypted_content after stream started; retry without previous_response_id or encrypted reasoning"
	}
	chunk := handlers.BuildOpenAIResponsesStreamErrorChunk(status, errText, 0)
	_, _ = fmt.Fprintf(c.Writer, "\nevent: error\ndata: %s\n\n", string(chunk))
}
func (h *OpenAIResponsesAPIHandler) forwardResponsesStream(c *gin.Context, flusher http.Flusher, cancel func(error), data <-chan []byte, errs <-chan *interfaces.ErrorMessage, framer *responsesSSEFramer) {
	if framer == nil {
		framer = &responsesSSEFramer{}
	}
	h.ForwardStream(c, flusher, cancel, data, errs, handlers.StreamForwardOptions{
		WriteChunk: func(chunk []byte) {
			framer.WriteChunk(c.Writer, chunk)
		},
		WriteTerminalError: func(errMsg *interfaces.ErrorMessage) {
			framer.Flush(c.Writer)
			h.writeResponsesStreamTerminalError(c, errMsg, false)
		},
		WriteDone: func() {
			framer.Flush(c.Writer)
			_, _ = c.Writer.Write([]byte("\n"))
		},
	})
}

func (h *OpenAIResponsesAPIHandler) executeNonStreamingWithAffinity(c *gin.Context, rawJSON []byte, forceUnpinned bool) ([]byte, http.Header, *interfaces.ErrorMessage, string, string) {
	modelName := gjson.GetBytes(rawJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	selectedAuthID := ""
	pinnedAuthID := ""
	if !forceUnpinned {
		pinnedAuthID = resolvePinnedAuthIDForResponses(rawJSON)
	}
	if pinnedAuthID != "" {
		cliCtx = handlers.WithPinnedAuthID(cliCtx, pinnedAuthID)
		selectedAuthID = pinnedAuthID
	} else {
		cliCtx = handlers.WithSelectedAuthIDCallback(cliCtx, func(authID string) {
			selectedAuthID = strings.TrimSpace(authID)
		})
	}
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)

	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, "")
	stopKeepAlive()
	if errMsg != nil {
		cliCancel(errMsg.Error)
		return nil, nil, errMsg, selectedAuthID, pinnedAuthID
	}
	cliCancel()
	return resp, upstreamHeaders, nil, selectedAuthID, pinnedAuthID
}
