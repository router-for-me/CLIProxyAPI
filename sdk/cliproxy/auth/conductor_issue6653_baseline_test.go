package auth

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	executor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type issue6653BaselineExecutor struct {
	calls     atomic.Int32
	refreshes atomic.Int32
}

func (*issue6653BaselineExecutor) Identifier() string { return "qa-native-6653" }
func (e *issue6653BaselineExecutor) Execute(_ context.Context, a *Auth, _ executor.Request, _ executor.Options) (executor.Response, error) {
	e.calls.Add(1)
	return executor.Response{Payload: []byte(authAccessToken(a))}, nil
}
func (e *issue6653BaselineExecutor) ExecuteStream(ctx context.Context, a *Auth, req executor.Request, opts executor.Options) (*executor.StreamResult, error) {
	resp, err := e.Execute(ctx, a, req, opts)
	if err != nil {
		return nil, err
	}
	chunks := make(chan executor.StreamChunk, 1)
	chunks <- executor.StreamChunk{Payload: resp.Payload}
	close(chunks)
	return &executor.StreamResult{Chunks: chunks}, nil
}
func (e *issue6653BaselineExecutor) CountTokens(ctx context.Context, a *Auth, req executor.Request, opts executor.Options) (executor.Response, error) {
	return e.Execute(ctx, a, req, opts)
}
func (e *issue6653BaselineExecutor) Refresh(_ context.Context, a *Auth) (*Auth, error) {
	e.refreshes.Add(1)
	updated := a.Clone()
	updated.Metadata["access_token"] = "qa-rotated-access"
	updated.Metadata["refresh_token"] = "qa-rotated-refresh"
	updated.Metadata["expires_at"] = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
	return updated, nil
}
func (*issue6653BaselineExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestIssue6653BaselineExpiredSelectionGate(t *testing.T) {
	for _, path := range []string{"buffered-chat", "streaming-chat", "image-dispatch", "count"} {
		for _, expired := range []bool{false, true} {
			name := path + "/valid"
			if expired {
				name = path + "/expired"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				e := &issue6653BaselineExecutor{}
				m := NewManager(nil, &FillFirstSelector{}, nil)
				m.RegisterExecutor(e)
				id := "qa-6653-" + path
				exp := time.Now().Add(time.Hour)
				if expired {
					exp = time.Now().Add(-time.Hour)
				}
				a := &Auth{ID: id, Provider: e.Identifier(), Status: StatusActive, Metadata: map[string]any{
					"access_token": "qa-disposable-access", "refresh_token": "qa-disposable-refresh", "expires_at": exp.Format(time.RFC3339Nano),
				}}
				if _, err := m.Register(ctx, a); err != nil {
					t.Fatal(err)
				}
				reg := registry.GetGlobalRegistry()
				reg.RegisterClient(id, e.Identifier(), []*registry.ModelInfo{{ID: "qa-model-6653"}})
				t.Cleanup(func() { reg.UnregisterClient(id) })
				req := executor.Request{Model: "qa-model-6653"}
				opts := executor.Options{}
				var err error
				switch path {
				case "streaming-chat":
					var stream *executor.StreamResult
					stream, err = m.ExecuteStream(ctx, []string{e.Identifier()}, req, opts)
					if stream != nil {
						for chunk := range stream.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
						}
					}
				case "count":
					_, err = m.ExecuteCount(ctx, []string{e.Identifier()}, req, opts)
				default:
					if path == "image-dispatch" {
						opts.Metadata = map[string]any{executor.RequestPathMetadataKey: "/v1/images/generations"}
					}
					_, err = m.Execute(ctx, []string{e.Identifier()}, req, opts)
				}
				if expired {
					if err == nil || e.calls.Load() != 0 || e.refreshes.Load() != 0 {
						t.Fatalf("expired: err=%v calls=%d refreshes=%d; want selection failure and zero callbacks", err, e.calls.Load(), e.refreshes.Load())
					}
					t.Logf("expired credential rejected before executor: %v; inference=%d refresh=%d", err, e.calls.Load(), e.refreshes.Load())
				} else if err != nil || e.calls.Load() != 1 || e.refreshes.Load() != 0 {
					t.Fatalf("valid: err=%v calls=%d refreshes=%d", err, e.calls.Load(), e.refreshes.Load())
				}
			})
		}
	}
}

func TestIssue6653BaselineExpiryBoundary(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		delta time.Duration
		want  bool
	}{
		{"before-expiry", time.Nanosecond, true}, {"at-expiry", 0, false}, {"after-expiry", -time.Nanosecond, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Auth{Provider: "qa-native-6653", Metadata: map[string]any{"access_token": "qa-access", "refresh_token": "qa-refresh", "expires_at": now.Add(tc.delta).Format(time.RFC3339Nano)}}
			if got := IsAuthAvailableForModel(a, "", now); got != tc.want {
				t.Fatalf("available=%v want %v", got, tc.want)
			}
		})
	}
}

func TestIssue6653BaselineRequestRefreshCoalesces(t *testing.T) {
	ctx := context.Background()
	e := &issue6653BaselineExecutor{}
	m := NewManager(nil, &FillFirstSelector{}, nil)
	m.RegisterExecutor(e)
	a := &Auth{ID: "qa-coalesce-6653", Provider: e.Identifier(), Status: StatusActive, Metadata: map[string]any{
		"access_token": "qa-disposable-access", "refresh_token": "qa-disposable-refresh", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339Nano),
	}}
	if _, err := m.Register(ctx, a); err != nil {
		t.Fatal(err)
	}
	const callers = 32
	start := make(chan struct{})
	results := make(chan error, callers)
	for range callers {
		go func() {
			<-start
			updated, err := m.refreshAuthForRequest(ctx, a.ID, "qa-disposable-access")
			if err == nil && (updated == nil || authAccessToken(updated) != "qa-rotated-access") {
				err = fmt.Errorf("waiter received stale token")
			}
			results <- err
		}()
	}
	close(start)
	for range callers {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if got := e.refreshes.Load(); got != 1 {
		t.Fatalf("refresh calls=%d want 1", got)
	}
	t.Log("32 same-token request refreshes coalesced to one rotation and all callers received new token")
}
