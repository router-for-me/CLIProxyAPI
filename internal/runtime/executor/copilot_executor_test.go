package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	tr "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func copilotTestAuth(t *testing.T, id, model, endpoint string) *cliproxyauth.Auth {
	t.Helper()
	return &cliproxyauth.Auth{ID: id, Provider: copilot.Provider, Metadata: map[string]any{"access_token": "test-credential", "base_url": "https://api.individual.githubcopilot.com"}, CopilotCatalog: copilot.NewCatalog([]copilot.Model{{ID: model, Endpoints: []string{endpoint}}}, time.Now())}
}

// The default registry is also used by SDK callers and plugin capability discovery.
func TestCopilotResponsesRoutesArePrivate(t *testing.T) {
	const model = "claude-opus-5"
	e := NewCopilotExecutor(nil)
	auth := copilotTestAuth(t, "account", model, copilot.Responses)
	for _, source := range []tr.Format{tr.FormatOpenAI, tr.FormatClaude} {
		const input = `{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`
		req := cliproxyexecutor.Request{Model: model, Payload: []byte(input)}
		p, err := e.prepare(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: source}, false, false)
		if err != nil || !ojson.GetBytesNoCopy(p.body, "input.0").Exists() {
			t.Fatalf("Copilot %s to Responses did not translate: %v", source, err)
		}
		if tr.HasRequestTransformer(source, tr.FormatOpenAIResponse) || tr.HasStreamResponseTransformer(source, tr.FormatOpenAIResponse) || tr.HasNonStreamResponseTransformer(source, tr.FormatOpenAIResponse) {
			t.Fatalf("private %s route was advertised globally", source)
		}
		if got := tr.TranslateRequest(source, tr.FormatOpenAIResponse, model, req.Payload, false); string(got) != input {
			t.Fatalf("default %s fallback changed: %s", source, got)
		}
	}
}

func TestDefaultChatToClaudeStreamUsesOriginalFlag(t *testing.T) {
	line := []byte(`data: {"id":"chat-1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"}}]}`)
	for _, tc := range []struct {
		original string
		want     bool
	}{{`{}`, false}, {`{"stream":false}`, false}, {`{"stream":true}`, true}} {
		var state any
		chunks := tr.TranslateStream(context.Background(), tr.FormatOpenAI, tr.FormatClaude, "m", []byte(tc.original), []byte(`{"stream":true}`), line, &state)
		if got := bytes.Contains(bytes.Join(chunks, nil), []byte("event: message_start")); got != tc.want {
			t.Errorf("original %s: streaming=%t", tc.original, got)
		}
	}
}

