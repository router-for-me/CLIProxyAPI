package pluginhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

const (
	executorUsageTestProvider        = "codebuddy"
	executorUsageTestClientID        = "plugin:codebuddy:codebuddy:executor"
	executorUsageTestBarrierProvider = "__pluginhost_executor_usage_test_barrier__"
	executorUsageTestTimeout         = 2 * time.Second
)

type noopUsagePlugin struct{}

func (noopUsagePlugin) HandleUsage(context.Context, coreusage.Record) {}

type capturedExecutorUsage struct {
	requestID string
	record    coreusage.Record
}

type captureExecutorUsagePlugin struct {
	requestIDs map[string]struct{}
	barrierID  string
	records    chan capturedExecutorUsage
	barrier    chan struct{}
}

func (p *captureExecutorUsagePlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	requestID := internallogging.GetRequestID(ctx)
	if requestID == p.barrierID && record.Provider == executorUsageTestBarrierProvider {
		select {
		case p.barrier <- struct{}{}:
		default:
		}
		return
	}
	if _, ok := p.requestIDs[requestID]; !ok {
		return
	}
	p.records <- capturedExecutorUsage{requestID: requestID, record: record}
}

func registerCaptureExecutorUsagePlugin(t *testing.T, requestIDs ...string) *captureExecutorUsagePlugin {
	t.Helper()
	name := "test-pluginhost-executor-usage-" + executorUsageTestSuffix(t.Name())
	requested := make(map[string]struct{}, len(requestIDs))
	for _, requestID := range requestIDs {
		requested[requestID] = struct{}{}
	}
	plugin := &captureExecutorUsagePlugin{
		requestIDs: requested,
		barrierID:  name + "-barrier",
		records:    make(chan capturedExecutorUsage, 32),
		barrier:    make(chan struct{}, 1),
	}
	coreusage.RegisterNamedPlugin(name, plugin)
	t.Cleanup(func() {
		coreusage.RegisterNamedPlugin(name, noopUsagePlugin{})
	})
	return plugin
}

func (p *captureExecutorUsagePlugin) waitRecord(t *testing.T) capturedExecutorUsage {
	t.Helper()
	select {
	case record := <-p.records:
		return record
	case <-time.After(executorUsageTestTimeout):
		t.Fatal("timed out waiting for plugin executor usage record")
		return capturedExecutorUsage{}
	}
}

func (p *captureExecutorUsagePlugin) settle(t *testing.T) {
	t.Helper()
	ctx := internallogging.WithRequestID(context.Background(), p.barrierID)
	coreusage.PublishRecord(ctx, coreusage.Record{Provider: executorUsageTestBarrierProvider})
	select {
	case <-p.barrier:
	case <-time.After(executorUsageTestTimeout):
		t.Fatal("timed out waiting for usage dispatcher barrier")
	}
}

func (p *captureExecutorUsagePlugin) requireNoAdditionalRecords(t *testing.T) {
	t.Helper()
	p.settle(t)
	select {
	case record := <-p.records:
		t.Fatalf("unexpected additional plugin executor usage record: request_id=%q record=%+v", record.requestID, record.record)
	default:
	}
}

type executorUsageFixture struct {
	manager  *coreauth.Manager
	adapter  *executorAdapter
	auth     *coreauth.Auth
	model    string
	requests chan pluginapi.ExecutorRequest
}

func newExecutorUsageFixture(t *testing.T, executor *fakeExecutor) *executorUsageFixture {
	t.Helper()
	suffix := executorUsageTestSuffix(t.Name())
	record := normalizeTestCapabilityRecord(capabilityRecord{id: "codebuddy-usage-" + suffix})
	host := newHostWithRecords(record)

	requests := make(chan pluginapi.ExecutorRequest, 16)
	if execute := executor.execute; execute != nil {
		executor.execute = func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			requests <- req
			return execute(ctx, req)
		}
	}
	if executeStream := executor.executeStream; executeStream != nil {
		executor.executeStream = func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			requests <- req
			return executeStream(ctx, req)
		}
	}

	adapter := newExecutorAdapterForRecordForTest(
		host,
		record,
		executor,
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
	)
	adapter.provider = executorUsageTestProvider

	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(adapter)

	model := "codebuddy-usage-model-" + suffix
	auth := &coreauth.Auth{
		ID:       "codebuddy-usage-auth-" + suffix,
		Provider: executorUsageTestProvider,
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			coreauth.AttributeAuthKind:      coreauth.AuthKindOAuth,
			coreauth.AttributeSourceBackend: coreauth.AuthSourceMemory,
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("manager.Register() error = %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	return &executorUsageFixture{
		manager:  manager,
		adapter:  adapter,
		auth:     auth,
		model:    model,
		requests: requests,
	}
}

