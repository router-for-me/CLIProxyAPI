package handlers

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type issue7658BootstrapTOSExecutor struct {
	mu           sync.Mutex
	normalCalls  []string
	creditsCalls []string
}

func (e *issue7658BootstrapTOSExecutor) Identifier() string { return "antigravity" }

func (e *issue7658BootstrapTOSExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, &coreauth.Error{Code: "not_implemented", Message: "Execute not implemented"}
}

func (e *issue7658BootstrapTOSExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	creditsRequested := coreauth.AntigravityCreditsRequested(ctx)
	e.mu.Lock()
	if creditsRequested {
		e.creditsCalls = append(e.creditsCalls, auth.ID)
	} else {
		e.normalCalls = append(e.normalCalls, auth.ID)
	}
	e.mu.Unlock()

	chunks := make(chan coreexecutor.StreamChunk, 1)
	if !creditsRequested {
		chunks <- coreexecutor.StreamChunk{Err: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}}
		close(chunks)
		return &coreexecutor.StreamResult{
			Headers: http.Header{"Content-Type": {"text/event-stream"}},
			Chunks:  chunks,
		}, nil
	}
	chunks <- coreexecutor.StreamChunk{Err: &coreauth.Error{
		HTTPStatus: http.StatusForbidden,
		Code:       "tos_violation",
		Message:    "TOS_VIOLATION",
	}}
	close(chunks)
	return &coreexecutor.StreamResult{
		Headers: http.Header{"Content-Type": {"text/event-stream"}},
		Chunks:  chunks,
	}, nil
}

func (e *issue7658BootstrapTOSExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *issue7658BootstrapTOSExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, &coreauth.Error{Code: "not_implemented", Message: "CountTokens not implemented"}
}

func (e *issue7658BootstrapTOSExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, &coreauth.Error{Code: "not_implemented", Message: "HttpRequest not implemented"}
}

func (e *issue7658BootstrapTOSExecutor) snapshot() ([]string, []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	normal := append([]string(nil), e.normalCalls...)
	credits := append([]string(nil), e.creditsCalls...)
	return normal, credits
}

func TestExecuteStreamWithAuthManager_FirstChunkTOSInCreditsDoesNotReplaySecondAuth(t *testing.T) {
	const model = "claude-sonnet-4-6-issue7658-handler-tos"
	executor := &issue7658BootstrapTOSExecutor{}
	manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
	manager.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true},
	})
	manager.SetRetryConfig(0, 0, 1)
	manager.RegisterExecutor(executor)

	for _, id := range []string{"ag-7658-bs-1", "ag-7658-bs-2"} {
		if _, errReg := manager.Register(context.Background(), &coreauth.Auth{
			ID:         id,
			Provider:   "antigravity",
			Status:     coreauth.StatusActive,
			Attributes: map[string]string{coreauth.AttributeAuthKind: coreauth.AuthKindOAuth},
		}); errReg != nil {
			t.Fatalf("Register(%s): %v", id, errReg)
		}
		registry.GetGlobalRegistry().RegisterClient(id, "antigravity", []*registry.ModelInfo{{ID: model}})
	}
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient("ag-7658-bs-1")
		registry.GetGlobalRegistry().UnregisterClient("ag-7658-bs-2")
	})

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{
			BootstrapRetries: 1,
		},
	}, manager)

	dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "claude", model, []byte(`{"model":"`+model+`"}`), "")
	if dataChan != nil {
		for range dataChan {
		}
	}
	var gotErr *interfaces.ErrorMessage
	for msg := range errChan {
		if msg != nil {
			gotErr = msg
		}
	}
	if gotErr == nil || gotErr.Error == nil || !strings.Contains(gotErr.Error.Error(), "TOS_VIOLATION") {
		t.Fatalf("err = %+v, want TOS_VIOLATION", gotErr)
	}
	normalCalls, creditsCalls := executor.snapshot()
	if len(normalCalls) != 1 || normalCalls[0] != "ag-7658-bs-1" {
		t.Fatalf("normalCalls = %v, want [ag-7658-bs-1] (bootstrap retry must not replay)", normalCalls)
	}
	if len(creditsCalls) != 1 || creditsCalls[0] != "ag-7658-bs-1" {
		t.Fatalf("creditsCalls = %v, want [ag-7658-bs-1] (second auth must never be called)", creditsCalls)
	}
}

type issue7658IncompleteSSEValidationExecutor struct {
	mu           sync.Mutex
	normalCalls  []string
	creditsCalls []string
	secondAuthID string
	secondData   []byte
	errBody      string
}

func (e *issue7658IncompleteSSEValidationExecutor) Identifier() string { return "antigravity" }

func (e *issue7658IncompleteSSEValidationExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, &coreauth.Error{Code: "not_implemented", Message: "Execute not implemented"}
}