func TestCopilotChatToClaudeExecuteStreamUsesExecutionMode(t *testing.T) {
	const model = "claude-opus-5"
	const upstream = "data: " + `{"id":"chat-1","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"}}]}` + "\n\n" +
		"data: " + `{"id":"chat-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"
	e := NewCopilotExecutor(nil)
	auth := copilotTestAuth(t, "account", model, copilot.Chat)
	for _, tc := range []struct{ name, field string }{{"omitted", ""}, {"true", `"stream":true,`}, {"false", `"stream":false,`}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", roundTripFunc(func(r *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					return nil, err
				}
				if ojson.GetBytesNoCopy(body, "stream").Type != ojson.True {
					t.Errorf("upstream request is not streaming: %s", body)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: r, Body: io.NopCloser(strings.NewReader(upstream))}, nil
			}))
			original := `{` + tc.field + `"messages":[{"role":"user","content":"hi"}],"max_tokens":16}`
			req := cliproxyexecutor.Request{Model: copilot.ModelPrefix + model, Payload: []byte(original)}
			opts := cliproxyexecutor.Options{SourceFormat: tr.FormatClaude, OriginalRequest: req.Payload, Stream: true}
			res, err := e.ExecuteStream(ctx, auth, req, opts)
			if err != nil {
				t.Fatal(err)
			}
			var output strings.Builder
			for chunk := range res.Chunks {
				if chunk.Err != nil {
					t.Fatal(chunk.Err)
				}
				output.Write(chunk.Payload)
			}
			if got := output.String(); !strings.Contains(got, "event: content_block_delta") || !strings.Contains(got, `"text":"hello"`) || !strings.Contains(got, "event: message_stop") {
				t.Errorf("incomplete Claude stream: %s", got)
			}
			if string(opts.OriginalRequest) != original {
				t.Errorf("original request was modified: %s", opts.OriginalRequest)
			}
		})
	}
}

func TestCopilotProtocolMatrix(t *testing.T) {
	executor := NewCopilotExecutor(&config.Config{})
	const model = "claude-opus-5"
	versions := []struct {
		format                   tr.Format
		path, req, response, sse string
	}{
		{tr.FormatOpenAI, copilot.Chat, `{"messages":[{"role":"user","content":"hello"}]}`,
			`{"id":"chat-1","model":"claude-opus-5","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"success"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`,
			"data: {\"id\":\"chat-1\",\"model\":\"test-model\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"success\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"chat-1\",\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"},
		{tr.FormatClaude, copilot.Messages, `{"max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`,
			`{"id":"msg_1","type":"message","model":"claude-opus-5","role":"assistant","content":[{"type":"text","text":"success"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`,
			"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"test-model\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":3}}}\n\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"success\"}}\n\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\ndata: {\"type\":\"message_stop\"}\n\n"},
		{tr.FormatOpenAIResponse, copilot.Responses, `{"input":"hello"}`,
			`{"id":"resp_1","object":"response","model":"claude-opus-5","status":"completed","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"success"}]}],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}`,
			"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"test-model\",\"status\":\"in_progress\"}}\n\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"success\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"test-model\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n"},
	}
	for _, from := range versions {
		for _, to := range versions {
			t.Run(from.format.String()+" to "+to.format.String(), func(t *testing.T) {
				auth := copilotTestAuth(t, to.path, model, to.path)
				req := cliproxyexecutor.Request{Model: copilot.ModelPrefix + model, Payload: []byte(from.req)}
				for _, stream := range []bool{false, true} {
					opts := executor.PlanRequestRoute(auth, req, cliproxyexecutor.Options{SourceFormat: from.format, Stream: stream})
					if got := executor.RequestToFormat(req, opts); got != to.format {
						t.Fatalf("planned format = %s, want %s", got, to.format)
					}
					ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripFunc(func(r *http.Request) (*http.Response, error) {
						if r.URL.Path != to.path || r.Header.Get("Authorization") != "Bearer test-credential" || r.Header.Get("X-Initiator") != "user" {
							t.Fatalf("unexpected request: %s %v", r.URL, r.Header)
						}
						if r.Header.Get("x-api-key") != "" || r.Header.Get("Copilot-Integration-Id") != "copilot-developer-cli" || r.Header.Get("X-GitHub-Api-Version") != "2026-08-01" || !strings.HasPrefix(r.Header.Get("User-Agent"), "copilot/") || r.Header.Get("Editor-Plugin-Version") != "" || r.Header.Get("X-Interaction-Type") != "conversation-user" || r.Header.Get("X-Client-Session-Id") == "" || r.Header.Get("X-Interaction-Id") == "" || r.Header.Get("X-Agent-Task-Id") == "" {
							t.Fatal("missing official CLI inference headers")
						}
						accept, helper := "application/json", ""
						if to.format == tr.FormatClaude {
							accept = ""
						}
						if stream {
							helper = "stream"
						}
						if r.Header.Get("Accept") != accept || r.Header.Get("X-Stainless-Helper-Method") != helper {
							t.Fatal("incorrect CLI transport headers")
						}
						if to.format == tr.FormatClaude {
							if r.Header.Get("Anthropic-Version") != "2023-06-01" || r.Header.Get("Openai-Intent") != "" {
								t.Fatal("unexpected Messages headers")
							}
						} else if r.Header.Get("Openai-Intent") != "conversation-agent" {
							t.Fatal("missing OpenAI conversation intent")
						}
						body, _ := io.ReadAll(r.Body)
						promptKey := "messages"
						if to.format == tr.FormatOpenAIResponse {
							promptKey = "input"
						}
						wireModel := ojson.GetBytesNoCopy(body, "model").String()
						if wireModel != model || !ojson.GetBytesNoCopy(body, promptKey).Exists() || ojson.GetBytesNoCopy(body, "stream").Bool() != stream || !bytes.Contains(body, []byte("hello")) {
							t.Fatalf("unexpected upstream request: %s", body)
						}
						wire := to.response
						if stream {
							wire = to.sse
						}
						return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(wire)), Request: r}, nil
					}))
					if stream {
						result, err := executor.ExecuteStream(ctx, auth, req, opts)
						if err != nil {
							t.Fatal(err)
						}
						var output []byte
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
							output = append(output, chunk.Payload...)
						}
						if from.format == tr.FormatClaude && to.format == tr.FormatOpenAI && (!bytes.Contains(output, []byte("event: message_start")) || !bytes.Contains(output, []byte("event: content_block_delta"))) {
							t.Fatal("Copilot stream without an explicit flag did not emit Claude SSE")
						}
						if !bytes.Contains(output, []byte("success")) {
							t.Fatalf("unexpected stream: %s", output)
						}
					} else if response, err := executor.Execute(ctx, auth, req, opts); err != nil || !bytes.Contains(response.Payload, []byte("success")) {
						t.Fatalf("unexpected response: %s, %v", response.Payload, err)
					}
				}
			})
		}
	}
}

