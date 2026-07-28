package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type selectedAuthFormatExecutor struct {
	id string

	mu      sync.Mutex
	authIDs []string
}

func (e *selectedAuthFormatExecutor) Identifier() string { return e.id }

func (*selectedAuthFormatExecutor) RequestToFormat(cliproxyexecutor.Request, cliproxyexecutor.Options) sdktranslator.Format {
	return sdktranslator.FormatGemini
}

func (e *selectedAuthFormatExecutor) RequestToFormatWithAuth(auth *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) sdktranslator.Format {
	if auth != nil {
		e.mu.Lock()
		e.authIDs = append(e.authIDs, auth.ID)
		e.mu.Unlock()
	}
	if auth != nil && auth.Attributes["wire_api"] == "responses" && opts.SourceFormat == sdktranslator.FormatOpenAIResponse {
		return sdktranslator.FormatOpenAIResponse
	}
	return sdktranslator.FormatOpenAI
}

func (*selectedAuthFormatExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (*selectedAuthFormatExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"type":"response.completed"}`)}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (*selectedAuthFormatExecutor) Refresh(context.Context, *Auth) (*Auth, error) { return nil, nil }

func (*selectedAuthFormatExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte(`{"usage":{"total_tokens":1}}`)}, nil
}

func (*selectedAuthFormatExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *selectedAuthFormatExecutor) selectedAuthIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.authIDs...)
}

func TestRequestToFormatPrefersSelectedAuthResolver(t *testing.T) {
	exec := &selectedAuthFormatExecutor{id: "format-auth"}
	auth := &Auth{ID: "selected-auth", Attributes: map[string]string{"wire_api": "responses"}}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
	if got := requestToFormat(exec.Identifier(), exec, auth, cliproxyexecutor.Request{}, opts); got != sdktranslator.FormatOpenAIResponse {
		t.Fatalf("requestToFormat() = %q, want %q", got, sdktranslator.FormatOpenAIResponse)
	}
	if got := exec.selectedAuthIDs(); len(got) != 1 || got[0] != auth.ID {
		t.Fatalf("selected auth IDs = %v, want [%s]", got, auth.ID)
	}
}

func TestManagerRequestAfterAuthInterceptorUsesSelectedAuthForFormat(t *testing.T) {
	const (
		provider = "format-auth-manager"
		model    = "format-model"
	)
	exec := &selectedAuthFormatExecutor{id: provider}
	auth := &Auth{ID: "selected-manager-auth", Provider: provider, Status: StatusActive, Attributes: map[string]string{"wire_api": "responses"}}
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(exec)
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	globalRegistry := registry.GetGlobalRegistry()
	globalRegistry.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { globalRegistry.UnregisterClient(auth.ID) })
	manager.RefreshSchedulerEntry(auth.ID)

	var mu sync.Mutex
	var formats []sdktranslator.Format
	interceptor := func(_ context.Context, request cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
		mu.Lock()
		formats = append(formats, request.ToFormat)
		mu.Unlock()
		return cliproxyexecutor.RequestAfterAuthInterceptResponse{}
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, RequestAfterAuthInterceptor: interceptor}
	req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"model":"format-model","input":"hi"}`)}

	if _, errExecute := manager.Execute(context.Background(), []string{provider}, req, opts); errExecute != nil {
		t.Fatalf("Execute error: %v", errExecute)
	}
	if _, errCount := manager.ExecuteCount(context.Background(), []string{provider}, req, opts); errCount != nil {
		t.Fatalf("ExecuteCount error: %v", errCount)
	}
	streamResult, errStream := manager.ExecuteStream(context.Background(), []string{provider}, req, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream error: %v", errStream)
	}
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
	}

	mu.Lock()
	gotFormats := append([]sdktranslator.Format(nil), formats...)
	mu.Unlock()
	if len(gotFormats) != 3 {
		t.Fatalf("interceptor formats = %v, want three calls", gotFormats)
	}
	for _, format := range gotFormats {
		if format != sdktranslator.FormatOpenAIResponse {
			t.Fatalf("interceptor format = %q, want %q; all=%v", format, sdktranslator.FormatOpenAIResponse, gotFormats)
		}
	}
	if gotAuthIDs := exec.selectedAuthIDs(); len(gotAuthIDs) != 3 {
		t.Fatalf("selected auth IDs = %v, want three calls", gotAuthIDs)
	} else {
		for _, authID := range gotAuthIDs {
			if authID != auth.ID {
				t.Fatalf("selected auth ID = %q, want %q", authID, auth.ID)
			}
		}
	}
}

type selectedAuthFormatHomeDispatcher struct{}

func (selectedAuthFormatHomeDispatcher) HeartbeatOK() bool { return true }

func (selectedAuthFormatHomeDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	return json.Marshal(homeAuthDispatchResponse{Auth: Auth{
		ID:         "selected-home-auth",
		Provider:   "format-auth-home",
		Status:     StatusActive,
		Attributes: map[string]string{"wire_api": "responses"},
	}})
}

func (selectedAuthFormatHomeDispatcher) AbortAmbiguousDispatch() {}

func TestHomeRequestAfterAuthInterceptorUsesSelectedAuthForFormat(t *testing.T) {
	exec := &selectedAuthFormatExecutor{id: "format-auth-home"}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(selectedAuthFormatHomeDispatcher{}, executionregistry.New(), 1)
	manager.RegisterExecutor(exec)

	var gotFormat sdktranslator.Format
	interceptor := func(_ context.Context, request cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
		gotFormat = request.ToFormat
		return cliproxyexecutor.RequestAfterAuthInterceptResponse{}
	}
	_, errExecute := manager.Execute(context.Background(), []string{exec.Identifier()}, cliproxyexecutor.Request{
		Model: "format-model", Payload: []byte(`{"model":"format-model","input":"hi"}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, RequestAfterAuthInterceptor: interceptor})
	if errExecute != nil {
		t.Fatalf("Execute error: %v", errExecute)
	}
	if gotFormat != sdktranslator.FormatOpenAIResponse {
		t.Fatalf("Home interceptor format = %q, want %q", gotFormat, sdktranslator.FormatOpenAIResponse)
	}
	if gotAuthIDs := exec.selectedAuthIDs(); len(gotAuthIDs) != 1 || gotAuthIDs[0] != "selected-home-auth" {
		t.Fatalf("Home selected auth IDs = %v, want [selected-home-auth]", gotAuthIDs)
	}
}
