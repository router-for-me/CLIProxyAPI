package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const mistralResponsesToolHistory = `{
	"model":"mistral-large-latest","reasoning":{"effort":"high"},
	"input":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":[{"type":"output_text","text":"running tool"}]},
		{"type":"function_call","call_id":"call00001","name":"terminal","arguments":"{}"},
		{"type":"function_call_output","call_id":"call00001","output":"ok"}
	]
}`

const mistralResponsesReasoningHistory = `{
	"model":"mistral-large-latest","reasoning":{"effort":"high"},
	"input":[
		{"role":"user","content":"hi"},
		{"type":"reasoning","summary":[{"type":"summary_text","text":"actual plan"}]},
		{"role":"assistant","content":[{"type":"output_text","text":"running tool"}]},
		{"type":"function_call","call_id":"call00001","name":"terminal","arguments":"{}"},
		{"type":"function_call_output","call_id":"call00001","output":"ok"},
		{"type":"function_call","call_id":"call00002","name":"terminal","arguments":"{}"},
		{"type":"function_call_output","call_id":"call00002","output":"done"}
	]
}`

func TestOpenAICompatExecutorMistralChatRequests(t *testing.T) {
	for _, tc := range []struct {
		name          string
		baseURL       string
		payload       string
		source        sdktranslator.Format
		isCompat      bool
		wantMessages  int
		wantReasoning string
		wantCalls     int
	}{
		{name: "responses placeholder", payload: mistralResponsesToolHistory, wantMessages: 3, wantCalls: 1},
		{name: "responses compatibility mode", payload: mistralResponsesToolHistory, isCompat: true, wantMessages: 3, wantCalls: 1},
		{name: "responses real reasoning and follow up tools", payload: mistralResponsesReasoningHistory, wantMessages: 5, wantCalls: 2},
		{
			name: "ordinary assistant reasoning without tools", wantMessages: 3,
			payload: `{"model":"mistral-large-latest","input":[{"role":"user","content":"hi"},{"type":"reasoning","summary":[{"type":"summary_text","text":"actual plan"}]},{"role":"assistant","content":[{"type":"output_text","text":"answer"}]},{"role":"user","content":"continue"}]}`,
		},
		{
			name: "synthetic reasoning only turn", wantMessages: 2,
			payload: `{"model":"mistral-large-latest","reasoning":{"effort":"none"},"input":[{"role":"user","content":"hi"},{"type":"reasoning","summary":[]},{"role":"user","content":"continue"}]}`,
		},
		{
			name: "native chat prefix", source: sdktranslator.FormatOpenAI, wantMessages: 2,
			payload: `{"model":"mistral-large-latest","messages":[{"role":"user","content":"return formatted text"},{"role":"assistant","content":"\n","prefix":true,"reasoning_content":"actual plan"}]}`,
		},
		{
			name: "deepseek placeholder preserved", baseURL: "http://api.deepseek.com/v1",
			payload: mistralResponsesToolHistory, wantMessages: 3, wantCalls: 1, wantReasoning: "[reasoning unavailable]",
		},
		{
			name: "deepseek follow up reasoning preserved", baseURL: "http://api.deepseek.com/v1",
			payload: mistralResponsesReasoningHistory, wantMessages: 5, wantCalls: 2, wantReasoning: "actual plan",
		},
		{
			name: "moonshot reasoning preserved", baseURL: "http://api.moonshot.ai/v1",
			payload: mistralResponsesReasoningHistory, wantMessages: 5, wantCalls: 2, wantReasoning: "actual plan",
		},
		{
			name: "relay reasoning preserved", baseURL: "http://relay.example/v1",
			payload: mistralResponsesToolHistory, wantMessages: 3, wantCalls: 1, wantReasoning: "[reasoning unavailable]",
		},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				baseURL := tc.baseURL
				mistral := baseURL == ""
				if mistral {
					baseURL = "http://api.mistral.ai/v1"
				}
				source := tc.source
				if source == "" {
					source = sdktranslator.FormatOpenAIResponse
				}
				body := executeMistralChatTestRequest(t, baseURL, tc.payload, source, stream, tc.isCompat, mistral, config.PayloadConfig{})
				messages := ojson.GetBytes(body, "messages").Array()
				if len(messages) != tc.wantMessages {
					t.Fatalf("messages count = %d, want %d; body=%s", len(messages), tc.wantMessages, body)
				}
				calls, reasoningMessages := 0, 0
				for _, message := range messages {
					if message.Get("role").String() != "assistant" {
						continue
					}
					calls += len(message.Get("tool_calls").Array())
					if reasoning := message.Get("reasoning_content"); reasoning.Exists() {
						reasoningMessages++
						if tc.wantReasoning == "" || reasoning.String() != tc.wantReasoning {
							t.Fatalf("unexpected reasoning_content: %s", message.Raw)
						}
					} else if tc.wantReasoning != "" && message.Get("tool_calls").Exists() {
						t.Fatalf("required reasoning replay lost: %s", message.Raw)
					}
				}
				if calls != tc.wantCalls || (tc.wantReasoning != "" && reasoningMessages == 0) {
					t.Fatalf("conversation changed: calls=%d reasoning_messages=%d body=%s", calls, reasoningMessages, body)
				}
				if tc.wantCalls > 0 {
					if messages[1].Get("tool_calls.0.id").String() != "call00001" || messages[2].Get("tool_call_id").String() != "call00001" {
						t.Fatalf("tool call and result pairing changed: %s", body)
					}
				}
				if tc.source == sdktranslator.FormatOpenAI {
					if !messages[1].Get("prefix").Bool() || messages[1].Get("content").String() != "\n" {
						t.Fatalf("assistant prefix changed: %s", body)
					}
				}
				if usage := ojson.GetBytes(body, "stream_options.include_usage"); usage.Exists() != (!mistral && stream) || (usage.Exists() && !usage.Bool()) {
					t.Fatalf("unexpected stream usage option: %s", body)
				}
			})
		}
	}
}

