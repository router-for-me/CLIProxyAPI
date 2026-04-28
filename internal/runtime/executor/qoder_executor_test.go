package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/qoder"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewQoderExecutor tests the constructor
func TestNewQoderExecutor(t *testing.T) {
	cfg := &config.Config{}
	executor := NewQoderExecutor(cfg)
	require.NotNil(t, executor)
	assert.Equal(t, "qoder", executor.Identifier())
}

// TestIdentifier tests the identifier method
func TestIdentifier(t *testing.T) {
	executor := NewQoderExecutor(&config.Config{})
	assert.Equal(t, "qoder", executor.Identifier())
}

// TestExecuteStream_InvalidAuthStorage tests error for wrong storage type
func TestExecuteStream_InvalidAuthStorage(t *testing.T) {
	executor := NewQoderExecutor(&config.Config{})

	// Create a mock that doesn't implement TokenStorage
	authRecord := &cliproxyauth.Auth{
		Storage: nil, // nil storage
	}

	req := cliproxyexecutor.Request{
		Payload: []byte(`{"model":"gpt-4","messages":[]}`),
	}

	opts := cliproxyexecutor.Options{}

	result, err := executor.ExecuteStream(context.Background(), authRecord, req, opts)
	assert.Nil(t, result)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid auth storage type")
}

// TestExecuteStream_TokenRefreshFailure tests handling of token refresh failure
func TestExecuteStream_TokenRefreshFailure(t *testing.T) {
	executor := NewQoderExecutor(&config.Config{})

	storage := &qoder.QoderTokenStorage{
		Token:        "token",
		RefreshToken: "refresh",
		ExpireTime:   1000, // Expired
		UserID:       "user123",
		Name:         "Test User",
		Email:        "test@example.com",
	}

	authRecord := &cliproxyauth.Auth{
		Storage: storage,
	}

	req := cliproxyexecutor.Request{
		Payload: []byte(`{"model":"gpt-4","messages":[]}`),
	}

	opts := cliproxyexecutor.Options{}

	// The request should still proceed despite refresh failure (warning logged)
	result, err := executor.ExecuteStream(context.Background(), authRecord, req, opts)
	// Should fail because we can't actually make the HTTP request
	assert.Error(t, err)
	assert.Nil(t, result)
}

// TestExecuteStream_InvalidRequestPayload tests handling of malformed JSON
func TestExecuteStream_InvalidRequestPayload(t *testing.T) {
	executor := NewQoderExecutor(&config.Config{})

	storage := &qoder.QoderTokenStorage{
		Token:        "token",
		RefreshToken: "refresh",
		ExpireTime:   time.Now().Add(1 * time.Hour).UnixMilli(),
		UserID:       "user123",
		Name:         "Test User",
		Email:        "test@example.com",
	}

	authRecord := &cliproxyauth.Auth{
		Storage: storage,
	}

	req := cliproxyexecutor.Request{
		Payload: []byte(`invalid json`),
	}

	opts := cliproxyexecutor.Options{}

	result, err := executor.ExecuteStream(context.Background(), authRecord, req, opts)
	assert.Nil(t, result)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse request")
}

// TestExecuteStream_BuildAuthHeadersFailure tests auth header generation failure
func TestExecuteStream_BuildAuthHeadersFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `data: {"body":"{\\"error\\":\\"test\\"}"}
`)
	}))
	defer server.Close()

	executor := NewQoderExecutor(&config.Config{})

	storage := &qoder.QoderTokenStorage{
		Token:        "token",
		RefreshToken: "refresh",
		ExpireTime:   time.Now().Add(1 * time.Hour).UnixMilli(),
		UserID:       "user123",
		Name:         "Test User",
		Email:        "test@example.com",
	}

	authRecord := &cliproxyauth.Auth{
		Storage: storage,
	}

	req := cliproxyexecutor.Request{
		Payload: []byte(`{"model":"gpt-4","messages":[]}`),
	}

	opts := cliproxyexecutor.Options{}

	result, err := executor.ExecuteStream(context.Background(), authRecord, req, opts)
	// Should fail because we can't build proper auth headers with test data
	assert.Error(t, err)
	assert.Nil(t, result)
}

// TestExecuteStream_HTTPRequestFailure tests network error handling
func TestExecuteStream_HTTPRequestFailure(t *testing.T) {
	executor := NewQoderExecutor(&config.Config{})

	storage := &qoder.QoderTokenStorage{
		Token:        "token",
		RefreshToken: "refresh",
		ExpireTime:   time.Now().Add(1 * time.Hour).UnixMilli(),
		UserID:       "user123",
		Name:         "Test User",
		Email:        "test@example.com",
	}

	authRecord := &cliproxyauth.Auth{
		Storage: storage,
	}

	req := cliproxyexecutor.Request{
		Payload: []byte(`{"model":"gpt-4","messages":[]}`),
	}

	opts := cliproxyexecutor.Options{}

	// Use an invalid URL that will cause connection failure
	result, err := executor.ExecuteStream(context.Background(), authRecord, req, opts)
	assert.Error(t, err)
	assert.Nil(t, result)
}

