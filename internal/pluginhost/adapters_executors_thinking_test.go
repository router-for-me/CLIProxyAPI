package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

type recordingThinkingApplier struct {
	provider string
	calls    []pluginapi.ThinkingApplyRequest
}

func (r *recordingThinkingApplier) Identifier() string {
	return r.provider
}

func (r *recordingThinkingApplier) ApplyThinking(_ context.Context, req pluginapi.ThinkingApplyRequest) (pluginapi.PayloadResponse, error) {
	r.calls = append(r.calls, req)
	var payload map[string]any
	if errUnmarshal := json.Unmarshal(req.Body, &payload); errUnmarshal != nil {
		return pluginapi.PayloadResponse{}, errUnmarshal
	}
	payload["applied_by_plugin"] = r.provider
	payload["thinking_mode"] = req.Config.Mode
	if req.Config.Level != "" {
		payload["thinking_level"] = req.Config.Level
	}
	if req.Config.Budget != 0 {
		payload["thinking_budget"] = req.Config.Budget
	}
	out, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return pluginapi.PayloadResponse{}, errMarshal
	}
	return pluginapi.PayloadResponse{Body: out}, nil
}

func TestExecutorAdapterAppliesCanonicalThinkingWithPluginApplier(t *testing.T) {
	const provider = "plugin-thinking-exec"
	applier := &recordingThinkingApplier{provider: provider}

	record := normalizeTestCapabilityRecord(capabilityRecord{
		id:       "thinking-exec-plugin",
		priority: 10,
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				ThinkingApplier: applier,
			},
		},
	})
	host := newHostWithRecords(record)
	host.refreshThinkingProviders([]capabilityRecord{record})
	t.Cleanup(func() {
		thinking.ClearPluginProviders()
	})

	host.modelRegistrations[record.id] = pluginModelRegistration{
		pluginID: record.id,
		provider: provider,
		models: []*registry.ModelInfo{{
			ID: "my-model",
			Thinking: &registry.ThinkingSupport{
				Min:    1024,
				Max:    8192,
				Levels: []string{"low", "medium", "high"},
			},
		}},
		hasExecutor: true,
	}

	var gotExecuteReq, gotStreamReq, gotCountReq pluginapi.ExecutorRequest
	adapter := newExecutorAdapterForRecordForTest(host, record, &fakeExecutor{
		identifier: provider,
		execute: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			gotExecuteReq = req
			return pluginapi.ExecutorResponse{Payload: []byte(`{"choices":[{"message":{"content":"ok"}}]}`)}, nil
		},
		executeStream: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			gotStreamReq = req
			ch := make(chan pluginapi.ExecutorStreamChunk)
			close(ch)
			return pluginapi.ExecutorStreamResponse{Chunks: ch}, nil
		},
		countTokens: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			gotCountReq = req
			return pluginapi.ExecutorResponse{Payload: []byte(`{"usage":{"prompt_tokens":5}}`)}, nil
		},
	}, []sdktranslator.Format{sdktranslator.FormatOpenAI}, []sdktranslator.Format{sdktranslator.FormatOpenAI})
	adapter.provider = provider

	_, errExecute := adapter.Execute(context.Background(), nil, coreexecutor.Request{
		Model:   "my-model(high)",
		Format:  sdktranslator.FormatOpenAI,
		Payload: []byte(`{"model":"my-model(high)","reasoning_effort":"low","messages":[{"role":"user","content":"hi"}]}`),
	}, coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if gotExecuteReq.Model != "my-model" {
		t.Fatalf("Execute req.Model = %q, want %q", gotExecuteReq.Model, "my-model")
	}
	if got := gjson.GetBytes(gotExecuteReq.Payload, "model").String(); got != "my-model" {
		t.Fatalf("Execute payload model = %q, want %q; payload=%s", got, "my-model", gotExecuteReq.Payload)
	}
	if got := gjson.GetBytes(gotExecuteReq.Payload, "applied_by_plugin").String(); got != provider {
		t.Fatalf("Execute payload applied_by_plugin = %q, want %q; payload=%s", got, provider, gotExecuteReq.Payload)
	}
	if got := gjson.GetBytes(gotExecuteReq.Payload, "thinking_level").String(); got != "high" {
		t.Fatalf("Execute payload thinking_level = %q, want high (suffix overrides body); payload=%s", got, gotExecuteReq.Payload)
	}

	streamRes, errStream := adapter.ExecuteStream(context.Background(), nil, coreexecutor.Request{
		Model:   "my-model(8192)",
		Format:  sdktranslator.FormatOpenAI,
		Payload: []byte(`{"model":"my-model(8192)","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
	}, coreexecutor.Options{
		Stream:         true,
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
	})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	for range streamRes.Chunks {
	}
	if gotStreamReq.Model != "my-model" {
		t.Fatalf("ExecuteStream req.Model = %q, want %q", gotStreamReq.Model, "my-model")
	}
	if got := gjson.GetBytes(gotStreamReq.Payload, "thinking_budget").Int(); got != 8192 {
		t.Fatalf("ExecuteStream payload thinking_budget = %d, want 8192; payload=%s", got, gotStreamReq.Payload)
	}

	_, errCount := adapter.CountTokens(context.Background(), nil, coreexecutor.Request{
		Model:   "my-model(medium)",
		Format:  sdktranslator.FormatOpenAI,
		Payload: []byte(`{"model":"my-model(medium)","messages":[{"role":"user","content":"hi"}]}`),
	}, coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
	})
	if errCount != nil {
		t.Fatalf("CountTokens() error = %v", errCount)
	}
	if gotCountReq.Model != "my-model" {
		t.Fatalf("CountTokens req.Model = %q, want %q", gotCountReq.Model, "my-model")
	}
	if got := gjson.GetBytes(gotCountReq.Payload, "thinking_level").String(); got != "medium" {
		t.Fatalf("CountTokens payload thinking_level = %q, want medium; payload=%s", got, gotCountReq.Payload)
	}
	if len(applier.calls) != 3 {
		t.Fatalf("ThinkingApplier calls = %d, want 3", len(applier.calls))
	}
}

func TestExecutorAdapterCallsPluginThinkingApplierWithoutModelThinkingMetadata(t *testing.T) {
	const provider = "plugin-thinking-no-meta"
	applier := &recordingThinkingApplier{provider: provider}

	record := normalizeTestCapabilityRecord(capabilityRecord{
		id:       "thinking-no-meta-plugin",
		priority: 10,
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				ThinkingApplier: applier,
			},
		},
	})
	host := newHostWithRecords(record)
	host.refreshThinkingProviders([]capabilityRecord{record})
	t.Cleanup(func() {
		thinking.ClearPluginProviders()
	})

	// ModelInfo is registered with Thinking: nil and UserDefined: false.
	host.modelRegistrations[record.id] = pluginModelRegistration{
		pluginID:    record.id,
		provider:    provider,
		models:      []*registry.ModelInfo{{ID: "my-model"}},
		hasExecutor: true,
	}

	var gotReq pluginapi.ExecutorRequest
	adapter := newExecutorAdapterForRecordForTest(host, record, &fakeExecutor{
		identifier: provider,
		execute: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			gotReq = req
			return pluginapi.ExecutorResponse{Payload: []byte(`{"choices":[{"message":{"content":"ok"}}]}`)}, nil
		},
	}, []sdktranslator.Format{sdktranslator.FormatOpenAI}, []sdktranslator.Format{sdktranslator.FormatOpenAI})
	adapter.provider = provider

	_, errExecute := adapter.Execute(context.Background(), nil, coreexecutor.Request{
		Model:   "my-model(high)",
		Format:  sdktranslator.FormatOpenAI,
		Payload: []byte(`{"model":"my-model(high)","messages":[{"role":"user","content":"hi"}]}`),
	}, coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if len(applier.calls) != 1 {
		t.Fatalf("ThinkingApplier calls = %d, want 1", len(applier.calls))
	}
	if gotReq.Model != "my-model" {
		t.Fatalf("req.Model = %q, want %q", gotReq.Model, "my-model")
	}
	if got := gjson.GetBytes(gotReq.Payload, "thinking_level").String(); got != "high" {
		t.Fatalf("payload thinking_level = %q, want high; payload=%s", got, gotReq.Payload)
	}
}

func TestExecutorAdapterAppliesModelThinkingMetadataWithoutPluginApplier(t *testing.T) {
	const provider = "plugin-builtin-thinking"
	record := normalizeTestCapabilityRecord(capabilityRecord{id: "builtin-thinking-plugin"})
	host := newHostWithRecords(record)
	host.modelRegistrations[record.id] = pluginModelRegistration{
		pluginID: record.id,
		provider: provider,
		models: []*registry.ModelInfo{
			{
				ID: "my-model",
				Thinking: &registry.ThinkingSupport{
					Levels: []string{"low", "medium", "high"},
				},
			},
			{
				ID:       "no-thinking-model",
				Thinking: nil,
			},
		},
		hasExecutor: true,
	}

	var gotReq pluginapi.ExecutorRequest
	adapter := newExecutorAdapterForRecordForTest(host, record, &fakeExecutor{
		identifier: provider,
		execute: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			gotReq = req
			return pluginapi.ExecutorResponse{Payload: []byte(`{"choices":[{"message":{"content":"ok"}}]}`)}, nil
		},
	}, []sdktranslator.Format{sdktranslator.FormatOpenAI}, []sdktranslator.Format{sdktranslator.FormatOpenAI})
	adapter.provider = provider

	_, errExecute := adapter.Execute(context.Background(), nil, coreexecutor.Request{
		Model:   "my-model(high)",
		Format:  sdktranslator.FormatClaude,
		Payload: []byte(`{"model":"my-model(high)","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`),
	}, coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatOpenAI,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if gotReq.Model != "my-model" {
		t.Fatalf("req.Model = %q, want %q", gotReq.Model, "my-model")
	}
	if got := gjson.GetBytes(gotReq.Payload, "model").String(); got != "my-model" {
		t.Fatalf("payload model = %q, want %q; payload=%s", got, "my-model", gotReq.Payload)
	}
	if got := gjson.GetBytes(gotReq.Payload, "reasoning_effort").String(); got != "high" {
		t.Fatalf("payload reasoning_effort = %q, want high; payload=%s", got, gotReq.Payload)
	}

	_, errNoThinking := adapter.Execute(context.Background(), nil, coreexecutor.Request{
		Model:   "no-thinking-model",
		Format:  sdktranslator.FormatOpenAI,
		Payload: []byte(`{"model":"no-thinking-model","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`),
	}, coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
	})
	if errNoThinking != nil {
		t.Fatalf("Execute(no-thinking-model) error = %v", errNoThinking)
	}
	if gjson.GetBytes(gotReq.Payload, "reasoning_effort").Exists() {
		t.Fatalf("expected reasoning_effort to be stripped for non-thinking model; payload=%s", gotReq.Payload)
	}
}

func TestExecutorAdapterValidatesThinkingAndClampsOrRejectsOutOfRange(t *testing.T) {
	const provider = "plugin-validation-thinking"
	record := normalizeTestCapabilityRecord(capabilityRecord{id: "validation-thinking-plugin"})
	host := newHostWithRecords(record)
	host.modelRegistrations[record.id] = pluginModelRegistration{
		pluginID: record.id,
		provider: provider,
		models: []*registry.ModelInfo{{
			ID:   "my-model",
			Type: provider,
			Thinking: &registry.ThinkingSupport{
				Min:    1024,
				Max:    8192,
				Levels: []string{"low", "medium", "high"},
			},
		}},
		hasExecutor: true,
	}

	var executeCalls int
	var gotReq pluginapi.ExecutorRequest
	adapter := newExecutorAdapterForRecordForTest(host, record, &fakeExecutor{
		identifier: provider,
		execute: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			executeCalls++
			gotReq = req
			return pluginapi.ExecutorResponse{Payload: []byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}]}`)}, nil
		},
	}, []sdktranslator.Format{sdktranslator.FormatClaude}, []sdktranslator.Format{sdktranslator.FormatClaude})
	adapter.provider = provider

	// Invalid suffix ("bogus") -> rejected with ThinkingError.
	_, errBogus := adapter.Execute(context.Background(), nil, coreexecutor.Request{
		Model:   "my-model(bogus)",
		Format:  sdktranslator.FormatClaude,
		Payload: []byte(`{"model":"my-model(bogus)","max_tokens":16384,"messages":[{"role":"user","content":"hi"}]}`),
	}, coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	var thinkingErr *thinking.ThinkingError
	if !errors.As(errBogus, &thinkingErr) {
		t.Fatalf("Execute(bogus) error = %v, want ThinkingError", errBogus)
	}
	if executeCalls != 0 {
		t.Fatalf("plugin Execute was called %d times on bogus suffix", executeCalls)
	}

	// Unsupported level suffix -> rejected with ThinkingError (ErrLevelNotSupported).
	_, errLevel := adapter.Execute(context.Background(), nil, coreexecutor.Request{
		Model:   "my-model(xhigh)",
		Format:  sdktranslator.FormatClaude,
		Payload: []byte(`{"model":"my-model(xhigh)","max_tokens":16384,"messages":[{"role":"user","content":"hi"}]}`),
	}, coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	thinkingErr = nil
	if !errors.As(errLevel, &thinkingErr) || thinkingErr.Code != thinking.ErrLevelNotSupported {
		t.Fatalf("Execute(xhigh) error = %v, want ThinkingError(%s)", errLevel, thinking.ErrLevelNotSupported)
	}
	if executeCalls != 0 {
		t.Fatalf("plugin Execute was called %d times on invalid level", executeCalls)
	}

	// Out-of-range budget in request body -> rejected with ThinkingError (ErrBudgetOutOfRange).
	_, errBudget := adapter.Execute(context.Background(), nil, coreexecutor.Request{
		Model:   "my-model",
		Format:  sdktranslator.FormatClaude,
		Payload: []byte(`{"model":"my-model","max_tokens":16384,"thinking":{"type":"enabled","budget_tokens":9000},"messages":[{"role":"user","content":"hi"}]}`),
	}, coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	thinkingErr = nil
	if !errors.As(errBudget, &thinkingErr) || thinkingErr.Code != thinking.ErrBudgetOutOfRange {
		t.Fatalf("Execute(budget 9000 in body) error = %v, want ThinkingError(%s)", errBudget, thinking.ErrBudgetOutOfRange)
	}
	if executeCalls != 0 {
		t.Fatalf("plugin Execute was called %d times on out-of-range body budget", executeCalls)
	}

	// Out-of-range numeric suffix (9000 > Max 8192) -> clamped to 8192 by central validation.
	_, errSuffixClamp := adapter.Execute(context.Background(), nil, coreexecutor.Request{
		Model:   "my-model(9000)",
		Format:  sdktranslator.FormatClaude,
		Payload: []byte(`{"model":"my-model(9000)","max_tokens":16384,"messages":[{"role":"user","content":"hi"}]}`),
	}, coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if errSuffixClamp != nil {
		t.Fatalf("Execute(my-model(9000)) error = %v", errSuffixClamp)
	}
	if got := gjson.GetBytes(gotReq.Payload, "thinking.budget_tokens").Int(); got != 8192 {
		t.Fatalf("clamped budget_tokens = %d, want 8192; payload=%s", got, gotReq.Payload)
	}
}

func TestExecutorAdapterAppliesThinkingBeforePayloadRules(t *testing.T) {
	const provider = "plugin-thinking-payload-order"
	applier := &recordingThinkingApplier{provider: provider}

	record := normalizeTestCapabilityRecord(capabilityRecord{
		id:       "thinking-payload-order-plugin",
		priority: 10,
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				ThinkingApplier: applier,
			},
		},
	})
	host := newHostWithRecords(record)
	host.refreshThinkingProviders([]capabilityRecord{record})
	t.Cleanup(func() {
		thinking.ClearPluginProviders()
	})
	host.modelRegistrations[record.id] = pluginModelRegistration{
		pluginID: record.id,
		provider: provider,
		models: []*registry.ModelInfo{{
			ID: "my-model",
			Thinking: &registry.ThinkingSupport{
				Levels: []string{"low", "medium", "high"},
			},
		}},
		hasExecutor: true,
	}
	host.runtimeConfig = &config.Config{
		Payload: config.PayloadConfig{
			Default: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "my-model", Protocol: "openai"}},
				Params: map[string]any{"temperature": 0.25},
			}},
			Override: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "my-model", Protocol: "openai"}},
				Params: map[string]any{"thinking_level": "low"},
			}},
			Filter: []config.PayloadFilterRule{{
				Models: []config.PayloadModelRule{{Name: "my-model", Protocol: "openai"}},
				Params: []string{"applied_by_plugin"},
			}},
		},
	}

	var gotExecuteReq, gotStreamReq, gotCountReq pluginapi.ExecutorRequest
	adapter := newExecutorAdapterForRecordForTest(host, record, &fakeExecutor{
		identifier: provider,
		execute: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			gotExecuteReq = req
			return pluginapi.ExecutorResponse{Payload: []byte(`{"choices":[{"message":{"content":"ok"}}]}`)}, nil
		},
		executeStream: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			gotStreamReq = req
			ch := make(chan pluginapi.ExecutorStreamChunk)
			close(ch)
			return pluginapi.ExecutorStreamResponse{Chunks: ch}, nil
		},
		countTokens: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			gotCountReq = req
			return pluginapi.ExecutorResponse{Payload: []byte(`{"usage":{"prompt_tokens":5}}`)}, nil
		},
	}, []sdktranslator.Format{sdktranslator.FormatOpenAI}, []sdktranslator.Format{sdktranslator.FormatOpenAI})
	adapter.provider = provider

	req := coreexecutor.Request{
		Model:   "my-model(high)",
		Format:  sdktranslator.FormatOpenAI,
		Payload: []byte(`{"model":"my-model(high)","messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
	}

	if _, err := adapter.Execute(context.Background(), nil, req, opts); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	streamOpts := opts
	streamOpts.Stream = true
	streamRes, errStream := adapter.ExecuteStream(context.Background(), nil, req, streamOpts)
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	for range streamRes.Chunks {
	}
	if _, err := adapter.CountTokens(context.Background(), nil, req, opts); err != nil {
		t.Fatalf("CountTokens() error = %v", err)
	}

	for name, got := range map[string]pluginapi.ExecutorRequest{
		"Execute":       gotExecuteReq,
		"ExecuteStream": gotStreamReq,
		"CountTokens":   gotCountReq,
	} {
		if got.Model != "my-model" {
			t.Fatalf("%s req.Model = %q, want my-model", name, got.Model)
		}
		if level := gjson.GetBytes(got.Payload, "thinking_level").String(); level != "low" {
			t.Fatalf("%s thinking_level = %q, want payload override low after thinking applier; payload=%s", name, level, got.Payload)
		}
		if gjson.GetBytes(got.Payload, "applied_by_plugin").Exists() {
			t.Fatalf("%s applied_by_plugin was not removed by payload filter; payload=%s", name, got.Payload)
		}
		if temp := gjson.GetBytes(got.Payload, "temperature").Float(); temp != 0.25 {
			t.Fatalf("%s temperature = %v, want payload default 0.25; payload=%s", name, temp, got.Payload)
		}
	}
}
