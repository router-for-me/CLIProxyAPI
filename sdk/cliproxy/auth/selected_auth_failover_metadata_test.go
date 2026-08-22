package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"

	registry "github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

const failoverMetadataModel = "failover-metadata-model"

type failoverMetadataCall struct {
	authID    string
	authIndex string
	metaID    string
	metaIndex string
}

// failoverMetadataExecutor fails the first attempt so the conductor rotates to
// the next credential, and records for every attempt which auth it received
// together with the selected-auth metadata carried by the execution options.
type failoverMetadataExecutor struct {
	id string

	mu    sync.Mutex
	calls []failoverMetadataCall
}

func (e *failoverMetadataExecutor) Identifier() string { return e.id }

func (e *failoverMetadataExecutor) record(auth *Auth, opts cliproxyexecutor.Options) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	metaID, _ := opts.Metadata[cliproxyexecutor.SelectedAuthMetadataKey].(string)
	metaIndex, _ := opts.Metadata[cliproxyexecutor.SelectedAuthIndexMetadataKey].(string)
	e.calls = append(e.calls, failoverMetadataCall{
		authID:    auth.ID,
		authIndex: auth.EnsureIndex(),
		metaID:    metaID,
		metaIndex: metaIndex,
	})
	if len(e.calls) == 1 {
		return &Error{HTTPStatus: http.StatusInternalServerError, Message: "boom"}
	}
	return nil
}

func (e *failoverMetadataExecutor) Calls() []failoverMetadataCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]failoverMetadataCall, len(e.calls))
	copy(out, e.calls)
	return out
}

func (e *failoverMetadataExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if err := e.record(auth, opts); err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (e *failoverMetadataExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if err := e.record(auth, opts); err != nil {
		return nil, err
	}
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(auth.ID)}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (e *failoverMetadataExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *failoverMetadataExecutor) CountTokens(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if err := e.record(auth, opts); err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (e *failoverMetadataExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

// failoverMetadataProbe captures what the after-auth interceptor observed and
// how often the selected-auth callbacks fired.
type failoverMetadataProbe struct {
	mu               sync.Mutex
	interceptedIDs   []string
	interceptedIndex []string
	callbackIDs      []string
	callbackIndexes  []string
}

func (p *failoverMetadataProbe) observeIntercept(meta map[string]any) {
	id, _ := meta[cliproxyexecutor.SelectedAuthMetadataKey].(string)
	index, _ := meta[cliproxyexecutor.SelectedAuthIndexMetadataKey].(string)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.interceptedIDs = append(p.interceptedIDs, id)
	p.interceptedIndex = append(p.interceptedIndex, index)
}

func (p *failoverMetadataProbe) snapshot() ([]string, []string, []string, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.interceptedIDs...),
		append([]string(nil), p.interceptedIndex...),
		append([]string(nil), p.callbackIDs...),
		append([]string(nil), p.callbackIndexes...)
}

func newFailoverMetadataOptions() (cliproxyexecutor.Options, *failoverMetadataProbe) {
	probe := &failoverMetadataProbe{}
	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.SelectedAuthCallbackMetadataKey: func(authID string) {
				probe.mu.Lock()
				probe.callbackIDs = append(probe.callbackIDs, authID)
				probe.mu.Unlock()
			},
			cliproxyexecutor.SelectedAuthIndexCallbackMetadataKey: func(authIndex string) {
				probe.mu.Lock()
				probe.callbackIndexes = append(probe.callbackIndexes, authIndex)
				probe.mu.Unlock()
			},
		},
		RequestAfterAuthInterceptor: func(_ context.Context, req cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
			probe.observeIntercept(req.Metadata)
			return cliproxyexecutor.RequestAfterAuthInterceptResponse{}
		},
	}
	return opts, probe
}

func newFailoverMetadataManager(t *testing.T, prefix string) (*Manager, *failoverMetadataExecutor) {
	t.Helper()

	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(0, 0, 0)
	executor := &failoverMetadataExecutor{id: "claude"}
	m.RegisterExecutor(executor)

	ids := []string{prefix + "-auth-1", prefix + "-auth-2"}
	reg := registry.GetGlobalRegistry()
	for _, id := range ids {
		reg.RegisterClient(id, "claude", []*registry.ModelInfo{{ID: failoverMetadataModel}})
	}
	t.Cleanup(func() {
		for _, id := range ids {
			reg.UnregisterClient(id)
		}
	})
	for _, id := range ids {
		auth := &Auth{ID: id, Provider: "claude", FileName: id + ".json", Status: StatusActive}
		if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", id, errRegister)
		}
	}
	return m, executor
}

