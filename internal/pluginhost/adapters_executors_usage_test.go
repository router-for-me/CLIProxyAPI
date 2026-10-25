package pluginhost

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type testUsageCapturePlugin struct {
	targetProvider string
	records        chan coreusage.Record
}

func newTestUsageCapturePlugin(targetProvider string) *testUsageCapturePlugin {
	return &testUsageCapturePlugin{
		targetProvider: targetProvider,
		records:        make(chan coreusage.Record, 50),
	}
}

func (p *testUsageCapturePlugin) HandleUsage(_ context.Context, record coreusage.Record) {
	if p.targetProvider != "" && record.Provider != p.targetProvider {
		return
	}
	select {
	case p.records <- record:
	default:
	}
}

func (p *testUsageCapturePlugin) waitRecord(t *testing.T, timeout time.Duration) coreusage.Record {
	t.Helper()
	select {
	case rec := <-p.records:
		return rec
	case <-time.After(timeout):
		t.Fatal("timed out waiting for usage record")
		return coreusage.Record{}
	}
}

func (p *testUsageCapturePlugin) assertNoRecord(t *testing.T, wait time.Duration) {
	t.Helper()
	select {
	case rec := <-p.records:
		t.Fatalf("expected no usage record for %q, got %+v", p.targetProvider, rec)
	case <-time.After(wait):
	}
}

func registerTestUsagePlugin(t *testing.T, name string, plugin coreusage.Plugin) {
	t.Helper()
	coreusage.RegisterNamedPlugin(name, plugin)
	t.Cleanup(func() {
		coreusage.RegisterNamedPlugin(name, noopUsagePlugin{})
	})
}

type noopUsagePlugin struct{}

func (noopUsagePlugin) HandleUsage(context.Context, coreusage.Record) {}

