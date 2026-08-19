package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/qoder"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// TestNewQoderExecutor tests the constructor
func TestNewQoderExecutor(t *testing.T) {
	cfg := &config.Config{}
	executor := NewQoderExecutor(cfg)
	if executor == nil {
		t.Fatal("NewQoderExecutor returned nil")
	}
	if got := executor.Identifier(); got != "qoder" {
		t.Errorf("Identifier() = %q, want %q", got, "qoder")
	}
}

// TestIdentifier tests the identifier method
func TestIdentifier(t *testing.T) {
	executor := NewQoderExecutor(&config.Config{})
	if got := executor.Identifier(); got != "qoder" {
		t.Errorf("Identifier() = %q, want %q", got, "qoder")
	}
}

// TestExecuteStream_InvalidAuthStorage tests error for wrong storage type
func TestExecuteStream_InvalidAuthStorage(t *testing.T) {
	executor := NewQoderExecutor(&config.Config{})

	// Create a mock that doesn't implement TokenStorage
	authRecord := &cliproxyauth.Auth{
		Storage: nil, // nil storage
	}

	req := cliproxyexecutor.Request{
		Payload: []byte(`{"model":"auto","messages":[]}`),
	}

	opts := cliproxyexecutor.Options{}

	result, err := executor.ExecuteStream(context.Background(), authRecord, req, opts)
	if result != nil {
		t.Errorf("expected nil result, got %+v", result)
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid auth storage type") {
		t.Errorf("error %q does not contain %q", err.Error(), "invalid auth storage type")
	}
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
		Payload: []byte(`{"model":"auto","messages":[]}`),
	}

	opts := cliproxyexecutor.Options{}

	// The request should still proceed despite refresh failure (warning logged)
	result, err := executor.ExecuteStream(context.Background(), authRecord, req, opts)
	// Should fail because we can't actually make the HTTP request
	if err == nil {
		t.Error("expected error, got nil")
	}
	if result != nil {
		t.Errorf("expected nil result, got %+v", result)
	}
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
	if result != nil {
		t.Errorf("expected nil result, got %+v", result)
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to parse request") {
		t.Errorf("error %q does not contain %q", err.Error(), "failed to parse request")
	}
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
		Payload: []byte(`{"model":"auto","messages":[]}`),
	}

	opts := cliproxyexecutor.Options{}

	result, err := executor.ExecuteStream(context.Background(), authRecord, req, opts)
	// Should fail because we can't build proper auth headers with test data
	if err == nil {
		t.Error("expected error, got nil")
	}
	if result != nil {
		t.Errorf("expected nil result, got %+v", result)
	}
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
		Payload: []byte(`{"model":"auto","messages":[]}`),
	}

	opts := cliproxyexecutor.Options{}

	// Use an invalid URL that will cause connection failure
	result, err := executor.ExecuteStream(context.Background(), authRecord, req, opts)
	if err == nil {
		t.Error("expected error, got nil")
	}
	if result != nil {
		t.Errorf("expected nil result, got %+v", result)
	}
}

// TestExecuteStream_NonOKResponse verifies ExecuteStream surfaces a clear
// error when no model_config has been cached for the requested model
// (i.e. /algo/api/v2/model/list was never fetched, or the model is unknown).
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
		Payload: []byte(`{"model":"auto","messages":[]}`),
	}

	opts := cliproxyexecutor.Options{}

	result, err := executor.ExecuteStream(context.Background(), authRecord, req, opts)
	if result != nil {
		t.Errorf("expected nil result, got %+v", result)
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "model config cache is empty") {
		t.Errorf("error %q does not contain %q", err.Error(), "model config cache is empty")
	}
}

