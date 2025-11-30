package executor

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/tidwall/gjson"
)

func TestIsResponsesAPIAgentItem(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected bool
	}{
		// User messages - not agent
		{
			name:     "user message",
			json:     `{"role": "user", "content": [{"type": "input_text", "text": "hello"}]}`,
			expected: false,
		},
		{
			name:     "system message",
			json:     `{"role": "system", "content": "You are helpful"}`,
			expected: false,
		},
		// Assistant messages - agent
		{
			name:     "assistant message",
			json:     `{"role": "assistant", "content": [{"type": "output_text", "text": "hi"}]}`,
			expected: true,
		},
		// Function/tool types - agent
		{
			name:     "function_call",
			json:     `{"type": "function_call", "call_id": "123", "name": "test"}`,
			expected: true,
		},
		{
			name:     "function_call_output",
			json:     `{"type": "function_call_output", "call_id": "123", "output": "done"}`,
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := gjson.Parse(tt.json)
			got := isResponsesAPIAgentItem(result)
			if got != tt.expected {
				t.Errorf("isResponsesAPIAgentItem(%s) = %v, want %v", tt.name, got, tt.expected)
			}
		})
	}
}

func TestIsResponsesAPIVisionContent(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected bool
	}{
		{
			name:     "text only",
			json:     `{"role": "user", "content": "hello"}`,
			expected: false,
		},
		{
			name:     "with image_url",
			json:     `{"role": "user", "content": [{"type": "image_url", "image_url": {"url": "data:image/png;base64,..."}}]}`,
			expected: false, // image_url is handled separately in collectCopilotHeaderHints
		},
		{
			name:     "with input_image",
			json:     `{"type": "input_image", "source": {"data": "..."}}`,
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := gjson.Parse(tt.json)
			got := isResponsesAPIVisionContent(result)
			if got != tt.expected {
				t.Errorf("isResponsesAPIVisionContent(%s) = %v, want %v", tt.name, got, tt.expected)
			}
		})
	}
}

func TestApplyCopilotHeaders_XInitiator(t *testing.T) {
	tests := []struct {
		name          string
		payload       string
		expectedValue string
	}{
		{
			name:          "user only - should be user",
			payload:       `{"messages": [{"role": "user", "content": "hello"}]}`,
			expectedValue: "user",
		},
		{
			name:          "with assistant - should be agent",
			payload:       `{"messages": [{"role": "user", "content": "hi"}, {"role": "assistant", "content": "hello"}]}`,
			expectedValue: "agent",
		},
		{
			name:          "with tool_calls - should be agent",
			payload:       `{"messages": [{"role": "user", "content": "hi"}, {"role": "assistant", "tool_calls": [{"id": "1"}]}]}`,
			expectedValue: "agent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{}
			e := NewCopilotExecutor(cfg)
			req := httptest.NewRequest(http.MethodPost, "/", nil)

			e.applyCopilotHeaders(req, "test-token", []byte(tt.payload))

			got := req.Header.Get("X-Initiator")
			if got != tt.expectedValue {
				t.Errorf("X-Initiator = %q, want %q", got, tt.expectedValue)
			}
		})
	}
}

func TestApplyCopilotHeaders_Vision(t *testing.T) {
	tests := []struct {
		name          string
		payload       string
		expectedValue string
	}{
		{
			name:          "no images",
			payload:       `{"messages": [{"role": "user", "content": "hello"}]}`,
			expectedValue: "",
		},
		{
			name:          "with image_url",
			payload:       `{"messages": [{"role": "user", "content": [{"type": "image_url", "image_url": {"url": "data:image/png;base64,..."}}]}]}`,
			expectedValue: "true",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{}
			e := NewCopilotExecutor(cfg)
			req := httptest.NewRequest(http.MethodPost, "/", nil)

			e.applyCopilotHeaders(req, "test-token", []byte(tt.payload))

			got := req.Header.Get("Copilot-Vision-Request")
			if got != tt.expectedValue {
				t.Errorf("Copilot-Vision-Request = %q, want %q", got, tt.expectedValue)
			}
		})
	}
}