func TestExecutorAdapterExecutePublishesUsage(t *testing.T) {
	plugin := newTestUsageCapturePlugin("plugin-provider")
	registerTestUsagePlugin(t, "test-executor-adapter-execute-usage", plugin)

	executorRecord := normalizeTestCapabilityRecord(capabilityRecord{id: "executor-plugin"})
	host := newHostWithRecords(executorRecord)

	exec := &fakeExecutor{
		identifier: "plugin-provider",
		execute: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			return pluginapi.ExecutorResponse{
				Payload: []byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"hello"}}],"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}`),
				Headers: http.Header{"Content-Type": []string{"application/json"}},
			}, nil
		},
	}

	adapter := newExecutorAdapterForRecordForTest(host, executorRecord, exec,
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
	)

	auth := &coreauth.Auth{
		ID:         "auth-1",
		Provider:   "plugin-provider",
		FileName:   "auth-1.json",
		Attributes: map[string]string{"type": "oauth"},
	}

	req := coreexecutor.Request{
		Model:   "test-model",
		Payload: []byte(`{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
	}

	resp, err := adapter.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("adapter.Execute returned unexpected error: %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("adapter.Execute returned empty payload")
	}

	rec := plugin.waitRecord(t, 200*time.Millisecond)
	if rec.Provider != "plugin-provider" {
		t.Errorf("got provider %q, want %q", rec.Provider, "plugin-provider")
	}
	if rec.Detail.InputTokens != 10 || rec.Detail.OutputTokens != 20 || rec.Detail.TotalTokens != 30 {
		t.Errorf("got usage %+v, want 10 input, 20 output, 30 total", rec.Detail)
	}
}

func TestExecutorAdapterExecuteThroughAuthManagerPublishesUsage(t *testing.T) {
	plugin := newTestUsageCapturePlugin("plugin-provider-mgr")
	registerTestUsagePlugin(t, "test-executor-adapter-auth-manager-usage", plugin)

	executorRecord := normalizeTestCapabilityRecord(capabilityRecord{id: "executor-plugin-mgr"})
	host := newHostWithRecords(executorRecord)

	exec := &fakeExecutor{
		identifier: "plugin-provider-mgr",
		execute: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			return pluginapi.ExecutorResponse{
				Payload: []byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"hello"}}],"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}`),
				Headers: http.Header{"Content-Type": []string{"application/json"}},
			}, nil
		},
	}

	adapter := newExecutorAdapterForRecordForTest(host, executorRecord, exec,
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
	)
	adapter.provider = "plugin-provider-mgr"

	authMgr := coreauth.NewManager(nil, nil, nil)
	authMgr.RegisterExecutor(adapter)

	model := "test-model-mgr"
	auth := &coreauth.Auth{
		ID:         "auth-mgr-1",
		Provider:   "plugin-provider-mgr",
		Status:     coreauth.StatusActive,
		FileName:   "auth-mgr-1.json",
		Attributes: map[string]string{"type": "oauth"},
	}
	if _, err := authMgr.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	req := coreexecutor.Request{
		Model:   model,
		Payload: []byte(`{"model":"test-model-mgr","messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
	}

	resp, err := authMgr.Execute(context.Background(), []string{"plugin-provider-mgr"}, req, opts)
	if err != nil {
		t.Fatalf("authMgr.Execute returned unexpected error: %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("authMgr.Execute returned empty payload")
	}

	rec := plugin.waitRecord(t, 200*time.Millisecond)
	if rec.Provider != "plugin-provider-mgr" {
		t.Errorf("got provider %q, want %q", rec.Provider, "plugin-provider-mgr")
	}
	if rec.Detail.InputTokens != 10 || rec.Detail.OutputTokens != 20 || rec.Detail.TotalTokens != 30 {
		t.Errorf("got usage %+v, want 10 input, 20 output, 30 total", rec.Detail)
	}
}

func TestExecutorAdapterExecuteNilAuthSkipsUsage(t *testing.T) {
	plugin := newTestUsageCapturePlugin("plugin-provider-nil-auth")
	registerTestUsagePlugin(t, "test-executor-adapter-execute-nil-auth", plugin)

	executorRecord := normalizeTestCapabilityRecord(capabilityRecord{id: "executor-plugin-nil-auth"})
	host := newHostWithRecords(executorRecord)

	exec := &fakeExecutor{
		identifier: "plugin-provider-nil-auth",
		execute: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			return pluginapi.ExecutorResponse{
				Payload: []byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"hello"}}],"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}`),
			}, nil
		},
	}

	adapter := newExecutorAdapterForRecordForTest(host, executorRecord, exec,
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
	)
	adapter.provider = "plugin-provider-nil-auth"

	req := coreexecutor.Request{
		Model:   "test-model",
		Payload: []byte(`{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
	}

	resp, err := adapter.Execute(context.Background(), nil, req, opts)
	if err != nil {
		t.Fatalf("adapter.Execute returned unexpected error: %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("adapter.Execute returned empty payload")
	}

	plugin.assertNoRecord(t, 50*time.Millisecond)
}

func TestExecutorAdapterExecuteErrorPublishesFailure(t *testing.T) {
	plugin := newTestUsageCapturePlugin("plugin-provider-error")
	registerTestUsagePlugin(t, "test-executor-adapter-execute-error", plugin)

	executorRecord := normalizeTestCapabilityRecord(capabilityRecord{id: "executor-plugin-error"})
	host := newHostWithRecords(executorRecord)

	exec := &fakeExecutor{
		identifier: "plugin-provider-error",
		execute: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			return pluginapi.ExecutorResponse{}, errors.New("upstream failed")
		},
	}

	adapter := newExecutorAdapterForRecordForTest(host, executorRecord, exec,
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
	)
	adapter.provider = "plugin-provider-error"

	auth := &coreauth.Auth{
		ID:       "auth-1",
		Provider: "plugin-provider-error",
	}

	req := coreexecutor.Request{
		Model:   "test-model",
		Payload: []byte(`{"model":"test-model"}`),
	}

	_, err := adapter.Execute(context.Background(), auth, req, coreexecutor.Options{})
	if err == nil {
		t.Fatal("expected error from adapter.Execute")
	}

	rec := plugin.waitRecord(t, 200*time.Millisecond)
	if !rec.Failed {
		t.Errorf("rec.Failed = false, want true")
	}
}

func TestExecutorAdapterExecutePanicPublishesFailure(t *testing.T) {
	plugin := newTestUsageCapturePlugin("plugin-provider-panic")
	registerTestUsagePlugin(t, "test-executor-adapter-execute-panic", plugin)

	executorRecord := normalizeTestCapabilityRecord(capabilityRecord{id: "executor-plugin-panic"})
	host := newHostWithRecords(executorRecord)

	exec := &fakeExecutor{
		identifier: "plugin-provider-panic",
		execute: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			panic("execute panic boom")
		},
	}

	adapter := newExecutorAdapterForRecordForTest(host, executorRecord, exec,
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
	)
	adapter.provider = "plugin-provider-panic"

	auth := &coreauth.Auth{
		ID:       "auth-panic-1",
		Provider: "plugin-provider-panic",
	}

	req := coreexecutor.Request{
		Model:   "test-model",
		Payload: []byte(`{"model":"test-model"}`),
	}

	_, err := adapter.Execute(context.Background(), auth, req, coreexecutor.Options{})
	if err == nil {
		t.Fatal("expected error from adapter.Execute on panic")
	}

	rec := plugin.waitRecord(t, 200*time.Millisecond)
	if !rec.Failed {
		t.Errorf("rec.Failed = false, want true")
	}
}

func TestExecutorAdapterExecuteStreamPublishesUsage(t *testing.T) {
	plugin := newTestUsageCapturePlugin("plugin-provider-stream")
	registerTestUsagePlugin(t, "test-executor-adapter-stream-usage", plugin)

	executorRecord := normalizeTestCapabilityRecord(capabilityRecord{id: "executor-plugin-stream"})
	host := newHostWithRecords(executorRecord)

	streamChunks := make(chan pluginapi.ExecutorStreamChunk, 4)
	streamChunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n")}
	streamChunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":15,\"completion_tokens\":25,\"total_tokens\":40}}\n\n")}
	streamChunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: [DONE]\n\n")}
	close(streamChunks)

	exec := &fakeExecutor{
		identifier: "plugin-provider-stream",
		executeStream: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			return pluginapi.ExecutorStreamResponse{
				Chunks: streamChunks,
			}, nil
		},
	}

	adapter := newExecutorAdapterForRecordForTest(host, executorRecord, exec,
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
	)
	adapter.provider = "plugin-provider-stream"

	auth := &coreauth.Auth{
		ID:       "auth-stream-1",
		Provider: "plugin-provider-stream",
	}

	req := coreexecutor.Request{
		Model:   "test-model",
		Payload: []byte(`{"model":"test-model","stream":true}`),
	}
	opts := coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
		Stream:         true,
	}

	streamRes, err := adapter.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream returned unexpected error: %v", err)
	}

	var receivedChunks [][]byte
	for chunk := range streamRes.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
		receivedChunks = append(receivedChunks, chunk.Payload)
	}

	if len(receivedChunks) == 0 {
		t.Fatal("expected non-empty received chunks")
	}

	rec := plugin.waitRecord(t, 200*time.Millisecond)
	if rec.Provider != "plugin-provider-stream" {
		t.Errorf("got provider %q, want %q", rec.Provider, "plugin-provider-stream")
	}
	if rec.Detail.InputTokens != 15 || rec.Detail.OutputTokens != 25 || rec.Detail.TotalTokens != 40 {
		t.Errorf("got usage %+v, want prompt=15 completion=25 total=40", rec.Detail)
	}
}

func TestExecutorAdapterExecuteStreamThroughAuthManagerPublishesUsage(t *testing.T) {
	plugin := newTestUsageCapturePlugin("plugin-provider-stream-mgr")
	registerTestUsagePlugin(t, "test-executor-adapter-auth-manager-stream-usage", plugin)

	executorRecord := normalizeTestCapabilityRecord(capabilityRecord{id: "executor-plugin-stream-mgr"})
	host := newHostWithRecords(executorRecord)

	streamChunks := make(chan pluginapi.ExecutorStreamChunk, 4)
	streamChunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n")}
	streamChunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":15,\"completion_tokens\":25,\"total_tokens\":40}}\n\n")}
	streamChunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: [DONE]\n\n")}
	close(streamChunks)

	exec := &fakeExecutor{
		identifier: "plugin-provider-stream-mgr",
		executeStream: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			return pluginapi.ExecutorStreamResponse{
				Chunks: streamChunks,
			}, nil
		},
	}

	adapter := newExecutorAdapterForRecordForTest(host, executorRecord, exec,
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
	)
	adapter.provider = "plugin-provider-stream-mgr"

	authMgr := coreauth.NewManager(nil, nil, nil)
	authMgr.RegisterExecutor(adapter)

	model := "test-model-stream-mgr"
	auth := &coreauth.Auth{
		ID:         "auth-stream-mgr-1",
		Provider:   "plugin-provider-stream-mgr",
		Status:     coreauth.StatusActive,
		FileName:   "auth-stream-mgr-1.json",
		Attributes: map[string]string{"type": "oauth"},
	}
	if _, err := authMgr.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	req := coreexecutor.Request{
		Model:   model,
		Payload: []byte(`{"model":"test-model-stream-mgr","stream":true}`),
	}
	opts := coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
		Stream:         true,
	}

	streamRes, err := authMgr.ExecuteStream(context.Background(), []string{"plugin-provider-stream-mgr"}, req, opts)
	if err != nil {
		t.Fatalf("authMgr.ExecuteStream returned unexpected error: %v", err)
	}

	for range streamRes.Chunks {
	}

	rec := plugin.waitRecord(t, 200*time.Millisecond)
	if rec.Provider != "plugin-provider-stream-mgr" {
		t.Errorf("got provider %q, want %q", rec.Provider, "plugin-provider-stream-mgr")
	}
	if rec.Detail.InputTokens != 15 || rec.Detail.OutputTokens != 25 || rec.Detail.TotalTokens != 40 {
		t.Errorf("got usage %+v, want prompt=15 completion=25 total=40", rec.Detail)
	}
}

func TestExecutorAdapterExecuteStreamTwoCompleteChunksWithoutNewline(t *testing.T) {
	plugin := newTestUsageCapturePlugin("plugin-provider-no-newline")
	registerTestUsagePlugin(t, "test-executor-adapter-stream-no-newline", plugin)

	executorRecord := normalizeTestCapabilityRecord(capabilityRecord{id: "executor-plugin-no-newline"})
	host := newHostWithRecords(executorRecord)

	streamChunks := make(chan pluginapi.ExecutorStreamChunk, 3)
	// Two complete chunks without trailing \n
	streamChunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}")}
	streamChunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":22,\"total_tokens\":33}}")}
	streamChunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: [DONE]")}
	close(streamChunks)

	exec := &fakeExecutor{
		identifier: "plugin-provider-no-newline",
		executeStream: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			return pluginapi.ExecutorStreamResponse{
				Chunks: streamChunks,
			}, nil
		},
	}

	adapter := newExecutorAdapterForRecordForTest(host, executorRecord, exec,
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
	)
	adapter.provider = "plugin-provider-no-newline"

	auth := &coreauth.Auth{
		ID:       "auth-no-newline-1",
		Provider: "plugin-provider-no-newline",
	}

	req := coreexecutor.Request{
		Model:   "test-model",
		Payload: []byte(`{"model":"test-model","stream":true}`),
	}

	streamRes, err := adapter.ExecuteStream(context.Background(), auth, req, coreexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream returned unexpected error: %v", err)
	}

	for range streamRes.Chunks {
	}

	rec := plugin.waitRecord(t, 200*time.Millisecond)
	if rec.Detail.InputTokens != 11 || rec.Detail.OutputTokens != 22 || rec.Detail.TotalTokens != 33 {
		t.Errorf("got usage %+v, want prompt=11 completion=22 total=33", rec.Detail)
	}
}

func TestExecutorAdapterExecuteStreamSplitChunks(t *testing.T) {
	plugin := newTestUsageCapturePlugin("plugin-provider-split")
	registerTestUsagePlugin(t, "test-executor-adapter-stream-split", plugin)

	executorRecord := normalizeTestCapabilityRecord(capabilityRecord{id: "executor-plugin-split"})
	host := newHostWithRecords(executorRecord)

	streamChunks := make(chan pluginapi.ExecutorStreamChunk, 5)
	// Split SSE frame across two chunks
	streamChunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[],\"usage\":{\"prompt_tok")}
	streamChunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("ens\":18,\"completion_tokens\":22,\"total_tokens\":40}}\n\n")}
	streamChunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: [DONE]\n\n")}
	close(streamChunks)

	exec := &fakeExecutor{
		identifier: "plugin-provider-split",
		executeStream: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			return pluginapi.ExecutorStreamResponse{
				Chunks: streamChunks,
			}, nil
		},
	}

	adapter := newExecutorAdapterForRecordForTest(host, executorRecord, exec,
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
	)
	adapter.provider = "plugin-provider-split"

	auth := &coreauth.Auth{
		ID:       "auth-split-1",
		Provider: "plugin-provider-split",
	}

	req := coreexecutor.Request{
		Model:   "test-model",
		Payload: []byte(`{"model":"test-model","stream":true}`),
	}

	streamRes, err := adapter.ExecuteStream(context.Background(), auth, req, coreexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream returned unexpected error: %v", err)
	}

	for range streamRes.Chunks {
	}

	rec := plugin.waitRecord(t, 200*time.Millisecond)
	if rec.Detail.InputTokens != 18 || rec.Detail.OutputTokens != 22 || rec.Detail.TotalTokens != 40 {
		t.Errorf("got usage %+v, want prompt=18 completion=22 total=40", rec.Detail)
	}
}

func TestExecutorAdapterExecuteStreamErrorChunk(t *testing.T) {
	plugin := newTestUsageCapturePlugin("plugin-provider-stream-err")
	registerTestUsagePlugin(t, "test-executor-adapter-stream-err", plugin)

	executorRecord := normalizeTestCapabilityRecord(capabilityRecord{id: "executor-plugin-stream-err"})
	host := newHostWithRecords(executorRecord)

	expectedErr := errors.New("mid-stream chunk failure")
	streamChunks := make(chan pluginapi.ExecutorStreamChunk, 2)
	streamChunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"start\"}}]}\n\n")}
	streamChunks <- pluginapi.ExecutorStreamChunk{Err: expectedErr}
	close(streamChunks)

	exec := &fakeExecutor{
		identifier: "plugin-provider-stream-err",
		executeStream: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			return pluginapi.ExecutorStreamResponse{
				Chunks: streamChunks,
			}, nil
		},
	}

	adapter := newExecutorAdapterForRecordForTest(host, executorRecord, exec,
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
	)
	adapter.provider = "plugin-provider-stream-err"

	auth := &coreauth.Auth{
		ID:       "auth-stream-err-1",
		Provider: "plugin-provider-stream-err",
	}

	req := coreexecutor.Request{
		Model:   "test-model",
		Payload: []byte(`{"model":"test-model","stream":true}`),
	}

	streamRes, err := adapter.ExecuteStream(context.Background(), auth, req, coreexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream returned unexpected error: %v", err)
	}

	var gotError error
	for chunk := range streamRes.Chunks {
		if chunk.Err != nil {
			gotError = chunk.Err
		}
	}

	if gotError == nil || !errors.Is(gotError, expectedErr) {
		t.Fatalf("expected error %v, got %v", expectedErr, gotError)
	}

	rec := plugin.waitRecord(t, 200*time.Millisecond)
	if !rec.Failed {
		t.Errorf("rec.Failed = false, want true")
	}
}

func TestExecutorAdapterExecuteStreamPanicPublishesFailure(t *testing.T) {
	plugin := newTestUsageCapturePlugin("plugin-provider-stream-panic")
	registerTestUsagePlugin(t, "test-executor-adapter-stream-panic", plugin)

	executorRecord := normalizeTestCapabilityRecord(capabilityRecord{id: "executor-plugin-stream-panic"})
	host := newHostWithRecords(executorRecord)

	exec := &fakeExecutor{
		identifier: "plugin-provider-stream-panic",
		executeStream: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			panic("execute stream panic boom")
		},
	}

	adapter := newExecutorAdapterForRecordForTest(host, executorRecord, exec,
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
	)
	adapter.provider = "plugin-provider-stream-panic"

	auth := &coreauth.Auth{
		ID:       "auth-stream-panic-1",
		Provider: "plugin-provider-stream-panic",
	}

	req := coreexecutor.Request{
		Model:   "test-model",
		Payload: []byte(`{"model":"test-model","stream":true}`),
	}

	_, err := adapter.ExecuteStream(context.Background(), auth, req, coreexecutor.Options{})
	if err == nil {
		t.Fatal("expected error from adapter.ExecuteStream on panic")
	}

	rec := plugin.waitRecord(t, 200*time.Millisecond)
	if !rec.Failed {
		t.Errorf("rec.Failed = false, want true")
	}
}

func TestExecutorAdapterExecuteStreamNilAuthSkipsUsage(t *testing.T) {
	plugin := newTestUsageCapturePlugin("plugin-provider-stream-nil")
	registerTestUsagePlugin(t, "test-executor-adapter-stream-nil", plugin)

	executorRecord := normalizeTestCapabilityRecord(capabilityRecord{id: "executor-plugin-stream-nil"})
	host := newHostWithRecords(executorRecord)

	streamChunks := make(chan pluginapi.ExecutorStreamChunk, 2)
	streamChunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":20,\"total_tokens\":30}}\n\n")}
	streamChunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: [DONE]\n\n")}
	close(streamChunks)

	exec := &fakeExecutor{
		identifier: "plugin-provider-stream-nil",
		executeStream: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			return pluginapi.ExecutorStreamResponse{
				Chunks: streamChunks,
			}, nil
		},
	}

	adapter := newExecutorAdapterForRecordForTest(host, executorRecord, exec,
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
	)
	adapter.provider = "plugin-provider-stream-nil"

	req := coreexecutor.Request{
		Model:   "test-model",
		Payload: []byte(`{"model":"test-model","stream":true}`),
	}

	streamRes, err := adapter.ExecuteStream(context.Background(), nil, req, coreexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream returned unexpected error: %v", err)
	}

	for range streamRes.Chunks {
	}

	plugin.assertNoRecord(t, 50*time.Millisecond)
}

func TestExecutorAdapterExecuteStreamSplitAcrossBraceBoundary(t *testing.T) {
	plugin := newTestUsageCapturePlugin("plugin-provider-split-brace")
	registerTestUsagePlugin(t, "test-executor-adapter-stream-split-brace", plugin)

	executorRecord := normalizeTestCapabilityRecord(capabilityRecord{id: "executor-plugin-split-brace"})
	host := newHostWithRecords(executorRecord)

	streamChunks := make(chan pluginapi.ExecutorStreamChunk, 3)
	// Split exactly before opening brace of usage object
	streamChunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[],\"usage\":")}
	streamChunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("{\"prompt_tokens\":18,\"completion_tokens\":22,\"total_tokens\":40}}\n\n")}
	streamChunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: [DONE]\n\n")}
	close(streamChunks)

	exec := &fakeExecutor{
		identifier: "plugin-provider-split-brace",
		executeStream: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			return pluginapi.ExecutorStreamResponse{
				Chunks: streamChunks,
			}, nil
		},
	}

	adapter := newExecutorAdapterForRecordForTest(host, executorRecord, exec,
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
	)
	adapter.provider = "plugin-provider-split-brace"

	auth := &coreauth.Auth{
		ID:       "auth-split-brace-1",
		Provider: "plugin-provider-split-brace",
	}

	req := coreexecutor.Request{
		Model:   "test-model",
		Payload: []byte(`{"model":"test-model","stream":true}`),
	}

	streamRes, err := adapter.ExecuteStream(context.Background(), auth, req, coreexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream returned unexpected error: %v", err)
	}

	for range streamRes.Chunks {
	}

	rec := plugin.waitRecord(t, 200*time.Millisecond)
	if rec.Detail.InputTokens != 18 || rec.Detail.OutputTokens != 22 || rec.Detail.TotalTokens != 40 {
		t.Errorf("got usage %+v, want prompt=18 completion=22 total=40", rec.Detail)
	}
}

func TestExecutorAdapterExecuteStreamErrorChunkImmediatePublishWithoutClose(t *testing.T) {
	plugin := newTestUsageCapturePlugin("plugin-provider-stream-err-unclosed")
	registerTestUsagePlugin(t, "test-executor-adapter-stream-err-unclosed", plugin)

	executorRecord := normalizeTestCapabilityRecord(capabilityRecord{id: "executor-plugin-stream-err-unclosed"})
	host := newHostWithRecords(executorRecord)

	expectedErr := errors.New("mid-stream chunk failure without close")
	streamChunks := make(chan pluginapi.ExecutorStreamChunk)

	exec := &fakeExecutor{
		identifier: "plugin-provider-stream-err-unclosed",
		executeStream: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			return pluginapi.ExecutorStreamResponse{
				Chunks: streamChunks,
			}, nil
		},
	}

	adapter := newExecutorAdapterForRecordForTest(host, executorRecord, exec,
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
	)
	adapter.provider = "plugin-provider-stream-err-unclosed"

	auth := &coreauth.Auth{
		ID:       "auth-stream-err-unclosed-1",
		Provider: "plugin-provider-stream-err-unclosed",
	}

	req := coreexecutor.Request{
		Model:   "test-model",
		Payload: []byte(`{"model":"test-model","stream":true}`),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	streamRes, err := adapter.ExecuteStream(ctx, auth, req, coreexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream returned unexpected error: %v", err)
	}

	// Send error chunk but do NOT close the channel
	go func() {
		streamChunks <- pluginapi.ExecutorStreamChunk{Err: expectedErr}
	}()

	chunk := <-streamRes.Chunks
	if chunk.Err == nil || !errors.Is(chunk.Err, expectedErr) {
		t.Fatalf("expected chunk error %v, got %v", expectedErr, chunk.Err)
	}

	// Verify that usage failure record was published immediately without channel close
	rec := plugin.waitRecord(t, 200*time.Millisecond)
	if !rec.Failed {
		t.Errorf("rec.Failed = false, want true")
	}
}

func TestObservePluginExecutorStreamTTFT_Antigravity(t *testing.T) {
	reporter := helps.NewUsageReporter(context.Background(), "antigravity", "test-model", nil)
	reporter.StartResponseTTFT()
	antigravityChunk := []byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}}`)
	helps.ObservePluginExecutorStreamTTFT("antigravity", reporter, antigravityChunk)
	if !reporter.IsTTFTSet() {
		t.Errorf("ObservePluginExecutorStreamTTFT for antigravity should set TTFT on token event")
	}
}

