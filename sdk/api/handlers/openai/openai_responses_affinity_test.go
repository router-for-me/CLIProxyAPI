package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

type invalidEncryptedErr struct {
	msg string
}

func (e invalidEncryptedErr) Error() string   { return e.msg }
func (e invalidEncryptedErr) StatusCode() int { return http.StatusBadRequest }

type rateLimitedErr struct {
	msg string
}

func (e rateLimitedErr) Error() string   { return e.msg }
func (e rateLimitedErr) StatusCode() int { return http.StatusTooManyRequests }

type responsesAffinityCall struct {
	authID  string
	payload string
}

type responsesAffinityExecutor struct {
	mu                              sync.Mutex
	calls                           []responsesAffinityCall
	streamCalls                     []responsesAffinityCall
	originAuthID                    string
	responseEncryptedContent        string
	failWhenEncryptedPresent        bool
	failWhenEncryptedWrongAuth      bool
	failOriginAfterFirst            bool
	streamFailOriginAfterFirst      bool
	streamFailOriginAfterFirstChunk bool
	streamFailOriginAfterMessageAdd bool
	failWhenPreviousResponseID      bool
}

func (e *responsesAffinityExecutor) Identifier() string { return "responses-affinity-provider" }

func (e *responsesAffinityExecutor) Execute(_ context.Context, auth *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	authID := ""
	if auth != nil {
		authID = auth.ID
	}
	payload := string(req.Payload)

	e.mu.Lock()
	e.calls = append(e.calls, responsesAffinityCall{authID: authID, payload: payload})
	encrypted := extractReasoningEncryptedFromInput(req.Payload)
	if len(encrypted) == 0 && e.originAuthID == "" {
		e.originAuthID = authID
	}
	originAuth := e.originAuthID
	failOnAny := e.failWhenEncryptedPresent
	failOnWrong := e.failWhenEncryptedWrongAuth
	failOriginAfterFirst := e.failOriginAfterFirst
	failOnPrevRespID := e.failWhenPreviousResponseID
	callCount := len(e.calls)
	respEncrypted := e.responseEncryptedContent
	e.mu.Unlock()

	if failOriginAfterFirst && originAuth != "" && authID == originAuth && callCount > 1 {
		return coreexecutor.Response{}, rateLimitedErr{
			msg: `{"error":{"message":"rate limited","type":"rate_limit_error","code":"rate_limit_exceeded"}}`,
		}
	}

	// Simulate server-side invalid_encrypted_content when previous_response_id
	// references a response from a different org (no inline reasoning needed).
	if failOnPrevRespID && strings.Contains(payload, `"previous_response_id"`) {
		return coreexecutor.Response{}, invalidEncryptedErr{
			msg: `{"error":{"message":"The encrypted content could not be verified. Reason: Encrypted content could not be decrypted or parsed.","type":"invalid_request_error","code":"invalid_encrypted_content"}}`,
		}
	}

	if len(encrypted) > 0 {
		if failOnAny || (failOnWrong && originAuth != "" && authID != originAuth) {
			return coreexecutor.Response{}, invalidEncryptedErr{
				msg: `{"error":{"message":"The encrypted content gAAA... could not be verified. Reason: Encrypted content could not be decrypted or parsed.","type":"invalid_request_error","code":"invalid_encrypted_content"}}`,
			}
		}
	}

	if strings.TrimSpace(respEncrypted) == "" {
		respEncrypted = "enc-default"
	}
	respPayload := fmt.Sprintf(`{"id":"resp-%s","output":[{"type":"reasoning","encrypted_content":"%s"}]}`, authID, respEncrypted)
	return coreexecutor.Response{Payload: []byte(respPayload)}, nil
}