func TestCopilotCountTokensGeminiResponse(t *testing.T) {
	const model = "claude-opus-5"
	const geminiBody = `{"contents":[{"role":"user","parts":[{"text":"Hello there"}]}]}`
	e := NewCopilotExecutor(nil)
	for _, tc := range []struct {
		name, endpoint, body string
		source, response     tr.Format
	}{
		{"gemini via chat", copilot.Chat, geminiBody, tr.FormatGemini, ""},
		{"gemini via messages", copilot.Messages, geminiBody, tr.FormatGemini, ""},
		{"explicit gemini response", copilot.Chat, `{"messages":[{"role":"user","content":"Hello there"}]}`, tr.FormatOpenAI, tr.FormatGemini},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := copilotTestAuth(t, "account", model, tc.endpoint)
			req := cliproxyexecutor.Request{Model: copilot.ModelPrefix + model, Payload: []byte(tc.body)}
			resp, err := e.CountTokens(t.Context(), auth, req, cliproxyexecutor.Options{SourceFormat: tc.source, ResponseFormat: tc.response})
			if err != nil {
				t.Fatal(err)
			}
			body := ojson.ParseBytes(resp.Payload)
			total := body.Get("totalTokens")
			if total.Type != ojson.Number || total.Int() <= 0 || body.Get("promptTokensDetails.0.tokenCount").Int() != total.Int() || body.Get("usage").Exists() {
				t.Fatalf("unexpected Gemini countTokens response: %s", resp.Payload)
			}
			if got := resp.Headers.Get("X-Cpa-Token-Count"); got != "estimated" {
				t.Errorf("X-Cpa-Token-Count = %q, want estimated", got)
			}
		})
	}
}

func TestCopilotCLIRequestCompatibility(t *testing.T) {
	executor := NewCopilotExecutor(&config.Config{})
	for _, model := range []string{"gemini-4-argon", "kimi-k3"} {
		t.Run(model, func(t *testing.T) {
			auth := copilotTestAuth(t, "account", model, copilot.Chat)
			req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"reasoning_effort":"high","store":true,"messages":[{"role":"developer","content":"instructions"},{"role":"user","content":"hello"}]}`)}
			prepared, err := executor.prepare(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: tr.FormatOpenAI}, false, false)
			if err != nil {
				t.Fatal(err)
			}
			doc := ojson.NewDoc(prepared.body)
			if doc.Get("reasoning_effort").String() != "high" || !doc.Get("store").Bool() || doc.Get("messages.0.role").String() != "system" {
				t.Fatalf("unexpected Chat request: %s", prepared.body)
			}
		})
	}
	t.Run("Claude adaptive thinking keeps temperature", func(t *testing.T) {
		const model = "claude-sonnet-5.5"
		auth := copilotTestAuth(t, "account", model, copilot.Messages)
		req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"max_tokens":128,"temperature":1,"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hello"}]}`)}
		prepared, err := executor.prepare(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: tr.FormatClaude}, false, false)
		if err != nil {
			t.Fatal(err)
		}
		doc := ojson.NewDoc(prepared.body)
		if doc.Get("temperature").Float() != 1 || doc.Get("thinking.type").String() != "adaptive" {
			t.Fatalf("unexpected Messages request: %s", prepared.body)
		}
	})
}

