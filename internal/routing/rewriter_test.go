package routing

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelRewriter_RewriteRequestBody(t *testing.T) {
	rewriter := NewModelRewriter()

	tests := []struct {
		name       string
		body       []byte
		newModel   string
		wantModel  string
		wantChange bool
	}{
		{
			name:       "rewrites model field in JSON body",
			body:       []byte(`{"model":"gpt-4.1","messages":[]}`),
			newModel:   "claude-local",
			wantModel:  "claude-local",
			wantChange: true,
		},
		{
			name:       "rewrites with empty body returns empty",
			body:       []byte{},
			newModel:   "gpt-4",
			wantModel:  "",
			wantChange: false,
		},
		{
			name:       "handles missing model field gracefully",
			body:       []byte(`{"messages":[{"role":"user"}]}`),
			newModel:   "gpt-4",
			wantModel:  "",
			wantChange: false,
		},
		{
			name:       "preserves other fields when rewriting",
			body:       []byte(`{"model":"old-model","temperature":0.7,"max_tokens":100}`),
			newModel:   "new-model",
			wantModel:  "new-model",
			wantChange: true,
		},
		{
			name:       "handles nested JSON structure",
			body:       []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}],"stream":true}`),
			newModel:   "claude-3-opus",
			wantModel:  "claude-3-opus",
			wantChange: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := rewriter.RewriteRequestBody(tt.body, tt.newModel)
			require.NoError(t, err)

			if tt.wantChange {
				assert.NotEqual(t, string(tt.body), string(result), "body should have been modified")
			}

			if tt.wantModel != "" {
				// Parse result and check model field
				model, _ := NewModelExtractor().Extract(result, nil)
				assert.Equal(t, tt.wantModel, model)
			}
		})
	}
}

func TestModelRewriter_WrapResponseWriter(t *testing.T) {
	rewriter := NewModelRewriter()

	t.Run("response writer wraps without error", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		wrapped, cleanup := rewriter.WrapResponseWriter(recorder, "gpt-4", "claude-local")
		require.NotNil(t, wrapped)
		require.NotNil(t, cleanup)
		defer cleanup()
	})

	t.Run("rewrites model in non-streaming response", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		wrapped, cleanup := rewriter.WrapResponseWriter(recorder, "gpt-4", "claude-local")

		// Write a response with the resolved model
		response := []byte(`{"model":"claude-local","content":"hello"}`)
		wrapped.Header().Set("Content-Type", "application/json")
		_, err := wrapped.Write(response)
		require.NoError(t, err)

		// Cleanup triggers the rewrite
		cleanup()

		// Check the response was rewritten to the requested model
		body := recorder.Body.Bytes()
		assert.Contains(t, string(body), `"model":"gpt-4"`)
		assert.NotContains(t, string(body), `"model":"claude-local"`)
	})

	t.Run("no-op when requested equals resolved", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		wrapped, cleanup := rewriter.WrapResponseWriter(recorder, "gpt-4", "gpt-4")

		response := []byte(`{"model":"gpt-4","content":"hello"}`)
		wrapped.Header().Set("Content-Type", "application/json")
		_, err := wrapped.Write(response)
		require.NoError(t, err)

		cleanup()

		body := recorder.Body.Bytes()
		assert.Contains(t, string(body), `"model":"gpt-4"`)
	})

	t.Run("rewrites modelVersion field", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		wrapped, cleanup := rewriter.WrapResponseWriter(recorder, "gpt-4", "claude-local")

		response := []byte(`{"modelVersion":"claude-local","content":"hello"}`)
		wrapped.Header().Set("Content-Type", "application/json")
		_, err := wrapped.Write(response)
		require.NoError(t, err)

		cleanup()

		body := recorder.Body.Bytes()
		assert.Contains(t, string(body), `"modelVersion":"gpt-4"`)
	})

	t.Run("handles streaming responses", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		wrapped, cleanup := rewriter.WrapResponseWriter(recorder, "gpt-4", "claude-local")

		// Set streaming content type
		wrapped.Header().Set("Content-Type", "text/event-stream")

		// Write SSE chunks with resolved model
		chunk1 := []byte("data: {\"model\":\"claude-local\",\"delta\":\"hello\"}\n\n")
		_, err := wrapped.Write(chunk1)
		require.NoError(t, err)

		chunk2 := []byte("data: {\"model\":\"claude-local\",\"delta\":\" world\"}\n\n")
		_, err = wrapped.Write(chunk2)
		require.NoError(t, err)

		cleanup()

		// For streaming, data is written immediately with rewrites
		body := recorder.Body.Bytes()
		assert.Contains(t, string(body), `"model":"gpt-4"`)
		assert.NotContains(t, string(body), `"model":"claude-local"`)
	})

	t.Run("empty body handled gracefully", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		wrapped, cleanup := rewriter.WrapResponseWriter(recorder, "gpt-4", "claude-local")

		wrapped.Header().Set("Content-Type", "application/json")
		// Don't write anything

		cleanup()

		body := recorder.Body.Bytes()
		assert.Empty(t, body)
	})

	t.Run("preserves other JSON fields", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		wrapped, cleanup := rewriter.WrapResponseWriter(recorder, "gpt-4", "claude-local")

		response := []byte(`{"model":"claude-local","temperature":0.7,"usage":{"prompt_tokens":10}}`)
		wrapped.Header().Set("Content-Type", "application/json")
		_, err := wrapped.Write(response)
		require.NoError(t, err)

		cleanup()

		body := recorder.Body.Bytes()
		assert.Contains(t, string(body), `"temperature":0.7`)
		assert.Contains(t, string(body), `"prompt_tokens":10`)
	})
}

func TestResponseRewriter_ImplementsInterfaces(t *testing.T) {
	rewriter := NewModelRewriter()
	recorder := httptest.NewRecorder()
	wrapped, cleanup := rewriter.WrapResponseWriter(recorder, "gpt-4", "claude-local")
	defer cleanup()

	// Should implement http.ResponseWriter
	assert.Implements(t, (*http.ResponseWriter)(nil), wrapped)

	// Should preserve header access
	wrapped.Header().Set("X-Custom", "value")
	assert.Equal(t, "value", recorder.Header().Get("X-Custom"))

	// Should write status
	wrapped.WriteHeader(http.StatusCreated)
	assert.Equal(t, http.StatusCreated, recorder.Code)
}

func TestResponseRewriter_Flush(t *testing.T) {
	t.Run("flush writes buffered content", func(t *testing.T) {
		rewriter := NewModelRewriter()
		recorder := httptest.NewRecorder()
		wrapped, cleanup := rewriter.WrapResponseWriter(recorder, "gpt-4", "claude-local")

		response := []byte(`{"model":"claude-local","content":"test"}`)
		wrapped.Header().Set("Content-Type", "application/json")
		wrapped.Write(response)

		// Before cleanup, response should be empty (buffered)
		assert.Empty(t, recorder.Body.Bytes())

		// After cleanup, response should be written
		cleanup()
		assert.NotEmpty(t, recorder.Body.Bytes())
	})

	t.Run("multiple flush calls are safe", func(t *testing.T) {
		rewriter := NewModelRewriter()
		recorder := httptest.NewRecorder()
		wrapped, cleanup := rewriter.WrapResponseWriter(recorder, "gpt-4", "claude-local")

		response := []byte(`{"model":"claude-local"}`)
		wrapped.Header().Set("Content-Type", "application/json")
		wrapped.Write(response)

		// First cleanup
		cleanup()
		firstBody := recorder.Body.Bytes()

		// Second cleanup should not write again
		cleanup()
		secondBody := recorder.Body.Bytes()

		assert.Equal(t, firstBody, secondBody)
	})
}

func TestResponseRewriter_StreamingWithDataLines(t *testing.T) {
	rewriter := NewModelRewriter()
	recorder := httptest.NewRecorder()
	wrapped, cleanup := rewriter.WrapResponseWriter(recorder, "gpt-4", "claude-local")

	wrapped.Header().Set("Content-Type", "text/event-stream")

	// SSE format with multiple data lines
	chunk := []byte("data: {\"model\":\"claude-local\"}\n\ndata: {\"model\":\"claude-local\",\"done\":true}\n\n")
	wrapped.Write(chunk)

	cleanup()

	body := recorder.Body.Bytes()
	// Both data lines should have model rewritten
	assert.Contains(t, string(body), `"model":"gpt-4"`)
	assert.NotContains(t, string(body), `"model":"claude-local"`)
}

func TestModelRewriter_RoundTrip(t *testing.T) {
	// Simulate a full request -> response cycle with model rewriting
	rewriter := NewModelRewriter()

	// Step 1: Rewrite request body
	originalRequest := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`)
	rewrittenRequest, err := rewriter.RewriteRequestBody(originalRequest, "claude-local")
	require.NoError(t, err)

	// Verify request was rewritten
	extractor := NewModelExtractor()
	requestModel, _ := extractor.Extract(rewrittenRequest, nil)
	assert.Equal(t, "claude-local", requestModel)

	// Step 2: Simulate response with resolved model
	recorder := httptest.NewRecorder()
	wrapped, cleanup := rewriter.WrapResponseWriter(recorder, "gpt-4", "claude-local")

	response := []byte(`{"model":"claude-local","content":"Hello! How can I help?"}`)
	wrapped.Header().Set("Content-Type", "application/json")
	wrapped.Write(response)
	cleanup()

	// Verify response was rewritten back
	body, _ := io.ReadAll(recorder.Result().Body)
	responseModel, _ := extractor.Extract(body, nil)
	assert.Equal(t, "gpt-4", responseModel)
}