func (e *responsesAffinityExecutor) ExecuteStream(_ context.Context, auth *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	authID := ""
	if auth != nil {
		authID = auth.ID
	}
	payload := string(req.Payload)

	e.mu.Lock()
	e.streamCalls = append(e.streamCalls, responsesAffinityCall{authID: authID, payload: payload})
	if e.originAuthID == "" {
		e.originAuthID = authID
	}
	originAuth := e.originAuthID
	failOrigin := e.streamFailOriginAfterFirst
	failOriginAfterFirstChunk := e.streamFailOriginAfterFirstChunk
	failOriginAfterMessageAdd := e.streamFailOriginAfterMessageAdd
	respEncrypted := e.responseEncryptedContent
	e.mu.Unlock()

	if failOrigin && originAuth != "" && authID == originAuth {
		return nil, rateLimitedErr{
			msg: `{"error":{"message":"rate limited","type":"rate_limit_error","code":"rate_limit_exceeded"}}`,
		}
	}

	if failOriginAfterFirstChunk && originAuth != "" && authID == originAuth && strings.Contains(payload, `"previous_response_id"`) {
		ch := make(chan coreexecutor.StreamChunk, 2)
		ch <- coreexecutor.StreamChunk{
			Payload: []byte(fmt.Sprintf("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-stream-%s\"}}\n\n", authID)),
		}
		ch <- coreexecutor.StreamChunk{
			Err: invalidEncryptedErr{
				msg: `{"error":{"message":"The encrypted content could not be verified. Reason: Encrypted content could not be decrypted or parsed.","type":"invalid_request_error","code":"invalid_encrypted_content"}}`,
			},
		}
		close(ch)
		return &coreexecutor.StreamResult{Chunks: ch}, nil
	}

	if failOriginAfterMessageAdd && originAuth != "" && authID == originAuth && strings.Contains(payload, `"previous_response_id"`) {
		ch := make(chan coreexecutor.StreamChunk, 2)
		ch <- coreexecutor.StreamChunk{
			Payload: []byte(fmt.Sprintf("data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"msg-stream-%s\",\"type\":\"message\",\"status\":\"in_progress\",\"content\":[],\"role\":\"assistant\"}}\n\n", authID)),
		}
		ch <- coreexecutor.StreamChunk{
			Err: invalidEncryptedErr{
				msg: `{"error":{"message":"The encrypted content could not be verified. Reason: Encrypted content could not be decrypted or parsed.","type":"invalid_request_error","code":"invalid_encrypted_content"}}`,
			},
		}
		close(ch)
		return &coreexecutor.StreamResult{Chunks: ch}, nil
	}

	if strings.TrimSpace(respEncrypted) == "" {
		respEncrypted = "enc-default"
	}

	ch := make(chan coreexecutor.StreamChunk, 1)
	ch <- coreexecutor.StreamChunk{
		Payload: []byte(fmt.Sprintf("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-stream-%s\",\"output\":[{\"type\":\"reasoning\",\"encrypted_content\":\"%s\"}]}}\n\n", authID, respEncrypted)),
	}
	close(ch)
	return &coreexecutor.StreamResult{Chunks: ch}, nil
}

func (e *responsesAffinityExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *responsesAffinityExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *responsesAffinityExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *responsesAffinityExecutor) Calls() []responsesAffinityCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]responsesAffinityCall, len(e.calls))
	copy(out, e.calls)
	return out
}

func (e *responsesAffinityExecutor) StreamCalls() []responsesAffinityCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]responsesAffinityCall, len(e.streamCalls))
	copy(out, e.streamCalls)
	return out
}

