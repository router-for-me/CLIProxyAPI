package handlers

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestBuildOpenAIResponsesStreamErrorChunk(t *testing.T) {
	chunk := BuildOpenAIResponsesStreamErrorChunk(http.StatusInternalServerError, "unexpected EOF", 0)
	var payload map[string]any
	if err := json.Unmarshal(chunk, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload["type"] != "error" {
		t.Fatalf("type = %v, want %q", payload["type"], "error")
	}
	if payload["code"] != "internal_server_error" {
		t.Fatalf("code = %v, want %q", payload["code"], "internal_server_error")
	}
	if payload["message"] != "unexpected EOF" {
		t.Fatalf("message = %v, want %q", payload["message"], "unexpected EOF")
	}
	if payload["sequence_number"] != float64(0) {
		t.Fatalf("sequence_number = %v, want %v", payload["sequence_number"], 0)
	}
}

func TestBuildOpenAIResponsesStreamErrorChunkExtractsHTTPErrorBody(t *testing.T) {
	chunk := BuildOpenAIResponsesStreamErrorChunk(
		http.StatusInternalServerError,
		`{"error":{"message":"oops","type":"server_error","code":"internal_server_error"}}`,
		0,
	)
	var payload map[string]any
	if err := json.Unmarshal(chunk, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload["type"] != "error" {
		t.Fatalf("type = %v, want %q", payload["type"], "error")
	}
	if payload["code"] != "internal_server_error" {
		t.Fatalf("code = %v, want %q", payload["code"], "internal_server_error")
	}
	if payload["message"] != "oops" {
		t.Fatalf("message = %v, want %q", payload["message"], "oops")
	}
}

func TestOpenAIResponsesFailedErrorCode(t *testing.T) {
	tests := []struct {
		name   string
		status int
		code   string
		want   string
	}{
		{name: "transport failure", status: http.StatusInternalServerError, code: "internal_server_error", want: "server_error"},
		{name: "transport timeout", status: http.StatusRequestTimeout, code: "request_timeout", want: "server_error"},
		{name: "authentication failure", status: http.StatusUnauthorized, code: "invalid_api_key", want: "server_error"},
		{name: "permission failure", status: http.StatusForbidden, code: "permission_denied", want: "server_error"},
		{name: "model not found", status: http.StatusNotFound, code: "model_not_found", want: "invalid_prompt"},
		{name: "overloaded", status: http.StatusServiceUnavailable, code: "server_overloaded", want: "server_error"},
		{name: "rate limit", status: http.StatusTooManyRequests, code: "rate_limit_exceeded", want: "rate_limit_exceeded"},
		{name: "bad request", status: http.StatusBadRequest, code: "invalid_request_error", want: "invalid_prompt"},
		{name: "image code", status: http.StatusBadRequest, code: "invalid_image", want: "invalid_image"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := openAIResponsesFailedErrorCode(test.status, test.code); got != test.want {
				t.Fatalf("code = %q, want %q", got, test.want)
			}
		})
	}
}

func TestBuildOpenAIResponsesFailedStreamChunkServerFailures(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		errText string
	}{
		{
			name:    "request timeout",
			status:  http.StatusRequestTimeout,
			errText: "stream error: stream disconnected before completion: stream closed before response.completed",
		},
		{
			name:    "authentication failure",
			status:  http.StatusUnauthorized,
			errText: `{"error":{"type":"authentication_error","code":"invalid_api_key","message":"upstream credential rejected"}}`,
		},
		{
			name:    "permission failure",
			status:  http.StatusForbidden,
			errText: `{"error":{"type":"permission_error","code":"permission_denied","message":"upstream permission denied"}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			chunk := BuildOpenAIResponsesFailedStreamChunk(test.status, test.errText, "resp_failure", 2, nil)
			var payload struct {
				Response struct {
					Error openAIResponsesFailedError `json:"error"`
				} `json:"response"`
			}
			if err := json.Unmarshal(chunk, &payload); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if payload.Response.Error.Code != "server_error" {
				t.Fatalf("error code = %q, want server_error", payload.Response.Error.Code)
			}
		})
	}
}

func TestBuildOpenAIResponsesFailedStreamChunk(t *testing.T) {
	chunk := BuildOpenAIResponsesFailedStreamChunk(
		http.StatusServiceUnavailable,
		`{"error":{"message":"Our servers are currently overloaded.","code":"server_overloaded"}}`,
		"resp_fallback",
		7,
		[]byte(`{"id":"resp_test","object":"response","created_at":123,"status":"in_progress","model":"gpt-test","output":[],"parallel_tool_calls":true,"tool_choice":"auto","tools":[],"text":{"format":{"type":"text"}}}`),
	)

	var payload struct {
		Type           string `json:"type"`
		SequenceNumber int    `json:"sequence_number"`
		Response       struct {
			ID                string         `json:"id"`
			Object            string         `json:"object"`
			CreatedAt         int64          `json:"created_at"`
			Status            string         `json:"status"`
			Model             string         `json:"model"`
			Output            []any          `json:"output"`
			ParallelToolCalls bool           `json:"parallel_tool_calls"`
			ToolChoice        string         `json:"tool_choice"`
			Tools             []any          `json:"tools"`
			Text              map[string]any `json:"text"`
			Error             struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"response"`
	}
	if err := json.Unmarshal(chunk, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Type != "response.failed" {
		t.Fatalf("type = %q, want %q", payload.Type, "response.failed")
	}
	if payload.SequenceNumber != 7 {
		t.Fatalf("sequence_number = %d, want 7", payload.SequenceNumber)
	}
	if payload.Response.Status != "failed" {
		t.Fatalf("status = %q, want %q", payload.Response.Status, "failed")
	}
	if payload.Response.Object != "response" {
		t.Fatalf("object = %q, want %q", payload.Response.Object, "response")
	}
	if payload.Response.ID != "resp_test" {
		t.Fatalf("response id = %q, want %q", payload.Response.ID, "resp_test")
	}
	if payload.Response.CreatedAt != 123 || payload.Response.Model != "gpt-test" {
		t.Fatalf("response metadata was not preserved: %#v", payload.Response)
	}
	if !payload.Response.ParallelToolCalls || payload.Response.ToolChoice != "auto" {
		t.Fatalf("response tool settings were not preserved: %#v", payload.Response)
	}
	if payload.Response.Tools == nil || payload.Response.Text == nil {
		t.Fatalf("response schema fields were not preserved: %#v", payload.Response)
	}
	if payload.Response.Output == nil || len(payload.Response.Output) != 0 {
		t.Fatalf("output = %#v, want empty array", payload.Response.Output)
	}
	if payload.Response.Error.Code != "server_error" {
		t.Fatalf("error code = %q, want %q", payload.Response.Error.Code, "server_error")
	}
	if payload.Response.Error.Message != "Our servers are currently overloaded." {
		t.Fatalf("error message = %q", payload.Response.Error.Message)
	}
}
