package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type openAIResponsesStreamErrorChunk struct {
	Type           string         `json:"type"`
	Error          map[string]any `json:"error"`
	SequenceNumber int            `json:"sequence_number"`
}

type openAIResponsesStreamFailedChunk struct {
	Type           string                              `json:"type"`
	SequenceNumber int                                 `json:"sequence_number"`
	Response       openAIResponsesStreamFailedResponse `json:"response"`
}

type openAIResponsesStreamFailedResponse struct {
	Status string         `json:"status"`
	Error  map[string]any `json:"error"`
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

func unmarshalJSONWithNumber(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return dec.Decode(v)
}

// BuildOpenAIResponsesStreamErrorChunk builds an OpenAI Responses streaming error chunk.
// It matches the official responses streaming event shape where the error details are nested.
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
		if errUnmarshal := unmarshalJSONWithNumber([]byte(trimmed), &payload); errUnmarshal == nil {
			if v, ok := payload["sequence_number"]; ok {
				switch n := v.(type) {
				case json.Number:
					if seqInt, err := n.Int64(); err == nil {
						sequenceNumber = int(seqInt)
					}
				case float64:
					sequenceNumber = int(n)
				}
			}
		}
	}

	if strings.TrimSpace(code) == "" {
		code = "unknown_error"
	}

	errorDetail := openAIResponsesStreamErrorDetail(status, errText, code, message)

	data, errMarshal := json.Marshal(openAIResponsesStreamErrorChunk{
		Type:           "error",
		Error:          errorDetail,
		SequenceNumber: sequenceNumber,
	})
	if errMarshal == nil {
		return data
	}

	// Extremely defensive fallback.
	fallbackDetail := map[string]any{
		"type":    "server_error",
		"code":    "internal_server_error",
		"message": message,
		"param":   nil,
	}
	data, _ = json.Marshal(openAIResponsesStreamErrorChunk{
		Type:           "error",
		Error:          fallbackDetail,
		SequenceNumber: sequenceNumber,
	})
	if len(data) > 0 {
		return data
	}
	return []byte(`{"type":"error","error":{"type":"server_error","code":"internal_server_error","message":"internal error","param":null},"sequence_number":0}`)
}

func openAIResponsesStreamErrorDetail(status int, errText, code, message string) map[string]any {
	var payload map[string]any
	trimmed := strings.TrimSpace(errText)
	if trimmed != "" && json.Valid([]byte(trimmed)) {
		if errUnmarshal := unmarshalJSONWithNumber([]byte(trimmed), &payload); errUnmarshal == nil {
			if errorDetail, ok := payload["error"].(map[string]any); ok {
				return errorDetail
			}
			if response, ok := payload["response"].(map[string]any); ok {
				if errorDetail, ok := response["error"].(map[string]any); ok {
					return errorDetail
				}
			}
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
		}
	}

	errorType := "invalid_request_error"
	if status >= http.StatusInternalServerError {
		errorType = "server_error"
	}
	detail := map[string]any{
		"type":    errorType,
		"code":    code,
		"message": message,
		"param":   nil,
	}
	if payload != nil {
		if t, ok := payload["type"].(string); ok && strings.TrimSpace(t) != "" && strings.TrimSpace(t) != "error" {
			detail["type"] = strings.TrimSpace(t)
		}
		if paramVal, exists := payload["param"]; exists {
			detail["param"] = paramVal
		}
	}
	return detail
}

func openAIResponsesStreamFailedErrorDetail(status int, errText, code, message string) map[string]any {
	return openAIResponsesStreamErrorDetail(status, errText, code, message)
}

// BuildOpenAIResponsesStreamFailedChunk builds the terminal Responses event used by official Codex clients.
// It is intentionally separate from BuildOpenAIResponsesStreamErrorChunk so existing clients keep the legacy shape.
func BuildOpenAIResponsesStreamFailedChunk(status int, errText string, sequenceNumber int) []byte {
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	if sequenceNumber < 0 {
		sequenceNumber = 0
	}

	errorChunkBytes := BuildOpenAIResponsesStreamErrorChunk(status, errText, sequenceNumber)
	var errorChunk openAIResponsesStreamErrorChunk
	_ = unmarshalJSONWithNumber(errorChunkBytes, &errorChunk)
	sequenceNumber = errorChunk.SequenceNumber

	errorDetail := errorChunk.Error
	if errorDetail == nil {
		code := openAIResponsesStreamErrorCode(status)
		message := strings.TrimSpace(errText)
		if message == "" {
			message = http.StatusText(status)
		}
		errorDetail = openAIResponsesStreamErrorDetail(status, errText, code, message)
	}

	data, errMarshal := json.Marshal(openAIResponsesStreamFailedChunk{
		Type:           "response.failed",
		SequenceNumber: sequenceNumber,
		Response: openAIResponsesStreamFailedResponse{
			Status: "failed",
			Error:  errorDetail,
		},
	})
	if errMarshal == nil {
		return data
	}

	return []byte(`{"type":"response.failed","sequence_number":0,"response":{"status":"failed","error":{"type":"server_error","code":"internal_server_error","message":"internal error"}}}`)
}