func setupResponsesAffinityRouter(t *testing.T, executor *responsesAffinityExecutor, authIDs ...string) *gin.Engine {
	t.Helper()

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	for _, authID := range authIDs {
		auth := &coreauth.Auth{ID: authID, Provider: executor.Identifier(), Status: coreauth.StatusActive}
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register auth %s: %v", authID, err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	}
	t.Cleanup(func() {
		for _, authID := range authIDs {
			registry.GetGlobalRegistry().UnregisterClient(authID)
		}
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses", h.Responses)
	return router
}

func TestResponsesAffinityPinsAuthUsingEncryptedContent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetResponsesAuthAffinityForTests()

	executor := &responsesAffinityExecutor{
		responseEncryptedContent:   "enc-affinity",
		failWhenEncryptedWrongAuth: true,
	}
	router := setupResponsesAffinityRouter(t, executor, "auth1", "auth2")

	firstReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`))
	firstReq.Header.Set("Content-Type", "application/json")
	firstResp := httptest.NewRecorder()
	router.ServeHTTP(firstResp, firstReq)
	if firstResp.Code != http.StatusOK {
		t.Fatalf("first status = %d, want %d, body=%s", firstResp.Code, http.StatusOK, firstResp.Body.String())
	}

	secondReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","input":[{"type":"reasoning","encrypted_content":"enc-affinity"},{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}]}`))
	secondReq.Header.Set("Content-Type", "application/json")
	secondResp := httptest.NewRecorder()
	router.ServeHTTP(secondResp, secondReq)
	if secondResp.Code != http.StatusOK {
		t.Fatalf("second status = %d, want %d, body=%s", secondResp.Code, http.StatusOK, secondResp.Body.String())
	}

	calls := executor.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(calls))
	}
	if calls[1].authID != calls[0].authID {
		t.Fatalf("expected pinned auth on continuation, first=%q second=%q", calls[0].authID, calls[1].authID)
	}
	if !strings.Contains(calls[1].payload, `"encrypted_content":"enc-affinity"`) {
		t.Fatalf("expected encrypted_content to remain when affinity exists, payload=%s", calls[1].payload)
	}
}

func TestResponsesEncryptedContentRecoveryStripsReasoningInput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetResponsesAuthAffinityForTests()

	executor := &responsesAffinityExecutor{
		responseEncryptedContent: "enc-fallback",
		failWhenEncryptedPresent: true,
	}
	router := setupResponsesAffinityRouter(t, executor, "auth1")

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","input":[{"type":"reasoning","encrypted_content":"enc-fallback"},{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}

	calls := executor.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2 (retry after stripping encrypted reasoning)", len(calls))
	}
	if !strings.Contains(calls[0].payload, `"encrypted_content":"enc-fallback"`) {
		t.Fatalf("first attempt should carry encrypted reasoning, payload=%s", calls[0].payload)
	}
	if strings.Contains(calls[1].payload, `"encrypted_content":"enc-fallback"`) {
		t.Fatalf("second attempt should strip encrypted reasoning, payload=%s", calls[1].payload)
	}
}

func TestResponsesEncryptedContentRecoveryStripsPreviousResponseID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetResponsesAuthAffinityForTests()

	executor := &responsesAffinityExecutor{
		responseEncryptedContent: "enc-previd",
		failWhenEncryptedPresent: true,
	}
	router := setupResponsesAffinityRouter(t, executor, "auth1")

	// Request carries both encrypted reasoning and previous_response_id.
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(
		`{"model":"test-model","previous_response_id":"resp-old","input":[{"type":"reasoning","encrypted_content":"enc-previd"},{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}

	calls := executor.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2 (retry after stripping)", len(calls))
	}
	// First attempt should include both encrypted reasoning and previous_response_id.
	if !strings.Contains(calls[0].payload, `"previous_response_id":"resp-old"`) {
		t.Fatalf("first attempt should carry previous_response_id, payload=%s", calls[0].payload)
	}
	// Second attempt should strip both encrypted reasoning AND previous_response_id.
	if strings.Contains(calls[1].payload, `"encrypted_content"`) {
		t.Fatalf("retry should strip encrypted reasoning, payload=%s", calls[1].payload)
	}
	if strings.Contains(calls[1].payload, `"previous_response_id"`) {
		t.Fatalf("retry should strip previous_response_id, payload=%s", calls[1].payload)
	}
}

func TestResponsesPreviousResponseIDOnlyRecovery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetResponsesAuthAffinityForTests()

	// Executor fails with invalid_encrypted_content whenever
	// previous_response_id is present (simulates server-side state mismatch).
	executor := &responsesAffinityExecutor{
		responseEncryptedContent:   "enc-prev-only",
		failWhenPreviousResponseID: true,
	}
	router := setupResponsesAffinityRouter(t, executor, "auth1")

	// Request has previous_response_id but NO inline encrypted reasoning.
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(
		`{"model":"test-model","previous_response_id":"resp-foreign","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}

	calls := executor.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2 (initial fail + retry after stripping previous_response_id)", len(calls))
	}
	if !strings.Contains(calls[0].payload, `"previous_response_id":"resp-foreign"`) {
		t.Fatalf("first attempt should carry previous_response_id, payload=%s", calls[0].payload)
	}
	if strings.Contains(calls[1].payload, `"previous_response_id"`) {
		t.Fatalf("retry should have stripped previous_response_id, payload=%s", calls[1].payload)
	}
}

