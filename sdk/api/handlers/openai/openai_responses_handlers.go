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
	"strings"

	"github.com/gin-gonic/gin"
	. "github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
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
	pending              []byte
	outputItems          map[int][]byte
	outputOrder          []int
	unindexedOutputItems [][]byte
	completedImageCalls  map[string]struct{}
	partialImageCalls    map[string]responsesSSEPartialImage
	response             []byte
	lastSequenceNumber   int64
}

type responsesSSEPartialImage struct {
	itemID       string
	callID       string
	outputIndex  []byte
	outputFormat string
	result       []byte
}

const responsesSSEIncompleteCodexStreamMessage = "stream error: stream disconnected before completion: stream closed before response.completed"

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
	f.recordFrameState(payload)

	switch util.GetGJSONBytesNoCopy(payload, "type").String() {
	case "response.image_generation_call.completed":
		if f.imageCallCompleted(payload) {
			return nil
		}
		f.recordCompletedImageCall(payload)
	case "response.output_item.done":
		repaired, imageCompleted := responsesSSECompleteImageGenerationCall(payload)
		f.recordOutputItem(repaired)
		if len(imageCompleted) > 0 && !f.imageCallCompleted(imageCompleted) {
			f.recordCompletedImageCall(imageCompleted)
			imageCompletedFrame := responsesSSEEventFrame("response.image_generation_call.completed", imageCompleted)
			return append(imageCompletedFrame, responsesSSEFrameWithData(frame, repaired)...)
		}
		if len(imageCompleted) == 0 {
			f.recordNativeCompletedImageOutput(repaired)
		}
		if !bytes.Equal(repaired, payload) {
			return responsesSSEFrameWithData(frame, repaired)
		}
	case "response.completed":
		repaired := f.repairCompletedPayload(payload)
		if !bytes.Equal(repaired, payload) {
			return responsesSSEFrameWithData(frame, repaired)
		}
	}
	return frame
}

func (f *responsesSSEFramer) recordFrameState(payload []byte) {
	if sequenceNumber := util.GetGJSONBytesNoCopy(payload, "sequence_number"); sequenceNumber.Exists() && sequenceNumber.Int() > f.lastSequenceNumber {
		f.lastSequenceNumber = sequenceNumber.Int()
	}
	if response := util.GetGJSONBytesNoCopy(payload, "response"); response.IsObject() {
		f.response = append(f.response[:0], response.Raw...)
	}
	if util.GetGJSONBytesNoCopy(payload, "type").String() != "response.image_generation_call.partial_image" {
		return
	}

	itemID := strings.TrimSpace(util.GetGJSONBytesNoCopy(payload, "item_id").String())
	result := util.GetGJSONBytesNoCopy(payload, "partial_image_b64")
	if itemID == "" || result.Type != gjson.String || len(result.Raw) <= 2 {
		return
	}
	callID := strings.TrimSpace(util.GetGJSONBytesNoCopy(payload, "call_id").String())
	if callID == "" {
		callID = itemID
	}
	partialImage := responsesSSEPartialImage{
		itemID:       itemID,
		callID:       callID,
		outputFormat: strings.TrimSpace(util.GetGJSONBytesNoCopy(payload, "output_format").String()),
		result:       append([]byte(nil), result.Raw...),
	}
	if outputIndex := util.GetGJSONBytesNoCopy(payload, "output_index"); outputIndex.Exists() {
		partialImage.outputIndex = append([]byte(nil), outputIndex.Raw...)
	}
	if f.partialImageCalls == nil {
		f.partialImageCalls = make(map[string]responsesSSEPartialImage)
	}
	f.partialImageCalls[itemID] = partialImage
}

func responsesSSECompleteImageGenerationCall(payload []byte) ([]byte, []byte) {
	item := util.GetGJSONBytesNoCopy(payload, "item")
	if item.Get("type").String() != "image_generation_call" ||
		!strings.EqualFold(strings.TrimSpace(item.Get("status").String()), "generating") {
		return payload, nil
	}

	itemID := strings.TrimSpace(item.Get("id").String())
	result := item.Get("result")
	if itemID == "" || result.Type != gjson.String || len(result.Raw) <= 2 {
		return payload, nil
	}

	repaired, err := sjson.SetBytes(payload, "item.status", "completed")
	if err != nil {
		return payload, nil
	}

	completed := []byte(`{"type":"response.image_generation_call.completed","item_id":"","call_id":"","result":""}`)
	completed, _ = sjson.SetBytes(completed, "item_id", itemID)
	completed, _ = sjson.SetBytes(completed, "call_id", itemID)
	completed, _ = sjson.SetRawBytes(completed, "result", []byte(result.Raw))
	for _, field := range []string{"output_index", "sequence_number"} {
		if value := util.GetGJSONBytesNoCopy(payload, field); value.Exists() {
			completed, _ = sjson.SetRawBytes(completed, field, []byte(value.Raw))
		}
	}
	return repaired, completed
}