func (e *issue7658IncompleteSSEValidationExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	creditsRequested := coreauth.AntigravityCreditsRequested(ctx)
	e.mu.Lock()
	if creditsRequested {
		e.creditsCalls = append(e.creditsCalls, auth.ID)
	} else {
		e.normalCalls = append(e.normalCalls, auth.ID)
	}
	e.mu.Unlock()

	if e.secondAuthID != "" && auth.ID == e.secondAuthID {
		chunks := make(chan coreexecutor.StreamChunk, 1)
		chunks <- coreexecutor.StreamChunk{Payload: e.secondData}
		close(chunks)
		return &coreexecutor.StreamResult{
			Headers: http.Header{"Content-Type": {"text/event-stream"}},
			Chunks:  chunks,
		}, nil
	}
	if !creditsRequested {
		return nil, &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}
	}
	errMsg := e.errBody
	if errMsg == "" {
		errMsg = `{"error":{"code":403,"status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"VALIDATION_REQUIRED"}]}}`
	}
	chunks := make(chan coreexecutor.StreamChunk, 2)
	chunks <- coreexecutor.StreamChunk{Payload: []byte(`data: {"type":"response.created"`)}
	chunks <- coreexecutor.StreamChunk{Err: &coreauth.Error{
		HTTPStatus: http.StatusForbidden,
		Message:    errMsg,
	}}
	close(chunks)
	return &coreexecutor.StreamResult{
		Headers: http.Header{"Content-Type": {"text/event-stream"}},
		Chunks:  chunks,
	}, nil
}

func (e *issue7658IncompleteSSEValidationExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *issue7658IncompleteSSEValidationExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, &coreauth.Error{Code: "not_implemented", Message: "CountTokens not implemented"}
}

func (e *issue7658IncompleteSSEValidationExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, &coreauth.Error{Code: "not_implemented", Message: "HttpRequest not implemented"}
}

func (e *issue7658IncompleteSSEValidationExecutor) snapshot() ([]string, []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	normal := append([]string(nil), e.normalCalls...)
	credits := append([]string(nil), e.creditsCalls...)
	return normal, credits
}

func TestExecuteStreamWithAuthManager_IncompleteSSEBeforeValidationRequiredDoesNotReplaySecondAuth(t *testing.T) {
	const model = "claude-sonnet-4-6-issue7658-handler-sseval"
	executor := &issue7658IncompleteSSEValidationExecutor{}
	manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
	manager.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true},
	})
	manager.SetRetryConfig(0, 0, 1)
	manager.RegisterExecutor(executor)

	for _, id := range []string{"ag-7658-sse-1", "ag-7658-sse-2"} {
		if _, errReg := manager.Register(context.Background(), &coreauth.Auth{
			ID:         id,
			Provider:   "antigravity",
			Status:     coreauth.StatusActive,
			Attributes: map[string]string{coreauth.AttributeAuthKind: coreauth.AuthKindOAuth},
		}); errReg != nil {
			t.Fatalf("Register(%s): %v", id, errReg)
		}
		registry.GetGlobalRegistry().RegisterClient(id, "antigravity", []*registry.ModelInfo{{ID: model}})
	}
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient("ag-7658-sse-1")
		registry.GetGlobalRegistry().UnregisterClient("ag-7658-sse-2")
	})

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{
			BootstrapRetries: 1,
		},
	}, manager)

	dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "openai-response", model, []byte(`{"model":"`+model+`"}`), "")
	var gotData []byte
	if dataChan != nil {
		for chunk := range dataChan {
			gotData = append(gotData, chunk...)
		}
	}
	if len(gotData) != 0 {
		t.Fatalf("dataChan emitted %q, want empty before pre-output error", string(gotData))
	}
	var gotErr *interfaces.ErrorMessage
	for msg := range errChan {
		if msg != nil {
			gotErr = msg
		}
	}
	if gotErr == nil || gotErr.Error == nil || !strings.Contains(gotErr.Error.Error(), "VALIDATION_REQUIRED") {
		t.Fatalf("err = %+v, want VALIDATION_REQUIRED", gotErr)
	}
	normalCalls, creditsCalls := executor.snapshot()
	if len(normalCalls) != 1 || normalCalls[0] != "ag-7658-sse-1" {
		t.Fatalf("normalCalls = %v, want [ag-7658-sse-1] (bootstrap retry must not replay)", normalCalls)
	}
	if len(creditsCalls) != 1 || creditsCalls[0] != "ag-7658-sse-1" {
		t.Fatalf("creditsCalls = %v, want [ag-7658-sse-1] (second auth must never be called)", creditsCalls)
	}
}