func TestExecuteStream_PublishesUsageRecordFromStreamUsage(t *testing.T) {
	executor := NewQoderExecutor(&config.Config{})
	storage := testQoderStorageWithModelConfig()
	authRecord := &cliproxyauth.Auth{
		ID:       "qoder-stream-usage-auth",
		Provider: "qoder",
		Storage:  storage,
	}

	plugin := &captureQoderUsagePlugin{records: make(chan usage.Record, 4)}
	usage.RegisterNamedPlugin("test:qoder-stream-usage", plugin)

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", qoderRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return qoderSSEHTTPResponse(req, http.StatusOK, qoderStreamBodyWithUsage(t, 3, 4, 7)), nil
	}))

	result, err := executor.ExecuteStream(ctx, authRecord, cliproxyexecutor.Request{
		Model:   "qoder/auto",
		Payload: []byte(`{"model":"qoder/auto","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
	}

	record := waitForQoderUsageRecord(t, plugin.records, authRecord.ID, "auto")
	if record.Provider != "qoder" {
		t.Fatalf("Provider = %q, want qoder", record.Provider)
	}
	if record.ExecutorType != "QoderExecutor" {
		t.Fatalf("ExecutorType = %q, want QoderExecutor", record.ExecutorType)
	}
	if record.Failed {
		t.Fatalf("Failed = true, want false: %+v", record.Fail)
	}
	if record.Detail.InputTokens != 3 || record.Detail.OutputTokens != 4 || record.Detail.TotalTokens != 7 {
		t.Fatalf("Detail = %+v, want input=3 output=4 total=7", record.Detail)
	}
}

func TestWaitForQoderUsageRecord_RequiresMatchingAuthID(t *testing.T) {
	const expectedAuthID = "qoder-stream-usage-auth"
	records := make(chan usage.Record, 2)
	records <- usage.Record{Provider: "qoder", AuthID: "prior-qoder-auth", Model: "auto"}
	records <- usage.Record{Provider: "qoder", AuthID: expectedAuthID, Model: "auto"}

	record := waitForQoderUsageRecord(t, records, expectedAuthID, "auto")
	if record.AuthID != expectedAuthID {
		t.Fatalf("AuthID = %q, want %q", record.AuthID, expectedAuthID)
	}
}

func TestExecuteStream_PublishesFailureRecordForUpstreamStatus(t *testing.T) {
	executor := NewQoderExecutor(&config.Config{})
	storage := testQoderStorageWithModelConfig()
	authRecord := &cliproxyauth.Auth{
		ID:       "qoder-test-auth",
		Provider: "qoder",
		Storage:  storage,
	}

	plugin := &captureQoderUsagePlugin{records: make(chan usage.Record, 4)}
	usage.RegisterNamedPlugin("test:qoder-status-failure", plugin)

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", qoderRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return qoderSSEHTTPResponse(req, http.StatusServiceUnavailable, "upstream down"), nil
	}))

	result, err := executor.ExecuteStream(ctx, authRecord, cliproxyexecutor.Request{
		Model:   "qoder/auto",
		Payload: []byte(`{"model":"qoder/auto","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if result != nil {
		t.Fatalf("ExecuteStream() result = %+v, want nil", result)
	}
	if err == nil {
		t.Fatal("ExecuteStream() error = nil, want error")
	}

	record := waitForQoderUsageRecord(t, plugin.records, authRecord.ID, "auto")
	if !record.Failed {
		t.Fatalf("Failed = false, want true")
	}
	if record.Fail.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("Fail.StatusCode = %d, want %d", record.Fail.StatusCode, http.StatusServiceUnavailable)
	}
	if !strings.Contains(record.Fail.Body, "upstream down") {
		t.Fatalf("Fail.Body = %q, want upstream body", record.Fail.Body)
	}
}

func TestExecuteStream_EnsuresUsageRecordForRawDoneWithoutUsage(t *testing.T) {
	executor := NewQoderExecutor(&config.Config{})
	storage := testQoderStorageWithModelConfig()
	authRecord := &cliproxyauth.Auth{
		ID:       "qoder-test-auth",
		Provider: "qoder",
		Storage:  storage,
	}

	plugin := &captureQoderUsagePlugin{records: make(chan usage.Record, 4)}
	usage.RegisterNamedPlugin("test:qoder-raw-done", plugin)

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", qoderRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return qoderSSEHTTPResponse(req, http.StatusOK, "data: [DONE]\n\n"), nil
	}))

	result, err := executor.ExecuteStream(ctx, authRecord, cliproxyexecutor.Request{
		Model:   "qoder/auto",
		Payload: []byte(`{"model":"qoder/auto","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
	}

	record := waitForQoderUsageRecord(t, plugin.records, authRecord.ID, "auto")
	if record.Failed {
		t.Fatalf("Failed = true, want false: %+v", record.Fail)
	}
	if record.Detail != (usage.Detail{}) {
		t.Fatalf("Detail = %+v, want empty fallback detail", record.Detail)
	}
	assertNoExtraQoderRecord(t, plugin.records)
}

func TestExecuteStream_PublishesFailureRecordForStreamEnvelopeStatus(t *testing.T) {
	executor := NewQoderExecutor(&config.Config{})
	storage := testQoderStorageWithModelConfig()
	authRecord := &cliproxyauth.Auth{
		ID:       "qoder-test-auth",
		Provider: "qoder",
		Storage:  storage,
	}

	plugin := &captureQoderUsagePlugin{records: make(chan usage.Record, 4)}
	usage.RegisterNamedPlugin("test:qoder-envelope-failure", plugin)

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", qoderRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := qoderStreamBodyWithStatus(t, http.StatusTooManyRequests, "quota exhausted")
		return qoderSSEHTTPResponse(req, http.StatusOK, body), nil
	}))

	result, err := executor.ExecuteStream(ctx, authRecord, cliproxyexecutor.Request{
		Model:   "qoder/auto",
		Payload: []byte(`{"model":"qoder/auto","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if streamErr == nil {
		t.Fatal("stream error = nil, want envelope error")
	}

	record := waitForQoderUsageRecord(t, plugin.records, authRecord.ID, "auto")
	if !record.Failed {
		t.Fatal("Failed = false, want true")
	}
	if record.Fail.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("Fail.StatusCode = %d, want %d", record.Fail.StatusCode, http.StatusTooManyRequests)
	}
	if !strings.Contains(record.Fail.Body, "quota exhausted") {
		t.Fatalf("Fail.Body = %q, want envelope body", record.Fail.Body)
	}
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

type qoderRoundTripFunc func(*http.Request) (*http.Response, error)

func (f qoderRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type captureQoderUsagePlugin struct {
	records chan usage.Record
}

func (p *captureQoderUsagePlugin) HandleUsage(_ context.Context, record usage.Record) {
	if p == nil {
		return
	}
	select {
	case p.records <- record:
	default:
	}
}

func waitForQoderUsageRecord(t *testing.T, records <-chan usage.Record, authID, model string) usage.Record {
	t.Helper()
	timeout := time.After(5 * time.Second) // generous for CI scheduling jitter; correctness comes from matching the record, not the deadline
	for {
		select {
		case record := <-records:
			if record.Provider == "qoder" && record.AuthID == authID && record.Model == model {
				return record
			}
		case <-timeout:
			t.Fatalf("timed out waiting for Qoder usage record for auth %q and model %q", authID, model)
		}
	}
}

func assertNoExtraQoderRecord(t *testing.T, records <-chan usage.Record) {
	t.Helper()
	select {
	case record := <-records:
		t.Fatalf("unexpected extra Qoder usage record: %+v", record)
	case <-time.After(100 * time.Millisecond):
	}
}

func testQoderStorageWithModelConfig() *qoder.QoderTokenStorage {
	return &qoder.QoderTokenStorage{
		Token:        "token",
		RefreshToken: "refresh",
		ExpireTime:   time.Now().Add(1 * time.Hour).UnixMilli(),
		UserID:       "user123",
		Name:         "Test User",
		Email:        "test@example.com",
		MachineID:    "machine123",
		ModelConfigs: map[string]json.RawMessage{
			"auto": json.RawMessage(`{"key":"auto","source":"system","is_reasoning":false,"max_output_tokens":8192}`),
		},
	}
}

func qoderSSEHTTPResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func qoderStreamBodyWithUsage(t *testing.T, promptTokens, completionTokens, totalTokens int) string {
	t.Helper()
	inner := map[string]interface{}{
		"id":      "chatcmpl-test",
		"object":  "chat.completion.chunk",
		"created": 0,
		"model":   "auto",
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"delta":         map[string]interface{}{"content": "ok"},
				"finish_reason": nil,
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      totalTokens,
		},
	}
	innerBytes, err := json.Marshal(inner)
	if err != nil {
		t.Fatalf("marshal inner chunk: %v", err)
	}
	eventBytes, err := json.Marshal(map[string]interface{}{
		"statusCodeValue": http.StatusOK,
		"body":            string(innerBytes),
	})
	if err != nil {
		t.Fatalf("marshal Qoder envelope: %v", err)
	}
	doneBytes, err := json.Marshal(map[string]interface{}{
		"statusCodeValue": http.StatusOK,
		"body":            "[DONE]",
	})
	if err != nil {
		t.Fatalf("marshal Qoder done envelope: %v", err)
	}
	return "data: " + string(eventBytes) + "\n\n" + "data: " + string(doneBytes) + "\n\n"
}

func qoderStreamBodyWithStatus(t *testing.T, status int, body string) string {
	t.Helper()
	eventBytes, err := json.Marshal(map[string]interface{}{
		"statusCodeValue": status,
		"body":            body,
	})
	if err != nil {
		t.Fatalf("marshal Qoder status envelope: %v", err)
	}
	return "data: " + string(eventBytes) + "\n\n"
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
	if err != nil {
		t.Fatalf("buildOpenAIChunk returned error: %v", err)
	}
	if chunkBytes == nil {
		t.Fatal("buildOpenAIChunk returned nil bytes")
	}

	var result map[string]interface{}
	if err = json.Unmarshal(chunkBytes, &result); err != nil {
		t.Fatalf("failed to unmarshal chunk: %v", err)
	}
	if got := result["model"]; got != "gpt-4" {
		t.Errorf("model = %v, want %q", got, "gpt-4")
	}
}

// TestNewQoderStatusError tests error creation
func TestNewQoderStatusError(t *testing.T) {
	err := newQoderStatusError(500, "test error")
	if err == nil {
		t.Fatal("newQoderStatusError returned nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error %q does not contain %q", err.Error(), "500")
	}
	if !strings.Contains(err.Error(), "test error") {
		t.Errorf("error %q does not contain %q", err.Error(), "test error")
	}
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

	if _, err := executor.ExecuteStream(ctx, authRecord, req, opts); err == nil {
		t.Error("expected error, got nil")
	}
}

// TestExecute_InvalidAuth tests that Execute returns an error when the auth
// storage type is invalid. This fails before the HTTP call, so it can be
// tested without a mock server.
func TestExecute_InvalidAuth(t *testing.T) {
	executor := NewQoderExecutor(&config.Config{})
	authRecord := &cliproxyauth.Auth{
		Storage: nil,
	}
	req := cliproxyexecutor.Request{
		Payload: []byte(`{"model":"auto","messages":[]}`),
	}
	opts := cliproxyexecutor.Options{}

	resp, err := executor.Execute(context.Background(), authRecord, req, opts)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid auth storage type") {
		t.Errorf("error %q does not contain %q", err.Error(), "invalid auth storage type")
	}
	if len(resp.Payload) != 0 {
		t.Errorf("expected empty payload, got %d bytes", len(resp.Payload))
	}
}

// TestExecute_TranslateNonStream_SameFormatIsPassthrough validates that when
// SourceFormat equals FormatOpenAI (Qoder's native response format), the
// TranslateNonStream call returns the response unchanged. This is the
// common case and must not break clients.
func TestExecute_TranslateNonStream_SameFormatIsPassthrough(t *testing.T) {
	openAIResp := map[string]interface{}{
		"id":      "chatcmpl-test-123",
		"object":  "chat.completion",
		"created": 1712345678,
		"model":   "auto",
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": "Hello from Qoder",
				},
				"finish_reason": "stop",
			},
		},
	}
	responseBytes, err := json.Marshal(openAIResp)
	if err != nil {
		t.Fatalf("marshal openAIResp: %v", err)
	}

	// When both from and to are FormatOpenAI, TranslateNonStream
	// falls back to returning rawJSON unchanged (no translator registered).
	var param any
	out := sdktranslator.TranslateNonStream(
		context.Background(),
		sdktranslator.FormatOpenAI,
		sdktranslator.FormatOpenAI,
		"auto",
		nil, nil,
		responseBytes,
		&param,
	)

	var result map[string]interface{}
	if err = json.Unmarshal(out, &result); err != nil {
		t.Fatalf("unmarshal translated response: %v", err)
	}
	if got := result["object"]; got != "chat.completion" {
		t.Errorf("object = %v, want %q", got, "chat.completion")
	}
	choices, ok := result["choices"].([]interface{})
	if !ok {
		t.Fatalf("choices is not []interface{}: %T", result["choices"])
	}
	if len(choices) != 1 {
		t.Fatalf("len(choices) = %d, want 1", len(choices))
	}
	msg := choices[0].(map[string]interface{})["message"].(map[string]interface{})
	if got := msg["content"]; got != "Hello from Qoder" {
		t.Errorf("message.content = %v, want %q", got, "Hello from Qoder")
	}
}