func TestCopilotCLIDefaultsAndExplicitIntent(t *testing.T) {
	executor := NewCopilotExecutor(&config.Config{})
	for _, tc := range []struct {
		name, model, path string
		from              tr.Format
		input             string
		checks            map[string]string
	}{
		{"Responses default", "mai-code-2", copilot.Responses, tr.FormatOpenAIResponse, `{"input":"hello"}`, map[string]string{"reasoning.effort": "medium", "reasoning.summary": "auto", "include.0": "reasoning.encrypted_content"}},
		{"Responses explicit", "gpt-5.6-sol", copilot.Responses, tr.FormatOpenAIResponse, `{"input":"hello","reasoning":{"effort":"none"}}`, map[string]string{"reasoning.effort": "none", "include.0": ""}},
		{"Chat default", "kimi-k3", copilot.Chat, tr.FormatOpenAI, `{"messages":[{"role":"user","content":"hello"}]}`, map[string]string{"reasoning_effort": "high", "temperature": "1"}},
		{"Messages default from Chat", "claude-fable-5.2", copilot.Messages, tr.FormatOpenAI, `{"messages":[{"role":"user","content":"hello"}]}`, map[string]string{"thinking.type": "adaptive", "output_config.effort": "medium", "max_tokens": "48000", "temperature": "1", "thinking.block_binding.prefix_mismatch_behavior": "drop_block"}},
		{"Messages adaptive-only", "claude-fable-5.2", copilot.Messages, tr.FormatClaude, `{"messages":[{"role":"user","content":"hello"}],"max_tokens":512,"thinking":{"type":"disabled"}}`, map[string]string{"thinking.type": "adaptive", "output_config.effort": "low", "max_tokens": "512"}},
		{"Messages required supports disable", "claude-sonnet-5", copilot.Messages, tr.FormatClaude, `{"messages":[{"role":"user","content":"hello"}],"thinking":{"type":"disabled"}}`, map[string]string{"thinking.type": "disabled"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := copilotTestAuth(t, "account", tc.model, tc.path)
			req := cliproxyexecutor.Request{Model: tc.model, Payload: []byte(tc.input)}
			prepared, err := executor.prepare(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: tc.from}, true, false)
			if err != nil {
				t.Fatal(err)
			}
			doc := ojson.NewDoc(prepared.body)
			for path, want := range tc.checks {
				if got := doc.Get(path).String(); got != want {
					t.Errorf("%s = %q, want %q (payload: %s)", path, got, want, prepared.body)
				}
			}
		})
	}
}

