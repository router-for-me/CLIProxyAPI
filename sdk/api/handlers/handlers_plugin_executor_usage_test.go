package handlers

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type noopUsagePlugin struct{}

func (noopUsagePlugin) HandleUsage(context.Context, usage.Record) {}

type capturePluginExecutorUsagePlugin struct {
	targetProvider string
	records        chan usage.Record
}

func newCapturePluginExecutorUsagePlugin(targetProvider string) *capturePluginExecutorUsagePlugin {
	return &capturePluginExecutorUsagePlugin{
		targetProvider: targetProvider,
		records:        make(chan usage.Record, 50),
	}
}

func (p *capturePluginExecutorUsagePlugin) HandleUsage(_ context.Context, record usage.Record) {
	if p.targetProvider != "" && record.Provider != p.targetProvider {
		return
	}
	select {
	case p.records <- record:
	default:
	}
}

func (p *capturePluginExecutorUsagePlugin) waitRecord(t *testing.T) usage.Record {
	t.Helper()
	select {
	case rec := <-p.records:
		return rec
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for usage record")
		return usage.Record{}
	}
}

func (p *capturePluginExecutorUsagePlugin) assertNoRecord(t *testing.T) {
	t.Helper()
	select {
	case rec := <-p.records:
		t.Fatalf("expected no usage record for %q, got %+v", p.targetProvider, rec)
	case <-time.After(50 * time.Millisecond):
	}
}

func registerUsagePluginForTest(t *testing.T, name string, plugin usage.Plugin) {
	t.Helper()
	usage.RegisterNamedPlugin(name, plugin)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin(name, noopUsagePlugin{})
	})
}