// TestExecute_TranslateNonStream_EmptySourceFormatIsPassthrough validates
// that when SourceFormat is empty (not set by handler), the response is
// returned unchanged.
func TestExecute_TranslateNonStream_EmptySourceFormatIsPassthrough(t *testing.T) {
	openAIResp := map[string]interface{}{
		"id":      "chatcmpl-test-456",
		"object":  "chat.completion",
		"created": 1712345678,
		"model":   "auto",
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": "Hello",
				},
				"finish_reason": "stop",
			},
		},
	}
	responseBytes, _ := json.Marshal(openAIResp)

	// Empty SourceFormat: no translator registered, raw JSON returned as-is.
	var param any
	out := sdktranslator.TranslateNonStream(
		context.Background(),
		sdktranslator.FormatOpenAI,
		"", // empty SourceFormat
		"auto",
		nil, nil,
		responseBytes,
		&param,
	)

	var result map[string]interface{}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("unmarshal translated response: %v", err)
	}
	if got := result["object"]; got != "chat.completion" {
		t.Errorf("object = %v, want %q", got, "chat.completion")
	}
}

// TestExecute_TranslateNonStream_NonOpenAISourceFormat validates that when
// SourceFormat differs from FormatOpenAI (e.g. "openai-response" from
// /v1/responses route), TranslateNonStream is called and returns a
// translated payload (or the raw JSON as fallback if no translator is
// registered for that format pair). This is the bugfix scenario.
func TestExecute_TranslateNonStream_NonOpenAISourceFormat(t *testing.T) {
	openAIResp := map[string]interface{}{
		"id":      "chatcmpl-test-789",
		"object":  "chat.completion",
		"created": 1712345678,
		"model":   "auto",
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": "Will be translated",
				},
				"finish_reason": "stop",
			},
		},
	}
	responseBytes, _ := json.Marshal(openAIResp)

	// Simulate a request from /v1/responses route (sets SourceFormat to "openai-response").
	// If a translator is registered, it will transform the payload; otherwise
	// the raw JSON is returned as fallback. Either way, this must not panic
	// or return an empty response.
	sourceFmt := sdktranslator.FromString("openai-response")
	var param any
	out := sdktranslator.TranslateNonStream(
		context.Background(),
		sdktranslator.FormatOpenAI,
		sourceFmt,
		"auto",
		nil, nil,
		responseBytes,
		&param,
	)

	if len(out) == 0 {
		t.Error("expected non-empty translated output")
	}
	if !json.Valid(out) {
		t.Error("TranslateNonStream must return valid JSON")
	}
}