func TestCopilotVerifiedModelDefaults(t *testing.T) {
	executor := NewCopilotExecutor(&config.Config{})
	for _, tc := range []struct {
		models   []string
		endpoint string
		format   tr.Format
		input    string
		checks   map[string]string
	}{
		{[]string{"gpt-5.6-luna", "gpt-6-astra", "gpt-6-luna", "gpt-6-sol", "gpt-6.1-sol", "gpt-6.1-terra", "gpt-6.2-astra", "grok-4.6", "mai-code-1.1-flash", "mai-code-2-flash"}, copilot.Responses, tr.FormatOpenAIResponse, `{"input":"hi"}`, map[string]string{"reasoning.effort": "medium", "reasoning.summary": "auto", "include.0": "reasoning.encrypted_content"}},
		{[]string{"grok-4.7"}, copilot.Responses, tr.FormatOpenAIResponse, `{"input":"hi"}`, map[string]string{"reasoning.effort": "high", "reasoning.summary": "auto", "include.0": "reasoning.encrypted_content"}},
		{[]string{"gemini-4-carbon", "gemini-4-hydrogen", "gemini-4-neon"}, copilot.Chat, tr.FormatOpenAI, `{"messages":[{"role":"user","content":"hi"}]}`, map[string]string{"reasoning_effort": "medium", "temperature": "1"}},
		{[]string{"claude-haiku-5.5"}, copilot.Messages, tr.FormatClaude, `{"messages":[{"role":"user","content":"hi"}]}`, map[string]string{"thinking.type": "adaptive", "thinking.display": "summarized", "output_config.effort": "medium", "thinking.block_binding.prefix_mismatch_behavior": "drop_block", "temperature": "1", "max_tokens": "128000"}},
		{[]string{"claude-opus-4.8-fast"}, copilot.Messages, tr.FormatClaude, `{"messages":[{"role":"user","content":"hi"}]}`, map[string]string{"thinking.type": "adaptive", "thinking.display": "summarized", "output_config.effort": "medium", "temperature": "1", "max_tokens": "32000"}},
	} {
		for _, model := range tc.models {
			t.Run(model, func(t *testing.T) {
				auth := copilotTestAuth(t, "account", model, tc.endpoint)
				req := cliproxyexecutor.Request{Model: model, Payload: []byte(tc.input)}
				p, err := executor.prepare(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: tc.format}, false, false)
				if err != nil {
					t.Fatal(err)
				}
				for path, want := range tc.checks {
					if got := ojson.GetBytesNoCopy(p.body, path).String(); got != want {
						t.Errorf("%s = %q, want %q", path, got, want)
					}
				}
				if model == "claude-haiku-5.5" && strings.Join(p.route.AnthropicBetas, ",") != "thinking-binding-controls-2026-08-01" {
					t.Errorf("unexpected betas: %v", p.route.AnthropicBetas)
				}
			})
		}
	}
}

func TestCopilotInteractionsExplicitOutputLimit(t *testing.T) {
	const model = "claude-fable-5.2"
	e := NewCopilotExecutor(&config.Config{})
	auth := copilotTestAuth(t, "account", model, copilot.Messages)
	for _, cfg := range []string{"generation_config", "generationConfig"} {
		for _, field := range []string{"max_output_tokens", "maxOutputTokens"} {
			doc := ojson.NewDocString(`{"input":"hi"}`)
			_ = doc.Set(cfg+"."+field, 128)
			req := cliproxyexecutor.Request{Model: model, Payload: doc.Bytes()}
			p, err := e.prepare(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: tr.FormatInteractions}, false, false)
			if err != nil {
				t.Fatal(err)
			}
			if got := ojson.GetBytesNoCopy(p.body, "max_tokens").Int(); got != 128 {
				t.Errorf("%s.%s translated to %d, want 128", cfg, field, got)
			}
		}
	}
}

func TestCopilotMessagesTransportLimit(t *testing.T) {
	const id = "claude-fable-5.2"
	auth := copilotTestAuth(t, "account", id, copilot.Messages)
	model := copilot.Model{ID: id}
	model.Capabilities.Limits.Output = 128000
	model.Capabilities.Limits.NonStreamingOutput = 16000
	auth.CopilotCatalog = copilot.NewCatalog([]copilot.Model{model}, time.Now())
	for _, tc := range []struct {
		name                     string
		stream                   bool
		explicit, override, want int
		reject                   bool
	}{
		{"default", false, 0, 0, 16000, false},
		{"stream default", true, 0, 0, 48000, false},
		{"explicit", false, 128, 0, 128, false},
		{"explicit over cap", false, 48000, 0, 0, true},
		{"large stream", true, 64000, 0, 64000, false},
		{"payload lowers explicit", false, 48000, 256, 256, false},
		{"payload over cap", false, 128, 16001, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			if tc.override != 0 {
				cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: id}}, Params: map[string]any{"max_tokens": tc.override}}}
			}
			doc := ojson.NewDocString(`{"messages":[{"role":"user","content":"hi"}]}`)
			if tc.explicit != 0 {
				_ = doc.Set("max_tokens", tc.explicit)
			}
			p, err := NewCopilotExecutor(cfg).prepare(context.Background(), auth, cliproxyexecutor.Request{Model: id, Payload: doc.Bytes()}, cliproxyexecutor.Options{SourceFormat: tr.FormatClaude}, tc.stream, false)
			if tc.reject {
				if se, ok := err.(cliproxyexecutor.StatusError); !ok || se.StatusCode() != 400 || !strings.Contains(err.Error(), "16000") {
					t.Fatalf("expected non-streaming limit error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := ojson.GetBytesNoCopy(p.body, "max_tokens").Int(); int(got) != tc.want {
				t.Fatalf("max_tokens=%d, want %d", got, tc.want)
			}
		})
	}
}