func TestHandlerPluginExecutorPublishesUsageNonStreamOpenAI(t *testing.T) {
	targetPluginID := "custom-openai-plugin"
	plugin := newCapturePluginExecutorUsagePlugin(targetPluginID)
	registerUsagePluginForTest(t, "test-plugin-executor-usage-nonstream-openai", plugin)

	originalModel := "gpt-4o"

	openAIResponseBody := []byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"hello"}}],"usage":{"prompt_tokens":12,"completion_tokens":34,"total_tokens":46}}`)

	mockHost := &mockPluginUsageHost{
		execResp: coreexecutor.Response{
			Payload: openAIResponseBody,
		},
	}
	mockHost.hasRouters = true
	mockHost.route = func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: targetPluginID}, true
	}

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler.SetModelRouterHost(mockHost)

	body, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "openai", originalModel, []byte(fmt.Sprintf(`{"model":%q}`, originalModel)), "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager() error = %+v", errMsg)
	}
	if len(body) == 0 {
		t.Fatal("empty response body")
	}

	record := plugin.waitRecord(t)
	if record.Provider != targetPluginID {
		t.Errorf("record.Provider = %q, want %q", record.Provider, targetPluginID)
	}
	if record.Stream {
		t.Errorf("record.Stream = true, want false")
	}
	if record.Detail.InputTokens != 12 || record.Detail.OutputTokens != 34 || record.Detail.TotalTokens != 46 {
		t.Errorf("record.Detail = %+v, want prompt=12 completion=34 total=46", record.Detail)
	}
}

func TestHandlerPluginExecutorPublishesUsageStreamOpenAI(t *testing.T) {
	targetPluginID := "custom-stream-plugin"
	plugin := newCapturePluginExecutorUsagePlugin(targetPluginID)
	registerUsagePluginForTest(t, "test-plugin-executor-usage-stream-openai", plugin)

	originalModel := "gpt-4o"

	chunks := make(chan coreexecutor.StreamChunk, 3)
	chunks <- coreexecutor.StreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n")}
	chunks <- coreexecutor.StreamChunk{Payload: []byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":15,\"completion_tokens\":25,\"total_tokens\":40}}\n\n")}
	chunks <- coreexecutor.StreamChunk{Payload: []byte("data: [DONE]\n\n")}
	close(chunks)

	mockHost := &mockPluginUsageHost{
		streamResult: &coreexecutor.StreamResult{Chunks: chunks},
	}
	mockHost.hasRouters = true
	mockHost.route = func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: targetPluginID}, true
	}

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler.SetModelRouterHost(mockHost)

	dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "openai", originalModel, []byte(fmt.Sprintf(`{"model":%q,"stream":true}`, originalModel)), "")
	for range dataChan {
	}
	for err := range errChan {
		if err != nil {
			t.Fatalf("stream error = %+v", err)
		}
	}

	record := plugin.waitRecord(t)
	if record.Provider != targetPluginID {
		t.Errorf("record.Provider = %q, want %q", record.Provider, targetPluginID)
	}
	if !record.Stream {
		t.Errorf("record.Stream = false, want true")
	}
	if record.Detail.InputTokens != 15 || record.Detail.OutputTokens != 25 || record.Detail.TotalTokens != 40 {
		t.Errorf("record.Detail = %+v, want prompt=15 completion=25 total=40", record.Detail)
	}
}

func TestHandlerPluginExecutorPublishesUsageStreamCodex(t *testing.T) {
	targetPluginID := "custom-codex-stream-plugin"
	plugin := newCapturePluginExecutorUsagePlugin(targetPluginID)
	registerUsagePluginForTest(t, "test-plugin-executor-usage-stream-codex", plugin)

	originalModel := "codex-5.2"

	chunks := make(chan coreexecutor.StreamChunk, 2)
	chunks <- coreexecutor.StreamChunk{Payload: []byte("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":18,\"output_tokens\":22,\"total_tokens\":40}}}\n\n")}
	chunks <- coreexecutor.StreamChunk{Payload: []byte("data: [DONE]\n\n")}
	close(chunks)

	mockHost := &mockPluginUsageHost{
		streamResult: &coreexecutor.StreamResult{Chunks: chunks},
	}
	mockHost.hasRouters = true
	mockHost.route = func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: targetPluginID}, true
	}

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler.SetModelRouterHost(mockHost)

	dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "openai-response", originalModel, []byte(fmt.Sprintf(`{"model":%q,"stream":true}`, originalModel)), "")
	for range dataChan {
	}
	for err := range errChan {
		if err != nil {
			t.Fatalf("stream error = %+v", err)
		}
	}

	record := plugin.waitRecord(t)
	if record.Provider != targetPluginID {
		t.Errorf("record.Provider = %q, want %q", record.Provider, targetPluginID)
	}
	if record.Detail.InputTokens != 18 || record.Detail.OutputTokens != 22 || record.Detail.TotalTokens != 40 {
		t.Errorf("record.Detail = %+v, want input=18 output=22 total=40", record.Detail)
	}
}

func TestHandlerPluginExecutorPublishesUsageNonStreamClaude(t *testing.T) {
	targetPluginID := "custom-claude-plugin"
	plugin := newCapturePluginExecutorUsagePlugin(targetPluginID)
	registerUsagePluginForTest(t, "test-plugin-executor-usage-nonstream-claude", plugin)

	originalModel := "claude-3-5-sonnet"

	claudeResponseBody := []byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"hello"}],"usage":{"input_tokens":50,"output_tokens":30,"output_tokens_details":{"thinking_tokens":10}}}`)

	mockHost := &mockPluginUsageHost{
		execResp: coreexecutor.Response{
			Payload: claudeResponseBody,
		},
	}
	mockHost.hasRouters = true
	mockHost.route = func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: targetPluginID}, true
	}

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler.SetModelRouterHost(mockHost)

	body, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "claude", originalModel, []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}]}`, originalModel)), "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager() error = %+v", errMsg)
	}
	if len(body) == 0 {
		t.Fatal("empty response body")
	}

	record := plugin.waitRecord(t)
	if record.Provider != targetPluginID {
		t.Errorf("record.Provider = %q, want %q", record.Provider, targetPluginID)
	}
	if record.Detail.InputTokens != 50 || record.Detail.OutputTokens != 30 || record.Detail.ReasoningTokens != 10 || record.Detail.TotalTokens != 80 {
		t.Errorf("record.Detail = %+v, want input=50 output=30 reasoning=10 total=80", record.Detail)
	}
}

func TestHandlerPluginExecutorPublishesUsageStreamClaude(t *testing.T) {
	targetPluginID := "custom-claude-stream-plugin"
	plugin := newCapturePluginExecutorUsagePlugin(targetPluginID)
	registerUsagePluginForTest(t, "test-plugin-executor-usage-stream-claude", plugin)

	originalModel := "claude-3-5-sonnet"

	// Claude streams split usage between message_start (input, cache) and message_delta (output, thinking)
	chunks := make(chan coreexecutor.StreamChunk, 4)
	chunks <- coreexecutor.StreamChunk{Payload: []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"usage\":{\"input_tokens\":100,\"cache_read_input_tokens\":50,\"cache_creation_input_tokens\":20,\"output_tokens\":1}}}\n\n")}
	chunks <- coreexecutor.StreamChunk{Payload: []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\n")}
	chunks <- coreexecutor.StreamChunk{Payload: []byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":25,\"output_tokens_details\":{\"thinking_tokens\":5}}}\n\n")}
	chunks <- coreexecutor.StreamChunk{Payload: []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")}
	close(chunks)

	mockHost := &mockPluginUsageHost{
		streamResult: &coreexecutor.StreamResult{Chunks: chunks},
	}
	mockHost.hasRouters = true
	mockHost.route = func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: targetPluginID}, true
	}

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler.SetModelRouterHost(mockHost)

	dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "claude", originalModel, []byte(fmt.Sprintf(`{"model":%q,"stream":true}`, originalModel)), "")
	for range dataChan {
	}
	for err := range errChan {
		if err != nil {
			t.Fatalf("stream error = %+v", err)
		}
	}

	record := plugin.waitRecord(t)
	if record.Provider != targetPluginID {
		t.Errorf("record.Provider = %q, want %q", record.Provider, targetPluginID)
	}
	if record.Detail.InputTokens != 100 || record.Detail.CacheReadTokens != 50 || record.Detail.CacheCreationTokens != 20 || record.Detail.OutputTokens != 25 || record.Detail.ReasoningTokens != 5 || record.Detail.TotalTokens != 195 {
		t.Errorf("record.Detail = %+v, want input=100 cache_read=50 cache_creation=20 output=25 reasoning=5 total=195", record.Detail)
	}
	tb := record.Detail.TokenBreakdown
	if !tb.Valid() || tb.TotalTokens != 195 || tb.Input.TotalTokens != 170 || tb.Input.UncachedTokens != 100 || tb.Input.CacheReadTokens != 50 || tb.Input.CacheWriteTokens != 20 || tb.Output.TotalTokens != 25 || tb.Output.NonReasoningTokens != 20 || tb.Output.ReasoningTokens != 5 {
		t.Errorf("record.Detail.TokenBreakdown = %+v, want valid independent breakdown with total=195 input=170 output=25", tb)
	}
}

func TestHandlerPluginExecutorPublishesUsageGemini(t *testing.T) {
	targetPluginID := "custom-gemini-plugin"
	plugin := newCapturePluginExecutorUsagePlugin(targetPluginID)
	registerUsagePluginForTest(t, "test-plugin-executor-usage-gemini", plugin)

	originalModel := "gemini-2.5-flash"

	geminiResponseBody := []byte(`{"candidates":[{"content":{"parts":[{"text":"hello"}]}}],"usageMetadata":{"promptTokenCount":40,"candidatesTokenCount":60,"totalTokenCount":100}}`)

	mockHost := &mockPluginUsageHost{
		execResp: coreexecutor.Response{
			Payload: geminiResponseBody,
		},
	}
	mockHost.hasRouters = true
	mockHost.route = func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: targetPluginID}, true
	}

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler.SetModelRouterHost(mockHost)

	body, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "gemini", originalModel, []byte(fmt.Sprintf(`{"contents":[{"parts":[{"text":"hi"}]}]}`)), "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager() error = %+v", errMsg)
	}
	if len(body) == 0 {
		t.Fatal("empty response body")
	}

	record := plugin.waitRecord(t)
	if record.Provider != targetPluginID {
		t.Errorf("record.Provider = %q, want %q", record.Provider, targetPluginID)
	}
	if record.Detail.InputTokens != 40 || record.Detail.OutputTokens != 60 || record.Detail.TotalTokens != 100 {
		t.Errorf("record.Detail = %+v, want prompt=40 candidates=60 total=100", record.Detail)
	}
}

func TestHandlerPluginExecutorPublishesUsageStreamInteractions(t *testing.T) {
	targetPluginID := "custom-interactions-stream-plugin"
	plugin := newCapturePluginExecutorUsagePlugin(targetPluginID)
	registerUsagePluginForTest(t, "test-plugin-executor-usage-stream-interactions", plugin)

	originalModel := "gemini-2.5-flash"

	chunks := make(chan coreexecutor.StreamChunk, 2)
	chunks <- coreexecutor.StreamChunk{Payload: []byte("data: {\"event_type\":\"finish\",\"metadata\":{\"total_usage\":{\"total_input_tokens\":30,\"total_output_tokens\":70,\"total_tokens\":100}}}\n\n")}
	chunks <- coreexecutor.StreamChunk{Payload: []byte("data: [DONE]\n\n")}
	close(chunks)

	mockHost := &mockPluginUsageHost{
		streamResult: &coreexecutor.StreamResult{Chunks: chunks},
	}
	mockHost.hasRouters = true
	mockHost.route = func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: targetPluginID}, true
	}

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler.SetModelRouterHost(mockHost)

	dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "interactions", originalModel, []byte(fmt.Sprintf(`{"model":%q,"stream":true}`, originalModel)), "")
	for range dataChan {
	}
	for err := range errChan {
		if err != nil {
			t.Fatalf("stream error = %+v", err)
		}
	}

	record := plugin.waitRecord(t)
	if record.Provider != targetPluginID {
		t.Errorf("record.Provider = %q, want %q", record.Provider, targetPluginID)
	}
	if record.Detail.InputTokens != 30 || record.Detail.OutputTokens != 70 || record.Detail.TotalTokens != 100 {
		t.Errorf("record.Detail = %+v, want input=30 output=70 total=100", record.Detail)
	}
}

func TestHandlerPluginExecutorPublishesUsageStreamAntigravity(t *testing.T) {
	targetPluginID := "custom-antigravity-stream-plugin"
	plugin := newCapturePluginExecutorUsagePlugin(targetPluginID)
	registerUsagePluginForTest(t, "test-plugin-executor-usage-stream-antigravity", plugin)

	originalModel := "claude-3-5-sonnet"

	chunks := make(chan coreexecutor.StreamChunk, 2)
	chunks <- coreexecutor.StreamChunk{Payload: []byte("data: {\"response\":{\"usageMetadata\":{\"promptTokenCount\":33,\"candidatesTokenCount\":67,\"totalTokenCount\":100}}}\n\n")}
	chunks <- coreexecutor.StreamChunk{Payload: []byte("data: [DONE]\n\n")}
	close(chunks)

	mockHost := &mockPluginUsageHost{
		streamResult: &coreexecutor.StreamResult{Chunks: chunks},
	}
	mockHost.hasRouters = true
	mockHost.route = func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: targetPluginID}, true
	}

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler.SetModelRouterHost(mockHost)

	dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "antigravity", originalModel, []byte(fmt.Sprintf(`{"model":%q,"stream":true}`, originalModel)), "")
	for range dataChan {
	}
	for err := range errChan {
		if err != nil {
			t.Fatalf("stream error = %+v", err)
		}
	}

	record := plugin.waitRecord(t)
	if record.Provider != targetPluginID {
		t.Errorf("record.Provider = %q, want %q", record.Provider, targetPluginID)
	}
	if record.Detail.InputTokens != 33 || record.Detail.OutputTokens != 67 || record.Detail.TotalTokens != 100 {
		t.Errorf("record.Detail = %+v, want prompt=33 candidates=67 total=100", record.Detail)
	}
}

func TestHandlerPluginExecutorPublishesFailure(t *testing.T) {
	targetPluginID := "failing-plugin"
	plugin := newCapturePluginExecutorUsagePlugin(targetPluginID)
	registerUsagePluginForTest(t, "test-plugin-executor-usage-failure", plugin)

	originalModel := "gpt-4o"

	mockHost := &mockPluginUsageHost{
		execErr: errors.New("upstream plugin failure"),
	}
	mockHost.hasRouters = true
	mockHost.route = func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: targetPluginID}, true
	}

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler.SetModelRouterHost(mockHost)

	_, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "openai", originalModel, []byte(fmt.Sprintf(`{"model":%q}`, originalModel)), "")
	if errMsg == nil {
		t.Fatal("expected ExecuteWithAuthManager() to fail")
	}

	record := plugin.waitRecord(t)
	if record.Provider != targetPluginID {
		t.Errorf("record.Provider = %q, want %q", record.Provider, targetPluginID)
	}
	if !record.Failed {
		t.Error("record.Failed = false, want true")
	}
	if record.Fail.Body != "upstream plugin failure" {
		t.Errorf("record.Fail.Body = %q, want %q", record.Fail.Body, "upstream plugin failure")
	}
}

func TestHandlerPluginExecutorPublishesStreamFailure(t *testing.T) {
	targetPluginID := "failing-stream-plugin"
	plugin := newCapturePluginExecutorUsagePlugin(targetPluginID)
	registerUsagePluginForTest(t, "test-plugin-executor-usage-stream-failure", plugin)

	originalModel := "gpt-4o"

	chunks := make(chan coreexecutor.StreamChunk, 2)
	chunks <- coreexecutor.StreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n")}
	chunks <- coreexecutor.StreamChunk{Err: errors.New("upstream stream broke")}
	close(chunks)

	mockHost := &mockPluginUsageHost{
		streamResult: &coreexecutor.StreamResult{Chunks: chunks},
	}
	mockHost.hasRouters = true
	mockHost.route = func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: targetPluginID}, true
	}

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler.SetModelRouterHost(mockHost)

	dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "openai", originalModel, []byte(fmt.Sprintf(`{"model":%q,"stream":true}`, originalModel)), "")
	for range dataChan {
	}
	seenErr := false
	for err := range errChan {
		if err != nil {
			seenErr = true
		}
	}
	if !seenErr {
		t.Fatal("expected stream error")
	}

	record := plugin.waitRecord(t)
	if record.Provider != targetPluginID {
		t.Errorf("record.Provider = %q, want %q", record.Provider, targetPluginID)
	}
	if !record.Failed {
		t.Error("record.Failed = false, want true")
	}
}

func TestHandlerPluginExecutorPublishesStreamCancellation(t *testing.T) {
	targetPluginID := "canceling-stream-plugin"
	plugin := newCapturePluginExecutorUsagePlugin(targetPluginID)
	registerUsagePluginForTest(t, "test-plugin-executor-usage-stream-cancel", plugin)

	originalModel := "gpt-4o"

	ctx, cancel := context.WithCancel(context.Background())
	chunks := make(chan coreexecutor.StreamChunk, 5)
	chunks <- coreexecutor.StreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n")}

	mockHost := &mockPluginUsageHost{
		streamResult: &coreexecutor.StreamResult{Chunks: chunks},
	}
	mockHost.hasRouters = true
	mockHost.route = func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: targetPluginID}, true
	}

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler.SetModelRouterHost(mockHost)

	dataChan, _, _ := handler.ExecuteStreamWithAuthManager(ctx, "openai", originalModel, []byte(fmt.Sprintf(`{"model":%q,"stream":true}`, originalModel)), "")
	// Receive first chunk then cancel context
	<-dataChan
	cancel()
	close(chunks)

	record := plugin.waitRecord(t)
	if record.Provider != targetPluginID {
		t.Errorf("record.Provider = %q, want %q", record.Provider, targetPluginID)
	}
	if !record.Failed {
		t.Error("record.Failed = false, want true for canceled stream")
	}
}

func TestHandlerPluginExecutorSkipsUsageForNestedExecution(t *testing.T) {
	targetPluginID := "nested-plugin"
	plugin := newCapturePluginExecutorUsagePlugin(targetPluginID)
	registerUsagePluginForTest(t, "test-plugin-executor-usage-nested", plugin)

	originalModel := "gpt-4o"

	openAIResponseBody := []byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"hello"}}],"usage":{"prompt_tokens":12,"completion_tokens":34,"total_tokens":46}}`)

	mockHost := &mockPluginUsageHost{
		execResp: coreexecutor.Response{
			Payload: openAIResponseBody,
		},
	}
	mockHost.hasRouters = true
	mockHost.route = func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: targetPluginID}, true
	}

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler.SetModelRouterHost(mockHost)

	// ExecuteModel triggers execution with InternalSource = true (host.model.execute callback)
	resp, errMsg := handler.ExecuteModel(context.Background(), ModelExecutionRequest{
		EntryProtocol: "openai",
		ExitProtocol:  "openai",
		Model:         originalModel,
		Body:          []byte(fmt.Sprintf(`{"model":%q}`, originalModel)),
	})
	if errMsg != nil {
		t.Fatalf("ExecuteModel() error = %+v", errMsg)
	}
	if len(resp.Body) == 0 {
		t.Fatal("empty response body")
	}

	plugin.assertNoRecord(t)
}