func TestResponsesPinnedAuthFailureFallsBackUnpinned(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetResponsesAuthAffinityForTests()

	executor := &responsesAffinityExecutor{
		responseEncryptedContent: "enc-switch",
		failOriginAfterFirst:     true,
	}
	router := setupResponsesAffinityRouter(t, executor, "auth1", "auth2")

	firstReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"start"}]}]}`))
	firstReq.Header.Set("Content-Type", "application/json")
	firstResp := httptest.NewRecorder()
	router.ServeHTTP(firstResp, firstReq)
	if firstResp.Code != http.StatusOK {
		t.Fatalf("first status = %d, want %d, body=%s", firstResp.Code, http.StatusOK, firstResp.Body.String())
	}

	secondReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","input":[{"type":"reasoning","encrypted_content":"enc-switch"},{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}]}`))
	secondReq.Header.Set("Content-Type", "application/json")
	secondResp := httptest.NewRecorder()
	router.ServeHTTP(secondResp, secondReq)
	if secondResp.Code != http.StatusOK {
		t.Fatalf("second status = %d, want %d, body=%s", secondResp.Code, http.StatusOK, secondResp.Body.String())
	}

	calls := executor.Calls()
	if len(calls) < 3 {
		t.Fatalf("expected >= 3 calls (initial, pinned fail, unpinned retry), got %d", len(calls))
	}
	origin := calls[0].authID
	sawPinnedEncryptedFailure := false
	sawUnpinnedStrippedSuccessPath := false
	for _, call := range calls[1:] {
		hasEncrypted := strings.Contains(call.payload, `"encrypted_content":"enc-switch"`)
		if call.authID == origin && hasEncrypted {
			sawPinnedEncryptedFailure = true
		}
		if call.authID != origin && !hasEncrypted {
			sawUnpinnedStrippedSuccessPath = true
		}
	}
	if !sawPinnedEncryptedFailure {
		t.Fatalf("expected pinned call with encrypted continuation on origin auth, calls=%v", calls)
	}
	if !sawUnpinnedStrippedSuccessPath {
		t.Fatalf("expected unpinned retry on alternate auth without encrypted continuation, calls=%v", calls)
	}
}

