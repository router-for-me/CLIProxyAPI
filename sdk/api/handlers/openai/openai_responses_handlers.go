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
	"sort"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/client/codex/optimize-multi-agent-v2"
	. "github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
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
	pending               []byte
	outputItems           map[int][]byte
	outputOrder           []int
	unindexedOutputItems  [][]byte
	responseID            string
	responseObject        []byte
	requestJSON           []byte
	lastSequenceNumber    int
	hasLastSequenceNumber bool
	terminalEventSeen     bool
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
		f.writeFrame(w, f.pending[:frameLen])
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
	f.writeFrame(w, f.pending)
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
	f.writeFrame(w, f.pending)
	f.pending = f.pending[:0]
}

func (f *responsesSSEFramer) writeFrame(w io.Writer, frame []byte) {
	writeResponsesSSEChunk(w, f.repairFrame(frame))
}

func (f *responsesSSEFramer) repairFrame(frame []byte) []byte {
	payload, ok := responsesSSEDataPayload(frame)
	if !ok || len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) || !json.Valid(payload) {
		return frame
	}

	eventType := gjson.GetBytes(payload, "type").String()
	f.recordResponseMetadata(payload, eventType)

	switch eventType {
	case "response.output_item.done":
		f.recordOutputItem(payload)
	case "response.completed":
		f.terminalEventSeen = true
		repaired := f.repairCompletedPayload(payload)
		if !bytes.Equal(repaired, payload) {
			return responsesSSEFrameWithData(frame, repaired)
		}
	case "response.incomplete", "response.failed":
		f.terminalEventSeen = true
	}
	return frame
}

func (f *responsesSSEFramer) recordResponseMetadata(payload []byte, eventType string) {
	response := gjson.GetBytes(payload, "response")
	if response.IsObject() && (len(f.responseObject) == 0 || eventType == "response.created") {
		f.responseObject = append(f.responseObject[:0], response.Raw...)
	}
	if responseID := response.Get("id").String(); responseID != "" {
		f.responseID = responseID
	}
	sequenceNumber := gjson.GetBytes(payload, "sequence_number")
	if !sequenceNumber.Exists() {
		return
	}
	value := int(sequenceNumber.Int())
	if !f.hasLastSequenceNumber || value > f.lastSequenceNumber {
		f.lastSequenceNumber = value
		f.hasLastSequenceNumber = true
	}
}

func (f *responsesSSEFramer) terminalMetadata() (string, int, []byte) {
	responseObject := f.failedResponseObject()
	if !f.hasLastSequenceNumber {
		return f.responseID, 0, responseObject
	}
	return f.responseID, f.lastSequenceNumber + 1, responseObject
}