type capturingUsagePlugin struct {
	captured chan pluginapi.UsageRecord
}

func (p *capturingUsagePlugin) HandleUsage(_ context.Context, record pluginapi.UsageRecord) {
	p.captured <- record
}

func TestUsageAdapterRecoversSessionHierarchyFromContext(t *testing.T) {
	plugin := &capturingUsagePlugin{captured: make(chan pluginapi.UsageRecord, 1)}
	host := newHostWithRecords(capabilityRecord{
		id: "usage-session",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			UsagePlugin: plugin,
		}},
	})
	adapter := &usageAdapter{
		host:     host,
		pluginID: "usage-session",
	}

	ctx := logging.WithClientRequestMetadata(context.Background(), logging.ClientRequestMetadata{
		SessionID:       "sess-ctx-1",
		ParentSessionID: "parent-ctx-1",
	})

	// 1. Record has empty session fields, should recover from context
	adapter.HandleUsage(ctx, coreusage.Record{
		Provider: "test-provider",
		Model:    "test-model",
	})
	rec := <-plugin.captured
	if rec.SessionID != "sess-ctx-1" || rec.ParentSessionID != "parent-ctx-1" {
		t.Fatalf("recovered session = (%q, %q), want (sess-ctx-1, parent-ctx-1)", rec.SessionID, rec.ParentSessionID)
	}

	// 2. Self-referential loop protection in context
	ctxLoop := logging.WithClientRequestMetadata(context.Background(), logging.ClientRequestMetadata{
		SessionID:       "loop-sess",
		ParentSessionID: "loop-sess",
	})
	adapter.HandleUsage(ctxLoop, coreusage.Record{
		Provider: "test-provider",
		Model:    "test-model",
	})
	recLoop := <-plugin.captured
	if recLoop.SessionID != "loop-sess" || recLoop.ParentSessionID != "" {
		t.Fatalf("loop protection = (%q, %q), want (loop-sess, empty)", recLoop.SessionID, recLoop.ParentSessionID)
	}
}