func assertFailoverMetadata(t *testing.T, executor *failoverMetadataExecutor, probe *failoverMetadataProbe) {
	t.Helper()

	calls := executor.Calls()
	if len(calls) != 2 {
		t.Fatalf("executor attempts = %d, want 2 (failover to the second credential)", len(calls))
	}
	if calls[0].authID == calls[1].authID {
		t.Fatalf("failover reused auth %q for both attempts", calls[0].authID)
	}
	for i, call := range calls {
		if call.metaID != call.authID {
			t.Fatalf("attempt %d: execution metadata %s = %q, want %q (stale selected auth reaches after-auth plugins)",
				i, cliproxyexecutor.SelectedAuthMetadataKey, call.metaID, call.authID)
		}
		if call.authIndex == "" {
			t.Fatalf("attempt %d: auth index is empty, cannot verify %s", i, cliproxyexecutor.SelectedAuthIndexMetadataKey)
		}
		if call.metaIndex != call.authIndex {
			t.Fatalf("attempt %d: execution metadata %s = %q, want %q",
				i, cliproxyexecutor.SelectedAuthIndexMetadataKey, call.metaIndex, call.authIndex)
		}
	}

	interceptedIDs, interceptedIndexes, callbackIDs, callbackIndexes := probe.snapshot()
	if len(interceptedIDs) != len(calls) {
		t.Fatalf("after-auth interceptor invocations = %d, want %d", len(interceptedIDs), len(calls))
	}
	for i, call := range calls {
		if interceptedIDs[i] != call.authID {
			t.Fatalf("attempt %d: after-auth interceptor saw %s = %q, want %q",
				i, cliproxyexecutor.SelectedAuthMetadataKey, interceptedIDs[i], call.authID)
		}
		if interceptedIndexes[i] != call.authIndex {
			t.Fatalf("attempt %d: after-auth interceptor saw %s = %q, want %q",
				i, cliproxyexecutor.SelectedAuthIndexMetadataKey, interceptedIndexes[i], call.authIndex)
		}
	}

	if len(callbackIDs) != len(calls) {
		t.Fatalf("selected auth callback fired %d times, want %d (exactly once per attempt)", len(callbackIDs), len(calls))
	}
	if len(callbackIndexes) != len(calls) {
		t.Fatalf("selected auth index callback fired %d times, want %d (exactly once per attempt)", len(callbackIndexes), len(calls))
	}
	for i, call := range calls {
		if callbackIDs[i] != call.authID {
			t.Fatalf("attempt %d: selected auth callback got %q, want %q", i, callbackIDs[i], call.authID)
		}
		if callbackIndexes[i] != call.authIndex {
			t.Fatalf("attempt %d: selected auth index callback got %q, want %q", i, callbackIndexes[i], call.authIndex)
		}
	}
}

// TestSelectedAuthMetadataFollowsFailover proves that after the first credential
// fails, the cloned per-attempt options handed to the after-auth interceptor and
// to the executor carry the selected auth of the current attempt, not the one of
// the previous attempt, and that the selected-auth callbacks fire exactly once
// per attempt.
func TestSelectedAuthMetadataFollowsFailover(t *testing.T) {
	req := cliproxyexecutor.Request{Model: failoverMetadataModel}
	testCases := []struct {
		name   string
		invoke func(*testing.T, *Manager, cliproxyexecutor.Options)
	}{
		{
			name: "execute",
			invoke: func(t *testing.T, m *Manager, opts cliproxyexecutor.Options) {
				if _, errExecute := m.Execute(context.Background(), []string{"claude"}, req, opts); errExecute != nil {
					t.Fatalf("Execute() error = %v", errExecute)
				}
			},
		},
		{
			name: "execute_count",
			invoke: func(t *testing.T, m *Manager, opts cliproxyexecutor.Options) {
				if _, errExecute := m.ExecuteCount(context.Background(), []string{"claude"}, req, opts); errExecute != nil {
					t.Fatalf("ExecuteCount() error = %v", errExecute)
				}
			},
		},
		{
			name: "execute_stream",
			invoke: func(t *testing.T, m *Manager, opts cliproxyexecutor.Options) {
				result, errExecute := m.ExecuteStream(context.Background(), []string{"claude"}, req, opts)
				if errExecute != nil {
					t.Fatalf("ExecuteStream() error = %v", errExecute)
				}
				if result != nil {
					for range result.Chunks {
					}
				}
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			manager, executor := newFailoverMetadataManager(t, "failover-metadata-"+tc.name)
			opts, probe := newFailoverMetadataOptions()
			tc.invoke(t, manager, opts)
			assertFailoverMetadata(t, executor, probe)
		})
	}
}
