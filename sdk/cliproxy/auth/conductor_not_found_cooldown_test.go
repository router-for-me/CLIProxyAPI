package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Alias-mapped models: the provider's structured 404 names the alias-resolved
// upstream identifier, not the public route model. Classification must match
// either name before falling back to the short transient window (#5476 review).
func TestManager_MarkResult_ExplicitNotFoundMatchesUpstreamModel(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-alias-404", Provider: "codex"}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: "public-model",
		UpstreamModel: "upstream-name",
		Success:       false,
		Error: &Error{
			HTTPStatus: http.StatusNotFound,
			Message:    `{"type":"not_found_error","message":"model upstream-name was not found"}`,
		},
	})

	before := time.Now()
	updated, _ := m.GetByID(auth.ID)
	state := existingModelState(updated, canonicalModelKey("public-model"))
	if state == nil {
		t.Fatal("model state missing")
	}
	if state.NextRetryAfter.Before(before.Add(6 * time.Hour)) {
		t.Fatalf("upstream-named 404 fell into the transient branch: %v", state.NextRetryAfter.Sub(before))
	}
}

// The credential-level clamp inherited from the merged #5501 work keeps a live
// 401 deadline when a later generic 404 proposes a shorter window (#5476 review).
func TestManager_MarkResult_Transient404KeepsLongerCredentialDeadline(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-cred-404-order", Provider: "codex"}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: "",
		Success: false, Error: &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
	})
	before := time.Now()
	snap, _ := m.GetByID(auth.ID)
	if snap.NextRetryAfter.Before(before.Add(25 * time.Minute)) {
		t.Fatalf("precondition failed: expected ~30m 401 deadline, got %v", snap.NextRetryAfter.Sub(before))
	}

	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: "",
		Success: false, Error: &Error{HTTPStatus: http.StatusNotFound, Message: "upstream stream failed"},
	})

	updated, _ := m.GetByID(auth.ID)
	if updated.NextRetryAfter.Before(before.Add(25 * time.Minute)) {
		t.Fatalf("transient 404 shortened the live credential deadline to %v", updated.NextRetryAfter.Sub(before))
	}
}

// Concurrent in-flight 404s complete in any order: a generic transient 404
// landing after an explicit model-not-found result must not shorten the model's
// still-live long cooldown (#5476 review).
func TestManager_MarkResult_Transient404KeepsLongerModelDeadline(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-404-order", Provider: "codex"}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	model := "gpt-5.6-sol"

	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: model,
		Success: false,
		Error: &Error{
			Code:       "model_not_found",
			HTTPStatus: http.StatusNotFound,
			Message:    `model gpt-5.6-sol was not found`,
		},
	})
	before := time.Now()
	snap, _ := m.GetByID(auth.ID)
	state := existingModelState(snap, canonicalModelKey(model))
	if state == nil || state.NextRetryAfter.Before(before.Add(6*time.Hour)) {
		t.Fatalf("precondition failed: expected long model-not-found deadline, got %+v", state)
	}

	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: model,
		Success: false,
		Error:   &Error{HTTPStatus: http.StatusNotFound, Message: `upstream stream failed`},
	})

	updated, _ := m.GetByID(auth.ID)
	stateAfter := existingModelState(updated, canonicalModelKey(model))
	if stateAfter == nil || stateAfter.NextRetryAfter.Before(before.Add(6*time.Hour)) {
		t.Fatalf("transient 404 shortened the model's long deadline to %v", stateAfter.NextRetryAfter.Sub(before))
	}
}

// A single upstream 404 is frequently transient on Codex routes (streamed
// response.failed events map to 404), so it must cool the credential for
// minutes instead of the 12h fixed window (#5476). Structured model-support
// errors keep their own long cooldown path.
func TestManager_MarkResult_SingleNotFoundCooldownsMinutes(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	notFoundErr := &Error{
		HTTPStatus: http.StatusNotFound,
		Message:    `{"error":{"type":"response_failed","message":"upstream stream failed"}}`,
	}

	tests := []struct {
		name  string
		model string
	}{
		{name: "credential level", model: ""},
		{name: "model level", model: "gpt-5.6-sol"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			auth := &Auth{ID: "auth-not-found-" + tc.name, Provider: "codex"}
			if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}

			before := time.Now()
			m.MarkResult(context.Background(), Result{
				AuthID:   auth.ID,
				Provider: auth.Provider,
				Model:    tc.model,
				Success:  false,
				Error:    notFoundErr,
			})

			updated, ok := m.GetByID(auth.ID)
			if !ok || updated == nil {
				t.Fatalf("expected auth to be present")
			}
			cooldown := updated.NextRetryAfter.Sub(before)
			if cooldown <= 0 || cooldown > 15*time.Minute {
				t.Fatalf("single 404 cooldown = %v, want a short window (minutes)", cooldown)
			}
		})
	}
}