func TestCopilotCLIGeminiEfforts(t *testing.T) {
	for _, id := range []string{"gemini-4-neon", "gemini-4-hydrogen"} {
		t.Run(id, func(t *testing.T) {
			auth := copilotTestAuth(t, "a", id, copilot.Chat)
			m := copilot.Model{ID: id}
			m.Capabilities.Supports.Reasoning = json.RawMessage(`["low","high","xhigh"]`)
			auth.CopilotCatalog = copilot.NewCatalog([]copilot.Model{m}, time.Now())
			r, _ := auth.CopilotCatalog.Route(id)
			if r.Info.Thinking == nil || strings.Join(r.Info.Thinking.Levels, ",") != "low,high" || r.DefaultEffort != "" {
				t.Fatalf("account/CLI intersection lost: %+v", r)
			}
			e := NewCopilotExecutor(&config.Config{})
			for _, tc := range []struct {
				format            tr.Format
				model, body, want string
			}{
				{tr.FormatOpenAI, id, `{"reasoning_effort":"xhigh","messages":[{"role":"user","content":"hi"}]}`, "high"},
				{tr.FormatOpenAIResponse, id, `{"reasoning":{"effort":"xhigh"},"input":"hi"}`, "high"},
				{tr.FormatOpenAI, id + "(xhigh)", `{"messages":[{"role":"user","content":"hi"}]}`, "high"},
				{tr.FormatOpenAI, id, `{"reasoning_effort":"low","messages":[{"role":"user","content":"hi"}]}`, "low"},
			} {
				p, err := e.prepare(context.Background(), auth, cliproxyexecutor.Request{Model: tc.model, Payload: []byte(tc.body)}, cliproxyexecutor.Options{SourceFormat: tc.format}, false, false)
				if err != nil {
					t.Fatal(err)
				}
				if got := ojson.GetBytesNoCopy(p.body, "reasoning_effort").String(); got != tc.want {
					t.Errorf("%s %s: reasoning_effort = %q, want %q", tc.model, tc.format, got, tc.want)
				}
			}
		})
	}
}

