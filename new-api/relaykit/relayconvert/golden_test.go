package relayconvert

// golden_test.go pins the byte-level output of selected public conversion
// routes. Run with -update to regenerate testdata/golden.
//
// Volatile values (generated UUID-based ids, unix timestamps) are normalized
// before comparison so the snapshots are deterministic.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/require"
)

var updateGolden = flag.Bool("update", false, "update golden files")

// TestMain installs a deterministic media resolver so image-bearing fixtures
// convert without network access.
func TestMain(m *testing.M) {
	flag.Parse()
	SetMediaResolver(MediaResolver{
		GetBase64Data: func(c context.Context, source types.FileSource, reason ...string) (string, string, error) {
			return "aGVsbG8=", "image/png", nil
		},
		DecodeBase64FileData: func(base64String string) (string, string, error) {
			return "aGVsbG8=", "image/png", nil
		},
	})
	os.Exit(m.Run())
}

const goldenDir = "testdata/golden"

var (
	hex32Re     = regexp.MustCompile(`[0-9a-f]{32}`)
	timestampRe = regexp.MustCompile(`("created(_at)?"\s*:\s*)\d{9,}`)
)

func normalizeVolatile(data []byte) []byte {
	data = hex32Re.ReplaceAll(data, []byte("<uuid>"))
	data = timestampRe.ReplaceAll(data, []byte(`${1}0`))
	return data
}

func marshalGolden(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	require.NoError(t, err)
	return append(normalizeVolatile(data), '\n')
}

func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join(goldenDir, name+".golden.json")
	if *updateGolden {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, got, 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "golden file missing, run: cd relaykit && GOWORK=off go test ./relayconvert -run TestGolden -update")
	require.Equal(t, string(want), string(got), "conversion output drifted from golden snapshot %s", path)
}

func checkStreamEventsGolden(t *testing.T, name string, events []any) {
	t.Helper()
	got := marshalGolden(t, map[string]any{"events": events})
	path := filepath.Join(goldenDir, name+".golden.json")
	if *updateGolden {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, got, 0o644))
		return
	}

	wantData, err := os.ReadFile(path)
	require.NoError(t, err, "golden file missing, run: cd relaykit && GOWORK=off go test ./relayconvert -run TestGolden -update")
	var wantSnapshot map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(wantData, &wantSnapshot))
	wantEvents, ok := wantSnapshot["events"]
	require.True(t, ok, "stream golden snapshot %s has no events", path)
	want := marshalGolden(t, map[string]json.RawMessage{"events": wantEvents})
	require.Equal(t, string(want), string(got), "conversion events drifted from golden snapshot %s", path)
}

