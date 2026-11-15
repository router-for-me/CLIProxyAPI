package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/proxx/groups"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type proxxGroupCall struct {
	model     string
	requested string
	group     any
}

type proxxRetryAfterError struct {
	status int
	after  time.Duration
}

func (e *proxxRetryAfterError) Error() string              { return "rate limited" }
func (e *proxxRetryAfterError) StatusCode() int            { return e.status }
func (e *proxxRetryAfterError) RetryAfter() *time.Duration { return &e.after }

// proxxGroupTestExecutor records every execution and fails per model.
type proxxGroupTestExecutor struct {
	mu       sync.Mutex
	calls    []proxxGroupCall
	failures map[string]error
}

func (*proxxGroupTestExecutor) Identifier() string { return "proxxtest" }

func (e *proxxGroupTestExecutor) record(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	requested, _ := opts.Metadata[cliproxyexecutor.RequestedModelMetadataKey].(string)
	e.calls = append(e.calls, proxxGroupCall{model: req.Model, requested: requested, group: opts.Metadata[ProxxModelGroupMetadataKey]})
	return e.failures[req.Model]
}

func (e *proxxGroupTestExecutor) models() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0, len(e.calls))
	for _, call := range e.calls {
		out = append(out, call.model)
	}
	return out
}

func (e *proxxGroupTestExecutor) reset() {
	e.mu.Lock()
	e.calls = nil
	e.mu.Unlock()
}

func (e *proxxGroupTestExecutor) Execute(_ context.Context, _ *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if err := e.record(req, opts); err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte("ok-" + req.Model)}, nil
}

func (e *proxxGroupTestExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.Execute(ctx, auth, req, opts)
}

func (e *proxxGroupTestExecutor) ExecuteStream(_ context.Context, _ *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	err := e.record(req, opts)
	chunks := make(chan cliproxyexecutor.StreamChunk, 2)
	if err != nil {
		// Fail inside the stream, before the first payload chunk.
		chunks <- cliproxyexecutor.StreamChunk{Err: err}
	} else {
		chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("ok-" + req.Model)}
		chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("-done")}
	}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (*proxxGroupTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (*proxxGroupTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

// newProxxGroupTestManager registers one credential per model and a group "<prefix>-group"
// over the given models in order.
func newProxxGroupTestManager(t *testing.T, models []string, failures map[string]error) (*Manager, *proxxGroupTestExecutor, string) {
	t.Helper()
	executor := &proxxGroupTestExecutor{failures: failures}
	manager := NewManager(nil, nil, NoopHook{})
	manager.SetRetryConfig(3, 60*time.Second, 0)
	manager.RegisterExecutor(executor)
	for _, model := range models {
		auth := &Auth{ID: "proxx-group-auth-" + uuid.NewString(), Provider: "proxxtest", Attributes: map[string]string{"auth_kind": "api_key"}}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
		authID := auth.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register() error = %v", errRegister)
		}
	}
	group := "proxx-test-group-" + uuid.NewString()
	groups.Apply(&config.Config{Proxx: config.ProxxConfig{ModelGroups: []config.ProxxModelGroup{{Name: group, Models: models, Hidden: true}}}})
	t.Cleanup(func() { groups.Apply(nil) })
	return manager, executor, group
}

func proxxGroupRequest(group string) (cliproxyexecutor.Request, cliproxyexecutor.Options) {
	return cliproxyexecutor.Request{Model: group, Payload: []byte(`{"model":"` + group + `"}`)},
		cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.RequestedModelMetadataKey: group}}
}

func uniqueModel(name string) string { return "proxx-" + name + "-" + uuid.NewString() }