func TestUsageAdapterPropagatesNativeKeyAndAccessProvider(t *testing.T) {
	plugin := &capturingUsagePlugin{captured: make(chan pluginapi.UsageRecord, 1)}
	host := newHostWithRecords(capabilityRecord{
		id: "usage-native-key",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			UsagePlugin: plugin,
		}},
	})
	adapter := &usageAdapter{
		host:     host,
		pluginID: "usage-native-key",
	}

	// 1. Direct record propagation
	adapter.HandleUsage(context.Background(), coreusage.Record{
		Provider:       "test-provider",
		Model:          "test-model",
		APIKey:         "sk-native-key",
		IsNativeKey:    true,
		AccessProvider: "config-inline",
	})
	rec := <-plugin.captured
	if !rec.IsNativeKey {
		t.Fatalf("rec.IsNativeKey = false, want true")
	}
	if rec.AccessProvider != "config-inline" {
		t.Fatalf("rec.AccessProvider = %q, want %q", rec.AccessProvider, "config-inline")
	}

	// 2. Fallback to context values when record fields are omitted
	ctx := coreusage.WithAPIKey(context.Background(), "sk-ctx-native")
	ctx = coreusage.WithAccessProvider(ctx, "config-inline")
	adapter.HandleUsage(ctx, coreusage.Record{
		Provider: "test-provider",
		Model:    "test-model",
	})
	recCtx := <-plugin.captured
	if recCtx.APIKey != "sk-ctx-native" {
		t.Fatalf("recCtx.APIKey = %q, want sk-ctx-native", recCtx.APIKey)
	}
	if !recCtx.IsNativeKey {
		t.Fatalf("recCtx.IsNativeKey = false, want true")
	}
	if recCtx.AccessProvider != "config-inline" {
		t.Fatalf("recCtx.AccessProvider = %q, want config-inline", recCtx.AccessProvider)
	}
}