func (f *executorUsageFixture) executionRequest(stream bool) (coreexecutor.Request, coreexecutor.Options) {
	payload := []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}]}`, f.model))
	return coreexecutor.Request{
			Model:   f.model,
			Format:  sdktranslator.FormatOpenAI,
			Payload: payload,
		}, coreexecutor.Options{
			Stream:          stream,
			SourceFormat:    sdktranslator.FormatOpenAI,
			ResponseFormat:  sdktranslator.FormatOpenAI,
			OriginalRequest: bytes.Clone(payload),
		}
}

func (f *executorUsageFixture) execute(t *testing.T, ctx context.Context) (coreexecutor.Response, error) {
	t.Helper()
	req, opts := f.executionRequest(false)
	return f.manager.Execute(ctx, []string{executorUsageTestProvider}, req, opts)
}

func (f *executorUsageFixture) executeStream(t *testing.T, ctx context.Context) (*coreexecutor.StreamResult, error) {
	t.Helper()
	req, opts := f.executionRequest(true)
	return f.manager.ExecuteStream(ctx, []string{executorUsageTestProvider}, req, opts)
}

func (f *executorUsageFixture) requireSelectedRequest(t *testing.T, wantStream bool) pluginapi.ExecutorRequest {
	t.Helper()
	var request pluginapi.ExecutorRequest
	select {
	case request = <-f.requests:
	case <-time.After(executorUsageTestTimeout):
		t.Fatal("timed out waiting for plugin executor request")
	}
	if request.AuthID != f.auth.ID {
		t.Errorf("executor request AuthID = %q, want %q", request.AuthID, f.auth.ID)
	}
	if request.AuthProvider != f.auth.Provider {
		t.Errorf("executor request AuthProvider = %q, want %q", request.AuthProvider, f.auth.Provider)
	}
	if request.Model != f.model {
		t.Errorf("executor request Model = %q, want %q", request.Model, f.model)
	}
	if request.Format != sdktranslator.FormatOpenAI.String() {
		t.Errorf("executor request Format = %q, want %q", request.Format, sdktranslator.FormatOpenAI)
	}
	if request.Stream != wantStream {
		t.Errorf("executor request Stream = %v, want %v", request.Stream, wantStream)
	}
	return request
}

func (f *executorUsageFixture) requireNoAdditionalRequests(t *testing.T) {
	t.Helper()
	select {
	case request := <-f.requests:
		t.Fatalf("unexpected additional plugin executor request: %+v", request)
	default:
	}
}

func requireExecutorUsageIdentity(t *testing.T, captured capturedExecutorUsage, requestID string, fixture *executorUsageFixture) {
	t.Helper()
	record := captured.record
	if captured.requestID != requestID {
		t.Errorf("usage request ID = %q, want %q", captured.requestID, requestID)
	}
	if record.Provider != executorUsageTestProvider {
		t.Errorf("usage Provider = %q, want %q", record.Provider, executorUsageTestProvider)
	}
	if record.AuthID != fixture.auth.ID {
		t.Errorf("usage AuthID = %q, want %q", record.AuthID, fixture.auth.ID)
	}
	if record.AuthType != coreauth.AuthKindOAuth {
		t.Errorf("usage AuthType = %q, want %q", record.AuthType, coreauth.AuthKindOAuth)
	}
	if record.Model != fixture.model {
		t.Errorf("usage Model = %q, want %q", record.Model, fixture.model)
	}

	formattedAdapterType := fmt.Sprintf("%T", fixture.adapter)
	wantExecutorType := helps.ExecutorTypeName(fixture.adapter)
	if !strings.HasSuffix(formattedAdapterType, "."+wantExecutorType) {
		t.Fatalf("formatted adapter type %q does not identify canonical executor type %q", formattedAdapterType, wantExecutorType)
	}
	if record.ExecutorType != wantExecutorType {
		t.Errorf("usage ExecutorType = %q, want %q derived from %q", record.ExecutorType, wantExecutorType, formattedAdapterType)
	}
	if record.ExecutorType == executorUsageTestClientID {
		t.Errorf("usage ExecutorType = plugin model client ID %q, want concrete adapter type", executorUsageTestClientID)
	}
}

func requireOpenAIUsageDetail(t *testing.T, detail coreusage.Detail, input, output, total int64) {
	t.Helper()
	if detail.InputTokens != input || detail.OutputTokens != output || detail.TotalTokens != total {
		t.Errorf("usage detail = %+v, want input=%d output=%d total=%d", detail, input, output, total)
	}
	breakdown := detail.TokenBreakdown
	if !breakdown.Valid() {
		t.Fatalf("usage token breakdown = %+v, want valid v2 breakdown", breakdown)
	}
	if breakdown.TotalTokens != total ||
		breakdown.Input.TotalTokens != input || breakdown.Input.UncachedTokens != input ||
		breakdown.Input.CacheReadTokens != 0 || breakdown.Input.CacheWriteTokens != 0 ||
		breakdown.Output.TotalTokens != output || breakdown.Output.NonReasoningTokens != output || breakdown.Output.ReasoningTokens != 0 ||
		breakdown.UnclassifiedTokens != 0 {
		t.Errorf("usage token breakdown = %+v, want input=%d output=%d total=%d", breakdown, input, output, total)
	}
}

func requireZeroUsageTokens(t *testing.T, detail coreusage.Detail) {
	t.Helper()
	if detail.InputTokens != 0 || detail.OutputTokens != 0 || detail.ReasoningTokens != 0 ||
		detail.CachedTokens != 0 || detail.CacheReadTokens != 0 || detail.CacheCreationTokens != 0 || detail.TotalTokens != 0 {
		t.Errorf("usage detail = %+v, want zero tokens", detail)
	}
}

func requireZeroUsageDetail(t *testing.T, detail coreusage.Detail) {
	t.Helper()
	requireZeroUsageTokens(t, detail)
	if detail.TokenBreakdown != (coreusage.TokenBreakdown{}) {
		t.Errorf("zero-token breakdown = %+v, want native zero value before sink normalization", detail.TokenBreakdown)
	}
}

func requireNormalizedZeroUsageDetail(t *testing.T, detail coreusage.Detail) {
	t.Helper()
	requireZeroUsageTokens(t, detail)
	breakdown := detail.TokenBreakdown
	if !breakdown.Valid() || breakdown.TotalTokens != 0 || breakdown.Input.TotalTokens != 0 || breakdown.Output.TotalTokens != 0 || breakdown.UnclassifiedTokens != 0 {
		t.Errorf("zero-token breakdown = %+v, want valid normalized v2 breakdown", breakdown)
	}
}

func requireSuccessfulUsage(t *testing.T, record coreusage.Record) {
	t.Helper()
	if record.Failed {
		t.Errorf("usage Failed = true, want false; failure=%+v", record.Fail)
	}
	if record.Fail != (coreusage.Failure{}) {
		t.Errorf("usage failure = %+v, want empty", record.Fail)
	}
}

func requireFailedUsage(t *testing.T, record coreusage.Record, want error) {
	t.Helper()
	if !record.Failed {
		t.Error("usage Failed = false, want true")
	}
	if record.Fail.Body != want.Error() {
		t.Errorf("usage failure body = %q, want %q", record.Fail.Body, want.Error())
	}
}

func collectExecutorStream(t *testing.T, result *coreexecutor.StreamResult) []coreexecutor.StreamChunk {
	t.Helper()
	if result == nil || result.Chunks == nil {
		t.Fatal("stream result has no chunks")
	}
	chunks := make([]coreexecutor.StreamChunk, 0)
	for {
		select {
		case chunk, ok := <-result.Chunks:
			if !ok {
				return chunks
			}
			chunks = append(chunks, chunk)
		case <-time.After(executorUsageTestTimeout):
			t.Fatal("timed out waiting for plugin executor stream to close")
		}
	}
}

func sendExecutorUsageTestChunk(t *testing.T, target chan<- pluginapi.ExecutorStreamChunk, chunk pluginapi.ExecutorStreamChunk) {
	t.Helper()
	select {
	case target <- chunk:
	case <-time.After(executorUsageTestTimeout):
		t.Fatal("timed out sending plugin executor stream chunk")
	}
}

func executorUsageTestSuffix(name string) string {
	replacer := strings.NewReplacer("/", "-", "\\", "-", " ", "-", "_", "-")
	return strings.ToLower(replacer.Replace(name))
}

func TestExecutorAdapterPublishesUsageThroughAuthManager(t *testing.T) {
	t.Run("non-stream success", func(t *testing.T) {
		requestID := "req-" + executorUsageTestSuffix(t.Name())
		capture := registerCaptureExecutorUsagePlugin(t, requestID)
		fixture := newExecutorUsageFixture(t, &fakeExecutor{
			identifier: executorUsageTestProvider,
			execute: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
				time.Sleep(10 * time.Millisecond)
				return pluginapi.ExecutorResponse{Payload: []byte(`{"id":"chatcmpl-usage","usage":{"prompt_tokens":12,"completion_tokens":34,"total_tokens":46}}`)}, nil
			},
		})

		ctx := internallogging.WithRequestID(context.Background(), requestID)
		response, errExecute := fixture.execute(t, ctx)
		if errExecute != nil {
			t.Fatalf("Manager.Execute() error = %v", errExecute)
		}
		if !bytes.Contains(response.Payload, []byte(`"chatcmpl-usage"`)) {
			t.Fatalf("Manager.Execute() payload = %s, want plugin response", response.Payload)
		}
		fixture.requireSelectedRequest(t, false)
		fixture.requireNoAdditionalRequests(t)

		captured := capture.waitRecord(t)
		requireExecutorUsageIdentity(t, captured, requestID, fixture)
		requireSuccessfulUsage(t, captured.record)
		requireOpenAIUsageDetail(t, captured.record.Detail, 12, 34, 46)
		if captured.record.TTFT <= 0 {
			t.Fatalf("non-stream TTFT = %s, want positive duration", captured.record.TTFT)
		}
		capture.requireNoAdditionalRecords(t)
	})

	t.Run("non-stream error", func(t *testing.T) {
		requestID := "req-" + executorUsageTestSuffix(t.Name())
		capture := registerCaptureExecutorUsagePlugin(t, requestID)
		upstreamErr := errors.New("codebuddy execute failed")
		fixture := newExecutorUsageFixture(t, &fakeExecutor{
			identifier: executorUsageTestProvider,
			execute: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
				return pluginapi.ExecutorResponse{}, upstreamErr
			},
		})

		ctx := internallogging.WithRequestID(context.Background(), requestID)
		_, errExecute := fixture.execute(t, ctx)
		if !errors.Is(errExecute, upstreamErr) {
			t.Fatalf("Manager.Execute() error = %v, want %v", errExecute, upstreamErr)
		}
		fixture.requireSelectedRequest(t, false)
		fixture.requireNoAdditionalRequests(t)

		captured := capture.waitRecord(t)
		requireExecutorUsageIdentity(t, captured, requestID, fixture)
		requireFailedUsage(t, captured.record, upstreamErr)
		requireNormalizedZeroUsageDetail(t, captured.record.Detail)
		capture.requireNoAdditionalRecords(t)
	})

	t.Run("non-stream panic", func(t *testing.T) {
		requestID := "req-" + executorUsageTestSuffix(t.Name())
		capture := registerCaptureExecutorUsagePlugin(t, requestID)
		panicErr := errors.New("plugin executor codebuddy panic: codebuddy execute panic")
		fixture := newExecutorUsageFixture(t, &fakeExecutor{
			identifier: executorUsageTestProvider,
			execute: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
				panic("codebuddy execute panic")
			},
		})

		ctx := internallogging.WithRequestID(context.Background(), requestID)
		_, errExecute := fixture.execute(t, ctx)
		if errExecute == nil || errExecute.Error() != panicErr.Error() {
			t.Fatalf("Manager.Execute() error = %v, want %v", errExecute, panicErr)
		}
		fixture.requireSelectedRequest(t, false)
		fixture.requireNoAdditionalRequests(t)

		captured := capture.waitRecord(t)
		requireExecutorUsageIdentity(t, captured, requestID, fixture)
		requireFailedUsage(t, captured.record, panicErr)
		requireNormalizedZeroUsageDetail(t, captured.record.Detail)
		capture.requireNoAdditionalRecords(t)
	})
}

func TestExecutorAdapterUsageMetadata(t *testing.T) {
	t.Run("strips thinking suffix and preserves context metadata", func(t *testing.T) {
		requestID := "req-" + executorUsageTestSuffix(t.Name())
		capture := registerCaptureExecutorUsagePlugin(t, requestID)
		fixture := newExecutorUsageFixture(t, &fakeExecutor{
			identifier: executorUsageTestProvider,
			execute: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
				return pluginapi.ExecutorResponse{Payload: []byte(`{"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`)}, nil
			},
		})

		requestedModel := fixture.model + "(high)"
		payload := []byte(fmt.Sprintf(`{"model":%q}`, requestedModel))
		reportCtx := internallogging.WithRequestID(context.Background(), requestID)
		reportCtx = coreusage.WithRequestedModelAlias(reportCtx, requestedModel)
		reportCtx = coreusage.WithReasoningEffort(reportCtx, "high")
		_, errExecute := fixture.adapter.Execute(reportCtx, fixture.auth, coreexecutor.Request{
			Model:   requestedModel,
			Format:  sdktranslator.FormatOpenAI,
			Payload: payload,
		}, coreexecutor.Options{
			SourceFormat:    sdktranslator.FormatOpenAI,
			ResponseFormat:  sdktranslator.FormatOpenAI,
			OriginalRequest: bytes.Clone(payload),
		})
		if errExecute != nil {
			t.Fatalf("adapter.Execute() error = %v", errExecute)
		}
		select {
		case request := <-fixture.requests:
			if request.Model != requestedModel {
				t.Fatalf("executor request Model = %q, want %q", request.Model, requestedModel)
			}
		case <-time.After(executorUsageTestTimeout):
			t.Fatal("timed out waiting for plugin executor request")
		}

		captured := capture.waitRecord(t)
		if captured.requestID != requestID {
			t.Fatalf("usage request ID = %q, want %q", captured.requestID, requestID)
		}
		if captured.record.Model != fixture.model {
			t.Errorf("usage Model = %q, want base model %q", captured.record.Model, fixture.model)
		}
		if captured.record.Alias != requestedModel {
			t.Errorf("usage Alias = %q, want %q", captured.record.Alias, requestedModel)
		}
		if captured.record.ReasoningEffort != "high" {
			t.Errorf("usage ReasoningEffort = %q, want high", captured.record.ReasoningEffort)
		}
		requireSuccessfulUsage(t, captured.record)
		requireOpenAIUsageDetail(t, captured.record.Detail, 3, 4, 7)
		capture.requireNoAdditionalRecords(t)
	})

	t.Run("translated reasoning effort overrides context metadata", func(t *testing.T) {
		requestID := "req-" + executorUsageTestSuffix(t.Name())
		capture := registerCaptureExecutorUsagePlugin(t, requestID)
		fixture := newExecutorUsageFixture(t, &fakeExecutor{
			identifier: executorUsageTestProvider,
			execute: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
				return pluginapi.ExecutorResponse{Payload: []byte(`{"usage":{"prompt_tokens":8,"completion_tokens":9,"total_tokens":17}}`)}, nil
			},
		})

		ctx := internallogging.WithRequestID(context.Background(), requestID)
		ctx = coreusage.WithReasoningEffort(ctx, "low")
		_, errExecute := fixture.adapter.Execute(ctx, fixture.auth, coreexecutor.Request{
			Model:   fixture.model,
			Format:  sdktranslator.FormatOpenAI,
			Payload: []byte(fmt.Sprintf(`{"model":%q,"reasoning_effort":"xhigh"}`, fixture.model)),
		}, coreexecutor.Options{
			SourceFormat:   sdktranslator.FormatOpenAI,
			ResponseFormat: sdktranslator.FormatOpenAI,
		})
		if errExecute != nil {
			t.Fatalf("adapter.Execute() error = %v", errExecute)
		}
		captured := capture.waitRecord(t)
		if captured.record.ReasoningEffort != "xhigh" {
			t.Errorf("usage ReasoningEffort = %q, want xhigh", captured.record.ReasoningEffort)
		}
		requireSuccessfulUsage(t, captured.record)
		requireOpenAIUsageDetail(t, captured.record.Detail, 8, 9, 17)
		capture.requireNoAdditionalRecords(t)
	})

	t.Run("translation failure publishes only failure", func(t *testing.T) {
		requestID := "req-" + executorUsageTestSuffix(t.Name())
		capture := registerCaptureExecutorUsagePlugin(t, requestID)
		fixture := newExecutorUsageFixture(t, &fakeExecutor{
			identifier: executorUsageTestProvider,
			execute: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
				return pluginapi.ExecutorResponse{Payload: []byte(`{"usage":{"prompt_tokens":5,"completion_tokens":6,"total_tokens":11}}`)}, nil
			},
		})
		outputFormat := sdktranslator.Format("pluginhost-usage-panic-output")
		requestedFormat := sdktranslator.Format("pluginhost-usage-panic-requested")
		sdktranslator.Register(requestedFormat, outputFormat, nil, sdktranslator.ResponseTransform{
			NonStream: func(context.Context, string, []byte, []byte, []byte, *any) []byte {
				panic("response translation panic")
			},
		})
		fixture.adapter.outputFormats = []sdktranslator.Format{outputFormat}

		ctx := internallogging.WithRequestID(context.Background(), requestID)
		_, errExecute := fixture.adapter.Execute(ctx, fixture.auth, coreexecutor.Request{
			Model:   fixture.model,
			Format:  sdktranslator.FormatOpenAI,
			Payload: []byte(fmt.Sprintf(`{"model":%q}`, fixture.model)),
		}, coreexecutor.Options{
			SourceFormat:   sdktranslator.FormatOpenAI,
			ResponseFormat: requestedFormat,
		})
		wantErr := "plugin executor " + fixture.adapter.Identifier() + " panic: response translation panic"
		if errExecute == nil || errExecute.Error() != wantErr {
			t.Fatalf("adapter.Execute() error = %v, want %q", errExecute, wantErr)
		}
		select {
		case <-fixture.requests:
		case <-time.After(executorUsageTestTimeout):
			t.Fatal("timed out waiting for plugin executor request")
		}

		captured := capture.waitRecord(t)
		requireFailedUsage(t, captured.record, errors.New(wantErr))
		requireNormalizedZeroUsageDetail(t, captured.record.Detail)
		capture.requireNoAdditionalRecords(t)
	})

	t.Run("request translation delay is excluded from TTFT", func(t *testing.T) {
		requestID := "req-" + executorUsageTestSuffix(t.Name())
		capture := registerCaptureExecutorUsagePlugin(t, requestID)
		fixture := newExecutorUsageFixture(t, &fakeExecutor{
			identifier: executorUsageTestProvider,
			execute: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
				time.Sleep(10 * time.Millisecond)
				return pluginapi.ExecutorResponse{Payload: []byte(`{"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`)}, nil
			},
		})
		fromFormat := sdktranslator.Format("pluginhost-ttft-req-from-" + executorUsageTestSuffix(t.Name()))
		sdktranslator.Register(fromFormat, sdktranslator.FormatOpenAI, func(_ string, rawJSON []byte, _ bool) []byte {
			time.Sleep(40 * time.Millisecond)
			return rawJSON
		}, sdktranslator.ResponseTransform{})

		ctx := internallogging.WithRequestID(context.Background(), requestID)
		_, errExecute := fixture.adapter.Execute(ctx, fixture.auth, coreexecutor.Request{
			Model:   fixture.model,
			Format:  fromFormat,
			Payload: []byte(fmt.Sprintf(`{"model":%q}`, fixture.model)),
		}, coreexecutor.Options{
			SourceFormat:   fromFormat,
			ResponseFormat: sdktranslator.FormatOpenAI,
		})
		if errExecute != nil {
			t.Fatalf("adapter.Execute() error = %v", errExecute)
		}
		captured := capture.waitRecord(t)
		requireSuccessfulUsage(t, captured.record)
		if captured.record.TTFT <= 0 {
			t.Fatalf("TTFT = %s, want positive duration", captured.record.TTFT)
		}
		if captured.record.TTFT >= 40*time.Millisecond {
			t.Fatalf("TTFT = %s, want request translation delay excluded", captured.record.TTFT)
		}
		capture.requireNoAdditionalRecords(t)
	})

	t.Run("response translation delay is excluded from TTFT", func(t *testing.T) {
		requestID := "req-" + executorUsageTestSuffix(t.Name())
		capture := registerCaptureExecutorUsagePlugin(t, requestID)
		fixture := newExecutorUsageFixture(t, &fakeExecutor{
			identifier: executorUsageTestProvider,
			execute: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
				time.Sleep(10 * time.Millisecond)
				return pluginapi.ExecutorResponse{Payload: []byte(`{"usage":{"prompt_tokens":5,"completion_tokens":6,"total_tokens":11}}`)}, nil
			},
		})
		outputFormat := sdktranslator.Format("pluginhost-ttft-resp-output-" + executorUsageTestSuffix(t.Name()))
		requestedFormat := sdktranslator.Format("pluginhost-ttft-resp-requested-" + executorUsageTestSuffix(t.Name()))
		sdktranslator.Register(requestedFormat, outputFormat, nil, sdktranslator.ResponseTransform{
			NonStream: func(_ context.Context, _ string, _ []byte, _ []byte, payload []byte, _ *any) []byte {
				time.Sleep(40 * time.Millisecond)
				return payload
			},
		})
		fixture.adapter.outputFormats = []sdktranslator.Format{outputFormat}

		ctx := internallogging.WithRequestID(context.Background(), requestID)
		_, errExecute := fixture.adapter.Execute(ctx, fixture.auth, coreexecutor.Request{
			Model:   fixture.model,
			Format:  sdktranslator.FormatOpenAI,
			Payload: []byte(fmt.Sprintf(`{"model":%q}`, fixture.model)),
		}, coreexecutor.Options{
			SourceFormat:   sdktranslator.FormatOpenAI,
			ResponseFormat: requestedFormat,
		})
		if errExecute != nil {
			t.Fatalf("adapter.Execute() error = %v", errExecute)
		}
		captured := capture.waitRecord(t)
		requireSuccessfulUsage(t, captured.record)
		if captured.record.TTFT <= 0 {
			t.Fatalf("TTFT = %s, want positive duration", captured.record.TTFT)
		}
		if captured.record.TTFT >= 40*time.Millisecond {
			t.Fatalf("TTFT = %s, want response translation delay excluded", captured.record.TTFT)
		}
		capture.requireNoAdditionalRecords(t)
	})
}