func TestHandlerPluginExecutorSkipsOuterUsageWhenPluginCallsHostModelExecute(t *testing.T) {
	targetPluginID := "agent-wrapper-plugin"
	plugin := newCapturePluginExecutorUsagePlugin(targetPluginID)
	registerUsagePluginForTest(t, "test-plugin-executor-nested-callback", plugin)

	outerModel := "agent-wrapper-model"
	innerModel := "inner-model"

	manager := coreauth.NewManager(nil, nil, nil)
	innerExecutor := &modelExecutionCaptureExecutor{
		provider: "openai",
		execute: func(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
			return coreexecutor.Response{
				Payload: []byte(`{"id":"chatcmpl-inner","choices":[{"message":{"role":"assistant","content":"inner"}}],"usage":{"prompt_tokens":5,"completion_tokens":5,"total_tokens":10}}`),
			}, nil
		},
	}
	manager.RegisterExecutor(innerExecutor)
	auth := &coreauth.Auth{
		ID:       "auth-" + innerModel,
		Provider: innerExecutor.Identifier(),
		Status:   coreauth.StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("manager.Register(): %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: innerModel}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)

	mockHost := &mockPluginUsageHost{}
	mockHost.hasRouters = true
	mockHost.route = func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		if req.RequestedModel == outerModel {
			return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: targetPluginID}, true
		}
		return pluginapi.ModelRouteResponse{}, false
	}
	// When the plugin executor executes, it simulates calling back into the host via ExecuteModel
	mockHost.execFunc = func(ctx context.Context, pluginID string, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
		// Plugin calls back into host.model.execute using the provided ctx
		innerResp, errInner := handler.ExecuteModel(ctx, ModelExecutionRequest{
			EntryProtocol: "openai",
			ExitProtocol:  "openai",
			Model:         innerModel,
			Body:          []byte(fmt.Sprintf(`{"model":%q}`, innerModel)),
		})
		if errInner != nil {
			return coreexecutor.Response{}, errInner.Error
		}
		// Return response payload back to outer caller
		return coreexecutor.Response{Payload: innerResp.Body}, nil
	}

	handler.SetModelRouterHost(mockHost)

	body, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "openai", outerModel, []byte(fmt.Sprintf(`{"model":%q}`, outerModel)), "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager() error = %+v", errMsg)
	}
	if len(body) == 0 {
		t.Fatal("empty response body")
	}

	// Since inner ExecuteModel was executed, the outer plugin executor must NOT publish a duplicate record for targetPluginID
	plugin.assertNoRecord(t)
}