func TestExecutorAdapterExecuteAttributesResponsesUsageToSelectedAuth(t *testing.T) {
	plugin := newTestUsageCapturePlugin("plugin-provider-responses")
	registerTestUsagePlugin(t, "test-executor-adapter-responses-auth-usage", plugin)

	executorRecord := normalizeTestCapabilityRecord(capabilityRecord{id: "executor-plugin-responses"})
	host := newHostWithRecords(executorRecord)

	exec := &fakeExecutor{
		identifier: "plugin-provider-responses",
		execute: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			return pluginapi.ExecutorResponse{
				Payload: []byte(`{"id":"resp_1","object":"response","service_tier":"default","usage":{"input_tokens":34,"output_tokens":499,"total_tokens":533}}`),
			}, nil
		},
	}

	adapter := newExecutorAdapterForRecordForTest(host, executorRecord, exec,
		[]sdktranslator.Format{sdktranslator.FormatOpenAIResponse},
		[]sdktranslator.Format{sdktranslator.FormatOpenAIResponse},
	)
	adapter.provider = "plugin-provider-responses"

	auth := &coreauth.Auth{
		ID:         "auth-responses-1",
		Provider:   "plugin-provider-responses",
		FileName:   "auth-responses-1.json",
		Attributes: map[string]string{"type": "oauth"},
	}
	req := coreexecutor.Request{
		Model:   "deepseek/deepseek-v4.1-flash",
		Payload: []byte(`{"model":"deepseek/deepseek-v4.1-flash","input":"Write me a poem"}`),
	}
	opts := coreexecutor.Options{
		Stream:         false,
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
	}

	ctx := coreusage.WithStream(context.Background(), false)
	resp, errExecute := adapter.Execute(ctx, auth, req, opts)
	if errExecute != nil {
		t.Fatalf("adapter.Execute returned unexpected error: %v", errExecute)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("adapter.Execute returned empty payload")
	}

	rec := plugin.waitRecord(t, 200*time.Millisecond)
	if rec.AuthID != auth.ID {
		t.Errorf("AuthID = %q, want %q", rec.AuthID, auth.ID)
	}
	if rec.Stream {
		t.Errorf("Stream = true, want false")
	}
	if rec.Detail.InputTokens != 34 || rec.Detail.OutputTokens != 499 || rec.Detail.TotalTokens != 533 {
		t.Errorf("usage = %+v, want input=34 output=499 total=533", rec.Detail)
	}
}