func TestExecutorAdapterPublishesStreamUsageThroughAuthManager(t *testing.T) {
	t.Run("clean close with usage", func(t *testing.T) {
		requestID := "req-" + executorUsageTestSuffix(t.Name())
		capture := registerCaptureExecutorUsagePlugin(t, requestID)
		fixture := newExecutorUsageFixture(t, &fakeExecutor{
			identifier: executorUsageTestProvider,
			executeStream: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
				source := make(chan pluginapi.ExecutorStreamChunk, 3)
				go func() {
					time.Sleep(10 * time.Millisecond)
					source <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")}
					source <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":15,\"completion_tokens\":25,\"total_tokens\":40}}\n\n")}
					source <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: [DONE]\n\n")}
					close(source)
				}()
				return pluginapi.ExecutorStreamResponse{Chunks: source}, nil
			},
		})

		ctx := internallogging.WithRequestID(context.Background(), requestID)
		result, errExecute := fixture.executeStream(t, ctx)
		if errExecute != nil {
			t.Fatalf("Manager.ExecuteStream() error = %v", errExecute)
		}
		chunks := collectExecutorStream(t, result)
		if len(chunks) != 3 {
			t.Fatalf("stream chunk count = %d, want 3", len(chunks))
		}
		fixture.requireSelectedRequest(t, true)
		fixture.requireNoAdditionalRequests(t)

		captured := capture.waitRecord(t)
		requireExecutorUsageIdentity(t, captured, requestID, fixture)
		requireSuccessfulUsage(t, captured.record)
		requireOpenAIUsageDetail(t, captured.record.Detail, 15, 25, 40)
		if captured.record.TTFT <= 0 {
			t.Fatalf("stream TTFT = %s, want positive duration", captured.record.TTFT)
		}
		capture.requireNoAdditionalRecords(t)
	})

	t.Run("clean close without usage", func(t *testing.T) {
		requestID := "req-" + executorUsageTestSuffix(t.Name())
		capture := registerCaptureExecutorUsagePlugin(t, requestID)
		source := make(chan pluginapi.ExecutorStreamChunk, 2)
		source <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")}
		source <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: [DONE]\n\n")}
		close(source)
		fixture := newExecutorUsageFixture(t, &fakeExecutor{
			identifier: executorUsageTestProvider,
			executeStream: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
				return pluginapi.ExecutorStreamResponse{Chunks: source}, nil
			},
		})

		ctx := internallogging.WithRequestID(context.Background(), requestID)
		result, errExecute := fixture.executeStream(t, ctx)
		if errExecute != nil {
			t.Fatalf("Manager.ExecuteStream() error = %v", errExecute)
		}
		if chunks := collectExecutorStream(t, result); len(chunks) != 2 {
			t.Fatalf("stream chunk count = %d, want 2", len(chunks))
		}
		fixture.requireSelectedRequest(t, true)
		fixture.requireNoAdditionalRequests(t)

		captured := capture.waitRecord(t)
		requireExecutorUsageIdentity(t, captured, requestID, fixture)
		requireSuccessfulUsage(t, captured.record)
		requireZeroUsageDetail(t, captured.record.Detail)
		capture.requireNoAdditionalRecords(t)
	})

	t.Run("synchronous error", func(t *testing.T) {
		requestID := "req-" + executorUsageTestSuffix(t.Name())
		capture := registerCaptureExecutorUsagePlugin(t, requestID)
		upstreamErr := errors.New("codebuddy stream setup failed")
		fixture := newExecutorUsageFixture(t, &fakeExecutor{
			identifier: executorUsageTestProvider,
			executeStream: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
				return pluginapi.ExecutorStreamResponse{}, upstreamErr
			},
		})

		ctx := internallogging.WithRequestID(context.Background(), requestID)
		_, errExecute := fixture.executeStream(t, ctx)
		if !errors.Is(errExecute, upstreamErr) {
			t.Fatalf("Manager.ExecuteStream() error = %v, want %v", errExecute, upstreamErr)
		}
		fixture.requireSelectedRequest(t, true)
		fixture.requireNoAdditionalRequests(t)

		captured := capture.waitRecord(t)
		requireExecutorUsageIdentity(t, captured, requestID, fixture)
		requireFailedUsage(t, captured.record, upstreamErr)
		requireNormalizedZeroUsageDetail(t, captured.record.Detail)
		capture.requireNoAdditionalRecords(t)
	})

	t.Run("synchronous panic", func(t *testing.T) {
		requestID := "req-" + executorUsageTestSuffix(t.Name())
		capture := registerCaptureExecutorUsagePlugin(t, requestID)
		panicErr := errors.New("plugin executor codebuddy stream panic: codebuddy stream panic")
		fixture := newExecutorUsageFixture(t, &fakeExecutor{
			identifier: executorUsageTestProvider,
			executeStream: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
				panic("codebuddy stream panic")
			},
		})

		ctx := internallogging.WithRequestID(context.Background(), requestID)
		_, errExecute := fixture.executeStream(t, ctx)
		if errExecute == nil || errExecute.Error() != panicErr.Error() {
			t.Fatalf("Manager.ExecuteStream() error = %v, want %v", errExecute, panicErr)
		}
		fixture.requireSelectedRequest(t, true)
		fixture.requireNoAdditionalRequests(t)

		captured := capture.waitRecord(t)
		requireExecutorUsageIdentity(t, captured, requestID, fixture)
		requireFailedUsage(t, captured.record, panicErr)
		requireNormalizedZeroUsageDetail(t, captured.record.Detail)
		capture.requireNoAdditionalRecords(t)
	})

	t.Run("error chunks preserve order and first failure", func(t *testing.T) {
		requestID := "req-" + executorUsageTestSuffix(t.Name())
		capture := registerCaptureExecutorUsagePlugin(t, requestID)
		firstErr := errors.New("first codebuddy stream failure")
		secondErr := errors.New("second codebuddy stream failure")
		contentPayload := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
		firstErrorPayload := []byte("first-error-payload")
		usagePayload := []byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":19,\"completion_tokens\":23,\"total_tokens\":42}}\n\n")
		secondErrorPayload := []byte("second-error-payload")
		donePayload := []byte("data: [DONE]\n\n")
		source := make(chan pluginapi.ExecutorStreamChunk, 5)
		source <- pluginapi.ExecutorStreamChunk{Payload: contentPayload}
		source <- pluginapi.ExecutorStreamChunk{Payload: firstErrorPayload, Err: firstErr}
		source <- pluginapi.ExecutorStreamChunk{Payload: usagePayload}
		source <- pluginapi.ExecutorStreamChunk{Payload: secondErrorPayload, Err: secondErr}
		source <- pluginapi.ExecutorStreamChunk{Payload: donePayload}
		close(source)
		fixture := newExecutorUsageFixture(t, &fakeExecutor{
			identifier: executorUsageTestProvider,
			executeStream: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
				return pluginapi.ExecutorStreamResponse{Chunks: source}, nil
			},
		})

		ctx := internallogging.WithRequestID(context.Background(), requestID)
		result, errExecute := fixture.executeStream(t, ctx)
		if errExecute != nil {
			t.Fatalf("Manager.ExecuteStream() error = %v", errExecute)
		}
		chunks := collectExecutorStream(t, result)
		if len(chunks) != 5 {
			t.Fatalf("stream chunk count = %d, want 5", len(chunks))
		}
		wantPayloads := [][]byte{contentPayload, firstErrorPayload, usagePayload, secondErrorPayload, donePayload}
		wantErrors := []error{nil, firstErr, nil, secondErr, nil}
		for index := range chunks {
			if !bytes.Equal(chunks[index].Payload, wantPayloads[index]) {
				t.Errorf("chunk[%d] payload = %q, want %q", index, chunks[index].Payload, wantPayloads[index])
			}
			if chunks[index].Err != wantErrors[index] {
				t.Errorf("chunk[%d] error = %v, want identical %v", index, chunks[index].Err, wantErrors[index])
			}
		}
		chunks[0].Payload[0] = 'X'
		if contentPayload[0] == 'X' {
			t.Error("stream output payload aliases plugin source payload")
		}
		fixture.requireSelectedRequest(t, true)
		fixture.requireNoAdditionalRequests(t)

		captured := capture.waitRecord(t)
		requireExecutorUsageIdentity(t, captured, requestID, fixture)
		requireFailedUsage(t, captured.record, firstErr)
		requireOpenAIUsageDetail(t, captured.record.Detail, 19, 23, 42)
		capture.requireNoAdditionalRecords(t)
	})
}

