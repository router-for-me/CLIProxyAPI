package handlers

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	"github.com/tidwall/gjson"
)

type captureExecuteExecutor struct {
	payload        []byte
	original       []byte
	sourceFormat   string
	executeInvoked int
}

func (e *captureExecuteExecutor) Identifier() string { return "test-provider" }

func (e *captureExecuteExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.executeInvoked++
	e.payload = append([]byte(nil), req.Payload...)
	e.original = append([]byte(nil), opts.OriginalRequest...)
	e.sourceFormat = opts.SourceFormat.String()
	return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *captureExecuteExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *captureExecuteExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *captureExecuteExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *captureExecuteExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestExecuteWithAuthManager_NormalizesClaudeToolResultContentArrays(t *testing.T) {
	executor := &captureExecuteExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: "auth-normalize", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("manager.Register(auth): %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	input := []byte(`{
		"model":"test-model",
		"messages":[
			{
				"role":"assistant",
				"content":[
					{"type":"tool_use","id":"call_test","name":"json","input":{"ok":true}}
				]
			},
			{
				"role":"user",
				"content":[
					{
						"type":"tool_result",
						"tool_use_id":"call_test",
						"content":[
							{"type":"text","text":"alpha"},
							{"type":"image","source":{"type":"base64","media_type":"image/png"}}
						]
					}
				]
			}
		]
	}`)

	_, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "claude", "test-model", input, "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager returned error: %+v", errMsg)
	}

	if executor.executeInvoked != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.executeInvoked)
	}
	if executor.sourceFormat != "claude" {
		t.Fatalf("source format = %q, want %q", executor.sourceFormat, "claude")
	}

	gotPayload := gjson.ParseBytes(executor.payload)
	gotOriginal := gjson.ParseBytes(executor.original)
	want := `alpha` + "\n\n" + `{"type":"image","source":{"type":"base64","media_type":"image/png"}}`

	if got := gotPayload.Get("messages.1.content.0.content").String(); got != want {
		t.Fatalf("payload tool_result content = %q, want %q", got, want)
	}
	if got := gotOriginal.Get("messages.1.content.0.content").String(); got != want {
		t.Fatalf("original request tool_result content = %q, want %q", got, want)
	}
}

func TestExecuteWithAuthManager_LeavesNonClaudePayloadUntouched(t *testing.T) {
	executor := &captureExecuteExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: "auth-openai", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("manager.Register(auth): %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model-openai"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	input := []byte(`{"model":"test-model-openai","messages":[{"role":"tool","content":"keep me"}]}`)

	_, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "openai", "test-model-openai", input, "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager returned error: %+v", errMsg)
	}

	if got := string(executor.payload); got != string(input) {
		t.Fatalf("payload = %s, want %s", got, string(input))
	}
	if got := string(executor.original); got != string(input) {
		t.Fatalf("original request = %s, want %s", got, string(input))
	}
}