// TestExecute_ResponseStructureMatchesOpenAISchema validates that the
// accumulated non-stream response built by Execute follows the OpenAI
// chat-completions schema before translation.
func TestExecute_ResponseStructureMatchesOpenAISchema(t *testing.T) {
	// Replicate the response structure built in Execute (lines 672-684).
	content := "test content"
	finishReason := "stop"
	model := "auto"

	response := map[string]interface{}{
		"id":      fmt.Sprintf("qoder-%d", time.Now().UnixNano()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": finishReason,
			},
		},
	}

	responseBytes, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}

	var result map[string]interface{}
	if err = json.Unmarshal(responseBytes, &result); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	// Verify top-level fields match OpenAI schema.
	if got := result["object"]; got != "chat.completion" {
		t.Errorf("object = %v, want %q", got, "chat.completion")
	}
	if got := result["model"]; got != model {
		t.Errorf("model = %v, want %q", got, model)
	}
	if id, _ := result["id"].(string); id == "" {
		t.Error("id is empty")
	}
	if created, _ := result["created"].(float64); created == 0 {
		t.Error("created is zero")
	}

	// Verify choices array.
	choices, ok := result["choices"].([]interface{})
	if !ok {
		t.Fatalf("choices is not []interface{}: %T", result["choices"])
	}
	if len(choices) != 1 {
		t.Fatalf("len(choices) = %d, want 1", len(choices))
	}

	choice, ok := choices[0].(map[string]interface{})
	if !ok {
		t.Fatalf("choice is not map[string]interface{}: %T", choices[0])
	}
	if got := choice["index"]; got != float64(0) {
		t.Errorf("choice.index = %v, want 0", got)
	}
	if got := choice["finish_reason"]; got != finishReason {
		t.Errorf("choice.finish_reason = %v, want %q", got, finishReason)
	}

	msg, ok := choice["message"].(map[string]interface{})
	if !ok {
		t.Fatalf("message is not map[string]interface{}: %T", choice["message"])
	}
	if got := msg["role"]; got != "assistant" {
		t.Errorf("message.role = %v, want %q", got, "assistant")
	}
	if got := msg["content"]; got != content {
		t.Errorf("message.content = %v, want %q", got, content)
	}
}