func TestExecutorAdapterStreamCancellationUsageThroughAuthManager(t *testing.T) {
	t.Run("blocked send uses context cancellation", func(t *testing.T) {
		requestID := "req-" + executorUsageTestSuffix(t.Name())
		capture := registerCaptureExecutorUsagePlugin(t, requestID)
		source := make(chan pluginapi.ExecutorStreamChunk)
		defer close(source)
		fixture := newExecutorUsageFixture(t, &fakeExecutor{
			identifier: executorUsageTestProvider,
			executeStream: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
				return pluginapi.ExecutorStreamResponse{Chunks: source}, nil
			},
		})
		ctx, cancel := context.WithCancel(internallogging.WithRequestID(context.Background(), requestID))
		defer cancel()

		type streamCall struct {
			result *coreexecutor.StreamResult
			err    error
		}
		callResult := make(chan streamCall, 1)
		go func() {
			result, errExecute := fixture.executeStream(t, ctx)
			callResult <- streamCall{result: result, err: errExecute}
		}()
		sendExecutorUsageTestChunk(t, source, pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"bootstrap\"}}]}\n\n")})

		var call streamCall
		select {
		case call = <-callResult:
		case <-time.After(executorUsageTestTimeout):
			t.Fatal("timed out waiting for Manager.ExecuteStream()")
		}
		if call.err != nil {
			t.Fatalf("Manager.ExecuteStream() error = %v", call.err)
		}
		fixture.requireSelectedRequest(t, true)
		fixture.requireNoAdditionalRequests(t)

		// The auth-manager wrapper is blocked forwarding the bootstrap chunk. The
		// next chunk fills the mapper, and receipt of the third chunk proves the
		// usage wrapper has reached its blocked-send branch before cancellation.
		sendExecutorUsageTestChunk(t, source, pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"queued\"}}]}\n\n")})
		sendExecutorUsageTestChunk(t, source, pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":11,\"total_tokens\":18}}\n\n")})
		cancel()
		collectExecutorStream(t, call.result)

		captured := capture.waitRecord(t)
		requireExecutorUsageIdentity(t, captured, requestID, fixture)
		requireFailedUsage(t, captured.record, context.Canceled)
		requireOpenAIUsageDetail(t, captured.record.Detail, 7, 11, 18)
		capture.requireNoAdditionalRecords(t)
	})

	t.Run("real stream error wins over cancellation", func(t *testing.T) {
		requestID := "req-" + executorUsageTestSuffix(t.Name())
		capture := registerCaptureExecutorUsagePlugin(t, requestID)
		source := make(chan pluginapi.ExecutorStreamChunk)
		defer close(source)
		streamErr := errors.New("codebuddy terminal stream error")
		fixture := newExecutorUsageFixture(t, &fakeExecutor{
			identifier: executorUsageTestProvider,
			executeStream: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
				return pluginapi.ExecutorStreamResponse{Chunks: source}, nil
			},
		})
		ctx, cancel := context.WithCancel(internallogging.WithRequestID(context.Background(), requestID))
		defer cancel()

		type streamCall struct {
			result *coreexecutor.StreamResult
			err    error
		}
		callResult := make(chan streamCall, 1)
		go func() {
			result, errExecute := fixture.executeStream(t, ctx)
			callResult <- streamCall{result: result, err: errExecute}
		}()
		sendExecutorUsageTestChunk(t, source, pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"bootstrap\"}}]}\n\n")})

		var call streamCall
		select {
		case call = <-callResult:
		case <-time.After(executorUsageTestTimeout):
			t.Fatal("timed out waiting for Manager.ExecuteStream()")
		}
		if call.err != nil {
			t.Fatalf("Manager.ExecuteStream() error = %v", call.err)
		}
		fixture.requireSelectedRequest(t, true)
		fixture.requireNoAdditionalRequests(t)

		sendExecutorUsageTestChunk(t, source, pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"queued\"}}]}\n\n")})
		sendExecutorUsageTestChunk(t, source, pluginapi.ExecutorStreamChunk{
			Payload: []byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":13,\"total_tokens\":21}}\n\n"),
			Err:     streamErr,
		})
		cancel()
		collectExecutorStream(t, call.result)

		captured := capture.waitRecord(t)
		requireExecutorUsageIdentity(t, captured, requestID, fixture)
		requireFailedUsage(t, captured.record, streamErr)
		requireOpenAIUsageDetail(t, captured.record.Detail, 8, 13, 21)
		capture.requireNoAdditionalRecords(t)
	})
}