func TestHandlerPluginExecutorSkipsOuterFailureWhenPluginCallsHostModelExecute(t *testing.T) {
	targetPluginID := "agent-wrapper-plugin-failure"
	plugin := newCapturePluginExecutorUsagePlugin(targetPluginID)
	registerUsagePluginForTest(t, "test-plugin-executor-nested-callback-failure", plugin)

	outerModel := "agent-wrapper-model-fail"
	innerModel := "inner-model-fail"

	manager := coreauth.NewManager(nil, nil, nil)
	innerExecutor := &modelExecutionCaptureExecutor{
		provider: "openai",
		execute: func(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
			return coreexecutor.Response{}, errors.New("inner failure")
		},
	}
	manager.RegisterExecutor(innerExecutor)
	auth := &coreauth.Auth{
		ID:       "auth-" + innerModel,
		Provider: innerExecutor.Identifier(),
		Status:   coreauth.StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("manager.Register(): %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: innerModel}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)

	mockHost := &mockPluginUsageHost{}
	mockHost.hasRouters = true
	mockHost.route = func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		if req.RequestedModel == outerModel {
			return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: targetPluginID}, true
		}
		return pluginapi.ModelRouteResponse{}, false
	}
	mockHost.execFunc = func(ctx context.Context, pluginID string, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
		_, errInner := handler.ExecuteModel(ctx, ModelExecutionRequest{
			EntryProtocol: "openai",
			ExitProtocol:  "openai",
			Model:         innerModel,
			Body:          []byte(fmt.Sprintf(`{"model":%q}`, innerModel)),
		})
		if errInner != nil {
			return coreexecutor.Response{}, errInner.Error
		}
		return coreexecutor.Response{Payload: []byte("ok")}, nil
	}

	handler.SetModelRouterHost(mockHost)

	_, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "openai", outerModel, []byte(fmt.Sprintf(`{"model":%q}`, outerModel)), "")
	if errMsg == nil {
		t.Fatal("expected failure")
	}

	// Since inner ExecuteModel was executed, the outer plugin executor must NOT publish a duplicate failure record
	plugin.assertNoRecord(t)
}

type mockPluginUsageHost struct {
	handlerDirectExecutorRouteHost
	execResp     coreexecutor.Response
	execErr      error
	execFunc     func(context.Context, string, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error)
	streamResult *coreexecutor.StreamResult
}

func (h *mockPluginUsageHost) ExecutePluginExecutor(ctx context.Context, pluginID string, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	h.lastPluginID = pluginID
	h.lastRequest = req
	h.lastOptions = opts
	if h.execFunc != nil {
		return h.execFunc(ctx, pluginID, req, opts)
	}
	if h.execErr != nil {
		return coreexecutor.Response{}, h.execErr
	}
	return h.execResp, nil
}

func (h *mockPluginUsageHost) ExecutePluginExecutorStream(ctx context.Context, pluginID string, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	h.lastPluginID = pluginID
	h.lastRequest = req
	h.lastOptions = opts
	return h.streamResult, nil
}
