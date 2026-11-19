package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// This structural method deliberately compiles before the optional SDK contract
// exists. Request scope remains true before and after that contract is added.
type sameAuthRefreshError struct {
	status int
	allow  bool
}

func (sameAuthRefreshError) Error() string                { return "synthetic selected request failed" }
func (e sameAuthRefreshError) StatusCode() int            { return e.status }
func (sameAuthRefreshError) IsRequestScoped() bool        { return true }
func (e sameAuthRefreshError) AllowSameAuthRefresh() bool { return e.allow }

type sameAuthUnmarkedError struct{ status int }

func (sameAuthUnmarkedError) Error() string         { return "synthetic selected request failed" }
func (e sameAuthUnmarkedError) StatusCode() int     { return e.status }
func (sameAuthUnmarkedError) IsRequestScoped() bool { return true }

type sameAuthRefreshFixture struct {
	manager                   *Manager
	primary                   *Auth
	providers                 []string
	model, mode, method       string
	mu                        sync.Mutex
	primaryCalls, otherCalls  int
	refreshes, invalidRefresh int
}

type sameAuthRefreshExecutor struct {
	id string
	f  *sameAuthRefreshFixture
}

func (e *sameAuthRefreshExecutor) Identifier() string { return e.id }
func (e *sameAuthRefreshExecutor) Execute(_ context.Context, a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	err := e.f.dispatch(a)
	return cliproxyexecutor.Response{Payload: []byte("synthetic-ok")}, err
}
func (e *sameAuthRefreshExecutor) CountTokens(ctx context.Context, a *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.Execute(ctx, a, req, opts)
}
func (e *sameAuthRefreshExecutor) ExecuteStream(_ context.Context, a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	err := e.f.dispatch(a)
	if err != nil && e.f.method == "stream_open" {
		return nil, err
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 2)
	if e.f.mode == "visible" {
		chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("synthetic-ok")}
		chunks <- cliproxyexecutor.StreamChunk{Err: sameAuthRefreshError{status: 401, allow: true}}
	} else if err != nil {
		chunks <- cliproxyexecutor.StreamChunk{Err: err}
	} else {
		chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("synthetic-ok")}
	}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}
func (e *sameAuthRefreshExecutor) Refresh(_ context.Context, a *Auth) (*Auth, error) {
	f := e.f
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshes++
	if a == nil || a.ID != f.primary.ID || a.Provider != f.primary.Provider || a.RegistrationEpoch != f.primary.RegistrationEpoch || authAccessToken(a) != "synthetic-old" {
		f.invalidRefresh++
		return nil, errors.New("synthetic refresh refused")
	}
	if f.mode == "refresh_failed" || f.mode == "ordinary_failed" {
		return nil, errors.New("synthetic refresh refused")
	}
	next := a.Clone()
	next.Metadata["access_token"] = "synthetic-rotated"
	return next, nil
}
func (*sameAuthRefreshExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("synthetic HTTP path refused")
}

func (f *sameAuthRefreshFixture) dispatch(a *Auth) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a == nil || a.ID != f.primary.ID || a.Provider != f.primary.Provider || a.RegistrationEpoch != f.primary.RegistrationEpoch {
		f.otherCalls++
		return nil
	}
	f.primaryCalls++
	if f.mode == "visible" {
		return nil
	}
	status := http.StatusUnauthorized
	if f.mode == "non_401" || (authAccessToken(a) == "synthetic-rotated" && f.mode == "retry_503") {
		status = http.StatusServiceUnavailable
	}
	if authAccessToken(a) == "synthetic-rotated" && f.mode != "retry_401" && f.mode != "retry_503" {
		return nil
	}
	if f.mode == "unmarked" {
		return sameAuthUnmarkedError{status: status}
	}
	if f.mode == "ordinary_failed" {
		return &Error{HTTPStatus: 401, Message: "synthetic selected request failed"}
	}
	return sameAuthRefreshError{status: status, allow: f.mode != "marker_false"}
}