func TestExecutorAdapterStreamUsageOnceIsPerInvocation(t *testing.T) {
	firstRequestID := "req-" + executorUsageTestSuffix(t.Name()) + "-first"
	secondRequestID := "req-" + executorUsageTestSuffix(t.Name()) + "-second"
	capture := registerCaptureExecutorUsagePlugin(t, firstRequestID, secondRequestID)
	invocation := 0
	fixture := newExecutorUsageFixture(t, &fakeExecutor{
		identifier: executorUsageTestProvider,
		executeStream: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			invocation++
			source := make(chan pluginapi.ExecutorStreamChunk, 2)
			source <- pluginapi.ExecutorStreamChunk{Payload: []byte(fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"content\":\"stream-%d\"}}]}\n\n", invocation))}
			source <- pluginapi.ExecutorStreamChunk{Payload: []byte(fmt.Sprintf("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":%d,\"completion_tokens\":%d,\"total_tokens\":%d}}\n\n", invocation, invocation+1, invocation*2+1))}
			close(source)
			return pluginapi.ExecutorStreamResponse{Chunks: source}, nil
		},
	})

	for _, requestID := range []string{firstRequestID, secondRequestID} {
		ctx := internallogging.WithRequestID(context.Background(), requestID)
		result, errExecute := fixture.executeStream(t, ctx)
		if errExecute != nil {
			t.Fatalf("Manager.ExecuteStream(%q) error = %v", requestID, errExecute)
		}
		if chunks := collectExecutorStream(t, result); len(chunks) != 2 {
			t.Fatalf("Manager.ExecuteStream(%q) chunk count = %d, want 2", requestID, len(chunks))
		}
		fixture.requireSelectedRequest(t, true)
	}
	fixture.requireNoAdditionalRequests(t)

	got := map[string]capturedExecutorUsage{}
	for range 2 {
		captured := capture.waitRecord(t)
		got[captured.requestID] = captured
	}
	first, okFirst := got[firstRequestID]
	second, okSecond := got[secondRequestID]
	if !okFirst || !okSecond {
		t.Fatalf("usage request IDs = %v, want %q and %q", got, firstRequestID, secondRequestID)
	}
	requireExecutorUsageIdentity(t, first, firstRequestID, fixture)
	requireSuccessfulUsage(t, first.record)
	requireOpenAIUsageDetail(t, first.record.Detail, 1, 2, 3)
	requireExecutorUsageIdentity(t, second, secondRequestID, fixture)
	requireSuccessfulUsage(t, second.record)
	requireOpenAIUsageDetail(t, second.record.Detail, 2, 3, 5)
	capture.requireNoAdditionalRecords(t)
}

