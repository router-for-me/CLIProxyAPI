package executor

import (
	"net/http"
	"testing"

	"github.com/tidwall/gjson"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestPrepareCodeBuddyCNAuthUsesOAuthTokenAndHeaders(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider: "codebuddy-cn",
		Metadata: map[string]any{
			"access_token": "oauth-access",
		},
		Attributes: map[string]string{
			"header:X-IDE-Name": "custom-client",
		},
	}
	prepared := prepareCodeBuddyCNAuth(auth)
	if prepared == auth {
		t.Fatal("prepareCodeBuddyCNAuth returned original auth")
	}
	if got := prepared.Attributes["api_key"]; got != "oauth-access" {
		t.Fatalf("api_key = %q", got)
	}
	if got := prepared.Attributes["base_url"]; got != "https://copilot.tencent.com/v2" {
		t.Fatalf("base_url = %q", got)
	}
	if got := prepared.Attributes["header:X-IDE-Name"]; got != "custom-client" {
		t.Fatalf("custom X-IDE-Name = %q", got)
	}
	if got := prepared.Attributes["header:X-Product"]; got != "SaaS" {
		t.Fatalf("X-Product = %q", got)
	}
	if _, ok := auth.Attributes["api_key"]; ok {
		t.Fatal("original auth was mutated")
	}
}

func TestCodeBuddyCNPrepareRequestUsesOAuthAccessToken(t *testing.T) {
	executor := NewCodeBuddyCNExecutor(nil)
	req, err := http.NewRequest(http.MethodPost, "https://example.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	err = executor.PrepareRequest(req, &cliproxyauth.Auth{Metadata: map[string]any{"access_token": "oauth-access"}})
	if err != nil {
		t.Fatalf("PrepareRequest() error = %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer oauth-access" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := req.Header.Get("X-Codebuddy-Request"); got != "1" {
		t.Fatalf("X-Codebuddy-Request = %q", got)
	}
}

func TestApplyCodeBuddyCNReasoning(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantEff  bool // reasoning_effort should still be present
		wantSumm string
	}{
		{
			name:     "reasoning_effort present maps to reasoning_summary",
			body:     `{"model":"glm-5.2","reasoning_effort":"high"}`,
			wantEff:  false,
			wantSumm: "auto",
		},
		{
			name:     "reasoning_effort none drops field",
			body:     `{"model":"glm-5.2","reasoning_effort":"none"}`,
			wantEff:  false,
			wantSumm: "",
		},
		{
			name:     "reasoning_effort off drops field",
			body:     `{"model":"glm-5.2","reasoning_effort":"off"}`,
			wantEff:  false,
			wantSumm: "",
		},
		{
			name:     "absent reasoning_effort leaves body untouched",
			body:     `{"model":"glm-5.2"}`,
			wantEff:  false,
			wantSumm: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := applyCodeBuddyCNReasoning([]byte(tt.body))
			if eff := gjson.GetBytes(got, "reasoning_effort"); eff.Exists() != tt.wantEff {
				t.Fatalf("reasoning_effort exists=%v, want %v (body=%s)", eff.Exists(), tt.wantEff, got)
			}
			if summ := gjson.GetBytes(got, "reasoning_summary").String(); summ != tt.wantSumm {
				t.Fatalf("reasoning_summary=%q, want %q (body=%s)", summ, tt.wantSumm, got)
			}
		})
	}
}

func TestApplyCodeBuddyCNAgentSystemPrompt(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantRepl bool // top-level system should be replaced
		wantMsgs bool // messages system should be replaced
	}{
		{
			name:     "top-level agent system replaced",
			body:     `{"system":"You are Claude Code, Anthropic's official CLI for software engineering"}`,
			wantRepl: true,
		},
		{
			name:     "top-level benign system untouched",
			body:     `{"system":"You are a translator."}`,
			wantRepl: false,
		},
		{
			name:     "messages system agent replaced",
			body:     `{"messages":[{"role":"system","content":"You are Cursor, an AI coding agent"}]}`,
			wantMsgs: true,
		},
		{
			name:     "messages system benign untouched",
			body:     `{"messages":[{"role":"system","content":"Summarize this text."}]}`,
			wantMsgs: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := applyCodeBuddyCNAgentSystemPrompt([]byte(tt.body))

			if sys := gjson.GetBytes(got, "system"); sys.Exists() {
				if sys.String() == neutralSystemPrompt != tt.wantRepl {
					t.Fatalf("system=%q, wantRepl=%v (body=%s)", sys.String(), tt.wantRepl, got)
				}
			}

			msgs := gjson.GetBytes(got, "messages")
			if msgs.Exists() && msgs.IsArray() {
				first := msgs.Array()[0].Get("content")
				if first.String() == neutralSystemPrompt != tt.wantMsgs {
					t.Fatalf("messages[0].content=%q, wantMsgs=%v (body=%s)", first.String(), tt.wantMsgs, got)
				}
			}
		})
	}
}