func TestExecuteStreamWithAuthManager_IncompleteSSEBeforeValidationRequiredContinueRuleRotates(t *testing.T) {
	const model = "claude-sonnet-4-6-issue7658-handler-ssecont"
	wantPayload := "data: {\"type\":\"response.completed\"}\n\n"
	executor := &issue7658IncompleteSSEValidationExecutor{
		secondAuthID: "ag-7658-ssec-2",
		secondData:   []byte(wantPayload),
	}
	manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
	manager.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true},
		OAuthRequestScopedErrors: map[string][]internalconfig.RequestScopedErrorRule{
			"antigravity": {
				{
					Status: http.StatusForbidden,
					Match:  []string{"VALIDATION_REQUIRED"},
					Action: "continue",
				},
			},
		},
	})
	manager.SetRetryConfig(0, 0, 1)
	manager.RegisterExecutor(executor)

	for _, id := range []string{"ag-7658-ssec-1", "ag-7658-ssec-2"} {
		if _, errReg := manager.Register(context.Background(), &coreauth.Auth{
			ID:         id,
			Provider:   "antigravity",
			Status:     coreauth.StatusActive,
			Attributes: map[string]string{coreauth.AttributeAuthKind: coreauth.AuthKindOAuth},
		}); errReg != nil {
			t.Fatalf("Register(%s): %v", id, errReg)
		}
		registry.GetGlobalRegistry().RegisterClient(id, "antigravity", []*registry.ModelInfo{{ID: model}})
	}
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient("ag-7658-ssec-1")
		registry.GetGlobalRegistry().UnregisterClient("ag-7658-ssec-2")
	})

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{
			BootstrapRetries: 1,
		},
	}, manager)

	dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "openai-response", model, []byte(`{"model":"`+model+`"}`), "")
	var gotData []byte
	if dataChan != nil {
		for chunk := range dataChan {
			gotData = append(gotData, chunk...)
		}
	}
	var gotErr *interfaces.ErrorMessage
	for msg := range errChan {
		if msg != nil {
			gotErr = msg
		}
	}
	if gotErr != nil {
		t.Fatalf("unexpected err = %+v, want rotation to second auth on continue rule", gotErr)
	}
	if string(gotData) != wantPayload {
		t.Fatalf("data = %q, want %q", string(gotData), wantPayload)
	}
}

func TestExecuteStreamWithAuthManager_IncompleteSSEBeforeRequestScopedStopDoesNotReplay(t *testing.T) {
	const model = "claude-sonnet-4-6-issue7658-handler-ssestop"
	executor := &issue7658IncompleteSSEValidationExecutor{
		secondAuthID: "ag-7658-ssestop-2",
		secondData:   []byte("data: {\"type\":\"response.completed\"}\n\n"),
		errBody:      `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"permission denied"}}`,
	}
	manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
	manager.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true},
		OAuthRequestScopedErrors: map[string][]internalconfig.RequestScopedErrorRule{
			"antigravity": {
				{
					Status: http.StatusForbidden,
					Match:  []string{"permission denied"},
					Action: "stop",
				},
			},
		},
	})
	manager.SetRetryConfig(0, 0, 1)
	manager.RegisterExecutor(executor)

	for _, id := range []string{"ag-7658-ssestop-1", "ag-7658-ssestop-2"} {
		if _, errReg := manager.Register(context.Background(), &coreauth.Auth{
			ID:         id,
			Provider:   "antigravity",
			Status:     coreauth.StatusActive,
			Attributes: map[string]string{coreauth.AttributeAuthKind: coreauth.AuthKindOAuth},
		}); errReg != nil {
			t.Fatalf("Register(%s): %v", id, errReg)
		}
		registry.GetGlobalRegistry().RegisterClient(id, "antigravity", []*registry.ModelInfo{{ID: model}})
	}
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient("ag-7658-ssestop-1")
		registry.GetGlobalRegistry().UnregisterClient("ag-7658-ssestop-2")
	})

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{
			BootstrapRetries: 1,
		},
	}, manager)

	dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "openai-response", model, []byte(`{"model":"`+model+`"}`), "")
	var gotData []byte
	if dataChan != nil {
		for chunk := range dataChan {
			gotData = append(gotData, chunk...)
		}
	}
	var gotErr *interfaces.ErrorMessage
	for msg := range errChan {
		if msg != nil {
			gotErr = msg
		}
	}
	if len(gotData) != 0 {
		t.Fatalf("data = %q, want empty", string(gotData))
	}
	if gotErr == nil || gotErr.Error == nil || !strings.Contains(gotErr.Error.Error(), "permission denied") {
		t.Fatalf("err = %+v, want permission denied", gotErr)
	}
	normalCalls, creditsCalls := executor.snapshot()
	if len(normalCalls) != 1 || normalCalls[0] != "ag-7658-ssestop-1" {
		t.Fatalf("normalCalls = %v, want [ag-7658-ssestop-1] (stop rule must block bootstrap replay)", normalCalls)
	}
	if len(creditsCalls) != 1 || creditsCalls[0] != "ag-7658-ssestop-1" {
		t.Fatalf("creditsCalls = %v, want [ag-7658-ssestop-1]", creditsCalls)
	}
}