func TestExecutorAdapterExecuteStreamLargeSSELineKeepsUsage(t *testing.T) {
	plugin := newTestUsageCapturePlugin("plugin-provider-bigline")
	registerTestUsagePlugin(t, "test-executor-adapter-bigline-usage", plugin)

	executorRecord := normalizeTestCapabilityRecord(capabilityRecord{id: "executor-plugin-bigline"})
	host := newHostWithRecords(executorRecord)

	echo := strings.Repeat("i", 70*1024)
	terminal := `event: response.completed` + "\n" +
		`data: {"type":"response.completed","response":{"instructions":"` + echo +
		`","usage":{"input_tokens":7000,"output_tokens":30,"total_tokens":7030}}}` + "\n\n"
	prefix := "event: response.created\ndata: {\"type\":\"response.created\"}\n\n"
	whole := prefix + terminal

	// The first read ends 66KB into the terminal data line: more than 64KB
	// has accumulated and the newline has not arrived yet.
	streamChunks := make(chan pluginapi.ExecutorStreamChunk, 4)
	streamChunks <- pluginapi.ExecutorStreamChunk{Payload: []byte(whole[:66000])}
	streamChunks <- pluginapi.ExecutorStreamChunk{Payload: []byte(whole[66000:])}
	close(streamChunks)

	exec := &fakeExecutor{
		identifier: "plugin-provider-bigline",
		executeStream: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			return pluginapi.ExecutorStreamResponse{Chunks: streamChunks}, nil
		},
	}

	adapter := newExecutorAdapterForRecordForTest(host, executorRecord, exec,
		[]sdktranslator.Format{sdktranslator.FormatOpenAIResponse},
		[]sdktranslator.Format{sdktranslator.FormatOpenAIResponse},
	)
	adapter.provider = "plugin-provider-bigline"

	auth := &coreauth.Auth{ID: "auth-bigline-1", Provider: "plugin-provider-bigline"}
	req := coreexecutor.Request{Model: "test-model", Payload: []byte(`{"model":"test-model","stream":true}`)}
	opts := coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Stream:         true,
	}

	streamRes, errExecuteStream := adapter.ExecuteStream(context.Background(), auth, req, opts)
	if errExecuteStream != nil {
		t.Fatalf("ExecuteStream returned unexpected error: %v", errExecuteStream)
	}
	var got strings.Builder
	for chunk := range streamRes.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
		got.Write(chunk.Payload)
	}
	if got.String() != whole {
		t.Fatal("payload was altered")
	}

	rec := plugin.waitRecord(t, 500*time.Millisecond)
	if rec.Detail.InputTokens != 7000 || rec.Detail.OutputTokens != 30 || rec.Detail.TotalTokens != 7030 {
		t.Errorf("got usage input=%d output=%d total=%d, want input=7000 output=30 total=7030: "+
			"the terminal frame's usage was dropped",
			rec.Detail.InputTokens, rec.Detail.OutputTokens, rec.Detail.TotalTokens)
	}
}