// goldenInfo mirrors the host's default converter options (new-api's
// model_setting defaults at the time the snapshots were recorded) so the
// golden files stay comparable across the extraction.
func goldenInfo() convmeta.Meta {
	return &convmeta.Values{
		ChannelMetaAttached: true,
		UpstreamModelName:   "upstream-model",
		ClaudeConvertInfo: &convmeta.ClaudeConvertInfo{
			LastMessagesType: convmeta.LastMessageTypeNone,
		},
		Options: &convmeta.Options{
			Gemini: convmeta.GeminiOptions{
				ThinkingAdapterBudgetTokensPercentage: 0.6,
				FunctionCallThoughtSignatureEnabled:   true,
				SafetySetting:                         func(string) string { return "OFF" },
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Fixtures: one representative rich request per source format
// ---------------------------------------------------------------------------

// Fixtures are built by unmarshalling wire-format JSON into the dto types —
// the same path production requests take — so they stay valid as struct
// internals evolve.
func fixtureRequests() map[types.RelayFormat]any {
	openai := &dto.GeneralOpenAIRequest{}
	mustUnmarshalFixture(`{
		"model": "gpt-test",
		"max_tokens": 1024,
		"stream": true,
		"messages": [
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": [
				{"type": "text", "text": "What is in this image?"},
				{"type": "image_url", "image_url": {"url": "https://example.com/cat.png", "detail": "high"}}
			]},
			{"role": "assistant", "tool_calls": [{"id": "call_abc", "type": "function", "function": {"name": "get_weather", "arguments": "{\"city\":\"Paris\"}"}}]},
			{"role": "tool", "tool_call_id": "call_abc", "content": "15 degrees"},
			{"role": "user", "content": "Summarize."}
		],
		"tools": [{"type": "function", "function": {"name": "get_weather", "description": "Get weather by city", "parameters": {"type": "object", "properties": {"city": {"type": "string"}}, "required": ["city"]}}}],
		"tool_choice": "auto"
	}`, openai)

	responses := &dto.OpenAIResponsesRequest{}
	mustUnmarshalFixture(`{
		"model": "gpt-test",
		"stream": true,
		"max_output_tokens": 1024,
		"instructions": "You are a helpful assistant.",
		"input": [
			{"type": "message", "role": "user", "content": [
				{"type": "input_text", "text": "What is in this image?"},
				{"type": "input_image", "image_url": "https://example.com/cat.png"}
			]},
			{"type": "function_call", "call_id": "call_abc", "name": "get_weather", "arguments": "{\"city\":\"Paris\"}"},
			{"type": "function_call_output", "call_id": "call_abc", "output": "15 degrees"}
		],
		"tools": [{"type": "function", "name": "get_weather", "description": "Get weather by city", "parameters": {"type": "object", "properties": {"city": {"type": "string"}}, "required": ["city"]}}]
	}`, responses)

	return map[types.RelayFormat]any{
		types.RelayFormatOpenAI:          openai,
		types.RelayFormatOpenAIResponses: responses,
	}
}

// ---------------------------------------------------------------------------
// Fixtures: one representative non-stream response per source format
// ---------------------------------------------------------------------------

func fixtureResponses() map[types.RelayFormat]any {
	openai := &dto.OpenAITextResponse{}
	mustUnmarshalFixture(`{
		"id": "chatcmpl-fixed",
		"object": "chat.completion",
		"created": 1700000000,
		"model": "gpt-test",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "The answer is 42.",
				"reasoning_content": "Deep thought.",
				"tool_calls": [{"id": "call_abc", "type": "function", "function": {"name": "get_weather", "arguments": "{\"city\":\"Paris\"}"}}]
			},
			"finish_reason": "tool_calls"
		}],
		"usage": {
			"prompt_tokens": 10,
			"completion_tokens": 5,
			"total_tokens": 15,
			"prompt_tokens_details": {"cached_tokens": 3},
			"completion_tokens_details": {"reasoning_tokens": 2}
		}
	}`, openai)

	claude := &dto.ClaudeResponse{}
	mustUnmarshalFixture(`{
		"id": "msg_fixed",
		"type": "message",
		"role": "assistant",
		"model": "claude-test",
		"content": [
			{"type": "text", "text": "The answer is 42."},
			{"type": "tool_use", "id": "toolu_abc", "name": "get_weather", "input": {"city": "Paris"}}
		],
		"stop_reason": "tool_use",
		"usage": {"input_tokens": 10, "output_tokens": 5, "cache_read_input_tokens": 3, "cache_creation_input_tokens": 2}
	}`, claude)

	gemini := &dto.GeminiChatResponse{}
	mustUnmarshalFixture(`{
		"candidates": [{
			"finishReason": "STOP",
			"content": {
				"role": "model",
				"parts": [
					{"text": "The answer is 42."},
					{"functionCall": {"name": "get_weather", "args": {"city": "Paris"}}}
				]
			}
		}],
		"usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 5, "thoughtsTokenCount": 2, "totalTokenCount": 15}
	}`, gemini)

	responses := &dto.OpenAIResponsesResponse{}
	mustUnmarshalFixture(`{
		"id": "resp_fixed",
		"object": "response",
		"model": "gpt-test",
		"status": "completed",
		"output": [
			{"type": "reasoning", "summary": [{"type": "summary_text", "text": "Deep thought."}]},
			{"type": "message", "role": "assistant", "status": "completed", "content": [{"type": "output_text", "text": "The answer is 42."}]},
			{"type": "function_call", "call_id": "call_abc", "name": "get_weather", "arguments": "{\"city\":\"Paris\"}", "status": "completed"}
		],
		"usage": {"input_tokens": 10, "output_tokens": 5, "total_tokens": 15}
	}`, responses)

	return map[types.RelayFormat]any{
		types.RelayFormatOpenAI:          openai,
		types.RelayFormatClaude:          claude,
		types.RelayFormatGemini:          gemini,
		types.RelayFormatOpenAIResponses: responses,
	}
}

// ---------------------------------------------------------------------------
// Fixtures: stream chunk sequences per source format
// ---------------------------------------------------------------------------

func fixtureStreamChunks() map[types.RelayFormat][]any {
	return map[types.RelayFormat][]any{
		types.RelayFormatOpenAI: {
			chatStreamChunk(`{"id":"chatcmpl-fixed","object":"chat.completion.chunk","created":1700000000,"model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"}}]}`),
			chatStreamChunk(`{"id":"chatcmpl-fixed","object":"chat.completion.chunk","created":1700000000,"model":"gpt-test","choices":[{"index":0,"delta":{"content":" world"}}]}`),
			chatStreamChunk(`{"id":"chatcmpl-fixed","object":"chat.completion.chunk","created":1700000000,"model":"gpt-test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`),
		},
		types.RelayFormatClaude: {
			claudeStreamChunk(`{"type":"message_start","message":{"id":"msg_fixed","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":4,"output_tokens":0}}}`),
			claudeStreamChunk(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
			claudeStreamChunk(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello world"}}`),
			claudeStreamChunk(`{"type":"content_block_stop","index":0}`),
			claudeStreamChunk(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`),
			claudeStreamChunk(`{"type":"message_stop"}`),
		},
		types.RelayFormatGemini: {
			geminiStreamChunk(`{"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]}}]}`),
			geminiStreamChunk(`{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"text":" world"}]}}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":2,"totalTokenCount":6}}`),
		},
		types.RelayFormatOpenAIResponses: {
			responsesStreamChunk(`{"type":"response.output_text.delta","delta":"Hello"}`),
			responsesStreamChunk(`{"type":"response.output_text.delta","delta":" world"}`),
			responsesStreamChunk(`{"type":"response.completed","response":{"id":"resp_fixed","object":"response","status":"completed","model":"gpt-test","usage":{"input_tokens":4,"output_tokens":2,"total_tokens":6}}}`),
		},
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func allFormats() []types.RelayFormat {
	return []types.RelayFormat{
		types.RelayFormatOpenAI,
		types.RelayFormatClaude,
		types.RelayFormatGemini,
		types.RelayFormatOpenAIResponses,
	}
}

func TestGoldenRequestConversionMatrix(t *testing.T) {
	requests := fixtureRequests()
	fromFormats := []types.RelayFormat{
		types.RelayFormatOpenAI,
		types.RelayFormatOpenAIResponses,
	}
	toFormats := []types.RelayFormat{
		types.RelayFormatOpenAI,
		types.RelayFormatClaude,
		types.RelayFormatOpenAIResponses,
	}
	for _, from := range fromFormats {
		for _, to := range toFormats {
			if from == to {
				continue
			}
			if _, ok := lookupRequestRoute(from, to); !ok {
				t.Fatalf("request route %s -> %s is no longer registered", from, to)
			}
			name := fmt.Sprintf("request/%s_to_%s", from, to)
			t.Run(name, func(t *testing.T) {
				result, err := ConvertRequest(nil, goldenInfo(), to, deepCopyFixture(t, requests[from]))
				require.NoError(t, err)
				checkGolden(t, name, marshalGolden(t, result.Value))
			})
		}
	}
}

func TestGoldenResponseConversionMatrix(t *testing.T) {
	responses := fixtureResponses()
	for _, from := range allFormats() {
		for _, to := range allFormats() {
			if from == to || to == types.RelayFormatGemini ||
				(from == types.RelayFormatOpenAI && to == types.RelayFormatClaude) {
				continue
			}
			name := fmt.Sprintf("response/%s_to_%s", from, to)
			t.Run(name, func(t *testing.T) {
				result, err := ConvertResponse(nil, goldenInfo(), to, deepCopyFixture(t, responses[from]))
				require.NoError(t, err)
				checkGolden(t, name, marshalGolden(t, result.Value))
			})
		}
	}
}

func TestGoldenStreamConversionMatrix(t *testing.T) {
	chunkSets := fixtureStreamChunks()
	for _, from := range allFormats() {
		for _, to := range allFormats() {
			if from == to {
				continue
			}
			name := fmt.Sprintf("stream/%s_to_%s", from, to)
			t.Run(name, func(t *testing.T) {
				info := goldenInfo()
				state, err := NewResponseStreamState(from, to, ResponseStreamOptions{
					ID:    "stream_fixed",
					Model: "stream-model",
				})
				require.NoError(t, err)

				var outputs []any
				for _, chunk := range chunkSets[from] {
					results, err := ConvertStreamResponseChunk(nil, info, state, deepCopyFixture(t, chunk))
					require.NoError(t, err)
					for _, r := range results {
						outputs = append(outputs, r.Value)
					}
				}
				finals, err := FinalizeStreamResponse(nil, info, state)
				require.NoError(t, err)
				for _, r := range finals {
					outputs = append(outputs, r.Value)
				}

				// Billing usage has private, cross-module acceptance coverage. Keep
				// the public golden focused on client-visible stream events.
				checkStreamEventsGolden(t, name, outputs)
			})
		}
	}
}

// ---------------------------------------------------------------------------
// Fixture helpers
// ---------------------------------------------------------------------------

func rawJSON(s string) json.RawMessage {
	return json.RawMessage(s)
}

// deepCopyFixture guards against converters mutating shared fixture state
// between subtests (JSON round-trip through the concrete type).
func deepCopyFixture(t *testing.T, v any) any {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	switch v.(type) {
	case *dto.GeneralOpenAIRequest:
		out := &dto.GeneralOpenAIRequest{}
		require.NoError(t, json.Unmarshal(data, out))
		return out
	case *dto.ClaudeRequest:
		out := &dto.ClaudeRequest{}
		require.NoError(t, json.Unmarshal(data, out))
		return out
	case *dto.GeminiChatRequest:
		out := &dto.GeminiChatRequest{}
		require.NoError(t, json.Unmarshal(data, out))
		return out
	case *dto.OpenAIResponsesRequest:
		out := &dto.OpenAIResponsesRequest{}
		require.NoError(t, json.Unmarshal(data, out))
		return out
	case *dto.OpenAITextResponse:
		out := &dto.OpenAITextResponse{}
		require.NoError(t, json.Unmarshal(data, out))
		return out
	case *dto.ClaudeResponse:
		out := &dto.ClaudeResponse{}
		require.NoError(t, json.Unmarshal(data, out))
		return out
	case *dto.GeminiChatResponse:
		out := &dto.GeminiChatResponse{}
		require.NoError(t, json.Unmarshal(data, out))
		return out
	case *dto.OpenAIResponsesResponse:
		out := &dto.OpenAIResponsesResponse{}
		require.NoError(t, json.Unmarshal(data, out))
		return out
	case *dto.ChatCompletionsStreamResponse:
		out := &dto.ChatCompletionsStreamResponse{}
		require.NoError(t, json.Unmarshal(data, out))
		return out
	case *dto.ResponsesStreamResponse:
		out := &dto.ResponsesStreamResponse{}
		require.NoError(t, json.Unmarshal(data, out))
		return out
	default:
		t.Fatalf("deepCopyFixture: unsupported fixture type %T", v)
		return nil
	}
}

func chatStreamChunk(raw string) *dto.ChatCompletionsStreamResponse {
	var r dto.ChatCompletionsStreamResponse
	mustUnmarshalFixture(raw, &r)
	return &r
}

func claudeStreamChunk(raw string) *dto.ClaudeResponse {
	var r dto.ClaudeResponse
	mustUnmarshalFixture(raw, &r)
	return &r
}

func geminiStreamChunk(raw string) *dto.GeminiChatResponse {
	var r dto.GeminiChatResponse
	mustUnmarshalFixture(raw, &r)
	return &r
}

func responsesStreamChunk(raw string) *dto.ResponsesStreamResponse {
	var r dto.ResponsesStreamResponse
	mustUnmarshalFixture(raw, &r)
	return &r
}

func mustUnmarshalFixture(raw string, out any) {
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		panic(fmt.Sprintf("bad fixture JSON: %v", err))
	}
}