func TestModelRewriter_NonJSONBody(t *testing.T) {
	rewriter := NewModelRewriter()

	// Binary/non-JSON body should be returned unchanged
	body := []byte{0x00, 0x01, 0x02, 0x03}
	result, err := rewriter.RewriteRequestBody(body, "gpt-4")
	require.NoError(t, err)
	assert.Equal(t, body, result)
}

func TestModelRewriter_InvalidJSON(t *testing.T) {
	rewriter := NewModelRewriter()

	// Invalid JSON without model field should be returned unchanged
	body := []byte(`not valid json`)
	result, err := rewriter.RewriteRequestBody(body, "gpt-4")
	require.NoError(t, err)
	assert.Equal(t, body, result)
}

func TestResponseRewriter_StatusCodePreserved(t *testing.T) {
	rewriter := NewModelRewriter()
	recorder := httptest.NewRecorder()
	wrapped, cleanup := rewriter.WrapResponseWriter(recorder, "gpt-4", "claude-local")

	wrapped.WriteHeader(http.StatusAccepted)
	wrapped.Write([]byte(`{"model":"claude-local"}`))
	cleanup()

	assert.Equal(t, http.StatusAccepted, recorder.Code)
}

func TestResponseRewriter_HeaderFlushed(t *testing.T) {
	rewriter := NewModelRewriter()
	recorder := httptest.NewRecorder()
	wrapped, cleanup := rewriter.WrapResponseWriter(recorder, "gpt-4", "claude-local")

	wrapped.Header().Set("Content-Type", "application/json")
	wrapped.Header().Set("X-Request-ID", "abc123")
	wrapped.Write([]byte(`{"model":"claude-local"}`))
	cleanup()

	result := recorder.Result()
	assert.Equal(t, "application/json", result.Header.Get("Content-Type"))
	assert.Equal(t, "abc123", result.Header.Get("X-Request-ID"))
}