type endToEndSelectionTestExecutor struct {
	provider      string
	execute       func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error)
	executeStream func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error)
}

func (e *endToEndSelectionTestExecutor) Identifier() string { return e.provider }

func (e *endToEndSelectionTestExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	if e.execute != nil {
		return e.execute(ctx, auth, req, opts)
	}
	return coreexecutor.Response{}, nil
}

func (e *endToEndSelectionTestExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	if e.executeStream != nil {
		return e.executeStream(ctx, auth, req, opts)
	}
	return nil, nil
}

func (e *endToEndSelectionTestExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *endToEndSelectionTestExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}

func (e *endToEndSelectionTestExecutor) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestSchedulerPickAndUsageRecordShareSelectionIDEndToEnd(t *testing.T) {
	provider := "selection-e2e-provider"
	model := "selection-e2e-model"

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("auth-e2e-1", provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient("auth-e2e-1")
	})

	pickSelectionIDs := make(chan string, 32)
	usageRecords := make(chan pluginapi.UsageRecord, 32)

	host := newHostWithRecords(capabilityRecord{
		id:       "routing-usage-plugin",
		priority: 10,
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			Scheduler: schedulerFunc(func(ctx context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
				selID, _ := req.Options.Metadata[pluginapi.SelectionIDMetadataKey].(string)
				pickSelectionIDs <- selID
				return pluginapi.SchedulerPickResponse{
					Handled: true,
					AuthID:  "auth-e2e-1",
				}, nil
			}),
			UsagePlugin: usagePluginFunc(func(ctx context.Context, record pluginapi.UsageRecord) {
				if record.Provider == provider {
					usageRecords <- record
				}
			}),
		}},
	})
	host.RegisterUsagePlugins()
	t.Cleanup(func() {
		coreusage.RegisterNamedPlugin("plugin:routing-usage-plugin", noopUsagePlugin{})
	})

	manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
	manager.SetRetryConfig(1, 0, 0)
	coreauth.SetQuotaCooldownDisabled(true)
	t.Cleanup(func() { coreauth.SetQuotaCooldownDisabled(false) })
	manager.SetPluginScheduler(host)

	if _, errReg := manager.Register(context.Background(), &coreauth.Auth{ID: "auth-e2e-1", Provider: provider}); errReg != nil {
		t.Fatalf("Register(auth-e2e-1) error = %v", errReg)
	}

	attemptCount := 0
	streamRelease := make(chan struct{})
	manager.RegisterExecutor(&endToEndSelectionTestExecutor{
		provider: provider,
		execute: func(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
			reporter := helps.NewUsageReporter(ctx, provider, req.Model, auth)
			if opts.Metadata["mode"] == "retry-429-once" {
				attemptCount++
				if attemptCount == 1 {
					reporter.PublishFailure(ctx, &coreauth.Error{Code: "rate_limited", Message: "429", HTTPStatus: http.StatusTooManyRequests, Retryable: true})
					return coreexecutor.Response{}, &coreauth.Error{Code: "rate_limited", Message: "429", HTTPStatus: http.StatusTooManyRequests, Retryable: true}
				}
			}
			reporter.Publish(ctx, coreusage.Detail{InputTokens: 10, OutputTokens: 5, TotalTokens: 15})
			return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
		},
		executeStream: func(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
			reporter := helps.NewUsageReporter(ctx, provider, req.Model, auth)
			ch := make(chan coreexecutor.StreamChunk, 1)
			ch <- coreexecutor.StreamChunk{Payload: []byte("data: chunk\n\n")}
			go func() {
				<-streamRelease
				close(ch)
				// Delayed asynchronous usage publication after other requests have run.
				reporter.Publish(context.Background(), coreusage.Detail{InputTokens: 20, OutputTokens: 10, TotalTokens: 30})
			}()
			return &coreexecutor.StreamResult{Chunks: ch}, nil
		},
	})

	// 1. Single credential outer retry after 429: two Picks, two distinct SelectionIDs, each matching its UsageRecord.
	traceCtx := logging.WithRequestID(context.Background(), "trace-429")
	_, errExec := manager.Execute(traceCtx, []string{provider}, coreexecutor.Request{Model: model}, coreexecutor.Options{
		Metadata: map[string]any{"mode": "retry-429-once"},
	})
	if errExec != nil {
		t.Fatalf("Execute(retry-429-once) error = %v", errExec)
	}
	pick1 := <-pickSelectionIDs
	pick2 := <-pickSelectionIDs
	if pick1 == "" || pick2 == "" || pick1 == pick2 {
		t.Fatalf("outer retry pick SelectionIDs = (%q, %q), want two distinct non-empty IDs", pick1, pick2)
	}

	rec1 := waitPluginUsageRecord(t, usageRecords)
	rec2 := waitPluginUsageRecord(t, usageRecords)
	if rec1.SelectionID != pick1 || !rec1.Failed {
		t.Fatalf("first UsageRecord = (SelectionID=%q, Failed=%v), want (%q, true)", rec1.SelectionID, rec1.Failed, pick1)
	}
	if rec2.SelectionID != pick2 || rec2.Failed {
		t.Fatalf("second UsageRecord = (SelectionID=%q, Failed=%v), want (%q, false)", rec2.SelectionID, rec2.Failed, pick2)
	}
	if rec1.RequestID == rec2.RequestID {
		t.Fatalf("UsageRecord RequestIDs should remain distinct: %q", rec1.RequestID)
	}

	// 2. Concurrent requests and delayed asynchronous streaming usage keep their own SelectionIDs.
	sharedOpts := coreexecutor.Options{Stream: true, Metadata: map[string]any{"shared": "true"}}
	streamRes, errStream := manager.ExecuteStream(context.Background(), []string{provider}, coreexecutor.Request{Model: model}, sharedOpts)
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	streamPickID := <-pickSelectionIDs

	// Run a concurrent non-streaming request while the stream is still open and its usage has not been published.
	if _, errConcurrent := manager.Execute(context.Background(), []string{provider}, coreexecutor.Request{Model: model}, coreexecutor.Options{Metadata: sharedOpts.Metadata}); errConcurrent != nil {
		t.Fatalf("concurrent Execute() error = %v", errConcurrent)
	}
	concurrentPickID := <-pickSelectionIDs
	if streamPickID == "" || concurrentPickID == "" || streamPickID == concurrentPickID {
		t.Fatalf("streamPickID=%q concurrentPickID=%q, want distinct non-empty IDs", streamPickID, concurrentPickID)
	}

	concurrentRec := waitPluginUsageRecord(t, usageRecords)
	if concurrentRec.SelectionID != concurrentPickID {
		t.Fatalf("concurrent UsageRecord.SelectionID = %q, want %q", concurrentRec.SelectionID, concurrentPickID)
	}

	close(streamRelease)
	for range streamRes.Chunks {
	}
	streamRec := waitPluginUsageRecord(t, usageRecords)
	if streamRec.SelectionID != streamPickID {
		t.Fatalf("delayed stream UsageRecord.SelectionID = %q, want %q", streamRec.SelectionID, streamPickID)
	}
}

func waitPluginUsageRecord(t *testing.T, ch <-chan pluginapi.UsageRecord) pluginapi.UsageRecord {
	t.Helper()
	select {
	case rec := <-ch:
		return rec
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for pluginapi.UsageRecord")
		return pluginapi.UsageRecord{}
	}
}
