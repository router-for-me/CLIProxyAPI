package auth

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"golang.org/x/net/http2"
)

func protocolRoundTripError() error {
	return &url.Error{Op: "Post", URL: "https://chatgpt.com/backend-api/codex/responses", Err: http2.StreamError{StreamID: 1, Code: http2.ErrCodeProtocol, Cause: errors.New("received from peer")}}
}

func TestHTTP2ProtocolErrorOnlyRetriesBeforeResponse(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want bool
	}{
		{"POST round trip", protocolRoundTripError(), true},
		{"body read after headers", http2.StreamError{StreamID: 1, Code: http2.ErrCodeProtocol}, false},
		{"plain message after headers", errors.New("stream error: stream ID 1; PROTOCOL_ERROR; received from peer"), false},
		{"canceled request", errors.Join(protocolRoundTripError(), context.Canceled), false},
		{"caller deadline", errors.Join(protocolRoundTripError(), context.DeadlineExceeded), false},
		{"real unauthorized", &Error{HTTPStatus: http.StatusUnauthorized, Message: protocolRoundTripError().Error()}, false},
		{"real quota", &Error{HTTPStatus: http.StatusTooManyRequests, Message: protocolRoundTripError().Error()}, false},
		{"real upstream status", &Error{HTTPStatus: http.StatusServiceUnavailable, Message: protocolRoundTripError().Error()}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTransientTransportError(tt.err); got != tt.want {
				t.Fatalf("transient = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestHTTP2ProtocolRoundTripRecoveryWithoutCooldown(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "nonstream"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			m.SetRetryConfig(1, 0, 0)
			executor := &transportThenSuccessExecutor{identifier: "codex", fail: protocolRoundTripError()}
			m.RegisterExecutor(executor)
			model, authID := "protocol-model-"+uuid.NewString(), "protocol-auth-"+uuid.NewString()
			registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
			if _, err := m.Register(context.Background(), &Auth{ID: authID, Provider: "codex"}); err != nil {
				t.Fatal(err)
			}
			if stream {
				result, err := m.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				if err != nil {
					t.Fatal(err)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
				}
			} else if _, err := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); err != nil {
				t.Fatal(err)
			}
			if executor.callCount() != 2 {
				t.Fatalf("calls = %d, want 2", executor.callCount())
			}
			assertNoCooldown(t, m, authID, model)
		})
	}
}

func TestHTTP2ProtocolRoundTripRetryBound(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(3, 0, 0)
	executor := &alwaysFailExecutor{identifier: "codex", fail: protocolRoundTripError()}
	m.RegisterExecutor(executor)
	model, authID := "protocol-model-"+uuid.NewString(), "protocol-auth-"+uuid.NewString()
	registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	if _, err := m.Register(context.Background(), &Auth{ID: authID, Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	_, err := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if !errors.Is(err, executor.fail) || executor.callCount() != 4 {
		t.Fatalf("error = %v, calls = %d; want original failure after four attempts", err, executor.callCount())
	}
	assertNoCooldown(t, m, authID, model)
}