func TestApplyCodeBuddyCNOutgoingTransformsForcesStream(t *testing.T) {
	got := applyCodeBuddyCNOutgoingTransforms(nil, nil, "glm-5.2", cliproxyexecutor.Options{}, []byte(`{"model":"glm-5.2"}`))
	if !gjson.GetBytes(got, "stream").Bool() {
		t.Fatalf("stream should be forced true, got body=%s", got)
	}
}

func TestAggregateCodeBuddyCNChunks(t *testing.T) {
	raw := []byte(`{"id":"cmb-1","model":"glm-5.2","object":"chat.completion.chunk","created":123,"choices":[{"index":0,"delta":{"role":"assistant","content":"hello","reasoning_content":""},"finish_reason":""}],"usage":null}` + "\n" +
		`{"id":"cmb-1","model":"glm-5.2","object":"chat.completion.chunk","created":123,"choices":[{"index":0,"delta":{"role":"assistant","content":" world","reasoning_content":""},"finish_reason":""}],"usage":null}` + "\n" +
		`{"id":"cmb-1","model":"glm-5.2","object":"chat.completion.chunk","created":123,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":2,"total_tokens":4}}` + "\n")

	got := aggregateCodeBuddyCNChunks(raw)

	if obj := gjson.GetBytes(got, "object").String(); obj != "chat.completion" {
		t.Fatalf("object=%q, want chat.completion (body=%s)", obj, got)
	}
	if content := gjson.GetBytes(got, "choices.0.message.content").String(); content != "hello world" {
		t.Fatalf("content=%q, want %q (body=%s)", content, "hello world", got)
	}
	if fr := gjson.GetBytes(got, "choices.0.finish_reason").String(); fr != "stop" {
		t.Fatalf("finish_reason=%q, want stop (body=%s)", fr, got)
	}
	if u := gjson.GetBytes(got, "usage.total_tokens").Int(); u != 4 {
		t.Fatalf("usage.total_tokens=%d, want 4 (body=%s)", u, got)
	}
}

func TestAggregateCodeBuddyCNChunksToolCalls(t *testing.T) {
	raw := []byte(`{"id":"cmb-2","model":"glm-5.2","object":"chat.completion.chunk","created":123,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":"}}]},"finish_reason":""}],"usage":null}` + "\n" +
		`{"id":"cmb-2","model":"glm-5.2","object":"chat.completion.chunk","created":123,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"SF\"}"}}]},"finish_reason":""}],"usage":null}` + "\n" +
		`{"id":"cmb-2","model":"glm-5.2","object":"chat.completion.chunk","created":123,"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":null}` + "\n")

	got := aggregateCodeBuddyCNChunks(raw)

	name := gjson.GetBytes(got, "choices.0.message.tool_calls.0.function.name").String()
	if name != "get_weather" {
		t.Fatalf("tool name=%q, want get_weather (body=%s)", name, got)
	}
	args := gjson.GetBytes(got, "choices.0.message.tool_calls.0.function.arguments").String()
	if args != `{"city":"SF"}` {
		t.Fatalf("tool args=%q, want merged JSON (body=%s)", args, got)
	}
	if fr := gjson.GetBytes(got, "choices.0.finish_reason").String(); fr != "tool_calls" {
		t.Fatalf("finish_reason=%q, want tool_calls (body=%s)", fr, got)
	}
}