func TestCopilotCLIWireOptions(t *testing.T) {
	const fn = `[{"type":"function","name":"lookup","parameters":{"type":"object"}}]`
	for _, tc := range []struct {
		name, model, endpoint string
		from                  tr.Format
		body                  string
		want                  map[string]string
		absent                []string
	}{
		{"Claude omitted summary keeps effort", "claude-fable-5.2", copilot.Messages, tr.FormatClaude, `{"thinking":{"type":"adaptive","display":"omitted"},"messages":[{"role":"user","content":"hi"}]}`, map[string]string{"thinking.display": "omitted", "output_config.effort": "medium"}, nil},
		{"Claude explicit effort", "claude-fable-5.2", copilot.Messages, tr.FormatClaude, `{"thinking":{"type":"adaptive","display":"omitted"},"output_config":{"effort":"high"},"messages":[]}`, map[string]string{"thinking.display": "omitted", "output_config.effort": "high"}, nil},
		{"Responses tools and verbosity", "mai-code-2-flash", copilot.Responses, tr.FormatOpenAIResponse, `{"input":"hi","tools":` + fn + `}`, map[string]string{"parallel_tool_calls": "true", "text.verbosity": "low"}, nil},
		{"Responses without tools", "gpt-5.3-codex", copilot.Responses, tr.FormatClaude, `{"messages":[{"role":"user","content":"hi"}]}`, map[string]string{"text.verbosity": "low"}, []string{"parallel_tool_calls"}},
		{"Responses explicit values", "mai-code-2", copilot.Responses, tr.FormatOpenAIResponse, `{"input":"hi","tools":` + fn + `,"parallel_tool_calls":false,"text":{"verbosity":"high"}}`, map[string]string{"parallel_tool_calls": "false", "text.verbosity": "high"}, nil},
		{"Responses from Chat explicit verbosity", "gpt-6-sol", copilot.Responses, tr.FormatOpenAI, `{"verbosity":"high","messages":[{"role":"user","content":"hi"}]}`, map[string]string{"text.verbosity": "high"}, nil},
		{"Grok no verbosity", "grok-4.7", copilot.Responses, tr.FormatOpenAIResponse, `{"input":"hi","tools":` + fn + `}`, map[string]string{"parallel_tool_calls": "true"}, []string{"text.verbosity"}},
		{"Gemini tools", "gemini-4-argon", copilot.Chat, tr.FormatOpenAI, `{"messages":[],"tools":[{"type":"function","function":{"name":"lookup"}}]}`, map[string]string{"tool_choice": "validated", "snippy.enabled": "false"}, nil},
		{"Gemini explicit choice", "gemini-4-neon", copilot.Chat, tr.FormatOpenAI, `{"messages":[],"tools":[{"type":"function","function":{"name":"lookup"}}],"tool_choice":"none","snippy":{"enabled":true}}`, map[string]string{"tool_choice": "none", "snippy.enabled": "true"}, nil},
		{"Gemini without tools", "gemini-4-carbon", copilot.Chat, tr.FormatOpenAI, `{"messages":[],"tools":[]}`, nil, []string{"tool_choice"}},
		{"Kimi tool choice", "kimi-k3", copilot.Chat, tr.FormatOpenAI, `{"messages":[],"tools":[{"type":"function","function":{"name":"lookup"}}]}`, map[string]string{"snippy.enabled": "false"}, []string{"tool_choice"}},
		{"Claude cache defaults", "claude-fable-5.2", copilot.Messages, tr.FormatClaude, `{"system":"instructions","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"lookup"}]}`, map[string]string{"system.0.cache_control.type": "ephemeral", "messages.0.content.0.cache_control.type": "ephemeral", "tools.0.cache_control.type": "ephemeral"}, nil},
		{"Claude existing cache policy", "claude-fable-5.2", copilot.Messages, tr.FormatClaude, `{"system":"instructions","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"lookup","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`, map[string]string{"system": "instructions", "messages.0.content": "hi", "tools.0.cache_control.ttl": "1h"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := NewCopilotExecutor(&config.Config{})
			p, err := e.prepare(context.Background(), copilotTestAuth(t, "a", tc.model, tc.endpoint), cliproxyexecutor.Request{Model: tc.model, Payload: []byte(tc.body)}, cliproxyexecutor.Options{SourceFormat: tc.from}, false, false)
			if err != nil {
				t.Fatal(err)
			}
			for k, v := range tc.want {
				if got := ojson.GetBytesNoCopy(p.body, k).String(); got != v {
					t.Errorf("%s=%q, want %q", k, got, v)
				}
			}
			for _, k := range tc.absent {
				if ojson.GetBytesNoCopy(p.body, k).Exists() {
					t.Errorf("unexpected %s", k)
				}
			}
		})
	}
}

