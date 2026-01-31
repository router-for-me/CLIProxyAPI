package gemini

import (
	"context"
	"testing"
)

func TestRestoreUsageMetadata(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		expected string
	}{
		{
			name:     "cpaUsageMetadata renamed to usageMetadata",
			input:    []byte(`{"modelVersion":"gemini-3-pro","cpaUsageMetadata":{"promptTokenCount":100,"candidatesTokenCount":200}}`),
			expected: `{"modelVersion":"gemini-3-pro","usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":200}}`,
		},
		{
			name:     "no cpaUsageMetadata unchanged",
			input:    []byte(`{"modelVersion":"gemini-3-pro","usageMetadata":{"promptTokenCount":100}}`),
			expected: `{"modelVersion":"gemini-3-pro","usageMetadata":{"promptTokenCount":100}}`,
		},
		{
			name:     "empty input",
			input:    []byte(`{}`),
			expected: `{}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := restoreUsageMetadata(tt.input)
			if string(result) != tt.expected {
				t.Errorf("restoreUsageMetadata() = %s, want %s", string(result), tt.expected)
			}
		})
	}
}

func TestConvertAntigravityResponseToGeminiNonStream(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		expected string
	}{
		{
			name:     "cpaUsageMetadata restored in response",
			input:    []byte(`{"response":{"modelVersion":"gemini-3-pro","cpaUsageMetadata":{"promptTokenCount":100}}}`),
			expected: `{"modelVersion":"gemini-3-pro","usageMetadata":{"promptTokenCount":100}}`,
		},
		{
			name:     "usageMetadata preserved",
			input:    []byte(`{"response":{"modelVersion":"gemini-3-pro","usageMetadata":{"promptTokenCount":100}}}`),
			expected: `{"modelVersion":"gemini-3-pro","usageMetadata":{"promptTokenCount":100}}`,
		},
		{
			name:     "no response wrapper with candidates",
			input:    []byte(`{"candidates":[{"content":{"parts":[{"text":"hello"}]}}],"modelVersion":"gemini-3-flash"}`),
			expected: `{"candidates":[{"content":{"parts":[{"text":"hello"}]}}],"modelVersion":"gemini-3-flash"}`,
		},
		{
			name:     "no response wrapper with cpaUsageMetadata",
			input:    []byte(`{"candidates":[{"content":{"parts":[{"text":"hello"}]}}],"cpaUsageMetadata":{"promptTokenCount":50}}`),
			expected: `{"candidates":[{"content":{"parts":[{"text":"hello"}]}}],"usageMetadata":{"promptTokenCount":50}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ConvertAntigravityResponseToGeminiNonStream(context.Background(), "", nil, nil, tt.input, nil)
			if result != tt.expected {
				t.Errorf("ConvertAntigravityResponseToGeminiNonStream() = %s, want %s", result, tt.expected)
			}
		})
	}
}

func TestConvertAntigravityResponseToGeminiStream(t *testing.T) {
	ctx := context.WithValue(context.Background(), "alt", "")

	tests := []struct {
		name     string
		input    []byte
		expected string
	}{
		{
			name:     "cpaUsageMetadata restored in streaming response",
			input:    []byte(`data: {"response":{"modelVersion":"gemini-3-pro","cpaUsageMetadata":{"promptTokenCount":100}}}`),
			expected: `{"modelVersion":"gemini-3-pro","usageMetadata":{"promptTokenCount":100}}`,
		},
		{
			name:     "no response wrapper streaming",
			input:    []byte(`data: {"candidates":[{"content":{"parts":[{"text":"hi"}]}}],"modelVersion":"gemini-3-flash"}`),
			expected: `{"candidates":[{"content":{"parts":[{"text":"hi"}]}}],"modelVersion":"gemini-3-flash"}`,
		},
		{
			name:     "no response wrapper streaming with cpaUsageMetadata",
			input:    []byte(`{"candidates":[{"content":{"parts":[{"text":"hi"}]}}],"cpaUsageMetadata":{"promptTokenCount":25}}`),
			expected: `{"candidates":[{"content":{"parts":[{"text":"hi"}]}}],"usageMetadata":{"promptTokenCount":25}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := ConvertAntigravityResponseToGemini(ctx, "", nil, nil, tt.input, nil)
			if len(results) != 1 {
				t.Fatalf("expected 1 result, got %d", len(results))
			}
			if results[0] != tt.expected {
				t.Errorf("ConvertAntigravityResponseToGemini() = %s, want %s", results[0], tt.expected)
			}
		})
	}
}

func TestConvertAntigravityResponseToGeminiStreamSSE(t *testing.T) {
	ctx := context.WithValue(context.Background(), "alt", "sse")

	tests := []struct {
		name     string
		input    []byte
		expected string
	}{
		{
			name:     "SSE single object with response wrapper",
			input:    []byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"hello"}]}}],"modelVersion":"gemini-3-flash"},"traceId":"abc123"}`),
			expected: `{"candidates":[{"content":{"parts":[{"text":"hello"}]}}],"modelVersion":"gemini-3-flash"}`,
		},
		{
			name:     "SSE single object without response wrapper",
			input:    []byte(`data: {"candidates":[{"content":{"parts":[{"text":"hello"}]}}],"modelVersion":"gemini-3-flash"}`),
			expected: `{"candidates":[{"content":{"parts":[{"text":"hello"}]}}],"modelVersion":"gemini-3-flash"}`,
		},
		{
			name:     "SSE with cpaUsageMetadata restored",
			input:    []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"hi"}]}}],"cpaUsageMetadata":{"promptTokenCount":50}}}`),
			expected: `{"candidates":[{"content":{"parts":[{"text":"hi"}]}}],"usageMetadata":{"promptTokenCount":50}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := ConvertAntigravityResponseToGemini(ctx, "", nil, nil, tt.input, nil)
			if len(results) != 1 {
				t.Fatalf("expected 1 result, got %d", len(results))
			}
			if results[0] != tt.expected {
				t.Errorf("ConvertAntigravityResponseToGemini() = %s, want %s", results[0], tt.expected)
			}
		})
	}
}
