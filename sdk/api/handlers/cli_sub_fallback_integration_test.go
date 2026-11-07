package handlers

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type cliSubFallbackExecutor struct {
	provider string
}

func (e cliSubFallbackExecutor) Identifier() string { return e.provider }
func (e cliSubFallbackExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{Payload: []byte(`{"id":"chatcmpl-test","model":"cursor/gpt-9-test-high","usage":{"model":"cursor/gpt-9-test-high"}}`)}, nil
}
func (e cliSubFallbackExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte("data: {\"id\":\"chunk-test\",\"model\":\"cursor/gpt-9-test-high\"}\n\n")}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}
func (e cliSubFallbackExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}
func (e cliSubFallbackExecutor) CountTokens(_ context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}
func (e cliSubFallbackExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func setupCLISubFallbackHandler(t *testing.T) *BaseAPIHandler {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	primaryID := "test-cli-sub-route-primary"
	cursorID := "test-cli-sub-route-cursor"
	t.Cleanup(func() {
		reg.UnregisterClient(primaryID)
		reg.UnregisterClient(cursorID)
	})
	reg.RegisterClient(primaryID, "openai", []*registry.ModelInfo{{ID: "gpt-9-test", OwnedBy: "openai"}})
	reg.RegisterClient(cursorID, "cursor", []*registry.ModelInfo{{ID: "cursor-gpt-9-test-high", OwnedBy: cliSubscriptionsOwner}})

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(cliSubFallbackExecutor{provider: "openai"})
	manager.RegisterExecutor(cliSubFallbackExecutor{provider: "cursor"})
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:             primaryID,
		Provider:       "openai",
		Status:         coreauth.StatusError,
		Unavailable:    true,
		NextRetryAfter: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("register primary auth: %v", err)
	}
	if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: cursorID, Provider: "cursor", Status: coreauth.StatusActive}); err != nil {
		t.Fatalf("register cursor auth: %v", err)
	}
	return NewBaseAPIHandlers(nil, manager)
}

func TestExecuteWithAuthManagerFallsBackAndPreservesWireModel(t *testing.T) {
	handler := setupCLISubFallbackHandler(t)
	body, headers, errMsg := handler.ExecuteWithAuthManager(context.Background(), "openai", "gpt-9-test", []byte(`{"model":"gpt-9-test","messages":[{"role":"user","content":"hi"}]}`), "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager() error = %v", errMsg.Error)
	}
	if got := headers.Get(cliProxyUpstreamHeader); got != "cursor/gpt-9-test-high" {
		t.Fatalf("upstream header = %q, want cursor/gpt-9-test-high", got)
	}
	want := `{"id":"chatcmpl-test","model":"gpt-9-test","usage":{"model":"gpt-9-test"}}`
	if string(body) != want {
		t.Fatalf("body = %s, want %s", body, want)
	}
}

func TestExecuteStreamWithAuthManagerFallsBackAndPreservesWireModel(t *testing.T) {
	handler := setupCLISubFallbackHandler(t)
	data, headers, errs := handler.ExecuteStreamWithAuthManager(context.Background(), "openai", "gpt-9-test", []byte(`{"model":"gpt-9-test","stream":true,"messages":[{"role":"user","content":"hi"}]}`), "")
	if got := headers.Get(cliProxyUpstreamHeader); got != "cursor/gpt-9-test-high" {
		t.Fatalf("upstream header = %q, want cursor/gpt-9-test-high", got)
	}
	var body []byte
	for chunk := range data {
		body = append(body, chunk...)
	}
	for errMsg := range errs {
		if errMsg != nil {
			t.Fatalf("stream error = %v", errMsg.Error)
		}
	}
	want := "data: {\"id\":\"chunk-test\",\"model\":\"gpt-9-test\"}\n\n"
	if string(body) != want {
		t.Fatalf("stream body = %q, want %q", body, want)
	}
}