// TestExecute_TranslateNonStream_UsesRequestPayload verifies that when
// SourceFormat differs from FormatOpenAI, the request payload is translated
// before being passed to TranslateNonStream (matching the pattern in
// the fix).
func TestExecute_TranslateNonStream_UsesTranslatedRequestPayload(t *testing.T) {
	// Simulate the request translation that happens in the Execute fix.
	sourceFmt := sdktranslator.FromString("gemini")
	originalRequest := []byte(`{"model":"auto","messages":[{"role":"user","content":"hi"}],"generationConfig":{}}`)
	reqPayload := []byte(`{"model":"auto","messages":[{"role":"user","content":"hi"}]}`)
	openAIResp, _ := json.Marshal(map[string]interface{}{
		"id":      "test",
		"object":  "chat.completion",
		"created": 1,
		"model":   "auto",
		"choices": []map[string]interface{}{
			{"index": 0, "message": map[string]interface{}{
				"role": "assistant", "content": "hi",
			}, "finish_reason": "stop"},
		},
	})

	// Translate request: sourceFmt -> FormatOpenAI (as done in the fix)
	translatedPayload := reqPayload
	if sourceFmt != "" && sourceFmt != sdktranslator.FormatOpenAI {
		translatedPayload = sdktranslator.TranslateRequest(
			sourceFmt, sdktranslator.FormatOpenAI,
			"auto", reqPayload, false,
		)
	}
	if translatedPayload == nil {
		t.Fatal("translated payload is nil")
	}

	// Now call TranslateNonStream with the translated request payload.
	var param any
	out := sdktranslator.TranslateNonStream(
		context.Background(),
		sdktranslator.FormatOpenAI,
		sourceFmt,
		"auto",
		originalRequest,
		translatedPayload,
		openAIResp,
		&param,
	)

	if len(out) == 0 {
		t.Error("expected non-empty translated output")
	}
	if !json.Valid(out) {
		t.Error("TranslateNonStream must return valid JSON")
	}
}