// Regression test for the request path: each member executes with req.Model and the requested-model
// metadata (which payload rules match on) set to the member, never the group name.
func TestProxxGroupExecuteFailsOverWithMemberModel(t *testing.T) {
	failing, ok := uniqueModel("fail"), uniqueModel("ok")
	manager, executor, group := newProxxGroupTestManager(t, []string{failing, ok}, map[string]error{
		failing: &Error{Code: "upstream", Message: "boom", HTTPStatus: http.StatusInternalServerError},
	})
	req, opts := proxxGroupRequest(group)
	resp, err := manager.Execute(context.Background(), []string{groups.Provider}, req, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if string(resp.Payload) != "ok-"+ok {
		t.Fatalf("payload = %q", resp.Payload)
	}
	executor.mu.Lock()
	calls := append([]proxxGroupCall(nil), executor.calls...)
	executor.mu.Unlock()
	if len(calls) != 2 || calls[0].model != failing || calls[1].model != ok {
		t.Fatalf("calls = %+v", calls)
	}
	for _, call := range calls {
		if call.requested != call.model {
			t.Fatalf("requested-model metadata = %q, want member %q", call.requested, call.model)
		}
		if call.group != group {
			t.Fatalf("group metadata = %v", call.group)
		}
	}
	if opts.Metadata[cliproxyexecutor.RequestedModelMetadataKey] != group {
		t.Fatalf("caller metadata was mutated")
	}
}

func TestProxxGroupDoesNotFailOverOnBadRequest(t *testing.T) {
	bad, ok := uniqueModel("bad"), uniqueModel("ok")
	manager, executor, group := newProxxGroupTestManager(t, []string{bad, ok}, map[string]error{
		bad: &Error{Code: "invalid_request", Message: "invalid parameter", HTTPStatus: http.StatusBadRequest},
	})
	req, opts := proxxGroupRequest(group)
	_, err := manager.Execute(context.Background(), []string{groups.Provider}, req, opts)
	if statusCodeFromError(err) != http.StatusBadRequest {
		t.Fatalf("error = %v, want 400", err)
	}
	if got := executor.models(); len(got) != 1 || got[0] != bad {
		t.Fatalf("calls = %v", got)
	}
}

func TestProxxGroupFailsOverOnContextLengthError(t *testing.T) {
	small, big := uniqueModel("small"), uniqueModel("big")
	manager, executor, group := newProxxGroupTestManager(t, []string{small, big}, map[string]error{
		small: &Error{Code: "context_length_exceeded", Message: "This model's maximum context length is 8192 tokens", HTTPStatus: http.StatusBadRequest},
	})
	req, opts := proxxGroupRequest(group)
	if _, err := manager.Execute(context.Background(), []string{groups.Provider}, req, opts); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := executor.models(); len(got) != 2 || got[1] != big {
		t.Fatalf("calls = %v", got)
	}
}

func TestProxxGroupRateLimitFailsOverWithoutWaitingAndSkipsCooledMember(t *testing.T) {
	limited, ok := uniqueModel("limited"), uniqueModel("ok")
	manager, executor, group := newProxxGroupTestManager(t, []string{limited, ok}, map[string]error{
		limited: &proxxRetryAfterError{status: http.StatusTooManyRequests, after: 20 * time.Second},
	})
	req, opts := proxxGroupRequest(group)
	start := time.Now()
	if _, err := manager.Execute(context.Background(), []string{groups.Provider}, req, opts); err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("429 failover waited %s; group members must not wait for cooldowns", elapsed)
	}
	if got := executor.models(); len(got) != 2 || got[0] != limited || got[1] != ok {
		t.Fatalf("first calls = %v", got)
	}
	executor.reset()
	if _, err := manager.Execute(context.Background(), []string{groups.Provider}, req, opts); err != nil {
		t.Fatalf("second Execute() error = %v", err)
	}
	if got := executor.models(); len(got) != 1 || got[0] != ok {
		t.Fatalf("cooled member was not skipped: calls = %v", got)
	}
	availability := manager.ProxxModelAvailability(context.Background(), limited)
	if availability.Available || availability.CooldownUntil.IsZero() || availability.Credentials != 1 {
		t.Fatalf("availability = %+v", availability)
	}
}

func TestProxxGroupStreamFailsOverBeforeFirstChunk(t *testing.T) {
	failing, ok := uniqueModel("stream-fail"), uniqueModel("stream-ok")
	manager, executor, group := newProxxGroupTestManager(t, []string{failing, ok}, map[string]error{
		failing: &Error{Code: "upstream", Message: "bad gateway", HTTPStatus: http.StatusBadGateway},
	})
	req, opts := proxxGroupRequest(group)
	opts.Stream = true
	result, err := manager.ExecuteStream(context.Background(), []string{groups.Provider}, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	var body strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		body.Write(chunk.Payload)
	}
	if body.String() != "ok-"+ok+"-done" {
		t.Fatalf("stream body = %q", body.String())
	}
	if got := executor.models(); len(got) != 2 || got[0] != failing || got[1] != ok {
		t.Fatalf("calls = %v", got)
	}
}

func TestProxxGroupNonGroupProvidersUntouched(t *testing.T) {
	if _, handled, _ := (&Manager{}).proxxExecuteGroup(context.Background(), []string{"proxxtest"}, cliproxyexecutor.Request{Model: "auto"}, cliproxyexecutor.Options{}); handled {
		t.Fatal("non-group provider list was handled by the group layer")
	}
	if got := proxxGroupMaxWait(context.Background(), time.Minute); got != time.Minute {
		t.Fatalf("maxWait without group marker = %s", got)
	}
}