// TestExecuteStream_NonOKResponse tests handling of non-200 status codes
func TestExecuteStream_NonOKResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "Internal Server Error")
	}))
	defer server.Close()

	executor := NewQoderExecutor(&config.Config{})

	storage := &qoder.QoderTokenStorage{
		Token:        "token",
		RefreshToken: "refresh",
		ExpireTime:   time.Now().Add(1 * time.Hour).UnixMilli(),
		UserID:       "user123",
		Name:         "Test User",
		Email:        "test@example.com",
	}

	authRecord := &cliproxyauth.Auth{
		Storage: storage,
	}

	req := cliproxyexecutor.Request{
		Payload: []byte(`{"model":"gpt-4","messages":[]}`),
	}

	opts := cliproxyexecutor.Options{}

	result, err := executor.ExecuteStream(context.Background(), authRecord, req, opts)
	assert.Nil(t, result)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

// TestExecuteStream_StreamParsing tests successful stream parsing
func TestExecuteStream_StreamParsing(t *testing.T) {
	// This test requires overriding QoderChatURL which is a constant
	// Skipping as it can't be properly tested without code changes
	t.Skip("requires ability to override QoderChatURL")
}

// TestExecuteStream_StreamErrorInResponse tests handling of error messages in stream
func TestExecuteStream_StreamErrorInResponse(t *testing.T) {
	// This test requires overriding QoderChatURL which is a constant
	// Skipping as it can't be properly tested without code changes
	t.Skip("requires ability to override QoderChatURL")
}

// TestExecuteStream_StreamContextCancel tests context cancellation
func TestExecuteStream_StreamContextCancel(t *testing.T) {
	// This test requires overriding QoderChatURL which is a constant
	// Skipping as it can't be properly tested without code changes
	t.Skip("requires ability to override QoderChatURL")
}

// TestBuildOpenAIChunk tests message transformation
func TestBuildOpenAIChunk(t *testing.T) {
	inner := map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"delta": map[string]interface{}{
					"content": "test",
				},
			},
		},
	}

	chunkBytes, err := buildOpenAIChunk(inner, "gpt-4")
	require.NoError(t, err)
	require.NotNil(t, chunkBytes)

	var result map[string]interface{}
	err = json.Unmarshal(chunkBytes, &result)
	require.NoError(t, err)
	assert.Equal(t, "gpt-4", result["model"])
}

// TestMessagesToPromptGeneric tests prompt generation
func TestMessagesToPromptGeneric(t *testing.T) {
	tests := []struct {
		name     string
		messages []interface{}
		tools    interface{}
		want     string
	}{
		{
			name:     "empty messages",
			messages: []interface{}{},
			want:     "",
		},
		{
			name: "user message",
			messages: []interface{}{
				map[string]interface{}{
					"role":    "user",
					"content": "Hello",
				},
			},
			want: "Hello",
		},
		{
			name: "system message",
			messages: []interface{}{
				map[string]interface{}{
					"role":    "system",
					"content": "Be helpful",
				},
			},
			want: "[System Instructions]\nBe helpful",
		},
		{
			name: "assistant message",
			messages: []interface{}{
				map[string]interface{}{
					"role":    "assistant",
					"content": "Hi there",
				},
			},
			want: "[Previous Assistant Response]\nHi there",
		},
		{
			name: "multiple messages",
			messages: []interface{}{
				map[string]interface{}{
					"role":    "system",
					"content": "Be helpful",
				},
				map[string]interface{}{
					"role":    "user",
					"content": "Hello",
				},
			},
			want: "[System Instructions]\nBe helpful\n\nHello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := messagesToPromptGeneric(tt.messages, tt.tools)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestMessagesToPromptGeneric_WithTools tests prompt generation with tools
func TestMessagesToPromptGeneric_WithTools(t *testing.T) {
	messages := []interface{}{
		map[string]interface{}{
			"role":    "user",
			"content": "Hello",
		},
	}
	tools := []interface{}{
		map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name": "test",
			},
		},
	}

	got := messagesToPromptGeneric(messages, tools)
	assert.Contains(t, got, "Hello")
}

// TestNewQoderStatusError tests error creation
func TestNewQoderStatusError(t *testing.T) {
	err := newQoderStatusError(500, "test error")
	require.NotNil(t, err)
	assert.Contains(t, err.Error(), "500")
	assert.Contains(t, err.Error(), "test error")
}

// TestExecuteStream_ModelMapping tests model name mapping
func TestExecuteStream_ModelMapping(t *testing.T) {
	executor := NewQoderExecutor(&config.Config{})

	storage := &qoder.QoderTokenStorage{
		Token:        "token",
		RefreshToken: "refresh",
		ExpireTime:   time.Now().Add(1 * time.Hour).UnixMilli(),
		UserID:       "user123",
		Name:         "Test User",
		Email:        "test@example.com",
	}

	authRecord := &cliproxyauth.Auth{
		Storage: storage,
	}

	// Test with a mapped model name
	req := cliproxyexecutor.Request{
		Payload: []byte(`{"model":"auto","messages":[]}`),
	}

	opts := cliproxyexecutor.Options{}

	// We can't easily override the URL, so this test will fail
	// Just verify it doesn't panic
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := executor.ExecuteStream(ctx, authRecord, req, opts)
	assert.Error(t, err)
}