func TestResponsesStreamingPinnedAuthFailureFallsBackUnpinned(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetResponsesAuthAffinityForTests()

	executor := &responsesAffinityExecutor{
		responseEncryptedContent:   "enc-stream",
		streamFailOriginAfterFirst: true,
	}
	router := setupResponsesAffinityRouter(t, executor, "auth1", "auth2")

	// Seed affinity with a successful non-streaming turn.
	firstReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"start"}]}]}`))
	firstReq.Header.Set("Content-Type", "application/json")
	firstResp := httptest.NewRecorder()
	router.ServeHTTP(firstResp, firstReq)
	if firstResp.Code != http.StatusOK {
		t.Fatalf("first status = %d, want %d, body=%s", firstResp.Code, http.StatusOK, firstResp.Body.String())
	}

	// Streaming continuation with encrypted reasoning should recover by stripping and unpinning.
	streamReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","stream":true,"input":[{"type":"reasoning","encrypted_content":"enc-stream"},{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}]}`))
	streamReq.Header.Set("Content-Type", "application/json")
	streamResp := httptest.NewRecorder()
	router.ServeHTTP(streamResp, streamReq)
	if streamResp.Code != http.StatusOK {
		t.Fatalf("stream status = %d, want %d, body=%s", streamResp.Code, http.StatusOK, streamResp.Body.String())
	}
	if !strings.Contains(streamResp.Body.String(), `"type":"response.completed"`) {
		t.Fatalf("expected completed SSE event, body=%s", streamResp.Body.String())
	}

	calls := executor.StreamCalls()
	if len(calls) < 2 {
		t.Fatalf("expected >= 2 stream attempts (pinned fail + unpinned retry), got %d", len(calls))
	}
	origin := executor.Calls()[0].authID
	sawPinnedEncryptedFailure := false
	sawUnpinnedStrippedSuccessPath := false
	for _, call := range calls {
		hasEncrypted := strings.Contains(call.payload, `"encrypted_content":"enc-stream"`)
		if call.authID == origin && hasEncrypted {
			sawPinnedEncryptedFailure = true
		}
		if call.authID != origin && !hasEncrypted {
			sawUnpinnedStrippedSuccessPath = true
		}
	}
	if !sawPinnedEncryptedFailure {
		t.Fatalf("expected stream call with encrypted continuation on origin auth, calls=%v", calls)
	}
	if !sawUnpinnedStrippedSuccessPath {
		t.Fatalf("expected stream retry on alternate auth without encrypted continuation, calls=%v", calls)
	}
}

func TestResponsesStreamingPreviousResponseIDOnlyPinnedFailureFallsBackUnpinned(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetResponsesAuthAffinityForTests()

	executor := &responsesAffinityExecutor{
		responseEncryptedContent:   "enc-stream-prev",
		streamFailOriginAfterFirst: true,
	}
	router := setupResponsesAffinityRouter(t, executor, "auth1", "auth2")

	// Seed affinity with response ID bound to auth1.
	firstReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"start"}]}]}`))
	firstReq.Header.Set("Content-Type", "application/json")
	firstResp := httptest.NewRecorder()
	router.ServeHTTP(firstResp, firstReq)
	if firstResp.Code != http.StatusOK {
		t.Fatalf("first status = %d, want %d, body=%s", firstResp.Code, http.StatusOK, firstResp.Body.String())
	}

	streamReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","stream":true,"previous_response_id":"resp-auth1","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}]}`))
	streamReq.Header.Set("Content-Type", "application/json")
	streamResp := httptest.NewRecorder()
	router.ServeHTTP(streamResp, streamReq)
	if streamResp.Code != http.StatusOK {
		t.Fatalf("stream status = %d, want %d, body=%s", streamResp.Code, http.StatusOK, streamResp.Body.String())
	}
	if !strings.Contains(streamResp.Body.String(), `"type":"response.completed"`) {
		t.Fatalf("expected completed SSE event, body=%s", streamResp.Body.String())
	}

	calls := executor.StreamCalls()
	if len(calls) < 2 {
		t.Fatalf("expected >= 2 stream attempts (pinned fail + unpinned retry), got %d", len(calls))
	}
	origin := executor.Calls()[0].authID
	if !strings.Contains(calls[0].payload, `"previous_response_id":"resp-auth1"`) {
		t.Fatalf("first stream attempt should carry previous_response_id, payload=%s", calls[0].payload)
	}
	sawUnpinnedWithoutPreviousResponseID := false
	for _, call := range calls[1:] {
		if call.authID != origin && !strings.Contains(call.payload, `"previous_response_id"`) {
			sawUnpinnedWithoutPreviousResponseID = true
		}
	}
	if !sawUnpinnedWithoutPreviousResponseID {
		t.Fatalf("expected unpinned retry without previous_response_id, calls=%v", calls)
	}
}