// A streamed alias-mapped request can emit payloads before the structured 404
// arrives as a terminal chunk error. The result recorded by wrapStreamResult
// must carry the alias-resolved upstream model so classification matches the
// provider's error text and keeps the long model-support cooldown instead of
// the short transient window (#5514 review).
func TestWrapStreamResult_TerminalErrorCarriesUpstreamModel(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-stream-404-upstream", Provider: "codex"}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	remaining := make(chan cliproxyexecutor.StreamChunk, 2)
	remaining <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"delta":"hi"}`)}
	remaining <- cliproxyexecutor.StreamChunk{Err: &Error{
		HTTPStatus: http.StatusNotFound,
		Message:    `{"type":"not_found_error","message":"model upstream-name was not found"}`,
	}}
	close(remaining)

	streamResult := m.wrapStreamResult(context.Background(), auth.Clone(), "codex", "public-model", "public-model", "upstream-name", nil, nil, remaining, OAuthModelAliasResult{}, false, cliproxyexecutor.Options{}, 1)
	for range streamResult.Chunks {
	}

	before := time.Now()
	updated, _ := m.GetByID(auth.ID)
	state := existingModelState(updated, canonicalModelKey("public-model"))
	if state == nil {
		t.Fatal("model state missing")
	}
	if state.NextRetryAfter.Before(before.Add(6 * time.Hour)) {
		t.Fatalf("stream terminal 404 fell into the transient branch: %v", state.NextRetryAfter.Sub(before))
	}
}

// Alias-mapped or payload-rewritten streams also close cleanly; the success
// result must carry the executed upstream model so Home streaming observers
// see the same model the non-stream Home result reports (#5514 review).
func TestWrapStreamResult_SuccessCarriesUpstreamModel(t *testing.T) {
	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-stream-success-upstream", Provider: "codex"}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	hook := &recordingHook{}
	m.hook = hook

	remaining := make(chan cliproxyexecutor.StreamChunk, 1)
	remaining <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"delta":"hi"}`)}
	close(remaining)

	streamResult := m.wrapStreamResult(context.Background(), auth.Clone(), "codex", "public-model", "public-model", "upstream-name", nil, nil, remaining, OAuthModelAliasResult{}, true, cliproxyexecutor.Options{}, 1)
	for range streamResult.Chunks {
	}
	result := hook.lastResult.Load()
	if result == nil {
		t.Fatal("no result recorded")
	}
	if !result.Success {
		t.Fatalf("expected a success result, got %+v", result)
	}
	if result.UpstreamModel != "upstream-name" {
		t.Fatalf("success result UpstreamModel = %q, want the executed upstream model", result.UpstreamModel)
	}
}

// Selection-override executions send a different model than the recorded route
// candidate: when the auth-selection metadata names "tenant/public" while the
// wire request executes "gpt-6-astra", the provider's structured 404 names the
// sent model. The recorded result must carry the executed model as
// UpstreamModel so classification matches and keeps the long model-support
// cooldown instead of the short transient window (#5514 review).
type selectionOverrideExecutor struct {
	calls     int
	gotModels []string
}

func (e *selectionOverrideExecutor) Identifier() string { return "selection-override-provider" }

func (e *selectionOverrideExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls++
	e.gotModels = append(e.gotModels, req.Model)
	return cliproxyexecutor.Response{}, &Error{
		HTTPStatus: http.StatusNotFound,
		Message:    `{"type":"not_found_error","message":"model gpt-6-astra was not found"}`,
	}
}

func (e *selectionOverrideExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, errors.New("stream not supported")
}

func (e *selectionOverrideExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *selectionOverrideExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not supported")
}

func (e *selectionOverrideExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not supported")
}