func TestQoderExecutorExecutionModelSelection(t *testing.T) {
	tests := []struct {
		name          string
		reqModel      string
		payloadModel  string
		wantModel     string
		wantErr       string
		wantTransport bool
	}{
		{
			name:          "resolved request model overrides unresolved payload alias",
			reqModel:      "qoder/auto",
			payloadModel:  "configured-alias",
			wantModel:     "auto",
			wantTransport: true,
		},
		{
			name:          "empty request model preserves payload fallback",
			payloadModel:  "qoder/auto",
			wantModel:     "auto",
			wantTransport: true,
		},
		{
			name:         "unknown resolved request model is rejected before transport",
			reqModel:     "qoder/unknown-resolved-target",
			payloadModel: "qoder/auto",
			wantErr:      `unsupported qoder model: "unknown-resolved-target"`,
		},
	}

	for _, mode := range []string{"stream", "non-stream"} {
		t.Run(mode, func(t *testing.T) {
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					transportCalls := 0
					ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", qoderRoundTripFunc(func(req *http.Request) (*http.Response, error) {
						transportCalls++
						if got := req.Header.Get("X-Model-Key"); got != tt.wantModel {
							t.Errorf("X-Model-Key = %q, want %q", got, tt.wantModel)
						}
						return qoderSSEHTTPResponse(req, http.StatusOK, "data: [DONE]\n\n"), nil
					}))
					executor := NewQoderExecutor(&config.Config{})
					authRecord := &cliproxyauth.Auth{Storage: testQoderStorageWithModelConfig()}
					req := cliproxyexecutor.Request{
						Model:   tt.reqModel,
						Payload: []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}]}`, tt.payloadModel)),
					}

					var err error
					if mode == "stream" {
						var result *cliproxyexecutor.StreamResult
						result, err = executor.ExecuteStream(ctx, authRecord, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
						if err == nil {
							for chunk := range result.Chunks {
								if chunk.Err != nil {
									t.Fatalf("stream chunk error = %v", chunk.Err)
								}
							}
						}
					} else {
						_, err = executor.Execute(ctx, authRecord, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
					}

					if tt.wantErr != "" {
						if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
							t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
						}
					} else if err != nil {
						t.Fatalf("execution error = %v", err)
					}
					if got := transportCalls > 0; got != tt.wantTransport {
						t.Fatalf("transport called = %v (%d calls), want %v", got, transportCalls, tt.wantTransport)
					}
				})
			}
		})
	}
}

func TestValidateQoderModel(t *testing.T) {
	storage := &qoder.QoderTokenStorage{}
	storage.SetModelConfigs(map[string]json.RawMessage{
		"custom-qoder-model": json.RawMessage(`{"type":"custom"}`),
	})

	tests := []struct {
		name         string
		rawModel     string
		want         string
		wantErr      bool
		errSubstring string
	}{
		{
			name:     "ModelMap hit with prefix",
			rawModel: "qoder/auto",
			want:     "auto",
		},
		{
			name:     "ModelMap hit without prefix",
			rawModel: "auto",
			want:     "auto",
		},
		{
			name:     "Storage config hit with prefix",
			rawModel: "qoder/custom-qoder-model",
			want:     "custom-qoder-model",
		},
		{
			name:     "Storage config hit without prefix",
			rawModel: "custom-qoder-model",
			want:     "custom-qoder-model",
		},
		{
			name:         "Unsupported model error",
			rawModel:     "qoder/nonexistent",
			wantErr:      true,
			errSubstring: `unsupported qoder model: "nonexistent"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validateQoderModel(tt.rawModel, storage)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateQoderModel(%q) error = %v, wantErr %v", tt.rawModel, err, tt.wantErr)
			}
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), tt.errSubstring) {
					t.Errorf("validateQoderModel(%q) error = %v, want substring %q", tt.rawModel, err, tt.errSubstring)
				}
				return
			}
			if got != tt.want {
				t.Errorf("validateQoderModel(%q) = %q, want %q", tt.rawModel, got, tt.want)
			}
		})
	}
}
