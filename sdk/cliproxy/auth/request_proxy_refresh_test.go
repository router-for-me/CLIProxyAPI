package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type requestProxyRefreshExecutor struct {
	refreshProxy string
}

func (e *requestProxyRefreshExecutor) Identifier() string { return "request-proxy-refresh" }

func (e *requestProxyRefreshExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not used")
}

func (e *requestProxyRefreshExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, errors.New("not used")
}

func (e *requestProxyRefreshExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	e.refreshProxy = cliproxyexecutor.RequestProxyURL(ctx)
	return auth, nil
}

func (e *requestProxyRefreshExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not used")
}

func (e *requestProxyRefreshExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not used")
}

func TestRefreshAuthForRequestIgnoresExecutionProxy(t *testing.T) {
	executor := &requestProxyRefreshExecutor{}
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &Auth{ID: "request-proxy-auth", Provider: executor.Identifier(), Status: StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register: %v", errRegister)
	}

	ctx := cliproxyexecutor.WithRequestProxyURL(context.Background(), "http://request-proxy.example:8081")
	if _, errRefresh := manager.refreshAuthForRequest(ctx, auth.ID, ""); errRefresh != nil {
		t.Fatalf("refreshAuthForRequest: %v", errRefresh)
	}
	if executor.refreshProxy != "" {
		t.Fatalf("refresh proxy = %q, want credential or global proxy only", executor.refreshProxy)
	}
}
