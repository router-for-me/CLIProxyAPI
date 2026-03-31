package openai

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const responsesTurnStateTTL = 30 * time.Minute

type responsesTurnStateEntry struct {
	request []byte
	output  []byte
	expire  time.Time
}

type responsesTurnStateCache struct {
	entries sync.Map
}

func (c *responsesTurnStateCache) load(responseID string) ([]byte, []byte, bool) {
	responseID = strings.TrimSpace(responseID)
	if responseID == "" {
		return nil, nil, false
	}
	raw, ok := c.entries.Load(responseID)
	if !ok {
		return nil, nil, false
	}
	entry, ok := raw.(responsesTurnStateEntry)
	if !ok {
		c.entries.Delete(responseID)
		return nil, nil, false
	}
	if !entry.expire.After(time.Now()) {
		c.entries.Delete(responseID)
		return nil, nil, false
	}
	return bytes.Clone(entry.request), bytes.Clone(entry.output), true
}

func (c *responsesTurnStateCache) store(responseID string, requestJSON []byte, outputJSON []byte) {
	responseID = strings.TrimSpace(responseID)
	if responseID == "" || len(requestJSON) == 0 || len(outputJSON) == 0 {
		return
	}
	c.entries.Store(responseID, responsesTurnStateEntry{
		request: bytes.Clone(requestJSON),
		output:  bytes.Clone(outputJSON),
		expire:  time.Now().Add(responsesTurnStateTTL),
	})
}

func normalizeResponsesRequestInputRaw(rawJSON []byte) (string, *interfaces.ErrorMessage) {
	input := gjson.GetBytes(rawJSON, "input")
	if !input.Exists() {
		return "[]", nil
	}
	if input.IsArray() {
		return input.Raw, nil
	}
	if input.Type == gjson.String {
		message := []byte(`{"type":"message","role":"user","content":""}`)
		message, _ = sjson.SetBytes(message, "content", input.String())
		return fmt.Sprintf("[%s]", message), nil
	}
	return "", &interfaces.ErrorMessage{
		StatusCode: http.StatusBadRequest,
		Error:      fmt.Errorf("responses request requires array or string field: input"),
	}
}

func normalizeResponsesHTTPContinuationRequest(rawJSON []byte, lastRequest []byte, lastResponseOutput []byte) ([]byte, *interfaces.ErrorMessage) {
	if len(lastRequest) == 0 {
		return rawJSON, nil
	}

	nextInput := gjson.GetBytes(rawJSON, "input")
	if !nextInput.Exists() || !nextInput.IsArray() {
		return nil, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("responses request requires array field: input"),
		}
	}

	existingInputRaw, errMsg := normalizeResponsesRequestInputRaw(lastRequest)
	if errMsg != nil {
		return nil, errMsg
	}
	mergedInput, errMerge := mergeJSONArrayRaw(existingInputRaw, normalizeJSONArrayRaw(lastResponseOutput))
	if errMerge != nil {
		return nil, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("invalid previous response output: %w", errMerge),
		}
	}
	mergedInput, errMerge = mergeJSONArrayRaw(mergedInput, nextInput.Raw)
	if errMerge != nil {
		return nil, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("invalid request input: %w", errMerge),
		}
	}

	normalized, errDelete := sjson.DeleteBytes(rawJSON, "previous_response_id")
	if errDelete != nil {
		normalized = bytes.Clone(rawJSON)
	}
	var errSet error
	normalized, errSet = sjson.SetRawBytes(normalized, "input", []byte(mergedInput))
	if errSet != nil {
		return nil, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("failed to merge responses input: %w", errSet),
		}
	}
	if !gjson.GetBytes(normalized, "model").Exists() {
		modelName := strings.TrimSpace(gjson.GetBytes(lastRequest, "model").String())
		if modelName != "" {
			normalized, _ = sjson.SetBytes(normalized, "model", modelName)
		}
	}
	if !gjson.GetBytes(normalized, "instructions").Exists() {
		instructions := gjson.GetBytes(lastRequest, "instructions")
		if instructions.Exists() {
			normalized, _ = sjson.SetRawBytes(normalized, "instructions", []byte(instructions.Raw))
		}
	}
	return normalized, nil
}

func (h *OpenAIResponsesAPIHandler) normalizeContinuationRequest(rawJSON []byte) ([]byte, *interfaces.ErrorMessage) {
	if h == nil {
		return rawJSON, nil
	}
	previousResponseID := strings.TrimSpace(gjson.GetBytes(rawJSON, "previous_response_id").String())
	if previousResponseID == "" {
		return rawJSON, nil
	}
	lastRequest, lastResponseOutput, ok := h.turnState.load(previousResponseID)
	if !ok {
		return rawJSON, nil
	}
	return normalizeResponsesHTTPContinuationRequest(rawJSON, lastRequest, lastResponseOutput)
}

func (h *OpenAIResponsesAPIHandler) rememberCompletedResponse(requestJSON []byte, responseJSON []byte) {
	if h == nil {
		return
	}
	responseID := strings.TrimSpace(gjson.GetBytes(responseJSON, "id").String())
	if responseID == "" {
		responseID = strings.TrimSpace(gjson.GetBytes(responseJSON, "response.id").String())
	}
	if responseID == "" {
		return
	}
	output := gjson.GetBytes(responseJSON, "output")
	if !output.Exists() || !output.IsArray() {
		output = gjson.GetBytes(responseJSON, "response.output")
	}
	if !output.Exists() || !output.IsArray() {
		return
	}
	h.turnState.store(responseID, requestJSON, []byte(output.Raw))
}

func (h *OpenAIResponsesAPIHandler) rememberCompletedResponseFromChunk(requestJSON []byte, chunk []byte) {
	if h == nil || len(chunk) == 0 {
		return
	}
	for _, payload := range websocketJSONPayloadsFromChunk(chunk) {
		if gjson.GetBytes(payload, "type").String() != wsEventTypeCompleted {
			continue
		}
		h.rememberCompletedResponse(requestJSON, payload)
	}
}