func TestExecutorAdapterPublishesFragmentedStreamUsage(t *testing.T) {
	requestID := "req-" + executorUsageTestSuffix(t.Name())
	capture := registerCaptureExecutorUsagePlugin(t, requestID)
	first := []byte(`data: {"choices":[],"usage":{"prompt_tokens":19,"completion_tokens":`)
	second := []byte(`23,"total_tokens":42}}

`)
	source := make(chan pluginapi.ExecutorStreamChunk, 2)
	source <- pluginapi.ExecutorStreamChunk{Payload: first}
	source <- pluginapi.ExecutorStreamChunk{Payload: second}
	close(source)
	fixture := newExecutorUsageFixture(t, &fakeExecutor{
		identifier: executorUsageTestProvider,
		executeStream: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			return pluginapi.ExecutorStreamResponse{Chunks: source}, nil
		},
	})

	ctx := internallogging.WithRequestID(context.Background(), requestID)
	result, errExecute := fixture.executeStream(t, ctx)
	if errExecute != nil {
		t.Fatalf("Manager.ExecuteStream() error = %v", errExecute)
	}
	chunks := collectExecutorStream(t, result)
	if len(chunks) != 2 {
		t.Fatalf("stream chunk count = %d, want 2", len(chunks))
	}
	if !bytes.Equal(chunks[0].Payload, first) || !bytes.Equal(chunks[1].Payload, second) {
		t.Fatalf("stream payloads were not preserved: %#v", chunks)
	}
	fixture.requireSelectedRequest(t, true)
	fixture.requireNoAdditionalRequests(t)

	captured := capture.waitRecord(t)
	requireExecutorUsageIdentity(t, captured, requestID, fixture)
	requireSuccessfulUsage(t, captured.record)
	requireOpenAIUsageDetail(t, captured.record.Detail, 19, 23, 42)
	capture.requireNoAdditionalRecords(t)
}

