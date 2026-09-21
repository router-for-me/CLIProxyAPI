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

// openAIResponsesStreamErrorClass pairs the OpenAI error code and the OpenAI
// error type that a given HTTP status is reported as.
//
// The two are kept together deliberately. They are halves of one classification
// and a client reads both: the code names the condition, the type says whose
// fault it was and therefore whether a retry is allowed. Deriving them in two
// places let them contradict each other, which is how a Codex stream that was
// cut in transit came to be reported with the retryable code "request_timeout"
// alongside the type "invalid_request_error" — a type that tells every
// downstream consumer the request was malformed and must not be replayed.
type openAIResponsesStreamErrorClass struct {
	code      string
	errorType string
}

const (
	openAIResponsesClientErrorType = "invalid_request_error"
	openAIResponsesServerErrorType = "server_error"
)

// openAIResponsesStreamErrorClasses holds the statuses that do not follow the
// plain "4xx is the caller's fault, 5xx is ours" split.
//
// StatusRequestTimeout is the reason this table exists. A Codex stream that ends
// before response.completed is surfaced with that status, and the request itself
// was well formed: the connection failed, so the caller may replay it.
var openAIResponsesStreamErrorClasses = map[int]openAIResponsesStreamErrorClass{
	http.StatusUnauthorized:    {code: "invalid_api_key", errorType: openAIResponsesClientErrorType},
	http.StatusForbidden:       {code: "insufficient_quota", errorType: openAIResponsesClientErrorType},
	http.StatusTooManyRequests: {code: "rate_limit_exceeded", errorType: openAIResponsesClientErrorType},
	http.StatusNotFound:        {code: "model_not_found", errorType: openAIResponsesClientErrorType},
	http.StatusRequestTimeout:  {code: "request_timeout", errorType: openAIResponsesServerErrorType},
}

func openAIResponsesStreamErrorClassFor(status int) openAIResponsesStreamErrorClass {
	if class, ok := openAIResponsesStreamErrorClasses[status]; ok {
		return class
	}
	if status >= http.StatusInternalServerError {
		return openAIResponsesStreamErrorClass{code: "internal_server_error", errorType: openAIResponsesServerErrorType}
	}
	if status >= http.StatusBadRequest {
		return openAIResponsesStreamErrorClass{code: "invalid_request_error", errorType: openAIResponsesClientErrorType}
	}
	return openAIResponsesStreamErrorClass{code: "unknown_error", errorType: openAIResponsesClientErrorType}
}

func openAIResponsesStreamErrorCode(status int) string {
	return openAIResponsesStreamErrorClassFor(status).code
}

func openAIResponsesStreamErrorType(status int) string {
	return openAIResponsesStreamErrorClassFor(status).errorType
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

	errorType := openAIResponsesStreamErrorType(status)
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
