package auth

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type fastDirectResponseTestError struct {
	response *cliproxyexecutor.RequestTerminatedError
}

func (e *fastDirectResponseTestError) Error() string {
	return "Fast upstream request failed"
}

func (e *fastDirectResponseTestError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.response
}

func (e *fastDirectResponseTestError) IsRequestScoped() bool {
	return e != nil
}

func newFastDirectResponseTestError(status int, body string) error {
	return &fastDirectResponseTestError{response: &cliproxyexecutor.RequestTerminatedError{
		HTTPStatus: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       []byte(body),
	}}
}

func TestManagerFastLocalErrorDoesNotRefreshRetryOrCoolCredential(t *testing.T) {
	testCases := []struct {
		name      string
		configure func(*claudeCancellationTestExecutor, *atomic.Int32)
		run       func(*Manager, string) error
	}{
		{
			name: "non-stream",
			configure: func(executor *claudeCancellationTestExecutor, calls *atomic.Int32) {
				executor.executeFn = func(context.Context, *Auth) (cliproxyexecutor.Response, error) {
					if calls.Add(1) == 1 {
						return cliproxyexecutor.Response{}, &requestScopedStatusError{message: "decode Fast response"}
					}
					return cliproxyexecutor.Response{Payload: []byte(`{"type":"message","content":[]}`)}, nil
				}
			},
			run: func(manager *Manager, model string) error {
				_, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				return errExecute
			},
		},
		{
			name: "stream",
			configure: func(executor *claudeCancellationTestExecutor, calls *atomic.Int32) {
				executor.streamFn = func(context.Context, *Auth) (*cliproxyexecutor.StreamResult, error) {
					if calls.Add(1) == 1 {
						return nil, &requestScopedStatusError{message: "decode Fast stream response"}
					}
					chunks := make(chan cliproxyexecutor.StreamChunk, 1)
					chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("ok")}
					close(chunks)
					return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
				}
			},
			run: func(manager *Manager, model string) error {
				stream, errStream := manager.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
				if errStream != nil {
					return errStream
				}
				for range stream.Chunks {
				}
				return nil
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var calls atomic.Int32
			executor := &claudeCancellationTestExecutor{}
			testCase.configure(executor, &calls)
			manager, auth, model := newClaudeCancellationTestManager(t, executor, nil)

			errExecute := testCase.run(manager, model)
			if errExecute == nil {
				t.Fatal("first Fast request error = nil")
			}
			var direct *cliproxyexecutor.RequestTerminatedError
			if errors.As(errExecute, &direct) {
				t.Fatalf("local Fast error unexpectedly became a direct HTTP response: %v", errExecute)
			}
			if got := calls.Load(); got != 1 {
				t.Fatalf("first request upstream calls = %d, want 1", got)
			}
			if got := executor.refreshCalls.Load(); got != 0 {
				t.Fatalf("refresh calls = %d, want 0", got)
			}
			requireClaudeCancellationNeutral(t, manager, auth.ID, model)

			if errFollowUp := testCase.run(manager, model); errFollowUp != nil {
				t.Fatalf("follow-up request error = %v", errFollowUp)
			}
			if got := calls.Load(); got != 2 {
				t.Fatalf("total upstream calls = %d, want 2", got)
			}
			requireClaudeCancellationNeutral(t, manager, auth.ID, model)
		})
	}
}

func TestManagerFastDirectErrorDoesNotRefreshRetryOrCoolCredential(t *testing.T) {
	testCases := []struct {
		name      string
		configure func(*claudeCancellationTestExecutor, *atomic.Int32)
		run       func(*Manager, string) error
	}{
		{
			name: "non-stream",
			configure: func(executor *claudeCancellationTestExecutor, calls *atomic.Int32) {
				executor.executeFn = func(_ context.Context, _ *Auth) (cliproxyexecutor.Response, error) {
					if calls.Add(1) == 1 {
						return cliproxyexecutor.Response{}, newFastDirectResponseTestError(http.StatusUnauthorized, `{"type":"error","error":{"message":"Fast denied"}}`)
					}
					return cliproxyexecutor.Response{Payload: []byte(`{"type":"message","content":[]}`)}, nil
				}
			},
			run: func(manager *Manager, model string) error {
				_, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				return errExecute
			},
		},
		{
			name: "stream",
			configure: func(executor *claudeCancellationTestExecutor, calls *atomic.Int32) {
				executor.streamFn = func(_ context.Context, _ *Auth) (*cliproxyexecutor.StreamResult, error) {
					if calls.Add(1) == 1 {
						return nil, newFastDirectResponseTestError(http.StatusUnauthorized, `{"type":"error","error":{"message":"Fast denied"}}`)
					}
					chunks := make(chan cliproxyexecutor.StreamChunk, 1)
					chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("ok")}
					close(chunks)
					return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
				}
			},
			run: func(manager *Manager, model string) error {
				stream, errStream := manager.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
				if errStream != nil {
					return errStream
				}
				for range stream.Chunks {
				}
				return nil
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var calls atomic.Int32
			executor := &claudeCancellationTestExecutor{}
			testCase.configure(executor, &calls)
			manager, auth, model := newClaudeCancellationTestManager(t, executor, nil)

			errExecute := testCase.run(manager, model)
			if errExecute == nil {
				t.Fatal("first Fast request error = nil")
			}
			var direct *cliproxyexecutor.RequestTerminatedError
			if !errors.As(errExecute, &direct) || direct == nil {
				t.Fatalf("first error = %T %v, want direct response", errExecute, errExecute)
			}
			if got := direct.StatusCode(); got != http.StatusUnauthorized {
				t.Fatalf("direct status = %d, want 401", got)
			}
			if got := calls.Load(); got != 1 {
				t.Fatalf("first request upstream calls = %d, want 1", got)
			}
			if got := executor.refreshCalls.Load(); got != 0 {
				t.Fatalf("refresh calls = %d, want 0", got)
			}
			requireClaudeCancellationNeutral(t, manager, auth.ID, model)

			if errFollowUp := testCase.run(manager, model); errFollowUp != nil {
				t.Fatalf("follow-up request error = %v", errFollowUp)
			}
			if got := calls.Load(); got != 2 {
				t.Fatalf("total upstream calls = %d, want 2", got)
			}
			requireClaudeCancellationNeutral(t, manager, auth.ID, model)
		})
	}
}