func TestOpenAICompatExecutorMistralPayloadPrecedence(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			// Both conditions must observe the sanitized request, then explicit
			// overrides must survive any remaining request preparation.
			models := []config.PayloadModelRule{{
				Name: "mistral-large-latest", Protocol: "openai", FromProtocol: "openai-response",
				NotExist: []string{"messages.1.reasoning_content", "stream_options"},
			}}
			payload := config.PayloadConfig{
				Override: []config.PayloadRule{{Models: models, Params: map[string]any{
					"messages.1.reasoning_content": "operator supplied",
					"stream_options.include_usage": false,
				}}},
				Filter: []config.PayloadFilterRule{{
					Models: []config.PayloadModelRule{{Name: "mistral-large-latest", Protocol: "openai"}},
					Params: []string{"reasoning_effort"},
				}},
			}
			body := executeMistralChatTestRequest(t, "http://api.mistral.ai/v1", mistralResponsesToolHistory, sdktranslator.FormatOpenAIResponse, stream, false, false, payload)
			if ojson.GetBytes(body, "messages.1.reasoning_content").String() != "operator supplied" {
				t.Fatalf("conditional reasoning override lost: %s", body)
			}
			if option := ojson.GetBytes(body, "stream_options.include_usage"); !option.Exists() || option.Bool() {
				t.Fatalf("conditional stream option override lost: %s", body)
			}
			if ojson.GetBytes(body, "reasoning_effort").Exists() {
				t.Fatalf("filtered reasoning effort restored: %s", body)
			}
		})
	}
}

// The local HTTP proxy enforces the two Mistral field restrictions at issue;
// it does not call a real provider or attempt complete schema validation.
func executeMistralChatTestRequest(t *testing.T, baseURL, payload string, source sdktranslator.Format, stream, isCompat, rejectUnsupported bool, payloadConfig config.PayloadConfig) []byte {
	t.Helper()
	bodies := make(chan []byte, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read request: %v", errRead)
		}
		bodies <- body
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
		}
		if rejectUnsupported {
			forbidden := ojson.GetBytes(body, "stream_options").Exists()
			for _, message := range ojson.GetBytes(body, "messages").Array() {
				if message.Get("role").String() == "assistant" && message.Get("reasoning_content").Exists() {
					forbidden = true
				}
			}
			if forbidden {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = io.WriteString(w, `{"error":"unsupported Mistral request field"}`)
				return
			}
		}
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(proxy.Close)
	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{Payload: payloadConfig})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility", ProxyURL: proxy.URL,
		Attributes: map[string]string{"base_url": baseURL, "api_key": "test-key"},
	}
	req := cliproxyexecutor.Request{
		Model: "mistral-large-latest", Payload: []byte(payload),
		Metadata: map[string]any{"cliproxy.resolved_api_key_model_info": &registry.ModelInfo{
			ID: "mistral-large-latest", IsCompat: isCompat,
			Thinking: &registry.ThinkingSupport{ZeroAllowed: true, Levels: []string{"none", "high"}},
		}},
	}
	opts := cliproxyexecutor.Options{SourceFormat: source, ResponseFormat: sdktranslator.FormatOpenAI, Stream: stream}
	if stream {
		result, errStream := executor.ExecuteStream(context.Background(), auth, req, opts)
		if errStream != nil {
			t.Fatalf("ExecuteStream: %v", errStream)
		}
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				t.Fatalf("stream chunk: %v", chunk.Err)
			}
		}
	} else if _, errExecute := executor.Execute(context.Background(), auth, req, opts); errExecute != nil {
		t.Fatalf("Execute: %v", errExecute)
	}
	select {
	case body := <-bodies:
		return body
	default:
		t.Fatal("executor did not contact the local proxy")
		return nil
	}
}