func responsesSSEEventFrame(event string, payload []byte) []byte {
	frame := make([]byte, 0, len("event: ")+len(event)+len("\ndata: ")+len(payload)+len("\n\n"))
	frame = append(frame, "event: "...)
	frame = append(frame, event...)
	frame = append(frame, "\ndata: "...)
	frame = append(frame, payload...)
	return append(frame, "\n\n"...)
}

func (f *responsesSSEFramer) recordCompletedImageCall(payload []byte) {
	for _, field := range []string{"item_id", "call_id"} {
		id := strings.TrimSpace(util.GetGJSONBytesNoCopy(payload, field).String())
		if id == "" {
			continue
		}
		if f.completedImageCalls == nil {
			f.completedImageCalls = make(map[string]struct{})
		}
		f.completedImageCalls[id] = struct{}{}
	}
}

func (f *responsesSSEFramer) imageCallCompleted(payload []byte) bool {
	if len(f.completedImageCalls) == 0 {
		return false
	}
	for _, field := range []string{"item_id", "call_id"} {
		id := strings.TrimSpace(util.GetGJSONBytesNoCopy(payload, field).String())
		if _, ok := f.completedImageCalls[id]; ok && id != "" {
			return true
		}
	}
	return false
}

func (f *responsesSSEFramer) recordNativeCompletedImageOutput(payload []byte) {
	item := util.GetGJSONBytesNoCopy(payload, "item")
	if item.Get("type").String() != "image_generation_call" ||
		!strings.EqualFold(strings.TrimSpace(item.Get("status").String()), "completed") {
		return
	}
	itemID := strings.TrimSpace(item.Get("id").String())
	if itemID == "" {
		return
	}
	if f.completedImageCalls == nil {
		f.completedImageCalls = make(map[string]struct{})
	}
	f.completedImageCalls[itemID] = struct{}{}
}

func responsesSSEIsIncompleteCodexStream(errMsg *interfaces.ErrorMessage) bool {
	return errMsg != nil && errMsg.StatusCode == http.StatusRequestTimeout && errMsg.Error != nil &&
		errMsg.Error.Error() == responsesSSEIncompleteCodexStreamMessage
}

func (f *responsesSSEFramer) writePartialImageRecovery(w io.Writer) bool {
	if w == nil || len(f.response) == 0 {
		return false
	}

	images := make([]responsesSSEPartialImage, 0, len(f.partialImageCalls))
	for _, image := range f.partialImageCalls {
		if _, completed := f.completedImageCalls[image.itemID]; !completed {
			images = append(images, image)
		}
	}
	if len(images) == 0 {
		return false
	}
	sort.Slice(images, func(i, j int) bool {
		leftIndex := gjson.ParseBytes(images[i].outputIndex)
		rightIndex := gjson.ParseBytes(images[j].outputIndex)
		switch {
		case leftIndex.Exists() && rightIndex.Exists() && leftIndex.Int() != rightIndex.Int():
			return leftIndex.Int() < rightIndex.Int()
		case leftIndex.Exists() != rightIndex.Exists():
			return leftIndex.Exists()
		default:
			return images[i].itemID < images[j].itemID
		}
	})

	completedPayloads := make([][]byte, 0, len(images))
	donePayloads := make([][]byte, 0, len(images))
	for _, image := range images {
		completed, done, ok := f.partialImageRecoveryPayloads(image)
		if !ok {
			return false
		}
		completedPayloads = append(completedPayloads, completed)
		donePayloads = append(donePayloads, done)
		f.recordOutputItem(done)
	}
	responseCompleted := f.partialImageRecoveryCompletedPayload()
	if len(responseCompleted) == 0 {
		return false
	}

	for index := range completedPayloads {
		f.recordCompletedImageCall(completedPayloads[index])
		writeResponsesSSEChunk(w, responsesSSEEventFrame("response.image_generation_call.completed", completedPayloads[index]))
		writeResponsesSSEChunk(w, responsesSSEEventFrame("response.output_item.done", donePayloads[index]))
	}
	writeResponsesSSEChunk(w, responsesSSEEventFrame("response.completed", responseCompleted))
	return true
}