func TestResponsesStreamingPostFirstChunkInvalidEncryptedFallsBackWhenNoContentYet(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetResponsesAuthAffinityForTests()

	executor := &responsesAffinityExecutor{
		responseEncryptedContent:        "enc-stream-post-first",
		streamFailOriginAfterMessageAdd: true,
	}
	router := setupResponsesAffinityRouter(t, executor, "auth1", "auth2")

	// Seed affinity with response ID bound to auth1.
	firstReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"start"}]}]}`))
	firstReq.Header.Set("Content-Type", "application/json")
	firstResp := httptest.NewRecorder()
	router.ServeHTTP(firstResp, firstReq)
	if firstResp.Code != http.StatusOK {
		t.Fatalf("first status = %d, want %d, body=%s", firstResp.Code, http.StatusOK, firstResp.Body.String())
	}

	streamReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","stream":true,"previous_response_id":"resp-auth1","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}]}`))
	streamReq.Header.Set("Content-Type", "application/json")
	streamResp := httptest.NewRecorder()
	router.ServeHTTP(streamResp, streamReq)
	if streamResp.Code != http.StatusOK {
		t.Fatalf("stream status = %d, want %d, body=%s", streamResp.Code, http.StatusOK, streamResp.Body.String())
	}
	if !strings.Contains(streamResp.Body.String(), `"type":"response.completed"`) {
		t.Fatalf("expected completed SSE event after recovery, body=%s", streamResp.Body.String())
	}
	if strings.Contains(streamResp.Body.String(), `"type":"error"`) {
		t.Fatalf("did not expect terminal error chunk after successful post-first recovery, body=%s", streamResp.Body.String())
	}
	if strings.Contains(streamResp.Body.String(), `msg-stream-auth1`) {
		t.Fatalf("did not expect buffered first-attempt lifecycle event to leak into retried stream, body=%s", streamResp.Body.String())
	}

	calls := executor.StreamCalls()
	if len(calls) < 2 {
		t.Fatalf("expected >= 2 stream attempts, got %d", len(calls))
	}
	if !strings.Contains(calls[0].payload, `"previous_response_id":"resp-auth1"`) {
		t.Fatalf("first stream attempt should carry previous_response_id, payload=%s", calls[0].payload)
	}
	sawRecoveredRetry := false
	for _, call := range calls[1:] {
		if !strings.Contains(call.payload, `"previous_response_id"`) {
			sawRecoveredRetry = true
		}
	}
	if !sawRecoveredRetry {
		t.Fatalf("expected recovered retry without previous_response_id, calls=%v", calls)
	}
}

func TestResponsesAffinityStorePersistsAcrossRestart(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "responses_affinity.json")

	store := newResponsesAuthAffinityStoreWithPersistence(2*time.Hour, 128, storePath)
	store.rememberResponseID("resp-persist", "auth-persist")
	store.rememberEncrypted("enc-persist", "auth-persist")

	reloaded := newResponsesAuthAffinityStoreWithPersistence(2*time.Hour, 128, storePath)
	if got, ok := reloaded.lookupResponseID("resp-persist"); !ok || got != "auth-persist" {
		t.Fatalf("lookupResponseID after restart = (%q,%v), want (%q,true)", got, ok, "auth-persist")
	}
	if got, ok := reloaded.lookupEncrypted("enc-persist"); !ok || got != "auth-persist" {
		t.Fatalf("lookupEncrypted after restart = (%q,%v), want (%q,true)", got, ok, "auth-persist")
	}
}

func TestResolveResponsesAffinityPersistPathDefaultsDisabled(t *testing.T) {
	t.Setenv("CLIPROXY_RESPONSES_AFFINITY_PERSIST", "")
	t.Setenv("CLIPROXY_RESPONSES_AFFINITY_PATH", "")

	if got := resolveResponsesAffinityPersistPath(); got != "" {
		t.Fatalf("resolveResponsesAffinityPersistPath() = %q, want empty default", got)
	}
}

func TestResolveResponsesAffinityPersistPathUsesExplicitPath(t *testing.T) {
	expected := filepath.Join(t.TempDir(), "responses_affinity.json")
	t.Setenv("CLIPROXY_RESPONSES_AFFINITY_PERSIST", "true")
	t.Setenv("CLIPROXY_RESPONSES_AFFINITY_PATH", expected)

	if got := resolveResponsesAffinityPersistPath(); got != expected {
		t.Fatalf("resolveResponsesAffinityPersistPath() = %q, want %q", got, expected)
	}
}
