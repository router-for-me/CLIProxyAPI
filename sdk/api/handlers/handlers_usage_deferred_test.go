package handlers

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	runtimehelps "github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

type deferredUsageRecorder struct {
	authPrefix string
	records    chan coreusage.Record
	closed     atomic.Bool
}

func (r *deferredUsageRecorder) HandleUsage(_ context.Context, record coreusage.Record) {
	if r.closed.Load() {
		return
	}
	if strings.HasPrefix(record.AuthID, r.authPrefix) {
		r.records <- record
	}
}

type deferredUsageExecutor struct {
	fail       map[string]bool
	streamFail map[string]bool
}

func (e *deferredUsageExecutor) Identifier() string { return "codex" }

func (e *deferredUsageExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (resp coreexecutor.Response, err error) {
	reporter := runtimehelps.NewUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.TrackFailure(ctx, &err)
	if e.fail[auth.ID] {
		err = &coreauth.Error{Code: "upstream_error", Message: "upstream failed", HTTPStatus: http.StatusInternalServerError}
		return coreexecutor.Response{}, err
	}
	reporter.Publish(ctx, coreusage.Detail{InputTokens: 1, OutputTokens: 1})
	return coreexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (e *deferredUsageExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (resp coreexecutor.Response, err error) {
	return e.Execute(ctx, auth, req, coreexecutor.Options{})
}

func (e *deferredUsageExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (result *coreexecutor.StreamResult, err error) {
	reporter := runtimehelps.NewUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.TrackFailure(ctx, &err)
	if e.fail[auth.ID] {
		err = &coreauth.Error{Code: "upstream_error", Message: "upstream failed", HTTPStatus: http.StatusInternalServerError}
		return nil, err
	}
	if e.streamFail[auth.ID] {
		chunks := make(chan coreexecutor.StreamChunk, 1)
		go func() {
			reporter.PublishFailure(ctx)
			chunks <- coreexecutor.StreamChunk{Err: &coreauth.Error{Code: "stream_error", Message: "stream failed", HTTPStatus: http.StatusInternalServerError}}
			close(chunks)
		}()
		return &coreexecutor.StreamResult{Chunks: chunks}, nil
	}
	reporter.Publish(ctx, coreusage.Detail{InputTokens: 1, OutputTokens: 1})
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte("data: {}\n\n")}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *deferredUsageExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *deferredUsageExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, &coreauth.Error{Code: "not_implemented", Message: "HttpRequest not implemented", HTTPStatus: http.StatusNotImplemented}
}

func newDeferredUsageHandler(t *testing.T, model string, fail map[string]bool, authIDs ...string) (*BaseAPIHandler, *deferredUsageRecorder) {
	return newDeferredUsageHandlerWithStreamFailures(t, model, fail, nil, authIDs...)
}

func newDeferredUsageHandlerWithStreamFailures(t *testing.T, model string, fail, streamFail map[string]bool, authIDs ...string) (*BaseAPIHandler, *deferredUsageRecorder) {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	if fail == nil {
		fail = make(map[string]bool)
	}
	if streamFail == nil {
		streamFail = make(map[string]bool)
	}
	executor := &deferredUsageExecutor{fail: fail, streamFail: streamFail}
	manager.RegisterExecutor(executor)

	recorder := &deferredUsageRecorder{
		authPrefix: "usage-deferred-" + uuid.NewString(),
		records:    make(chan coreusage.Record, 8),
	}
	coreusage.RegisterPlugin(recorder)
	t.Cleanup(func() { recorder.closed.Store(true) })

	modelRegistry := registry.GetGlobalRegistry()
	for i, authID := range authIDs {
		priority := "0"
		if i == 0 {
			priority = "10"
		}
		fullID := recorder.authPrefix + "-" + authID
		auth := &coreauth.Auth{
			ID:       fullID,
			Provider: "codex",
			Attributes: map[string]string{
				"api_key":  fullID,
				"priority": priority,
			},
		}
		modelRegistry.RegisterClient(fullID, "codex", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { modelRegistry.UnregisterClient(fullID) })
		if fail[authID] {
			fail[fullID] = true
		}
		if streamFail[authID] {
			streamFail[fullID] = true
		}
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register auth %s: %v", fullID, err)
		}
	}

	return NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager), recorder
}

func readDeferredUsageRecord(t *testing.T, recorder *deferredUsageRecorder) coreusage.Record {
	t.Helper()
	select {
	case record := <-recorder.records:
		return record
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for usage record")
		return coreusage.Record{}
	}
}

func assertNoDeferredUsageRecord(t *testing.T, recorder *deferredUsageRecorder) {
	t.Helper()
	select {
	case record := <-recorder.records:
		t.Fatalf("unexpected usage record: auth=%s failed=%v", record.AuthID, record.Failed)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestExecuteWithAuthManagerDropsRetriedFailedUsageOnSuccess(t *testing.T) {
	model := "usage-deferred-model-" + uuid.NewString()
	handler, recorder := newDeferredUsageHandler(t, model, map[string]bool{"bad": true}, "bad", "good")

	payload, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "openai-response", model, []byte(`{"input":"hello"}`), "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager error: %v", errMsg.Error)
	}
	if !strings.Contains(string(payload), "good") {
		t.Fatalf("expected good auth payload, got %q", payload)
	}

	record := readDeferredUsageRecord(t, recorder)
	if record.Failed {
		t.Fatalf("expected only success usage, got failed record for %s", record.AuthID)
	}
	if !strings.Contains(record.AuthID, "good") {
		t.Fatalf("expected success record for good auth, got %s", record.AuthID)
	}
	assertNoDeferredUsageRecord(t, recorder)
}

func TestExecuteWithAuthManagerFlushesFinalFailedUsage(t *testing.T) {
	model := "usage-deferred-model-" + uuid.NewString()
	handler, recorder := newDeferredUsageHandler(t, model, map[string]bool{"bad": true}, "bad")

	_, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "openai-response", model, []byte(`{"input":"hello"}`), "")
	if errMsg == nil {
		t.Fatal("expected ExecuteWithAuthManager error")
	}

	record := readDeferredUsageRecord(t, recorder)
	if !record.Failed {
		t.Fatalf("expected failed usage, got success record for %s", record.AuthID)
	}
	assertNoDeferredUsageRecord(t, recorder)
}

