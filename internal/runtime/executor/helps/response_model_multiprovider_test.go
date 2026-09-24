package helps

import (
	"context"
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

type multiProviderTestExecutor struct {
	id string
}

func (e multiProviderTestExecutor) Identifier() string {
	return e.id
}

func newMultiProviderTestReporter(ctx context.Context, provider, model string, auth *cliproxyauth.Auth) *UsageReporter {
	return NewExecutorUsageReporter(ctx, multiProviderTestExecutor{id: provider}, model, auth)
}

func TestExtractResponseModelMultiProvider(t *testing.T) {
	tests := []struct {
		name         string
		provider     string
		payload      string
		wantModel    string
		wantTerminal bool
	}{
		// Claude format tests
		{
			name:         "claude sse message_start",
			provider:     "claude",
			payload:      `data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-3-7-sonnet-20250219"}}`,
			wantModel:    "claude-3-7-sonnet-20250219",
			wantTerminal: false,
		},
		{
			name:         "claude sse message_stop",
			provider:     "claude",
			payload:      `data: {"type":"message_stop"}`,
			wantModel:    "",
			wantTerminal: true,
		},
		{
			name:         "claude raw json non-stream message",
			provider:     "claude",
			payload:      `{"id":"msg_1","type":"message","role":"assistant","model":"claude-3-5-haiku-20241022","content":[{"type":"text","text":"hello"}]}`,
			wantModel:    "claude-3-5-haiku-20241022",
			wantTerminal: true,
		},
		// Gemini format tests
		{
			name:         "gemini sse chunk with modelVersion",
			provider:     "gemini",
			payload:      `data: {"candidates":[{"content":{"parts":[{"text":"hi"}]}}],"modelVersion":"gemini-2.5-flash"}`,
			wantModel:    "gemini-2.5-flash",
			wantTerminal: false,
		},
		{
			name:         "gemini sse terminal chunk with finishReason",
			provider:     "gemini",
			payload:      `data: {"candidates":[{"content":{"parts":[{"text":""}]},"finishReason":"STOP"}],"modelVersion":"gemini-2.5-flash"}`,
			wantModel:    "gemini-2.5-flash",
			wantTerminal: true,
		},
		{
			name:         "gemini raw json non-stream with modelVersion",
			provider:     "gemini",
			payload:      `{"candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}],"modelVersion":"gemini-2.5-pro"}`,
			wantModel:    "gemini-2.5-pro",
			wantTerminal: true,
		},
		{
			name:         "antigravity wrapped response.modelVersion",
			provider:     "antigravity",
			payload:      `data: {"response":{"candidates":[{"content":{"parts":[{"text":"hello"}]}}],"modelVersion":"gemini-3.7-flash"}}`,
			wantModel:    "gemini-3.7-flash",
			wantTerminal: false,
		},
		{
			name:         "gemini interactions sse interaction.completed with model",
			provider:     "gemini",
			payload:      `data: {"event_type":"interaction.completed","interaction":{"id":"i1","status":"requires_action","usage":{"total_input_tokens":2,"total_output_tokens":3,"total_tokens":5},"service_tier":"standard","model":"gemini-3.1-flash-lite"}}`,
			wantModel:    "gemini-3.1-flash-lite",
			wantTerminal: true,
		},
		{
			name:         "gemini interactions sse interaction.created with model",
			provider:     "gemini",
			payload:      `data: {"event_type":"interaction.created","interaction":{"id":"i1","model":"gemini-3.1-flash-lite"}}`,
			wantModel:    "gemini-3.1-flash-lite",
			wantTerminal: false,
		},
		{
			name:         "gemini interactions sse interaction.completed without model",
			provider:     "gemini",
			payload:      `data: {"event_type":"interaction.completed","interaction":{"id":"i1","status":"completed"}}`,
			wantModel:    "",
			wantTerminal: true,
		},
		{
			name:         "gemini-interactions provider sse interaction.completed with model",
			provider:     "gemini-interactions",
			payload:      `data: {"event_type":"interaction.completed","interaction":{"id":"i1","status":"completed","model":"gemini-3.1-flash-lite"}}`,
			wantModel:    "gemini-3.1-flash-lite",
			wantTerminal: true,
		},
		// OpenAI / OpenAICompat format tests
		{
			name:         "openai sse chat completion chunk",
			provider:     "openai",
			payload:      `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o-2024-08-06","choices":[{"index":0,"delta":{"content":"hi"}}]}`,
			wantModel:    "gpt-4o-2024-08-06",
			wantTerminal: false,
		},
		{
			name:         "openai sse chat completion terminal chunk",
			provider:     "openai",
			payload:      `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o-2024-08-06","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			wantModel:    "gpt-4o-2024-08-06",
			wantTerminal: true,
		},
		{
			name:         "openai raw json chat completion",
			provider:     "openai",
			payload:      `{"id":"chatcmpl-1","object":"chat.completion","model":"gpt-4o-2024-08-06","choices":[{"index":0,"message":{"role":"assistant","content":"hello"}}]}`,
			wantModel:    "gpt-4o-2024-08-06",
			wantTerminal: true,
		},
		// xAI format tests
		{
			name:         "xai sse chat completion chunk",
			provider:     "xai",
			payload:      `data: {"id":"chatcmpl-x1","object":"chat.completion.chunk","model":"grok-beta","choices":[{"index":0,"delta":{"content":"hi"}}]}`,
			wantModel:    "grok-beta",
			wantTerminal: false,
		},
		// Generic fallback tests
		{
			name:         "generic fallback with model",
			provider:     "custom",
			payload:      `{"model":"custom-model-v1","object":"chat.completion"}`,
			wantModel:    "custom-model-v1",
			wantTerminal: true,
		},
		{
			name:         "generic interactions sse interaction.completed with model",
			provider:     "custom",
			payload:      `data: {"event_type":"interaction.completed","interaction":{"model":"custom-model-v2"}}`,
			wantModel:    "custom-model-v2",
			wantTerminal: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotModel, gotTerminal := extractResponseModelEvent([]byte(tt.payload), tt.provider)
			if gotModel != tt.wantModel {
				t.Errorf("extractResponseModelEvent() gotModel = %q, want %q", gotModel, tt.wantModel)
			}
			if gotTerminal != tt.wantTerminal {
				t.Errorf("extractResponseModelEvent() gotTerminal = %v, want %v", gotTerminal, tt.wantTerminal)
			}
		})
	}
}

func TestIsModelSubstitutedMultiProvider(t *testing.T) {
	tests := []struct {
		name      string
		requested string
		served    string
		want      bool
	}{
		// Claude models
		{name: "claude dated alias match", requested: "claude-3-7-sonnet", served: "claude-3-7-sonnet-20250219", want: false},
		{name: "claude latest alias match", requested: "claude-3-5-sonnet-latest", served: "claude-3-5-sonnet-20241022", want: false},
		{name: "claude substitution opus to sonnet", requested: "claude-opus-5", served: "claude-sonnet-5", want: true},
		{name: "claude thinking suffix match", requested: "claude-3-7-sonnet(high)", served: "claude-3-7-sonnet", want: false},

		// Gemini models
		{name: "gemini dated alias match", requested: "gemini-2.5-flash", served: "gemini-2.5-flash-2025-05-20", want: false},
		{name: "gemini preview is substitution", requested: "gemini-2.5-flash", served: "gemini-2.5-flash-preview", want: true},
		{name: "gemini numeric version alias match", requested: "gemini-1.5-pro", served: "gemini-1.5-pro-002", want: false},
		{name: "gemini substitution pro to flash", requested: "gemini-2.5-pro", served: "gemini-2.5-flash", want: true},

		// OpenAI models
		{name: "openai dated alias match", requested: "gpt-4o", served: "gpt-4o-2024-08-06", want: false},
		{name: "openai substitution gpt-4o to gpt-4o-mini", requested: "gpt-4o", served: "gpt-4o-mini", want: true},
		{name: "openai provider prefix match", requested: "openai/gpt-4o", served: "gpt-4o", want: false},

		// Devin models
		{name: "devin provider prefix match", requested: "devin/swe-2", served: "swe-2", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsModelSubstituted(tt.requested, tt.served); got != tt.want {
				t.Fatalf("IsModelSubstituted(%q, %q) = %v, want %v", tt.requested, tt.served, got, tt.want)
			}
		})
	}
}

func TestUsageReporterMultiProviderSubstitutionWarning(t *testing.T) {
	providers := []struct {
		provider    string
		requested   string
		served      string
		streamChunk string
		expectedLog string
	}{
		{
			provider:    "claude",
			requested:   "claude-opus-5",
			served:      "claude-sonnet-5",
			streamChunk: `data: {"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-5"}}`,
			expectedLog: `claude executor: upstream served model "claude-sonnet-5" for requested model "claude-opus-5"`,
		},
		{
			provider:    "gemini",
			requested:   "gemini-2.5-pro",
			served:      "gemini-2.5-flash",
			streamChunk: `data: {"candidates":[{"content":{"parts":[{"text":"hi"}]}}],"modelVersion":"gemini-2.5-flash"}`,
			expectedLog: `gemini executor: upstream served model "gemini-2.5-flash" for requested model "gemini-2.5-pro"`,
		},
		{
			provider:    "openai",
			requested:   "gpt-4o",
			served:      "gpt-4o-mini",
			streamChunk: `data: {"id":"chatcmpl-1","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":"hi"}}]}`,
			expectedLog: `openai executor: upstream served model "gpt-4o-mini" for requested model "gpt-4o"`,
		},
		{
			provider:    "gemini-interactions",
			requested:   "gemini-2.5-pro",
			served:      "gemini-3.1-flash-lite",
			streamChunk: `data: {"event_type":"interaction.completed","interaction":{"id":"i1","status":"completed","service_tier":"standard","model":"gemini-3.1-flash-lite"}}`,
			expectedLog: `gemini-interactions executor: upstream served model "gemini-3.1-flash-lite" for requested model "gemini-2.5-pro"`,
		},
	}

	for _, tc := range providers {
		t.Run(tc.provider, func(t *testing.T) {
			hook := setupResponseModelLoggerHook(t)
			auth := &cliproxyauth.Auth{ID: "auth-" + tc.provider}
			reporter := newMultiProviderTestReporter(context.Background(), tc.provider, tc.requested, auth)

			reporter.ObserveResponseModel([]byte(tc.streamChunk))
			if got := reporter.ResponseModel(); got != tc.served {
				t.Fatalf("reporter.ResponseModel() = %q, want %q", got, tc.served)
			}

			reporter.Publish(context.Background(), usage.Detail{InputTokens: 10, OutputTokens: 20})

			warnings := substitutionWarnings(hook)
			if len(warnings) != 1 {
				t.Fatalf("expected 1 warning, got %d: %v", len(warnings), warnings)
			}
			if !strings.Contains(warnings[0], tc.expectedLog) {
				t.Fatalf("warning %q does not contain expected log %q", warnings[0], tc.expectedLog)
			}
		})
	}
}

func TestStreamUsageBufferMultiProviderResponseModelPropagation(t *testing.T) {
	// OpenAI stream
	var openAIBuffer StreamUsageBuffer
	openAIBuffer.ObserveOpenAIStream([]byte(`data: {"id":"1","model":"gpt-4o-mini","choices":[{"delta":{"content":"x"}}]}`))
	if got := openAIBuffer.ResponseModel(); got != "gpt-4o-mini" {
		t.Fatalf("openAIBuffer.ResponseModel() = %q, want gpt-4o-mini", got)
	}

	openAIReporter := newMultiProviderTestReporter(context.Background(), "openai", "gpt-4o", nil)
	openAIBuffer.Observe(usage.Detail{InputTokens: 5, OutputTokens: 5}, true)
	openAIBuffer.Publish(context.Background(), openAIReporter)
	if got := openAIReporter.ResponseModel(); got != "gpt-4o-mini" {
		t.Fatalf("openAIReporter.ResponseModel() = %q, want gpt-4o-mini", got)
	}

	// Claude stream
	var claudeBuffer StreamUsageBuffer
	claudeBuffer.ObserveClaudeStream([]byte(`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-3-5-haiku-20241022"}}`))
	if got := claudeBuffer.ResponseModel(); got != "claude-3-5-haiku-20241022" {
		t.Fatalf("claudeBuffer.ResponseModel() = %q, want claude-3-5-haiku-20241022", got)
	}

	claudeReporter := newMultiProviderTestReporter(context.Background(), "claude", "claude-3-5-sonnet", nil)
	claudeBuffer.Observe(usage.Detail{InputTokens: 10, OutputTokens: 10}, true)
	claudeBuffer.Publish(context.Background(), claudeReporter)
	if got := claudeReporter.ResponseModel(); got != "claude-3-5-haiku-20241022" {
		t.Fatalf("claudeReporter.ResponseModel() = %q, want claude-3-5-haiku-20241022", got)
	}
}