func responsesSSEDataPayload(frame []byte) ([]byte, bool) {
	var payload []byte
	found := false
	for _, line := range bytes.Split(frame, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		trimmed := bytes.TrimSpace(line)
		if !bytes.HasPrefix(trimmed, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(trimmed[len("data:"):])
		if found {
			payload = append(payload, '\n')
		}
		payload = append(payload, data...)
		found = true
	}
	return payload, found
}

func responsesSSEFrameWithData(frame, payload []byte) []byte {
	var out bytes.Buffer
	for _, line := range bytes.Split(frame, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || bytes.HasPrefix(trimmed, []byte("data:")) {
			continue
		}
		out.Write(line)
		out.WriteByte('\n')
	}
	for _, line := range bytes.Split(payload, []byte("\n")) {
		out.WriteString("data: ")
		out.Write(line)
		out.WriteByte('\n')
	}
	out.WriteByte('\n')
	return out.Bytes()
}

func (f *responsesSSEFramer) recordOutputItem(payload []byte) {
	item := gjson.GetBytes(payload, "item")
	if !item.Exists() || !item.IsObject() || item.Get("type").String() == "" {
		return
	}

	if outputIndex := gjson.GetBytes(payload, "output_index"); outputIndex.Exists() {
		index := int(outputIndex.Int())
		if f.outputItems == nil {
			f.outputItems = make(map[int][]byte)
		}
		if _, exists := f.outputItems[index]; !exists {
			f.outputOrder = append(f.outputOrder, index)
		}
		f.outputItems[index] = append([]byte(nil), item.Raw...)
		return
	}

	f.unindexedOutputItems = append(f.unindexedOutputItems, append([]byte(nil), item.Raw...))
}

func (f *responsesSSEFramer) recordedOutputJSON() []byte {
	if len(f.outputOrder) == 0 && len(f.unindexedOutputItems) == 0 {
		return nil
	}

	var outputJSON bytes.Buffer
	outputJSON.WriteByte('[')
	indexes := append([]int(nil), f.outputOrder...)
	sort.Ints(indexes)
	written := 0
	for _, index := range indexes {
		item, ok := f.outputItems[index]
		if !ok {
			continue
		}
		if written > 0 {
			outputJSON.WriteByte(',')
		}
		outputJSON.Write(item)
		written++
	}
	for _, item := range f.unindexedOutputItems {
		if written > 0 {
			outputJSON.WriteByte(',')
		}
		outputJSON.Write(item)
		written++
	}
	outputJSON.WriteByte(']')
	return outputJSON.Bytes()
}

type responsesJSONFieldKind uint8

const (
	responsesJSONFieldRaw responsesJSONFieldKind = iota
	responsesJSONFieldBoolean
	responsesJSONFieldConversation
)

type responsesJSONFieldPolicy struct {
	field    string
	fallback string
	kind     responsesJSONFieldKind
}

func applyResponsesJSONFieldPolicy(response, request []byte, policy responsesJSONFieldPolicy) []byte {
	responseValue := gjson.GetBytes(response, policy.field)
	switch policy.kind {
	case responsesJSONFieldBoolean:
		if responseValue.Type == gjson.True || responseValue.Type == gjson.False {
			return response
		}
	case responsesJSONFieldConversation:
		conversationID := responseValue.Get("id")
		if (responseValue.IsObject() && conversationID.Type == gjson.String && conversationID.String() != "") || responseValue.Raw == "null" {
			return response
		}
	default:
		if responseValue.Exists() {
			return response
		}
	}

	requestValue := gjson.GetBytes(request, policy.field)
	switch policy.kind {
	case responsesJSONFieldBoolean:
		if requestValue.Type == gjson.True || requestValue.Type == gjson.False {
			updated, err := sjson.SetBytes(response, policy.field, requestValue.Bool())
			if err == nil {
				return updated
			}
			return response
		}
	case responsesJSONFieldConversation:
		conversationID := ""
		if requestValue.Type == gjson.String {
			conversationID = requestValue.String()
		} else if id := requestValue.Get("id"); requestValue.IsObject() && id.Type == gjson.String {
			conversationID = id.String()
		}
		if conversationID != "" {
			value := []byte(`{"id":""}`)
			value, _ = sjson.SetBytes(value, "id", conversationID)
			updated, err := sjson.SetRawBytes(response, policy.field, value)
			if err == nil {
				return updated
			}
			return response
		}
	default:
		if requestValue.Exists() {
			updated, err := sjson.SetRawBytes(response, policy.field, []byte(requestValue.Raw))
			if err == nil {
				return updated
			}
			return response
		}
	}

	if policy.fallback == "" {
		if policy.kind == responsesJSONFieldConversation && responseValue.Exists() {
			updated, err := sjson.DeleteBytes(response, policy.field)
			if err == nil {
				return updated
			}
		}
		return response
	}
	var (
		updated []byte
		err     error
	)
	if policy.kind == responsesJSONFieldBoolean {
		updated, err = sjson.SetBytes(response, policy.field, policy.fallback == "true")
	} else {
		updated, err = sjson.SetRawBytes(response, policy.field, []byte(policy.fallback))
	}
	if err != nil {
		return response
	}
	return updated
}

func (f *responsesSSEFramer) failedResponseObject() []byte {
	response := append([]byte(nil), f.responseObject...)
	if !json.Valid(response) || !gjson.ParseBytes(response).IsObject() {
		response = []byte("{}")
	}
	if f.responseID != "" && !gjson.GetBytes(response, "id").Exists() {
		if updated, err := sjson.SetBytes(response, "id", f.responseID); err == nil {
			response = updated
		}
	}
	policies := []responsesJSONFieldPolicy{
		{field: "created_at", fallback: "0"},
		{field: "error", fallback: "null"},
		{field: "incomplete_details", fallback: "null"},
		{field: "instructions", fallback: "null"},
		{field: "metadata", fallback: "null"},
		{field: "model", fallback: `""`},
		{field: "object", fallback: `"response"`},
		{field: "output", fallback: "[]"},
		{field: "parallel_tool_calls", fallback: "true", kind: responsesJSONFieldBoolean},
		{field: "temperature", fallback: "null"},
		{field: "tool_choice", fallback: `"auto"`},
		{field: "tools", fallback: "[]"},
		{field: "top_p", fallback: "null"},
		{field: "background", fallback: "false"},
		{field: "status", fallback: `"failed"`},
		{field: "text", fallback: `{"format":{"type":"text"}}`},
		{field: "usage", fallback: "null"},
		{field: "store", fallback: "true", kind: responsesJSONFieldBoolean},
		{field: "conversation", kind: responsesJSONFieldConversation},
		{field: "max_output_tokens"},
		{field: "max_tool_calls"},
		{field: "previous_response_id"},
		{field: "prompt"},
		{field: "prompt_cache_key"},
		{field: "prompt_cache_options"},
		{field: "prompt_cache_retention"},
		{field: "reasoning"},
		{field: "safety_identifier"},
		{field: "service_tier"},
		{field: "top_logprobs"},
		{field: "truncation"},
		{field: "user"},
	}
	for _, policy := range policies {
		response = applyResponsesJSONFieldPolicy(response, f.requestJSON, policy)
	}
	if outputJSON := f.recordedOutputJSON(); len(outputJSON) > 0 {
		output := gjson.GetBytes(response, "output")
		if !output.Exists() || !output.IsArray() || len(output.Array()) == 0 {
			if updated, err := sjson.SetRawBytes(response, "output", outputJSON); err == nil {
				response = updated
			}
		}
	}
	return response
}

func (f *responsesSSEFramer) repairCompletedPayload(payload []byte) []byte {
	outputJSON := f.recordedOutputJSON()
	if len(outputJSON) == 0 {
		return payload
	}
	output := gjson.GetBytes(payload, "response.output")
	if output.Exists() && (!output.IsArray() || len(output.Array()) > 0) {
		return payload
	}
	repaired, err := sjson.SetRawBytes(payload, "response.output", outputJSON)
	if err != nil {
		return payload
	}
	return repaired
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

func (h *OpenAIResponsesAPIHandler) prepareCodexMultiAgentV2Tools(c *gin.Context, payload []byte) []byte {
	if h == nil || h.Cfg == nil {
		return payload
	}

	requestCtx := context.Background()
	if c != nil && c.Request != nil {
		requestCtx = c.Request.Context()
	}
	requestCtx = context.WithValue(requestCtx, "gin", c)

	var requestHeaders http.Header
	if c != nil && c.Request != nil {
		requestHeaders = c.Request.Header
	}
	homeEnabled := h.AuthManager != nil && h.AuthManager.HomeEnabled()
	updated, prepared := multiagentv2.PrepareCodexMultiAgentV2Tools(
		requestCtx,
		requestHeaders,
		payload,
		h.Cfg.CodexOptimizeMultiAgentV2,
		homeEnabled,
	)
	if prepared && c != nil {
		c.Set(multiagentv2.CodexMultiAgentV2ToolsPreparedContextKey, true)
	}
	return updated
}

// Responses handles the /v1/responses endpoint.
// It determines whether the request is for a streaming or non-streaming response
// and calls the appropriate handler based on the model provider.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
func (h *OpenAIResponsesAPIHandler) Responses(c *gin.Context) {
	rawJSON, err := handlers.ReadRequestBody(c)
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

	rawJSON = h.prepareCodexMultiAgentV2Tools(c, rawJSON)

	// Check if the client requested a streaming response.
	streamResult := gjson.GetBytes(rawJSON, "stream")
	if streamResult.Type == gjson.True {
		h.handleStreamingResponse(c, rawJSON)
	} else {
		h.handleNonStreamingResponse(c, rawJSON)
	}

}

func (h *OpenAIResponsesAPIHandler) Compact(c *gin.Context) {
	rawJSON, err := handlers.ReadRequestBody(c)
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

	modelName := gjson.GetBytes(rawJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)

	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, "")
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

	// New core execution path
	modelName := gjson.GetBytes(rawJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	dataChan, upstreamHeaders, errChan := h.ExecuteStreamWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, "")

	setSSEHeaders := func() {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("Access-Control-Allow-Origin", "*")
	}
	framer := &responsesSSEFramer{requestJSON: append([]byte(nil), rawJSON...)}

	// Peek at the first chunk
	for {
		select {
		case <-c.Request.Context().Done():
			cliCancel(c.Request.Context().Err())
			return
		case errMsg, ok := <-errChan:
			if !ok {
				// Err channel closed cleanly; wait for data channel.
				errChan = nil
				continue
			}
			// Upstream failed immediately. Return proper error status and JSON.
			h.WriteErrorResponse(c, errMsg)
			if errMsg != nil {
				cliCancel(errMsg.Error)
			} else {
				cliCancel(nil)
			}
			return
		case chunk, ok := <-dataChan:
			if !ok {
				// Stream closed without data? Send headers and done.
				setSSEHeaders()
				handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
				_, _ = c.Writer.Write([]byte("\n"))
				flusher.Flush()
				cliCancel(nil)
				return
			}

			// Success! Set headers.
			setSSEHeaders()
			handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)

			// Write first chunk logic (matching forwardResponsesStream)
			framer.WriteChunk(c.Writer, chunk)
			flusher.Flush()

			// Continue
			h.forwardResponsesStream(c, flusher, func(err error) { cliCancel(err) }, dataChan, errChan, framer)
			return
		}
	}
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
			if errMsg == nil || framer.terminalEventSeen {
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
			responseID, sequenceNumber, responseObject := framer.terminalMetadata()
			chunk := handlers.BuildOpenAIResponsesFailedStreamChunk(status, errText, responseID, sequenceNumber, responseObject)
			_, _ = fmt.Fprintf(c.Writer, "\nevent: response.failed\ndata: %s\n\n", string(chunk))
		},
		WriteDone: func() {
			framer.Flush(c.Writer)
			_, _ = c.Writer.Write([]byte("\n"))
		},
	})
}
