package auth

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type streamLeakTestExecutor struct {
	err error
}

func (e *streamLeakTestExecutor) Identifier() string { return "stream-leak" }

func (e *streamLeakTestExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (e *streamLeakTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Err: e.err}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (e *streamLeakTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *streamLeakTestExecutor) Refresh(context.Context, *Auth) (*Auth, error) { return nil, nil }

func (e *streamLeakTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

// TestStreamErrorRedactsQuotedJSONSecretInRecord verifies that an in-band stream
// error containing a non-sk secret in quoted JSON is sanitized before it reaches
// the output stream and the persisted auth result.
func TestStreamErrorRedactsQuotedJSONSecretInRecord(t *testing.T) {
	const model = "leak-model"
	auth := &Auth{ID: "leak-auth", Provider: "stream-leak", Status: StatusActive}

	exec := &streamLeakTestExecutor{err: &Error{
		Code:       "sk-live-test12345",
		HTTPStatus: http.StatusUnauthorized,
		Message:    `{"apiKey":"AIza-secret"}`,
	}}

	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(exec)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "stream-leak", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	_, err := m.Register(context.Background(), auth)
	if err != nil {
		t.Fatalf("register auth: %v", err)
	}

	stream, errStream := m.ExecuteStream(context.Background(), []string{"stream-leak"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errStream != nil {
		t.Fatalf("ExecuteStream() unexpected error = %v", errStream)
	}

	var emittedErr error
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			emittedErr = chunk.Err
			break
		}
	}
	if emittedErr == nil {
		t.Fatal("stream emitted no error chunk")
	}
	if strings.Contains(emittedErr.Error(), "AIza-secret") || strings.Contains(emittedErr.Error(), "sk-live-test12345") {
		t.Fatalf("emitted error leaks secrets: %q", emittedErr.Error())
	}
	if !strings.Contains(emittedErr.Error(), "REDACTED") {
		t.Fatalf("emitted error did not redact secret: %q", emittedErr.Error())
	}

	stored := m.auths[auth.ID]
	if stored == nil || stored.LastError == nil {
		t.Fatal("auth.LastError is nil, want recorded result")
	}
	if strings.Contains(stored.LastError.Message, "AIza-secret") {
		t.Fatalf("auth.LastError.Message leaks quoted JSON secret: %q", stored.LastError.Message)
	}
	if strings.Contains(stored.LastError.Code, "sk-live-test12345") {
		t.Fatalf("auth.LastError.Code leaks provider code secret: %q", stored.LastError.Code)
	}
	if !strings.Contains(stored.LastError.Message, "REDACTED") {
		t.Fatalf("auth.LastError.Message did not redact secret: %q", stored.LastError.Message)
	}
}