func TestExecutorAdapterPublishesSplitSSEPrefixes(t *testing.T) {
	tests := []struct {
		name   string
		first  []byte
		second []byte
	}{
		{
			name:   "split field prefix",
			first:  []byte("d"),
			second: []byte("ata: {\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":3,\"total_tokens\":5}}\n\n"),
		},
		{
			name:   "split field value",
			first:  []byte("data:"),
			second: []byte(" {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":8,\"total_tokens\":15}}\n\n"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requestID := "req-" + executorUsageTestSuffix(t.Name())
			capture := registerCaptureExecutorUsagePlugin(t, requestID)
			source := make(chan pluginapi.ExecutorStreamChunk, 2)
			source <- pluginapi.ExecutorStreamChunk{Payload: test.first}
			source <- pluginapi.ExecutorStreamChunk{Payload: test.second}
			close(source)
			fixture := newExecutorUsageFixture(t, &fakeExecutor{
				identifier: executorUsageTestProvider,
				executeStream: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
					return pluginapi.ExecutorStreamResponse{Chunks: source}, nil
				},
			})

			ctx := internallogging.WithRequestID(context.Background(), requestID)
			result, errExecute := fixture.executeStream(t, ctx)
			if errExecute != nil {
				t.Fatalf("Manager.ExecuteStream() error = %v", errExecute)
			}
			chunks := collectExecutorStream(t, result)
			if len(chunks) != 2 || !bytes.Equal(chunks[0].Payload, test.first) || !bytes.Equal(chunks[1].Payload, test.second) {
				t.Fatalf("stream chunks = %#v, want original split payloads", chunks)
			}

			captured := capture.waitRecord(t)
			requireSuccessfulUsage(t, captured.record)
			wantInput, wantOutput, wantTotal := int64(2), int64(3), int64(5)
			if test.name == "split field value" {
				wantInput, wantOutput, wantTotal = 7, 8, 15
			}
			requireOpenAIUsageDetail(t, captured.record.Detail, wantInput, wantOutput, wantTotal)
			capture.requireNoAdditionalRecords(t)
		})
	}
}

func TestExecutorAdapterPublishesCompleteRawJSONStreamFrames(t *testing.T) {
	requestID := "req-" + executorUsageTestSuffix(t.Name())
	capture := registerCaptureExecutorUsagePlugin(t, requestID)
	first := []byte(`{"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`)
	second := []byte(`{"usage":{"prompt_tokens":7,"completion_tokens":8,"total_tokens":15}}`)
	source := make(chan pluginapi.ExecutorStreamChunk, 2)
	source <- pluginapi.ExecutorStreamChunk{Payload: first}
	source <- pluginapi.ExecutorStreamChunk{Payload: second}
	close(source)
	fixture := newExecutorUsageFixture(t, &fakeExecutor{
		identifier: executorUsageTestProvider,
		executeStream: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			return pluginapi.ExecutorStreamResponse{Chunks: source}, nil
		},
	})

	ctx := internallogging.WithRequestID(context.Background(), requestID)
	result, errExecute := fixture.executeStream(t, ctx)
	if errExecute != nil {
		t.Fatalf("Manager.ExecuteStream() error = %v", errExecute)
	}
	chunks := collectExecutorStream(t, result)
	if len(chunks) != 2 || !bytes.Equal(chunks[0].Payload, first) || !bytes.Equal(chunks[1].Payload, second) {
		t.Fatalf("stream chunks = %#v, want original raw JSON payloads", chunks)
	}

	captured := capture.waitRecord(t)
	requireSuccessfulUsage(t, captured.record)
	requireOpenAIUsageDetail(t, captured.record.Detail, 7, 8, 15)
	capture.requireNoAdditionalRecords(t)
}

func TestExecutorAdapterDoesNotRetainOversizedUsageFragment(t *testing.T) {
	requestID := "req-" + executorUsageTestSuffix(t.Name())
	capture := registerCaptureExecutorUsagePlugin(t, requestID)
	oversized := make([]byte, pluginStreamUsagePendingMax+1)
	copy(oversized, []byte("data: "))
	for index := len("data: "); index < len(oversized); index++ {
		oversized[index] = 'x'
	}
	usagePayload := []byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":13,\"total_tokens\":24}}\n\n")
	source := make(chan pluginapi.ExecutorStreamChunk, 2)
	source <- pluginapi.ExecutorStreamChunk{Payload: oversized}
	source <- pluginapi.ExecutorStreamChunk{Payload: usagePayload}
	close(source)
	fixture := newExecutorUsageFixture(t, &fakeExecutor{
		identifier: executorUsageTestProvider,
		executeStream: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			return pluginapi.ExecutorStreamResponse{Chunks: source}, nil
		},
	})

	ctx := internallogging.WithRequestID(context.Background(), requestID)
	result, errExecute := fixture.executeStream(t, ctx)
	if errExecute != nil {
		t.Fatalf("Manager.ExecuteStream() error = %v", errExecute)
	}
	chunks := collectExecutorStream(t, result)
	if len(chunks) != 2 || !bytes.Equal(chunks[1].Payload, usagePayload) {
		t.Fatalf("stream chunks = %#v, want oversized fragment followed by usage", chunks)
	}

	captured := capture.waitRecord(t)
	requireSuccessfulUsage(t, captured.record)
	requireOpenAIUsageDetail(t, captured.record.Detail, 11, 13, 24)
	capture.requireNoAdditionalRecords(t)
}

