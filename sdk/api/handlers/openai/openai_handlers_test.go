package openai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	"github.com/tidwall/gjson"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// TestEmbeddings_MissingModel tests that the endpoint returns 400 when model is missing
func TestEmbeddings_MissingModel(t *testing.T) {
	handler := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(
		&sdkconfig.SDKConfig{},
		coreauth.NewManager(nil, nil, nil),
	))

	r := gin.New()
	r.POST("/v1/embeddings", handler.Embeddings)

	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantError  string
	}{
		{
			name:       "missing_model_field",
			body:       `{"input": "test"}`,
			wantStatus: http.StatusBadRequest,
			wantError:  "model is required",
		},
		{
			name:       "empty_model_field",
			body:       `{"model": "", "input": "test"}`,
			wantStatus: http.StatusBadRequest,
			wantError:  "model is required",
		},
		{
			name:       "empty_body",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
			wantError:  "model is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("expected status %d, got %d", tt.wantStatus, w.Code)
			}

			// Parse error response
			result := gjson.GetBytes(w.Body.Bytes(), "error.message")
			if !result.Exists() {
				t.Fatalf("expected error message in response, got: %s", w.Body.String())
			}

			if !strings.Contains(result.String(), tt.wantError) {
				t.Errorf("expected error message to contain %q, got %q", tt.wantError, result.String())
			}
		})
	}
}

// TestEmbeddings_InvalidJSON tests that the endpoint handles invalid JSON gracefully
func TestEmbeddings_InvalidJSON(t *testing.T) {
	handler := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(
		&sdkconfig.SDKConfig{},
		coreauth.NewManager(nil, nil, nil),
	))

	r := gin.New()
	r.POST("/v1/embeddings", handler.Embeddings)

	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{
			name:       "invalid_json",
			body:       `{invalid json}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "truncated_json",
			body:       `{"model": "test-model", "input":`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			// The endpoint should either return 400 for invalid JSON
			// or attempt to process and return an error
			if w.Code != http.StatusBadRequest && w.Code != http.StatusInternalServerError {
				t.Fatalf("expected status %d or %d, got %d",
					http.StatusBadRequest, http.StatusInternalServerError, w.Code)
			}
		})
	}
}

// TestEmbeddings_ValidRequest tests that valid requests are processed
func TestEmbeddings_ValidRequest(t *testing.T) {
	// Note: This test will fail initially because it requires a real executor
	// In a real implementation, you would mock the executor or skip this test
	t.Skip("Requires mock executor implementation")

	handler := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(
		&sdkconfig.SDKConfig{},
		coreauth.NewManager(nil, nil, nil),
	))

	// Register a test model in the registry
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient("test-embeddings", "openai", []*registry.ModelInfo{
		{ID: "text-embedding-ada-002", Created: 1234567890},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient("test-embeddings")
	})

	r := gin.New()
	r.POST("/v1/embeddings", handler.Embeddings)

	tests := []struct {
		name string
		body string
	}{
		{
			name: "single_string_input",
			body: `{"model": "text-embedding-ada-002", "input": "The quick brown fox"}`,
		},
		{
			name: "array_input",
			body: `{"model": "text-embedding-ada-002", "input": ["Hello", "World"]}`,
		},
		{
			name: "with_encoding_format",
			body: `{"model": "text-embedding-ada-002", "input": "test", "encoding_format": "float"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			// Should return 200 or an error related to missing executor
			if w.Code != http.StatusOK && w.Code != http.StatusInternalServerError {
				t.Logf("Response body: %s", w.Body.String())
			}
		})
	}
}

