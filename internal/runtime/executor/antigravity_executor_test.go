package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestSanitizeRequestContents(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		expectedCount  int
		shouldModify   bool
	}{
		{
			name: "valid contents unchanged",
			input: `{
				"request": {
					"contents": [
						{"role": "user", "parts": [{"text": "hello"}]},
						{"role": "model", "parts": [{"text": "hi"}]}
					]
				}
			}`,
			expectedCount: 2,
			shouldModify:  false,
		},
		{
			name: "removes entry with safetySettings",
			input: `{
				"request": {
					"contents": [
						{"role": "user", "parts": [{"text": "hello"}]},
						{"safetySettings": [], "model": "test"}
					]
				}
			}`,
			expectedCount: 1,
			shouldModify:  true,
		},
		{
			name: "removes entry with model field",
			input: `{
				"request": {
					"contents": [
						{"role": "user", "parts": [{"text": "hello"}]},
						{"model": "gemini-pro", "userAgent": "test"}
					]
				}
			}`,
			expectedCount: 1,
			shouldModify:  true,
		},
		{
			name: "removes entry with systemInstruction",
			input: `{
				"request": {
					"contents": [
						{"systemInstruction": {}, "toolConfig": {}}
					]
				}
			}`,
			expectedCount: 0,
			shouldModify:  true,
		},
		{
			name: "removes entry with request metadata fields",
			input: `{
				"request": {
					"contents": [
						{"role": "user", "parts": [{"text": "hello"}]},
						{"requestId": "123", "requestType": "agent", "sessionId": "456"}
					]
				}
			}`,
			expectedCount: 1,
			shouldModify:  true,
		},
		{
			name: "keeps function call/response entries",
			input: `{
				"request": {
					"contents": [
						{"role": "user", "parts": [{"text": "hello"}]},
						{"role": "model", "parts": [{"functionCall": {"name": "test", "args": {}}}]},
						{"role": "function", "parts": [{"functionResponse": {"name": "test", "response": {}}}]}
					]
				}
			}`,
			expectedCount: 3,
			shouldModify:  false,
		},
		{
			name:          "handles empty contents",
			input:         `{"request": {"contents": []}}`,
			expectedCount: 0,
			shouldModify:  false,
		},
		{
			name:          "handles missing contents",
			input:         `{"request": {}}`,
			expectedCount: -1,
			shouldModify:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeRequestContents([]byte(tt.input))
			
			contentsResult := gjson.GetBytes(result, "request.contents")
			
			if tt.expectedCount == -1 {
				if contentsResult.Exists() {
					t.Errorf("expected no contents field, but got one")
				}
				return
			}

			if !contentsResult.IsArray() {
				t.Fatalf("expected contents to be an array")
			}

			actualCount := len(contentsResult.Array())
			if actualCount != tt.expectedCount {
				t.Errorf("expected %d contents, got %d", tt.expectedCount, actualCount)
			}

			for i, content := range contentsResult.Array() {
				invalidFields := []string{"safetySettings", "model", "userAgent", "requestType", "requestId", "sessionId", "systemInstruction", "toolConfig", "generationConfig", "project", "request", "contents"}
				for _, field := range invalidFields {
					if content.Get(field).Exists() {
						t.Errorf("content[%d] should not have field %q", i, field)
					}
				}
			}
		})
	}
}