func TestExecutorAdapterStreamTTFTClassification(t *testing.T) {
	t.Run("claude metadata does not lock token TTFT", func(t *testing.T) {
		requestID := "req-" + executorUsageTestSuffix(t.Name())
		capture := registerCaptureExecutorUsagePlugin(t, requestID)
		fixture := newExecutorUsageFixture(t, &fakeExecutor{
			identifier: executorUsageTestProvider,
			executeStream: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
				source := make(chan pluginapi.ExecutorStreamChunk, 4)
				go func() {
					source <- pluginapi.ExecutorStreamChunk{Payload: []byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\n")}
					time.Sleep(20 * time.Millisecond)
					source <- pluginapi.ExecutorStreamChunk{Payload: []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"hello\"}}\n\n")}
					source <- pluginapi.ExecutorStreamChunk{Payload: []byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"input_tokens\":4,\"output_tokens\":5}}\n\n")}
					source <- pluginapi.ExecutorStreamChunk{Payload: []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")}
					close(source)
				}()
				return pluginapi.ExecutorStreamResponse{Chunks: source}, nil
			},
		})
		fixture.adapter.inputFormats = []sdktranslator.Format{sdktranslator.FormatClaude}
		fixture.adapter.outputFormats = []sdktranslator.Format{sdktranslator.FormatClaude}

		ctx := internallogging.WithRequestID(context.Background(), requestID)
		result, errExecute := fixture.adapter.ExecuteStream(ctx, fixture.auth, coreexecutor.Request{
			Model:   fixture.model,
			Format:  sdktranslator.FormatClaude,
			Payload: []byte(fmt.Sprintf(`{"model":%q}`, fixture.model)),
		}, coreexecutor.Options{
			Stream:         true,
			SourceFormat:   sdktranslator.FormatClaude,
			ResponseFormat: sdktranslator.FormatClaude,
		})
		if errExecute != nil {
			t.Fatalf("adapter.ExecuteStream() error = %v", errExecute)
		}
		collectExecutorStream(t, result)
		captured := capture.waitRecord(t)
		requireSuccessfulUsage(t, captured.record)
		if captured.record.TTFT < 20*time.Millisecond {
			t.Fatalf("TTFT = %s, want token event after metadata delay", captured.record.TTFT)
		}
		capture.requireNoAdditionalRecords(t)
	})

	t.Run("responses created does not lock token TTFT", func(t *testing.T) {
		requestID := "req-" + executorUsageTestSuffix(t.Name())
		capture := registerCaptureExecutorUsagePlugin(t, requestID)
		fixture := newExecutorUsageFixture(t, &fakeExecutor{
			identifier: executorUsageTestProvider,
			executeStream: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
				source := make(chan pluginapi.ExecutorStreamChunk, 3)
				go func() {
					source <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n")}
					time.Sleep(20 * time.Millisecond)
					source <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n")}
					source <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":6,\"output_tokens\":7,\"total_tokens\":13}}}\n\n")}
					close(source)
				}()
				return pluginapi.ExecutorStreamResponse{Chunks: source}, nil
			},
		})
		fixture.adapter.inputFormats = []sdktranslator.Format{sdktranslator.FormatOpenAIResponse}
		fixture.adapter.outputFormats = []sdktranslator.Format{sdktranslator.FormatOpenAIResponse}

		ctx := internallogging.WithRequestID(context.Background(), requestID)
		result, errExecute := fixture.adapter.ExecuteStream(ctx, fixture.auth, coreexecutor.Request{
			Model:   fixture.model,
			Format:  sdktranslator.FormatOpenAIResponse,
			Payload: []byte(fmt.Sprintf(`{"model":%q}`, fixture.model)),
		}, coreexecutor.Options{
			Stream:         true,
			SourceFormat:   sdktranslator.FormatOpenAIResponse,
			ResponseFormat: sdktranslator.FormatOpenAIResponse,
		})
		if errExecute != nil {
			t.Fatalf("adapter.ExecuteStream() error = %v", errExecute)
		}
		collectExecutorStream(t, result)
		captured := capture.waitRecord(t)
		requireSuccessfulUsage(t, captured.record)
		if captured.record.TTFT < 20*time.Millisecond {
			t.Fatalf("TTFT = %s, want token event after created-event delay", captured.record.TTFT)
		}
		capture.requireNoAdditionalRecords(t)
	})

	t.Run("split token frame still sets TTFT", func(t *testing.T) {
		requestID := "req-" + executorUsageTestSuffix(t.Name())
		capture := registerCaptureExecutorUsagePlugin(t, requestID)
		first := []byte(`data: {"choices":[{"delta":{"content":"he`)
		second := []byte(`llo"}}]}` + "\n\n")
		usagePayload := []byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":10,\"total_tokens\":19}}\n\n")
		fixture := newExecutorUsageFixture(t, &fakeExecutor{
			identifier: executorUsageTestProvider,
			executeStream: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
				source := make(chan pluginapi.ExecutorStreamChunk, 3)
				go func() {
					source <- pluginapi.ExecutorStreamChunk{Payload: first}
					time.Sleep(20 * time.Millisecond)
					source <- pluginapi.ExecutorStreamChunk{Payload: second}
					source <- pluginapi.ExecutorStreamChunk{Payload: usagePayload}
					close(source)
				}()
				return pluginapi.ExecutorStreamResponse{Chunks: source}, nil
			},
		})

		ctx := internallogging.WithRequestID(context.Background(), requestID)
		result, errExecute := fixture.executeStream(t, ctx)
		if errExecute != nil {
			t.Fatalf("Manager.ExecuteStream() error = %v", errExecute)
		}
		chunks := collectExecutorStream(t, result)
		if len(chunks) != 3 || !bytes.Equal(chunks[0].Payload, first) || !bytes.Equal(chunks[1].Payload, second) {
			t.Fatalf("stream chunks = %#v, want original split payloads", chunks)
		}
		captured := capture.waitRecord(t)
		requireSuccessfulUsage(t, captured.record)
		requireOpenAIUsageDetail(t, captured.record.Detail, 9, 10, 19)
		if captured.record.TTFT < 20*time.Millisecond {
			t.Fatalf("TTFT = %s, want token event after split frame completed", captured.record.TTFT)
		}
		capture.requireNoAdditionalRecords(t)
	})
}

func TestExecutorAdapterNilAuthDoesNotPublishUsage(t *testing.T) {
	requestID := "req-" + executorUsageTestSuffix(t.Name())
	capture := registerCaptureExecutorUsagePlugin(t, requestID)
	source := make(chan pluginapi.ExecutorStreamChunk, 2)
	source <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")}
	source <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":31,\"completion_tokens\":37,\"total_tokens\":68}}\n\n")}
	close(source)
	requests := make(chan pluginapi.ExecutorRequest, 2)
	executor := &fakeExecutor{
		identifier: executorUsageTestProvider,
		execute: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			requests <- req
			return pluginapi.ExecutorResponse{Payload: []byte(`{"usage":{"prompt_tokens":29,"completion_tokens":31,"total_tokens":60}}`)}, nil
		},
		executeStream: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			requests <- req
			return pluginapi.ExecutorStreamResponse{Chunks: source}, nil
		},
	}
	record := normalizeTestCapabilityRecord(capabilityRecord{id: "codebuddy-nil-auth-" + executorUsageTestSuffix(t.Name())})
	host := newHostWithRecords(record)
	adapter := newExecutorAdapterForRecordForTest(
		host,
		record,
		executor,
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
	)
	adapter.provider = executorUsageTestProvider
	model := "codebuddy-nil-auth-model"
	payload := []byte(fmt.Sprintf(`{"model":%q}`, model))
	req := coreexecutor.Request{Model: model, Format: sdktranslator.FormatOpenAI, Payload: payload}
	baseOpts := coreexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAI,
		ResponseFormat:  sdktranslator.FormatOpenAI,
		OriginalRequest: bytes.Clone(payload),
	}
	ctx := internallogging.WithRequestID(context.Background(), requestID)

	if _, errExecute := adapter.Execute(ctx, nil, req, baseOpts); errExecute != nil {
		t.Fatalf("adapter.Execute(nil auth) error = %v", errExecute)
	}
	streamOpts := baseOpts
	streamOpts.Stream = true
	result, errStream := adapter.ExecuteStream(ctx, nil, req, streamOpts)
	if errStream != nil {
		t.Fatalf("adapter.ExecuteStream(nil auth) error = %v", errStream)
	}
	if chunks := collectExecutorStream(t, result); len(chunks) != 2 {
		t.Fatalf("adapter.ExecuteStream(nil auth) chunk count = %d, want 2", len(chunks))
	}

	for index := 0; index < 2; index++ {
		select {
		case request := <-requests:
			if request.AuthID != "" || request.AuthProvider != "" {
				t.Errorf("nil-auth executor request = AuthID %q AuthProvider %q, want both empty", request.AuthID, request.AuthProvider)
			}
		case <-time.After(executorUsageTestTimeout):
			t.Fatal("timed out waiting for nil-auth plugin executor request")
		}
	}
	capture.requireNoAdditionalRecords(t)
}
