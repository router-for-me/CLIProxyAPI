package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

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

type responsesAffinityCall struct {
	authID  string
	payload string
}

type responsesAffinityExecutor struct {
	mu                         sync.Mutex
	calls                      []responsesAffinityCall
	originAuthID               string
	responseEncryptedContent   string
	failWhenEncryptedPresent   bool
	failWhenEncryptedWrongAuth bool
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
	respEncrypted := e.responseEncryptedContent
	e.mu.Unlock()

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

func (e *responsesAffinityExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
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