func TestManager_Execute_SelectionOverrideRecordsSentModelOn404(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	executor := &selectionOverrideExecutor{}
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)

	auth := &Auth{ID: "auth-selection-404", Provider: executor.Identifier(), Status: StatusActive}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "tenant/public"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	req := cliproxyexecutor.Request{Model: "gpt-6-astra", Payload: []byte(`{"input":"hi"}`)}
	opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.AuthSelectionModelMetadataKey: "tenant/public"}}

	if _, errExec := m.Execute(context.Background(), []string{executor.Identifier()}, req, opts); errExec == nil {
		t.Fatal("Execute expected the structured 404, got nil")
	}
	if len(executor.gotModels) != 1 || executor.gotModels[0] != "gpt-6-astra" {
		t.Fatalf("executor saw models %v, want [gpt-6-astra]", executor.gotModels)
	}

	before := time.Now()
	updated, _ := m.GetByID(auth.ID)
	state := existingModelState(updated, canonicalModelKey("tenant/public"))
	if state == nil {
		t.Fatal("model state missing")
	}
	if state.NextRetryAfter.Before(before.Add(6 * time.Hour)) {
		t.Fatalf("selection-override 404 fell into the transient branch: %v", state.NextRetryAfter.Sub(before))
	}
}

// Payload-override rules rewrite the request body's model inside the executor,
// after the conductor snapshots execReq.Model: a rule retargeting
// "gpt-6-astra" to "configured-wire" makes the provider's structured 404 name
// the final wire model. Executors report that name through Options.Metadata and
// the recorded result must classify against it across the execute, count and
// stream paths instead of falling into the short transient window (#5514
// review).
type wireModelOverrideExecutor struct {
	sawNilMetadata int
}

func (e *wireModelOverrideExecutor) Identifier() string { return "wire-override-provider" }

func (e *wireModelOverrideExecutor) recordWireModel(opts cliproxyexecutor.Options) {
	if opts.Metadata == nil {
		e.sawNilMetadata++
		return
	}
	opts.Metadata[cliproxyexecutor.WireModelMetadataKey] = "configured-wire"
}

func wireOverrideNotFound() *Error {
	return &Error{
		HTTPStatus: http.StatusNotFound,
		Message:    `{"type":"not_found_error","message":"model configured-wire was not found"}`,
	}
}

func (e *wireModelOverrideExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.recordWireModel(opts)
	return cliproxyexecutor.Response{}, wireOverrideNotFound()
}

func (e *wireModelOverrideExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.recordWireModel(opts)
	remaining := make(chan cliproxyexecutor.StreamChunk, 1)
	remaining <- cliproxyexecutor.StreamChunk{Err: wireOverrideNotFound()}
	close(remaining)
	return &cliproxyexecutor.StreamResult{Chunks: remaining}, nil
}

func (e *wireModelOverrideExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *wireModelOverrideExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.recordWireModel(opts)
	return cliproxyexecutor.Response{}, wireOverrideNotFound()
}

func (e *wireModelOverrideExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not supported")
}

func newWireOverrideManager(t *testing.T, executor *wireModelOverrideExecutor) (*Manager, *Auth) {
	t.Helper()
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)
	auth := &Auth{ID: "auth-wire-override-404", Provider: executor.Identifier(), Status: StatusActive}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "tenant/public"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	return m, auth
}

func assertWireOverrideLongCooldown(t *testing.T, m *Manager, executor *wireModelOverrideExecutor) {
	t.Helper()
	if executor.sawNilMetadata != 0 {
		t.Fatalf("executor received %d attempts with nil metadata; the conductor must pre-allocate the per-attempt map", executor.sawNilMetadata)
	}
	before := time.Now()
	updated, _ := m.GetByID("auth-wire-override-404")
	state := existingModelState(updated, canonicalModelKey("tenant/public"))
	if state == nil {
		t.Fatal("model state missing")
	}
	if state.NextRetryAfter.Before(before.Add(6 * time.Hour)) {
		t.Fatalf("wire-model-named 404 fell into the transient branch: %v", state.NextRetryAfter.Sub(before))
	}
}

func wireOverrideRequestOptions() (cliproxyexecutor.Request, cliproxyexecutor.Options) {
	req := cliproxyexecutor.Request{Model: "gpt-6-astra", Payload: []byte(`{"input":"hi"}`)}
	opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.AuthSelectionModelMetadataKey: "tenant/public"}}
	return req, opts
}