func TestExecuteCountWithAuthManagerDropsRetriedFailedUsageOnSuccess(t *testing.T) {
	model := "usage-deferred-model-" + uuid.NewString()
	handler, recorder := newDeferredUsageHandler(t, model, map[string]bool{"bad": true}, "bad", "good")

	payload, _, errMsg := handler.ExecuteCountWithAuthManager(context.Background(), "openai-response", model, []byte(`{"input":"hello"}`), "")
	if errMsg != nil {
		t.Fatalf("ExecuteCountWithAuthManager error: %v", errMsg.Error)
	}
	if !strings.Contains(string(payload), "good") {
		t.Fatalf("expected good auth payload, got %q", payload)
	}

	record := readDeferredUsageRecord(t, recorder)
	if record.Failed {
		t.Fatalf("expected only success usage, got failed record for %s", record.AuthID)
	}
	if !strings.Contains(record.AuthID, "good") {
		t.Fatalf("expected success record for good auth, got %s", record.AuthID)
	}
	assertNoDeferredUsageRecord(t, recorder)
}

func TestExecuteCountWithAuthManagerFlushesFinalFailedUsage(t *testing.T) {
	model := "usage-deferred-model-" + uuid.NewString()
	handler, recorder := newDeferredUsageHandler(t, model, map[string]bool{"bad": true}, "bad")

	_, _, errMsg := handler.ExecuteCountWithAuthManager(context.Background(), "openai-response", model, []byte(`{"input":"hello"}`), "")
	if errMsg == nil {
		t.Fatal("expected ExecuteCountWithAuthManager error")
	}

	record := readDeferredUsageRecord(t, recorder)
	if !record.Failed {
		t.Fatalf("expected failed usage, got success record for %s", record.AuthID)
	}
	assertNoDeferredUsageRecord(t, recorder)
}

func TestExecuteStreamWithAuthManagerDropsRetriedFailedUsageOnSuccess(t *testing.T) {
	model := "usage-deferred-model-" + uuid.NewString()
	handler, recorder := newDeferredUsageHandler(t, model, map[string]bool{"bad": true}, "bad", "good")

	data, _, errs := handler.ExecuteStreamWithAuthManager(context.Background(), "openai", model, []byte(`{"input":"hello"}`), "")
	var chunks [][]byte
	for chunk := range data {
		chunks = append(chunks, chunk)
	}
	for errMsg := range errs {
		if errMsg != nil {
			t.Fatalf("unexpected stream error: %v", errMsg.Error)
		}
	}
	if len(chunks) == 0 {
		t.Fatal("expected stream payload")
	}

	record := readDeferredUsageRecord(t, recorder)
	if record.Failed {
		t.Fatalf("expected only success usage, got failed record for %s", record.AuthID)
	}
	if !strings.Contains(record.AuthID, "good") {
		t.Fatalf("expected success record for good auth, got %s", record.AuthID)
	}
	assertNoDeferredUsageRecord(t, recorder)
}

func TestExecuteStreamWithAuthManagerFlushesInitialFailedUsage(t *testing.T) {
	model := "usage-deferred-model-" + uuid.NewString()
	handler, recorder := newDeferredUsageHandler(t, model, map[string]bool{"bad": true}, "bad")

	data, _, errs := handler.ExecuteStreamWithAuthManager(context.Background(), "openai", model, []byte(`{"input":"hello"}`), "")
	if data != nil {
		t.Fatal("expected no stream data channel")
	}
	errMsg, ok := <-errs
	if !ok || errMsg == nil {
		t.Fatal("expected stream error")
	}

	record := readDeferredUsageRecord(t, recorder)
	if !record.Failed {
		t.Fatalf("expected failed usage, got success record for %s", record.AuthID)
	}
	assertNoDeferredUsageRecord(t, recorder)
}

func TestExecuteStreamWithAuthManagerFlushesStreamFailedUsage(t *testing.T) {
	model := "usage-deferred-model-" + uuid.NewString()
	handler, recorder := newDeferredUsageHandlerWithStreamFailures(t, model, nil, map[string]bool{"bad": true}, "bad")

	data, _, errs := handler.ExecuteStreamWithAuthManager(context.Background(), "openai", model, []byte(`{"input":"hello"}`), "")
	for range data {
		t.Fatal("expected no stream payload")
	}
	errMsg, ok := <-errs
	if !ok || errMsg == nil {
		t.Fatal("expected stream error")
	}

	record := readDeferredUsageRecord(t, recorder)
	if !record.Failed {
		t.Fatalf("expected failed usage, got success record for %s", record.AuthID)
	}
	assertNoDeferredUsageRecord(t, recorder)
}
