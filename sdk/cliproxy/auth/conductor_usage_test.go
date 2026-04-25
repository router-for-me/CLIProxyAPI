package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

type usageCapturePlugin struct {
	provider string
	records  chan usage.Record
}

func (p *usageCapturePlugin) HandleUsage(_ context.Context, record usage.Record) {
	if record.Provider != p.provider {
		return
	}
	select {
	case p.records <- record:
	default:
	}
}

func installUsageCapturePlugin(t *testing.T, provider string) *usageCapturePlugin {
	t.Helper()
	plugin := &usageCapturePlugin{provider: provider, records: make(chan usage.Record, 8)}
	usage.RegisterPlugin(plugin)
	return plugin
}

type usagePublishingExecutor struct {
	id          string
	failAuthIDs map[string]bool
}

func (e *usagePublishingExecutor) Identifier() string { return e.id }

func (e *usagePublishingExecutor) Execute(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.failAuthIDs[auth.ID] {
		publishUsageRecord(ctx, usage.Record{Provider: e.id, Model: "test-model", AuthID: auth.ID, Failed: true})
		return cliproxyexecutor.Response{}, errors.New("retryable upstream failure")
	}
	publishUsageRecord(ctx, usage.Record{Provider: e.id, Model: "test-model", AuthID: auth.ID, Detail: usage.Detail{TotalTokens: 3}})
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (e *usagePublishingExecutor) ExecuteStream(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	if e.failAuthIDs[auth.ID] {
		publishUsageRecord(ctx, usage.Record{Provider: e.id, Model: "test-model", AuthID: auth.ID, Failed: true})
		chunks <- cliproxyexecutor.StreamChunk{Err: errors.New("retryable upstream stream failure")}
		close(chunks)
		return &cliproxyexecutor.StreamResult{Headers: http.Header{}, Chunks: chunks}, nil
	}
	publishUsageRecord(ctx, usage.Record{Provider: e.id, Model: "test-model", AuthID: auth.ID, Detail: usage.Detail{TotalTokens: 3}})
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(auth.ID)}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Headers: http.Header{}, Chunks: chunks}, nil
}

func publishUsageRecord(ctx context.Context, record usage.Record) {
	if record.Failed && cliproxyexecutor.DeferFailure(ctx, func(ctx context.Context) {
		usage.PublishRecord(ctx, record)
	}) {
		return
	}
	usage.PublishRecord(ctx, record)
}

func (e *usagePublishingExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *usagePublishingExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *usagePublishingExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func newUsageRetryTestManager(t *testing.T, failAuthIDs map[string]bool) (*Manager, string, string, string, string) {
	t.Helper()
	manager := NewManager(nil, nil, nil)
	provider := "usage-test-" + uuid.NewString()
	executor := &usagePublishingExecutor{id: provider, failAuthIDs: failAuthIDs}
	manager.RegisterExecutor(executor)

	model := "test-model"
	prefix := uuid.NewString()
	firstAuthID := prefix + "-auth-1"
	secondAuthID := prefix + "-auth-2"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(firstAuthID, provider, []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(secondAuthID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(firstAuthID)
		reg.UnregisterClient(secondAuthID)
	})

	if _, errRegister := manager.Register(context.Background(), &Auth{ID: firstAuthID, Provider: provider}); errRegister != nil {
		t.Fatalf("register first auth: %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: secondAuthID, Provider: provider}); errRegister != nil {
		t.Fatalf("register second auth: %v", errRegister)
	}
	return manager, provider, model, firstAuthID, secondAuthID
}

func readUsageRecord(t *testing.T, plugin *usageCapturePlugin) usage.Record {
	t.Helper()
	select {
	case record := <-plugin.records:
		return record
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for usage record")
	}
	return usage.Record{}
}

func assertNoUsageRecord(t *testing.T, plugin *usageCapturePlugin) {
	t.Helper()
	select {
	case record := <-plugin.records:
		t.Fatalf("unexpected usage record: %+v", record)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestManagerExecuteDiscardsRetriedFailedUsageAfterSuccess(t *testing.T) {
	failAuthIDs := map[string]bool{}
	manager, provider, model, firstAuthID, secondAuthID := newUsageRetryTestManager(t, failAuthIDs)
	plugin := installUsageCapturePlugin(t, provider)
	failAuthIDs[firstAuthID] = true

	resp, errExecute := manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute error = %v, want success", errExecute)
	}
	if string(resp.Payload) != secondAuthID {
		t.Fatalf("payload = %q, want %q", string(resp.Payload), secondAuthID)
	}

	record := readUsageRecord(t, plugin)
	if record.Failed {
		t.Fatalf("expected successful usage record, got failed: %+v", record)
	}
	if record.AuthID != secondAuthID {
		t.Fatalf("usage auth = %q, want %q", record.AuthID, secondAuthID)
	}
	assertNoUsageRecord(t, plugin)
}

func TestManagerExecuteFlushesLastFailedUsageWhenAllRetriesFail(t *testing.T) {
	failAuthIDs := map[string]bool{}
	manager, provider, model, firstAuthID, secondAuthID := newUsageRetryTestManager(t, failAuthIDs)
	plugin := installUsageCapturePlugin(t, provider)
	failAuthIDs[firstAuthID] = true
	failAuthIDs[secondAuthID] = true

	_, errExecute := manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("expected execute error")
	}

	record := readUsageRecord(t, plugin)
	if !record.Failed {
		t.Fatalf("expected failed usage record, got success: %+v", record)
	}
	if record.AuthID != secondAuthID {
		t.Fatalf("usage auth = %q, want last failed auth %q", record.AuthID, secondAuthID)
	}
	assertNoUsageRecord(t, plugin)
}

func TestManagerExecuteFlushesLastFailedUsageWhenRetryWaitIsCanceled(t *testing.T) {
	failAuthIDs := map[string]bool{}
	manager, provider, model, firstAuthID, _ := newUsageRetryTestManager(t, failAuthIDs)
	plugin := installUsageCapturePlugin(t, provider)
	failAuthIDs[firstAuthID] = true
	manager.SetRetryConfig(1, time.Second, 0)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, errExecute := manager.Execute(ctx, []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if !errors.Is(errExecute, context.Canceled) {
		t.Fatalf("execute error = %v, want context canceled", errExecute)
	}

	record := readUsageRecord(t, plugin)
	if !record.Failed {
		t.Fatalf("expected failed usage record, got success: %+v", record)
	}
	if record.AuthID != firstAuthID {
		t.Fatalf("usage auth = %q, want canceled retry auth %q", record.AuthID, firstAuthID)
	}
	assertNoUsageRecord(t, plugin)
}

func TestManagerExecuteStreamDiscardsRetriedFailedUsageAfterSuccess(t *testing.T) {
	failAuthIDs := map[string]bool{}
	manager, provider, model, firstAuthID, secondAuthID := newUsageRetryTestManager(t, failAuthIDs)
	plugin := installUsageCapturePlugin(t, provider)
	failAuthIDs[firstAuthID] = true

	streamResult, errExecute := manager.ExecuteStream(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute stream error = %v, want success", errExecute)
	}
	var payload []byte
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v, want success", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if string(payload) != secondAuthID {
		t.Fatalf("payload = %q, want %q", string(payload), secondAuthID)
	}

	record := readUsageRecord(t, plugin)
	if record.Failed {
		t.Fatalf("expected successful usage record, got failed: %+v", record)
	}
	if record.AuthID != secondAuthID {
		t.Fatalf("usage auth = %q, want %q", record.AuthID, secondAuthID)
	}
	assertNoUsageRecord(t, plugin)
}