func newSameAuthRefreshFixture(t *testing.T, method, mode string) *sameAuthRefreshFixture {
	t.Helper()
	f := &sameAuthRefreshFixture{method: method, mode: mode, model: "synthetic-refresh-model", providers: []string{"synthetic-refresh-primary", "synthetic-refresh-alternate"}}
	f.manager = NewManager(nil, &FillFirstSelector{}, nil)
	f.manager.SetRetryConfig(1, 0, 0)
	for _, provider := range f.providers {
		f.manager.RegisterExecutor(&sameAuthRefreshExecutor{id: provider, f: f})
	}
	reg := registry.GetGlobalRegistry()
	for i, suffix := range []string{"-a", "-b", "-c"} {
		provider := f.providers[0]
		if i == 2 {
			provider = f.providers[1]
		}
		a := &Auth{ID: t.Name() + suffix, Provider: provider, Status: StatusActive,
			Attributes: map[string]string{"priority": "0"},
			Metadata:   map[string]any{"access_token": "synthetic-old", "refresh_token": "synthetic-grant", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}}
		if i == 0 {
			a.Attributes["priority"] = "100"
			if mode == "no_grant" {
				delete(a.Metadata, "refresh_token")
			}
			if mode == "configured_rule" {
				a.Metadata["request_scoped_errors"] = []internalconfig.RequestScopedErrorRule{{Status: 401, Match: []string{"synthetic selected"}, Action: RequestScopedActionStop}}
			}
		}
		registered, err := f.manager.Register(WithSkipPersist(t.Context()), a)
		if err != nil || registered == nil {
			t.Fatal("same auth refresh setup")
		}
		reg.RegisterClient(a.ID, provider, []*registry.ModelInfo{{ID: f.model}})
		t.Cleanup(func() { reg.UnregisterClient(a.ID) })
		if i == 0 {
			f.primary = registered.Clone()
		}
	}
	return f
}

func (f *sameAuthRefreshFixture) run(t *testing.T) (bool, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(WithSkipPersist(t.Context()), 3*time.Second)
	defer cancel()
	req := cliproxyexecutor.Request{Model: f.model}
	if f.method == "execute" || f.method == "count" {
		var response cliproxyexecutor.Response
		var err error
		if f.method == "execute" {
			response, err = f.manager.Execute(ctx, f.providers, req, cliproxyexecutor.Options{})
		} else {
			response, err = f.manager.ExecuteCount(ctx, f.providers, req, cliproxyexecutor.Options{})
		}
		return err == nil && string(response.Payload) == "synthetic-ok", err
	}
	stream, err := f.manager.ExecuteStream(ctx, f.providers, req, cliproxyexecutor.Options{})
	if err != nil {
		return false, err
	}
	if stream == nil || stream.Chunks == nil {
		t.Fatal("same auth refresh stream")
	}
	t.Cleanup(func() {
		cancel()
		bound := time.NewTimer(3 * time.Second)
		defer bound.Stop()
		for {
			select {
			case _, ok := <-stream.Chunks:
				if !ok {
					return
				}
			case <-bound.C:
				t.Error("same auth refresh stream join")
				return
			}
		}
	})
	// Producers use closed, bounded channels. Consume the manager's wrapper to
	// completion, including after an error, before unregistering model rows.
	visible := false
	var terminal error
	for {
		select {
		case chunk, ok := <-stream.Chunks:
			if !ok {
				return visible, terminal
			}
			visible = visible || string(chunk.Payload) == "synthetic-ok"
			if chunk.Err != nil {
				terminal = chunk.Err
			}
		case <-ctx.Done():
			t.Fatal("same auth refresh stream join")
		}
	}
}

func (f *sameAuthRefreshFixture) assertCounts(t *testing.T, calls, refreshes int) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	t.Logf("same auth refresh counts: primary=%d other=%d refresh=%d invalid=%d", f.primaryCalls, f.otherCalls, f.refreshes, f.invalidRefresh)
	if f.primaryCalls != calls || f.otherCalls != 0 || f.refreshes != refreshes || f.invalidRefresh != 0 {
		t.Error("same auth refresh counts")
	}
}
