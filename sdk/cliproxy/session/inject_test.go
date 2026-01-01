package session

import (
	"encoding/json"
	"testing"
	"time"
)

func TestInjectHistory(t *testing.T) {
	t.Run("returns original request when session is nil", func(t *testing.T) {
		reqJSON := []byte(`{"messages":[{"role":"user","content":"Hello"}]}`)

		result, err := InjectHistory(reqJSON, nil)
		if err != nil {
			t.Fatalf("InjectHistory() error = %v", err)
		}
		if string(result) != string(reqJSON) {
			t.Error("should return original request unchanged")
		}
	})

	t.Run("returns original request when session has no messages", func(t *testing.T) {
		reqJSON := []byte(`{"messages":[{"role":"user","content":"Hello"}]}`)
		session := &Session{Messages: []Message{}}

		result, err := InjectHistory(reqJSON, session)
		if err != nil {
			t.Fatalf("InjectHistory() error = %v", err)
		}
		if string(result) != string(reqJSON) {
			t.Error("should return original request unchanged")
		}
	})

	t.Run("prepends history messages to request", func(t *testing.T) {
		reqJSON := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"New message"}]}`)
		session := &Session{
			Messages: []Message{
				{Role: "user", Content: "First message"},
				{Role: "assistant", Content: "First response"},
			},
		}

		result, err := InjectHistory(reqJSON, session)
		if err != nil {
			t.Fatalf("InjectHistory() error = %v", err)
		}

		var parsed map[string]interface{}
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to parse result: %v", err)
		}

		messages, ok := parsed["messages"].([]interface{})
		if !ok {
			t.Fatal("messages field not found or wrong type")
		}

		if len(messages) != 3 {
			t.Fatalf("expected 3 messages, got %d", len(messages))
		}

		// Check first message is from history
		first := messages[0].(map[string]interface{})
		if first["content"] != "First message" {
			t.Error("first message should be from history")
		}

		// Check last message is the new one
		last := messages[2].(map[string]interface{})
		if last["content"] != "New message" {
			t.Error("last message should be the new one")
		}
	})

	t.Run("preserves model and other fields", func(t *testing.T) {
		reqJSON := []byte(`{"model":"gpt-4","temperature":0.7,"messages":[{"role":"user","content":"Hi"}]}`)
		session := &Session{
			Messages: []Message{
				{Role: "system", Content: "You are helpful"},
			},
		}

		result, err := InjectHistory(reqJSON, session)
		if err != nil {
			t.Fatalf("InjectHistory() error = %v", err)
		}

		var parsed map[string]interface{}
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to parse result: %v", err)
		}

		if parsed["model"] != "gpt-4" {
			t.Error("model field should be preserved")
		}
		if parsed["temperature"] != 0.7 {
			t.Error("temperature field should be preserved")
		}
	})

	t.Run("returns original when no messages field", func(t *testing.T) {
		// Completion request without messages array
		reqJSON := []byte(`{"model":"text-davinci-003","prompt":"Hello"}`)
		session := &Session{
			Messages: []Message{
				{Role: "user", Content: "Old message"},
			},
		}

		result, err := InjectHistory(reqJSON, session)
		if err != nil {
			t.Fatalf("InjectHistory() error = %v", err)
		}
		if string(result) != string(reqJSON) {
			t.Error("should return original request when no messages field")
		}
	})

	t.Run("handles invalid JSON", func(t *testing.T) {
		reqJSON := []byte(`{invalid json}`)
		session := &Session{
			Messages: []Message{
				{Role: "user", Content: "test"},
			},
		}

		_, err := InjectHistory(reqJSON, session)
		if err == nil {
			t.Error("should return error for invalid JSON")
		}
	})
}

func TestExtractAssistantMessage(t *testing.T) {
	// Set up a fixed time for testing
	fixedTime := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	originalTimeNow := timeNow
	timeNow = func() time.Time { return fixedTime }
	t.Cleanup(func() { timeNow = originalTimeNow })

	t.Run("extracts OpenAI format message", func(t *testing.T) {
		respJSON := []byte(`{
			"choices": [
				{
					"message": {
						"role": "assistant",
						"content": "Hello! How can I help?"
					}
				}
			],
			"usage": {
				"completion_tokens": 10
			}
		}`)

		msg, err := ExtractAssistantMessage(respJSON, "openai", "gpt-4")
		if err != nil {
			t.Fatalf("ExtractAssistantMessage() error = %v", err)
		}
		if msg == nil {
			t.Fatal("expected message, got nil")
		}
		if msg.Role != "assistant" {
			t.Errorf("role = %v, want assistant", msg.Role)
		}
		if msg.Content != "Hello! How can I help?" {
			t.Errorf("content mismatch: %v", msg.Content)
		}
		if msg.Provider != "openai" {
			t.Errorf("provider = %v, want openai", msg.Provider)
		}
		if msg.Model != "gpt-4" {
			t.Errorf("model = %v, want gpt-4", msg.Model)
		}
		if msg.TokensUsed != 10 {
			t.Errorf("tokens = %d, want 10", msg.TokensUsed)
		}
	})

	t.Run("extracts Claude format message", func(t *testing.T) {
		respJSON := []byte(`{
			"content": [
				{"type": "text", "text": "Hello from Claude!"}
			],
			"usage": {
				"output_tokens": 15
			}
		}`)

		msg, err := ExtractAssistantMessage(respJSON, "claude", "claude-3-opus")
		if err != nil {
			t.Fatalf("ExtractAssistantMessage() error = %v", err)
		}
		if msg == nil {
			t.Fatal("expected message, got nil")
		}
		if msg.Role != "assistant" {
			t.Errorf("role = %v, want assistant", msg.Role)
		}
		if msg.Provider != "claude" {
			t.Errorf("provider = %v, want claude", msg.Provider)
		}
		if msg.TokensUsed != 15 {
			t.Errorf("tokens = %d, want 15", msg.TokensUsed)
		}
	})

	t.Run("extracts Gemini format message", func(t *testing.T) {
		respJSON := []byte(`{
			"candidates": [
				{
					"content": {
						"parts": [
							{"text": "Hello from Gemini!"}
						]
					}
				}
			],
			"usageMetadata": {
				"candidatesTokenCount": 20
			}
		}`)

		msg, err := ExtractAssistantMessage(respJSON, "gemini", "gemini-1.5-pro")
		if err != nil {
			t.Fatalf("ExtractAssistantMessage() error = %v", err)
		}
		if msg == nil {
			t.Fatal("expected message, got nil")
		}
		if msg.Role != "assistant" {
			t.Errorf("role = %v, want assistant", msg.Role)
		}
		if msg.Content != "Hello from Gemini!" {
			t.Errorf("content = %v, want 'Hello from Gemini!'", msg.Content)
		}
		if msg.Provider != "gemini" {
			t.Errorf("provider = %v, want gemini", msg.Provider)
		}
		if msg.TokensUsed != 20 {
			t.Errorf("tokens = %d, want 20", msg.TokensUsed)
		}
	})

	t.Run("returns nil for empty response", func(t *testing.T) {
		msg, err := ExtractAssistantMessage([]byte{}, "openai", "gpt-4")
		if err != nil {
			t.Fatalf("ExtractAssistantMessage() error = %v", err)
		}
		if msg != nil {
			t.Error("expected nil for empty response")
		}
	})

	t.Run("returns nil for unrecognized format", func(t *testing.T) {
		respJSON := []byte(`{"unknown": "format"}`)

		msg, err := ExtractAssistantMessage(respJSON, "unknown", "unknown-model")
		if err != nil {
			t.Fatalf("ExtractAssistantMessage() error = %v", err)
		}
		if msg != nil {
			t.Error("expected nil for unrecognized format")
		}
	})

	t.Run("handles invalid JSON", func(t *testing.T) {
		_, err := ExtractAssistantMessage([]byte(`{invalid}`), "openai", "gpt-4")
		if err == nil {
			t.Error("should return error for invalid JSON")
		}
	})

	t.Run("defaults role to assistant when not specified", func(t *testing.T) {
		respJSON := []byte(`{
			"choices": [
				{
					"message": {
						"content": "Response without role"
					}
				}
			]
		}`)

		msg, err := ExtractAssistantMessage(respJSON, "openai", "gpt-4")
		if err != nil {
			t.Fatalf("ExtractAssistantMessage() error = %v", err)
		}
		if msg == nil {
			t.Fatal("expected message")
		}
		if msg.Role != "assistant" {
			t.Errorf("role = %v, want assistant (default)", msg.Role)
		}
	})
}

func TestExtractTokenUsage(t *testing.T) {
	tests := []struct {
		name     string
		response string
		expected int
	}{
		{
			name:     "OpenAI completion_tokens",
			response: `{"usage": {"completion_tokens": 42}}`,
			expected: 42,
		},
		{
			name:     "Claude output_tokens",
			response: `{"usage": {"output_tokens": 33}}`,
			expected: 33,
		},
		{
			name:     "Gemini candidatesTokenCount",
			response: `{"usageMetadata": {"candidatesTokenCount": 55}}`,
			expected: 55,
		},
		{
			name:     "no usage data",
			response: `{"content": "test"}`,
			expected: 0,
		},
		{
			name:     "empty usage object",
			response: `{"usage": {}}`,
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resp map[string]interface{}
			if err := json.Unmarshal([]byte(tt.response), &resp); err != nil {
				t.Fatalf("failed to parse test data: %v", err)
			}

			result := extractTokenUsage(resp)
			if result != tt.expected {
				t.Errorf("extractTokenUsage() = %d, want %d", result, tt.expected)
			}
		})
	}
}

func TestExtractMessageFromMap(t *testing.T) {
	// Set up a fixed time for testing
	fixedTime := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	originalTimeNow := timeNow
	timeNow = func() time.Time { return fixedTime }
	t.Cleanup(func() { timeNow = originalTimeNow })

	t.Run("extracts all fields", func(t *testing.T) {
		msgMap := map[string]interface{}{
			"role":    "assistant",
			"content": "Test content",
		}
		fullResp := map[string]interface{}{
			"usage": map[string]interface{}{
				"completion_tokens": float64(25),
			},
		}

		msg, err := extractMessageFromMap(msgMap, "openai", "gpt-4", fullResp)
		if err != nil {
			t.Fatalf("extractMessageFromMap() error = %v", err)
		}
		if msg.Role != "assistant" {
			t.Errorf("role = %v, want assistant", msg.Role)
		}
		if msg.Content != "Test content" {
			t.Errorf("content = %v, want 'Test content'", msg.Content)
		}
		if msg.Provider != "openai" {
			t.Errorf("provider = %v, want openai", msg.Provider)
		}
		if msg.Model != "gpt-4" {
			t.Errorf("model = %v, want gpt-4", msg.Model)
		}
		if msg.TokensUsed != 25 {
			t.Errorf("tokens = %d, want 25", msg.TokensUsed)
		}
		if !msg.Timestamp.Equal(fixedTime) {
			t.Errorf("timestamp = %v, want %v", msg.Timestamp, fixedTime)
		}
	})

	t.Run("defaults empty role to assistant", func(t *testing.T) {
		msgMap := map[string]interface{}{
			"content": "No role specified",
		}

		msg, err := extractMessageFromMap(msgMap, "openai", "gpt-4", nil)
		if err != nil {
			t.Fatalf("extractMessageFromMap() error = %v", err)
		}
		if msg.Role != "assistant" {
			t.Errorf("role = %v, want assistant (default)", msg.Role)
		}
	})
}