func TestCopilotPayloadRulesWinOverCLIDefaults(t *testing.T) {
	for _, tc := range []struct {
		model, endpoint            string
		format                     tr.Format
		body, remove, change, want string
	}{
		{"mai-code-2-flash", copilot.Responses, tr.FormatOpenAIResponse, `{"input":"hi","tools":[{"type":"function","name":"lookup"}]}`, "parallel_tool_calls", "text.verbosity", "high"},
		{"gemini-4-neon", copilot.Chat, tr.FormatOpenAI, `{"messages":[],"tools":[{"type":"function","function":{"name":"lookup"}}]}`, "tool_choice", "reasoning_effort", "xhigh"},
		{"claude-fable-5.2", copilot.Messages, tr.FormatClaude, `{"system":"prefix","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"lookup"}]}`, "system.0.cache_control", "tools.0.cache_control.ttl", "1h"},
	} {
		t.Run(tc.model, func(t *testing.T) {
			cfg := &config.Config{}
			match := []config.PayloadModelRule{{Name: tc.model}}
			cfg.Payload.Override = []config.PayloadRule{{Models: match, Params: map[string]any{tc.change: tc.want}}}
			cfg.Payload.Filter = []config.PayloadFilterRule{{Models: match, Params: []string{tc.remove}}}
			e := NewCopilotExecutor(cfg)
			for _, stream := range []bool{true, false} {
				p, err := e.prepare(context.Background(), copilotTestAuth(t, "a", tc.model, tc.endpoint), cliproxyexecutor.Request{Model: tc.model, Payload: []byte(tc.body)}, cliproxyexecutor.Options{SourceFormat: tc.format}, stream, false)
				if err != nil {
					t.Fatal(err)
				}
				root := ojson.ParseBytes(p.body)
				if root.Get(tc.remove).Exists() || root.Get(tc.change).String() != tc.want {
					t.Errorf("stream=%t: payload rules lost: %s", stream, p.body)
				}
			}
		})
	}
}

func TestCopilotPayloadCompatibilityRunsBeforeUserRules(t *testing.T) {
	const model = "kimi-k3"
	cfg := &config.Config{}
	cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: model, Protocol: "openai"}}, Params: map[string]any{"store": true, "reasoning_effort": "high"}}}
	cfg.Payload.Filter = []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: model}}, Params: []string{"stream_options"}}}
	executor := NewCopilotExecutor(cfg)
	auth := copilotTestAuth(t, "a", model, copilot.Chat)
	req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"store":false,"reasoning_effort":"low","messages":[{"role":"developer","content":"system"},{"role":"user","content":"hello"}]}`)}
	prepared, err := executor.prepare(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: tr.FormatOpenAI}, true, false)
	if err != nil {
		t.Fatal(err)
	}
	doc := ojson.NewDoc(prepared.body)
	if !doc.Get("store").Bool() || doc.Get("reasoning_effort").String() != "high" || doc.Get("messages.0.role").String() != "system" || doc.Get("stream_options").Exists() {
		t.Fatalf("unexpected outgoing payload: %s", prepared.body)
	}
}

func TestCopilotColdRouteUsesPresetCapabilitiesAndDefaults(t *testing.T) {
	e := NewCopilotExecutor(nil)
	const id = "claude-haiku-4.5"
	auth := &cliproxyauth.Auth{ID: "cold", Provider: copilot.Provider, Metadata: map[string]any{"base_url": "https://api.business.githubcopilot.com", "access_token": "account-token"}}
	req := cliproxyexecutor.Request{Model: copilot.ModelPrefix + id, Payload: []byte(`{"messages":[{"role":"user","content":"hello"}]}`)}
	opts := e.PlanRequestRoute(auth, req, cliproxyexecutor.Options{SourceFormat: tr.FormatClaude})
	if e.RequestToFormat(req, opts) != tr.FormatClaude {
		t.Fatal("cold start did not plan the Messages route")
	}
	p, err := e.prepare(t.Context(), auth, req, opts, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if p.route.NonStreamingOutput != 16000 || ojson.GetBytesNoCopy(p.body, "thinking.budget_tokens").Int() != 1024 || ojson.GetBytesNoCopy(p.body, "max_tokens").Int() != 8192 {
		t.Fatal("lost cold Messages budget or output limits")
	}
	for _, tc := range []struct {
		name    string
		catalog *copilot.Catalog
	}{
		{"denied", copilot.UnavailableCatalog}, {"empty", copilot.NewCatalog(nil, time.Now())},
		{"expired", copilot.NewCatalog([]copilot.Model{{ID: id}}, time.Now().Add(-copilot.CatalogMaxAge))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := auth.Clone()
			a.CopilotCatalog = tc.catalog
			_, err := e.prepare(t.Context(), a, req, opts, false, false)
			if se, ok := err.(cliproxyexecutor.StatusError); !ok || se.StatusCode() != http.StatusNotFound {
				t.Fatalf("previously planned cold route survived %s: %v", tc.name, err)
			}
		})
	}
}
