package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/tidwall/sjson"
)

type openAIResponsesStreamErrorChunk struct {
	Type           string `json:"type"`
	Code           string `json:"code"`
	Message        string `json:"message"`
	SequenceNumber int    `json:"sequence_number"`
}

func openAIResponsesStreamErrorCode(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "invalid_api_key"
	case http.StatusForbidden:
		return "insufficient_quota"
	case http.StatusTooManyRequests:
		return "rate_limit_exceeded"
	case http.StatusNotFound:
		return "model_not_found"
	case http.StatusRequestTimeout:
		return "request_timeout"
	default:
		if status >= http.StatusInternalServerError {
			return "internal_server_error"
		}
		if status >= http.StatusBadRequest {
			return "invalid_request_error"
		}
		return "unknown_error"
	}
}

// BuildOpenAIResponsesStreamErrorChunk builds an OpenAI Responses streaming error chunk.
//
// Important: OpenAI's HTTP error bodies are shaped like {"error":{...}}; those are valid for
// non-streaming responses, but streaming clients validate SSE `data:` payloads against a union
// of chunks that requires a top-level `type` field.
func BuildOpenAIResponsesStreamErrorChunk(status int, errText string, sequenceNumber int) []byte {
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	if sequenceNumber < 0 {
		sequenceNumber = 0
	}

	message := strings.TrimSpace(errText)
	if message == "" {
		message = http.StatusText(status)
	}

	code := openAIResponsesStreamErrorCode(status)

	trimmed := strings.TrimSpace(errText)
	if trimmed != "" && json.Valid([]byte(trimmed)) {
		var payload map[string]any
		if err := json.Unmarshal([]byte(trimmed), &payload); err == nil {
			if t, ok := payload["type"].(string); ok && strings.TrimSpace(t) == "error" {
				if m, ok := payload["message"].(string); ok && strings.TrimSpace(m) != "" {
					message = strings.TrimSpace(m)
				}
				if v, ok := payload["code"]; ok && v != nil {
					if c, ok := v.(string); ok && strings.TrimSpace(c) != "" {
						code = strings.TrimSpace(c)
					} else {
						code = strings.TrimSpace(fmt.Sprint(v))
					}
				}
				if v, ok := payload["sequence_number"].(float64); ok && sequenceNumber == 0 {
					sequenceNumber = int(v)
				}
			}
			if e, ok := payload["error"].(map[string]any); ok {
				if m, ok := e["message"].(string); ok && strings.TrimSpace(m) != "" {
					message = strings.TrimSpace(m)
				}
				if v, ok := e["code"]; ok && v != nil {
					if c, ok := v.(string); ok && strings.TrimSpace(c) != "" {
						code = strings.TrimSpace(c)
					} else {
						code = strings.TrimSpace(fmt.Sprint(v))
					}
				}
			}
		}
	}

	if strings.TrimSpace(code) == "" {
		code = "unknown_error"
	}

	data, err := json.Marshal(openAIResponsesStreamErrorChunk{
		Type:           "error",
		Code:           code,
		Message:        message,
		SequenceNumber: sequenceNumber,
	})
	if err == nil {
		return data
	}

	// Extremely defensive fallback.
	data, _ = json.Marshal(openAIResponsesStreamErrorChunk{
		Type:           "error",
		Code:           "internal_server_error",
		Message:        message,
		SequenceNumber: sequenceNumber,
	})
	if len(data) > 0 {
		return data
	}
	return []byte(`{"type":"error","code":"internal_server_error","message":"internal error","sequence_number":0}`)
}

type openAIResponsesFailedError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func openAIResponsesFailedErrorCode(status int, code string) string {
	switch strings.TrimSpace(code) {
	case "server_error", "rate_limit_exceeded", "invalid_prompt", "data_residency_mismatch", "bio_policy", "vector_store_timeout",
		"invalid_image", "invalid_image_format", "invalid_base64_image", "invalid_image_url", "image_too_large", "image_too_small",
		"image_parse_error", "image_content_policy_violation", "invalid_image_mode", "image_file_too_large",
		"unsupported_image_media_type", "empty_image_file", "failed_to_download_image", "image_file_not_found":
		return strings.TrimSpace(code)
	}
	switch status {
	case http.StatusTooManyRequests:
		return "rate_limit_exceeded"
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusRequestTimeout:
		return "server_error"
	}
	if status >= http.StatusBadRequest && status < http.StatusInternalServerError {
		return "invalid_prompt"
	}
	return "server_error"
}

type openAIResponsesFailedResponse struct {
	ID     string                     `json:"id"`
	Object string                     `json:"object"`
	Status string                     `json:"status"`
	Output []any                      `json:"output"`
	Error  openAIResponsesFailedError `json:"error"`
}

type openAIResponsesFailedChunk struct {
	Type           string          `json:"type"`
	SequenceNumber int             `json:"sequence_number"`
	Response       json.RawMessage `json:"response"`
}

func buildOpenAIResponsesFailedResponse(responseObject []byte, responseID string, streamErr openAIResponsesStreamErrorChunk) json.RawMessage {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(responseObject, &fields); err == nil && fields != nil {
		response := append([]byte(nil), responseObject...)
		var currentID string
		_ = json.Unmarshal(fields["id"], &currentID)
		var errSet error
		if strings.TrimSpace(currentID) == "" {
			response, errSet = sjson.SetBytes(response, "id", responseID)
		}
		if errSet == nil {
			response, errSet = sjson.SetBytes(response, "object", "response")
		}
		if errSet == nil {
			response, errSet = sjson.SetBytes(response, "status", "failed")
		}
		if errSet == nil {
			errorJSON, _ := json.Marshal(openAIResponsesFailedError{Code: streamErr.Code, Message: streamErr.Message})
			response, errSet = sjson.SetRawBytes(response, "error", errorJSON)
		}
		if errSet == nil {
			if _, ok := fields["output"]; !ok {
				response, errSet = sjson.SetRawBytes(response, "output", []byte("[]"))
			}
		}
		if errSet == nil && json.Valid(response) {
			return response
		}
	}

	response, _ := json.Marshal(openAIResponsesFailedResponse{
		ID:     responseID,
		Object: "response",
		Status: "failed",
		Output: []any{},
		Error: openAIResponsesFailedError{
			Code:    streamErr.Code,
			Message: streamErr.Message,
		},
	})
	return response
}

// BuildOpenAIResponsesFailedStreamChunk builds a terminal response.failed event payload.
func BuildOpenAIResponsesFailedStreamChunk(status int, errText, responseID string, sequenceNumber int, responseObject []byte) []byte {
	if strings.TrimSpace(responseID) == "" {
		responseID = "resp_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	if sequenceNumber < 0 {
		sequenceNumber = 0
	}
	errorChunk := BuildOpenAIResponsesStreamErrorChunk(status, errText, 0)
	var streamErr openAIResponsesStreamErrorChunk
	if err := json.Unmarshal(errorChunk, &streamErr); err != nil {
		streamErr.Code = "internal_server_error"
		streamErr.Message = http.StatusText(http.StatusInternalServerError)
	}
	streamErr.Code = openAIResponsesFailedErrorCode(status, streamErr.Code)

	data, err := json.Marshal(openAIResponsesFailedChunk{
		Type:           "response.failed",
		SequenceNumber: sequenceNumber,
		Response:       buildOpenAIResponsesFailedResponse(responseObject, responseID, streamErr),
	})
	if err == nil {
		return data
	}
	return []byte(`{"type":"response.failed","sequence_number":0,"response":{"id":"resp_error","object":"response","status":"failed","output":[],"error":{"code":"server_error","message":"internal error"}}}`)
}