func TestManager_Execute_WireModelOverrideRecordsFinalModelOn404(t *testing.T) {
	executor := &wireModelOverrideExecutor{}
	m, _ := newWireOverrideManager(t, executor)
	req, opts := wireOverrideRequestOptions()

	if _, errExec := m.Execute(context.Background(), []string{executor.Identifier()}, req, opts); errExec == nil {
		t.Fatal("Execute expected the structured 404, got nil")
	}
	assertWireOverrideLongCooldown(t, m, executor)
}

func TestManager_ExecuteCount_WireModelOverrideRecordsFinalModelOn404(t *testing.T) {
	executor := &wireModelOverrideExecutor{}
	m, _ := newWireOverrideManager(t, executor)
	req, opts := wireOverrideRequestOptions()

	if _, errExec := m.ExecuteCount(context.Background(), []string{executor.Identifier()}, req, opts); errExec == nil {
		t.Fatal("ExecuteCount expected the structured 404, got nil")
	}
	assertWireOverrideLongCooldown(t, m, executor)
}

func TestManager_ExecuteStream_WireModelOverrideRecordsFinalModelOn404(t *testing.T) {
	executor := &wireModelOverrideExecutor{}
	m, _ := newWireOverrideManager(t, executor)
	req, opts := wireOverrideRequestOptions()
	opts.Stream = true

	streamResult, errStream := m.ExecuteStream(context.Background(), []string{executor.Identifier()}, req, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream: %v", errStream)
	}
	for range streamResult.Chunks {
	}
	assertWireOverrideLongCooldown(t, m, executor)
}

// Credential rotation must start each attempt with the wire-model key cleared:
// when the first credential's payload rule rewrites the body model (recording
// the wire model) and the rotation lands on a pass-through credential, the
// stale key keeps classifying the second credential's structured 404 against
// the first credential's rewritten name, cooling for minutes instead of the
// model-support window (#5514 review).
type wireModelRotationExecutor struct {
	attempts       []string
	sawNilMetadata int
}

func (e *wireModelRotationExecutor) Identifier() string { return "wire-override-provider" }

func (e *wireModelRotationExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.attempts = append(e.attempts, auth.ID)
	if opts.Metadata == nil {
		e.sawNilMetadata++
	}
	if len(e.attempts) == 1 {
		opts.Metadata[cliproxyexecutor.WireModelMetadataKey] = "configured-wire"
		return cliproxyexecutor.Response{}, overloadStatusError()
	}
	return cliproxyexecutor.Response{}, &Error{
		HTTPStatus: http.StatusNotFound,
		Message:    `{"type":"not_found_error","message":"model gpt-6-astra was not found"}`,
	}
}

func (e *wireModelRotationExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, errors.New("stream not supported")
}

func (e *wireModelRotationExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *wireModelRotationExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not supported")
}

func (e *wireModelRotationExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not supported")
}

func TestManager_Execute_WireModelMetadataResetBetweenAttempts(t *testing.T) {
	executor := &wireModelRotationExecutor{}
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(5, 0, 6)
	m.RegisterExecutor(executor)
	reg := registry.GetGlobalRegistry()
	priorities := map[string]string{"auth-wire-rotation-1": "100", "auth-wire-rotation-2": "99"}
	for id, priority := range priorities {
		auth := &Auth{ID: id, Provider: executor.Identifier(), Status: StatusActive, Attributes: map[string]string{"priority": priority}}
		reg.RegisterClient(id, executor.Identifier(), []*registry.ModelInfo{{ID: "tenant/public"}})
		if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", id, errRegister)
		}
		t.Cleanup(func() { reg.UnregisterClient(id) })
	}
	req, opts := wireOverrideRequestOptions()

	if _, errExec := m.Execute(context.Background(), []string{executor.Identifier()}, req, opts); errExec == nil {
		t.Fatal("Execute expected the structured 404, got nil")
	}
	if len(executor.attempts) != 2 {
		t.Fatalf("expected a rotation across two credentials, attempts = %v", executor.attempts)
	}
	if executor.sawNilMetadata != 0 {
		t.Fatalf("executor received %d attempts with nil metadata; the conductor must pre-allocate the per-attempt map", executor.sawNilMetadata)
	}
	before := time.Now()
	updated, _ := m.GetByID("auth-wire-rotation-2")
	state := existingModelState(updated, canonicalModelKey("tenant/public"))
	if state == nil {
		t.Fatal("model state missing")
	}
	if state.NextRetryAfter.Before(before.Add(6 * time.Hour)) {
		t.Fatalf("second credential's 404 fell into the transient branch: %v", state.NextRetryAfter.Sub(before))
	}
}
