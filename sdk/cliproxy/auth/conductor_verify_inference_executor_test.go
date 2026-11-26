package auth

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type verifyInferenceStubExecutor struct {
	id          string
	streamCalls atomic.Int32
}

func (e *verifyInferenceStubExecutor) Identifier() string { return e.id }

func (e *verifyInferenceStubExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *verifyInferenceStubExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.streamCalls.Add(1)
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{
		Payload: []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\"}}\n\n"),
	}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *verifyInferenceStubExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *verifyInferenceStubExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *verifyInferenceStubExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestVerifyInference_ResolvesExecutorViaExecutorKeyFromAuth(t *testing.T) {
	tests := []struct {
		name            string
		executorID      string
		extraExecutorID string
		auth            *Auth
		verifyModel     string
	}{
		{
			name:       "openai-compatibility with compat_name and provider_key",
			executorID: "openai-compatible-custom",
			auth: &Auth{
				ID:       "compat-auth-1",
				Provider: "openai-compatibility",
				Attributes: map[string]string{
					"compat_name":  "custom",
					"provider_key": "custom",
				},
			},
			verifyModel: "gpt-4o-mini",
		},
		{
			name:            "openai-compatibility prefers namespaced executor over baseline",
			executorID:      "openai-compatible-custom",
			extraExecutorID: "openai-compatibility",
			auth: &Auth{
				ID:       "compat-auth-2",
				Provider: "openai-compatibility",
				Attributes: map[string]string{
					"compat_name":  "custom",
					"provider_key": "custom",
				},
			},
			verifyModel: "gpt-4o-mini",
		},
		{
			name:       "openai-compatibility with label fallback",
			executorID: "openai-compatible-labeled",
			auth: &Auth{
				ID:       "compat-auth-3",
				Provider: "openai-compatibility",
				Label:    "labeled",
			},
			verifyModel: "gpt-4o-mini",
		},
		{
			name:       "kimi.ai resolves kimi executor",
			executorID: "kimi",
			auth: &Auth{
				ID:       "kimi-ai-dot-1",
				Provider: "kimi.ai",
			},
			verifyModel: "kimi-k2",
		},
		{
			name:       "kimi.com resolves kimi executor",
			executorID: "kimi",
			auth: &Auth{
				ID:       "kimi-com-1",
				Provider: "kimi.com",
			},
			verifyModel: "kimi-k2",
		},
		{
			name:       "kimi-ai resolves kimi executor",
			executorID: "kimi",
			auth: &Auth{
				ID:       "kimi-ai-dash-1",
				Provider: "kimi-ai",
			},
			verifyModel: "kimi-k2",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			manager := NewManager(nil, &RoundRobinSelector{}, nil)
			execStub := &verifyInferenceStubExecutor{id: tc.executorID}
			manager.RegisterExecutor(execStub)

			var extraStub *verifyInferenceStubExecutor
			if tc.extraExecutorID != "" {
				extraStub = &verifyInferenceStubExecutor{id: tc.extraExecutorID}
				manager.RegisterExecutor(extraStub)
			}

			if _, errRegister := manager.Register(ctx, tc.auth); errRegister != nil {
				t.Fatalf("Register() error = %v", errRegister)
			}

			verified, errVerify := manager.VerifyInference(ctx, tc.auth.ID, tc.verifyModel)
			if errVerify != nil {
				t.Fatalf("VerifyInference() error = %v, want nil", errVerify)
			}
			if verified == nil || !verified.InferenceVerified {
				t.Fatalf("VerifyInference() returned %+v, want InferenceVerified=true", verified)
			}
			if got := execStub.streamCalls.Load(); got != 1 {
				t.Fatalf("ExecuteStream calls = %d, want 1", got)
			}
			if extraStub != nil && extraStub.streamCalls.Load() != 0 {
				t.Fatalf("extra executor %q ExecuteStream calls = %d, want 0", extraStub.id, extraStub.streamCalls.Load())
			}
		})
	}
}