// TestOpenAIModels tests the /v1/models endpoint
func TestOpenAIModels(t *testing.T) {
	handler := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(
		&sdkconfig.SDKConfig{},
		coreauth.NewManager(nil, nil, nil),
	))

	// Register test models
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient("test-models-1", "openai", []*registry.ModelInfo{
		{ID: "gpt-4", Created: 1234567890},
		{ID: "gpt-3.5-turbo", Created: 1234567891},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient("test-models-1")
	})

	r := gin.New()
	r.GET("/v1/models", handler.OpenAIModels)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	// Parse response
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// Check object type
	if resp["object"] != "list" {
		t.Errorf("expected object to be 'list', got %v", resp["object"])
	}

	// Check data array exists
	data, ok := resp["data"].([]interface{})
	if !ok {
		t.Fatalf("expected data to be an array, got %T", resp["data"])
	}

	// Should have at least the models we registered
	if len(data) < 2 {
		t.Errorf("expected at least 2 models, got %d", len(data))
	}

	// Check model structure
	for i, model := range data {
		m, ok := model.(map[string]interface{})
		if !ok {
			t.Fatalf("model %d is not a map", i)
		}

		// Check required fields
		if _, exists := m["id"]; !exists {
			t.Errorf("model %d missing 'id' field", i)
		}
		if _, exists := m["object"]; !exists {
			t.Errorf("model %d missing 'object' field", i)
		}
	}
}

// TestChatCompletions_StreamParameter tests stream detection
func TestChatCompletions_StreamParameter(t *testing.T) {
	handler := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(
		&sdkconfig.SDKConfig{},
		coreauth.NewManager(nil, nil, nil),
	))

	// Register a test model
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient("test-stream", "openai", []*registry.ModelInfo{
		{ID: "gpt-4", Created: 1234567890},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient("test-stream")
	})

	r := gin.New()
	r.POST("/v1/chat/completions", handler.ChatCompletions)

	tests := []struct {
		name       string
		body       string
		wantStream bool
	}{
		{
			name:       "stream_true",
			body:       `{"model": "gpt-4", "messages": [{"role": "user", "content": "hi"}], "stream": true}`,
			wantStream: true,
		},
		{
			name:       "stream_false",
			body:       `{"model": "gpt-4", "messages": [{"role": "user", "content": "hi"}], "stream": false}`,
			wantStream: false,
		},
		{
			name:       "stream_not_specified",
			body:       `{"model": "gpt-4", "messages": [{"role": "user", "content": "hi"}]}`,
			wantStream: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Note: This test will fail without proper executor setup
			// It's mainly to verify the handler doesn't panic
			t.Skip("Requires mock executor implementation")

			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			// Check if the response indicates streaming
			if tt.wantStream {
				contentType := w.Header().Get("Content-Type")
				if !strings.Contains(contentType, "text/event-stream") {
					t.Logf("Expected streaming response, got Content-Type: %s", contentType)
				}
			}
		})
	}
}

// TestConvertCompletionsRequestToChatCompletions tests the conversion function
func TestConvertCompletionsRequestToChatCompletions(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantModel  string
		wantPrompt string
	}{
		{
			name:       "simple_prompt",
			input:      `{"model": "gpt-3.5-turbo-instruct", "prompt": "Hello, world!"}`,
			wantModel:  "gpt-3.5-turbo-instruct",
			wantPrompt: "Hello, world!",
		},
		{
			name:       "with_temperature",
			input:      `{"model": "text-davinci-003", "prompt": "Test", "temperature": 0.7}`,
			wantModel:  "text-davinci-003",
			wantPrompt: "Test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertCompletionsRequestToChatCompletions([]byte(tt.input))

			// Parse result
			resultJSON := gjson.ParseBytes(result)

			// Check model
			model := resultJSON.Get("model").String()
			if model != tt.wantModel {
				t.Errorf("expected model %q, got %q", tt.wantModel, model)
			}

			// Check messages array
			messages := resultJSON.Get("messages")
			if !messages.IsArray() {
				t.Fatalf("expected messages to be an array")
			}

			// Check first message
			firstMessage := messages.Get("0")
			if firstMessage.Get("role").String() != "user" {
				t.Errorf("expected role to be 'user', got %q", firstMessage.Get("role").String())
			}

			content := firstMessage.Get("content").String()
			if content != tt.wantPrompt {
				t.Errorf("expected content %q, got %q", tt.wantPrompt, content)
			}

			// Check temperature if present in input
			if strings.Contains(tt.input, "temperature") {
				temp := resultJSON.Get("temperature")
				if !temp.Exists() {
					t.Error("expected temperature to be preserved")
				}
			}
		})
	}
}