func (f *responsesSSEFramer) partialImageRecoveryPayloads(image responsesSSEPartialImage) ([]byte, []byte, bool) {
	if image.itemID == "" || image.callID == "" || len(image.result) == 0 || !json.Valid(image.result) {
		return nil, nil, false
	}

	completed := []byte(`{"type":"response.image_generation_call.completed","item_id":"","call_id":"","result":""}`)
	completed, _ = sjson.SetBytes(completed, "item_id", image.itemID)
	completed, _ = sjson.SetBytes(completed, "call_id", image.callID)
	completed, _ = sjson.SetRawBytes(completed, "result", image.result)
	completed, _ = sjson.SetBytes(completed, "sequence_number", f.nextSequenceNumber())
	if len(image.outputIndex) > 0 {
		completed, _ = sjson.SetRawBytes(completed, "output_index", image.outputIndex)
	}
	if image.outputFormat != "" {
		completed, _ = sjson.SetBytes(completed, "output_format", image.outputFormat)
	}

	item := []byte(`{"id":"","type":"image_generation_call","status":"completed","result":""}`)
	item, _ = sjson.SetBytes(item, "id", image.itemID)
	item, _ = sjson.SetRawBytes(item, "result", image.result)
	if image.outputFormat != "" {
		item, _ = sjson.SetBytes(item, "output_format", image.outputFormat)
	}
	done := []byte(`{"type":"response.output_item.done","item":{}}`)
	done, _ = sjson.SetRawBytes(done, "item", item)
	done, _ = sjson.SetBytes(done, "sequence_number", f.nextSequenceNumber())
	if len(image.outputIndex) > 0 {
		done, _ = sjson.SetRawBytes(done, "output_index", image.outputIndex)
	}
	return completed, done, true
}

func (f *responsesSSEFramer) partialImageRecoveryCompletedPayload() []byte {
	output, ok := f.recordedOutputItems()
	if !ok {
		return nil
	}
	response, err := sjson.SetBytes(f.response, "status", "completed")
	if err != nil {
		return nil
	}
	response, err = sjson.SetRawBytes(response, "output", output)
	if err != nil {
		return nil
	}
	payload := []byte(`{"type":"response.completed","response":{}}`)
	payload, err = sjson.SetRawBytes(payload, "response", response)
	if err != nil {
		return nil
	}
	payload, _ = sjson.SetBytes(payload, "sequence_number", f.nextSequenceNumber())
	return payload
}

func (f *responsesSSEFramer) nextSequenceNumber() int64 {
	f.lastSequenceNumber++
	return f.lastSequenceNumber
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

func (f *responsesSSEFramer) repairCompletedPayload(payload []byte) []byte {
	payload = responsesSSECompleteImageGenerationCallsInOutput(payload)
	outputJSON, ok := f.recordedOutputItems()
	if !ok {
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

func (f *responsesSSEFramer) recordedOutputItems() ([]byte, bool) {
	if len(f.outputOrder) == 0 && len(f.unindexedOutputItems) == 0 {
		return nil, false
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
	return outputJSON.Bytes(), true
}

func responsesSSECompleteImageGenerationCallsInOutput(payload []byte) []byte {
	output := gjson.GetBytes(payload, "response.output")
	if !output.IsArray() {
		return payload
	}

	repaired := payload
	for index, item := range output.Array() {
		if item.Get("type").String() != "image_generation_call" ||
			!strings.EqualFold(strings.TrimSpace(item.Get("status").String()), "generating") ||
			strings.TrimSpace(item.Get("result").String()) == "" {
			continue
		}
		next, err := sjson.SetBytes(repaired, fmt.Sprintf("response.output.%d.status", index), "completed")
		if err != nil {
			continue
		}
		repaired = next
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
	framer := &responsesSSEFramer{}

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
			if errMsg == nil {
				return
			}
			if responsesSSEIsIncompleteCodexStream(errMsg) && framer.writePartialImageRecovery(c.Writer) {
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
			chunk := handlers.BuildOpenAIResponsesStreamErrorChunk(status, errText, 0)
			_, _ = fmt.Fprintf(c.Writer, "\nevent: error\ndata: %s\n\n", string(chunk))
		},
		WriteDone: func() {
			framer.Flush(c.Writer)
			_, _ = c.Writer.Write([]byte("\n"))
		},
	})
}
