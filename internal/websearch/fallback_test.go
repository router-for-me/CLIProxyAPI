package websearch

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/tidwall/gjson"
)

func TestHasServerSearchToolPerFormat(t *testing.T) {
	cases := []struct {
		format  string
		payload string
		want    bool
	}{
		{"claude", `{"tools":[{"type":"web_search_20250305","name":"web_search"}]}`, true},
		{"claude", `{"tools":[{"type":"web_search_20990101","name":"web_search"}]}`, true},
		{"claude", `{"tools":[{"name":"lookup","input_schema":{}}]}`, false},
		{"claude", `{}`, false},
		{"openai-response", `{"tools":[{"type":"web_search"}]}`, true},
		{"openai-response", `{"tools":[{"type":"web_search_preview"}]}`, true},
		{"openai-response", `{"tools":[{"type":"function","name":"f"}]}`, false},
		{"codex", `{"tools":[{"type":"web_search"}]}`, true},
		{"openai", `{"tools":[{"type":"web_search"}]}`, true},
		{"openai", `{"tools":[{"type":"function","function":{"name":"web_search"}}]}`, false},
		{"gemini", `{"tools":[{"googleSearch":{}}]}`, true},
		{"gemini", `{"tools":[{"functionDeclarations":[]}]}`, false},
		{"interactions", `{"tools":[{"type":"web_search"}]}`, false},
	}
	for _, tc := range cases {
		if got := HasServerSearchTool(tc.format, []byte(tc.payload)); got != tc.want {
			t.Fatalf("%s %s = %v, want %v", tc.format, tc.payload, got, tc.want)
		}
	}
}

func TestRewriteClaudePreservesChoiceMapping(t *testing.T) {
	payload := []byte(`{
		"tools":[{"type":"web_search_20250305","name":"web_search"}],
		"tool_choice":{"type":"tool","name":"web_search"},
		"messages":[{"role":"user","content":"hi"}]
	}`)
	out, names := RewriteForFallback("claude", payload)
	if len(names) != 1 || names[0] != "web_search" {
		t.Fatalf("names = %v", names)
	}
	if got := gjson.GetBytes(out, "tools.0.type").String(); got != "" {
		t.Fatalf("rewritten tool keeps server type: %s", out)
	}
	if got := gjson.GetBytes(out, "tools.0.input_schema.required.0").String(); got != "query" {
		t.Fatalf("missing query schema: %s", out)
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != "web_search" {
		t.Fatalf("choice not remapped: %s", out)
	}
}

func TestRewriteClaudeAliasesCollidingFunction(t *testing.T) {
	payload := []byte(`{"tools":[
		{"type":"web_search_20250305","name":"web_search"},
		{"name":"web_search","input_schema":{"type":"object"}}
	]}`)
	out, names := RewriteForFallback("claude", payload)
	if len(names) != 1 || names[0] == "web_search" {
		t.Fatalf("names = %v, want alias", names)
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != names[0] {
		t.Fatalf("alias not applied: %s", out)
	}
	// Client function untouched.
	if got := gjson.GetBytes(out, "tools.1.input_schema.type").String(); got != "object" {
		t.Fatalf("client tool modified: %s", out)
	}
}

func TestRewriteResponsesChoice(t *testing.T) {
	payload := []byte(`{"tools":[{"type":"web_search"}],"tool_choice":{"type":"web_search"}}`)
	out, names := RewriteForFallback("openai-response", payload)
	if len(names) != 1 {
		t.Fatalf("names = %v", names)
	}
	if got := gjson.GetBytes(out, "tools.0.type").String(); got != "function" {
		t.Fatalf("tool type = %s", out)
	}
	choice := gjson.GetBytes(out, "tool_choice")
	if choice.Get("type").String() != "function" || choice.Get("name").String() != names[0] {
		t.Fatalf("choice = %s", out)
	}
}

func TestRewriteGeminiMovesGoogleSearchToFunction(t *testing.T) {
	payload := []byte(`{"tools":[{"googleSearch":{}}],"contents":[]}`)
	out, names := RewriteForFallback("gemini", payload)
	if len(names) != 1 {
		t.Fatalf("names = %v", names)
	}
	if gjson.GetBytes(out, "tools.0.googleSearch").Exists() {
		t.Fatalf("googleSearch not removed: %s", out)
	}
	if got := gjson.GetBytes(out, "tools.0.functionDeclarations.0.name").String(); got != names[0] {
		t.Fatalf("declaration missing: %s", out)
	}
}

func TestNativeSearchSupportedMatrix(t *testing.T) {
	claudeSearch := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[]}`)
	responsesSearch := []byte(`{"tools":[{"type":"web_search"}]}`)
	for _, provider := range []string{"claude", "codex", "xai", "CODEX"} {
		if !NativeSearchSupported("claude", []string{provider}, "any-model", claudeSearch) {
			t.Fatalf("%s should be native", provider)
		}
	}
	for _, provider := range []string{"kimi", "opencode", "zai", "devin", "meta", "aistudio", "vertex", "openai-compatible-foo", "unknown"} {
		if NativeSearchSupported("claude", []string{provider}, "any-model", claudeSearch) {
			t.Fatalf("%s should need fallback", provider)
		}
	}
	if NativeSearchSupported("openai-response", []string{"openai-compatible-x"}, "m", responsesSearch) {
		t.Fatal("openai-compat responses route should need fallback")
	}
	// Unknown provider mixed with a native one stays native.
	if !NativeSearchSupported("claude", []string{"mystery", "xai"}, "m", claudeSearch) {
		t.Fatal("mixed providers should stay native when one supports it")
	}
}

func TestNativeSearchAntigravityRequiresCapableSearchOnly(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-websearch-ag", "antigravity", []*registry.ModelInfo{
		{ID: "ag-capable", SupportsWebSearch: true},
		{ID: "ag-plain"},
	})
	t.Cleanup(func() { reg.UnregisterClient("test-websearch-ag") })

	searchOnly := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"}]}`)
	mixed := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"},{"name":"f","input_schema":{}}]}`)
	choiceNone := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"}],"tool_choice":{"type":"none"}}`)

	if !NativeSearchSupported("claude", []string{"antigravity"}, "ag-capable", searchOnly) {
		t.Fatal("capable search-only antigravity should be native")
	}
	if NativeSearchSupported("claude", []string{"antigravity"}, "ag-capable", mixed) {
		t.Fatal("mixed antigravity should need fallback")
	}
	if NativeSearchSupported("claude", []string{"antigravity"}, "ag-capable", choiceNone) {
		t.Fatal("choice=none antigravity should need fallback")
	}
	if NativeSearchSupported("claude", []string{"antigravity"}, "ag-plain", searchOnly) {
		t.Fatal("incapable antigravity should need fallback")
	}
}

func TestNativeSearchGeminiRequiresRegistryCapability(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-websearch-gem", "gemini", []*registry.ModelInfo{
		{ID: "gem-capable", SupportsWebSearch: true},
		{ID: "gem-plain"},
	})
	t.Cleanup(func() { reg.UnregisterClient("test-websearch-gem") })

	responsesSearch := []byte(`{"tools":[{"type":"web_search"}]}`)
	if !NativeSearchSupported("openai-response", []string{"gemini"}, "gem-capable", responsesSearch) {
		t.Fatal("capable gemini responses route should be native")
	}
	if NativeSearchSupported("openai-response", []string{"gemini"}, "gem-plain", responsesSearch) {
		t.Fatal("incapable gemini responses route should need fallback")
	}
}

func searchStubConfig() Config {
	cfg := Config{Enabled: true, Exclude: []string{"brave", "tavily", "exa", "searxng"}}.WithDefaults()
	return cfg.WithDoer(stubDoer{handle: func(*http.Request) (*http.Response, error) {
		page := `<html><body><div class="result"><a class="result__a" href="https://example.com/a">Title A</a>` +
			`<a class="result__snippet" href="https://example.com/a">snippet a</a></div></body></html>`
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(page))}, nil
	}})
}

func TestShouldFallbackRequiresChainAvailability(t *testing.T) {
	payload := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"}]}`)
	cfg := Config{Enabled: true, Exclude: DefaultProviderOrder}.WithDefaults()
	if ShouldFallback(cfg, "claude", []string{"kimi"}, "m", payload, false) {
		t.Fatal("fallback without providers should be false")
	}
	if !ShouldFallback(searchStubConfig(), "claude", []string{"kimi"}, "m", payload, false) {
		t.Fatal("fallback with duckduckgo should be true")
	}
	if ShouldFallback(searchStubConfig(), "claude", []string{"codex"}, "m", payload, false) {
		t.Fatal("native codex route should not fall back")
	}
	if ShouldFallback(searchStubConfig(), "interactions", []string{"kimi"}, "m", payload, false) {
		t.Fatal("interactions has no payload support")
	}
	disabled := searchStubConfig()
	disabled.Enabled = false
	if ShouldFallback(disabled, "claude", []string{"kimi"}, "m", payload, false) {
		t.Fatal("disabled config should not fall back")
	}
}

func TestRunClaudeLoopExecutesSearchAndAnswers(t *testing.T) {
	cfg := searchStubConfig()
	payload := []byte(`{
		"model":"m",
		"tools":[{"type":"web_search_20250305","name":"web_search"}],
		"messages":[{"role":"user","content":"search go"}]
	}`)
	calls := 0
	exec := func(_ context.Context, request []byte) ([]byte, error) {
		calls++
		if calls == 1 {
			if got := gjson.GetBytes(request, "tools.0.input_schema.required.0").String(); got != "query" {
				t.Fatalf("first request not rewritten: %s", request)
			}
			return []byte(`{"id":"msg_1","type":"message","role":"assistant","model":"m",
				"content":[{"type":"tool_use","id":"toolu_1","name":"web_search","input":{"query":"golang"}}],
				"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5}}`), nil
		}
		results := gjson.GetBytes(request, "messages.2.content.0.content").String()
		if !strings.Contains(results, "example.com") {
			t.Fatalf("search results not appended: %s", request)
		}
		return []byte(`{"id":"msg_2","type":"message","role":"assistant","model":"m",
			"content":[{"type":"text","text":"Go is great."}],
			"stop_reason":"end_turn","usage":{"input_tokens":50,"output_tokens":8}}`), nil
	}
	body, errRun := Run(context.Background(), cfg, "claude", payload, exec)
	if errRun != nil {
		t.Fatalf("Run error = %v", errRun)
	}
	if got := gjson.GetBytes(body, "content.0.text").String(); got != "Go is great." {
		t.Fatalf("final body = %s", body)
	}
	// Usage accumulates across loop iterations.
	if got := gjson.GetBytes(body, "usage.input_tokens").Int(); got != 60 {
		t.Fatalf("input tokens = %d: %s", got, body)
	}
	if got := gjson.GetBytes(body, "usage.output_tokens").Int(); got != 13 {
		t.Fatalf("output tokens = %d: %s", got, body)
	}
	if calls != 2 {
		t.Fatalf("exec calls = %d", calls)
	}
}

func TestRunStripsToolsWhenBudgetExhausted(t *testing.T) {
	cfg := searchStubConfig()
	cfg.MaxSearches = 1
	payload := []byte(`{
		"model":"m",
		"tools":[{"type":"web_search"}],"messages":[{"role":"user","content":"hi"}]
	}`)
	// Note: untyped tool is not a server tool; use a typed one.
	payload = []byte(`{
		"model":"m",
		"tools":[{"type":"web_search_20250305","name":"web_search"}],
		"messages":[{"role":"user","content":"hi"}]
	}`)
	calls := 0
	exec := func(_ context.Context, request []byte) ([]byte, error) {
		calls++
		if calls <= 2 {
			return []byte(`{"id":"msg_1","content":[{"type":"tool_use","id":"toolu_1","name":"web_search","input":{"query":"q"}}],
				"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`), nil
		}
		if gjson.GetBytes(request, "tools.#").Int() != 0 {
			t.Fatalf("tools not stripped on final call: %s", request)
		}
		return []byte(`{"id":"msg_2","content":[{"type":"text","text":"done"}],
			"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`), nil
	}
	body, errRun := Run(context.Background(), cfg, "claude", payload, exec)
	if errRun != nil {
		t.Fatalf("Run error = %v", errRun)
	}
	if got := gjson.GetBytes(body, "content.0.text").String(); got != "done" {
		t.Fatalf("final body = %s", body)
	}
	if calls != 3 {
		t.Fatalf("exec calls = %d, want 3", calls)
	}
}

func TestRunPassesThroughClientToolCalls(t *testing.T) {
	cfg := searchStubConfig()
	payload := []byte(`{
		"model":"m",
		"tools":[{"type":"web_search_20250305","name":"web_search"},{"name":"Read","input_schema":{"type":"object"}}],
		"messages":[{"role":"user","content":"hi"}]
	}`)
	exec := func(_ context.Context, _ []byte) ([]byte, error) {
		return []byte(`{"id":"msg_1","content":[{"type":"tool_use","id":"toolu_9","name":"Read","input":{"path":"x"}}],
			"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`), nil
	}
	body, errRun := Run(context.Background(), cfg, "claude", payload, exec)
	if errRun != nil {
		t.Fatalf("Run error = %v", errRun)
	}
	if got := gjson.GetBytes(body, "content.0.name").String(); got != "Read" {
		t.Fatalf("client call not passed through: %s", body)
	}
}

func TestRunChatLoop(t *testing.T) {
	cfg := searchStubConfig()
	payload := []byte(`{"model":"m","tools":[{"type":"web_search"}],"messages":[{"role":"user","content":"hi"}]}`)
	calls := 0
	exec := func(_ context.Context, request []byte) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte(`{"id":"c1","object":"chat.completion","created":1,"model":"m",
				"choices":[{"index":0,"message":{"role":"assistant","content":null,
				"tool_calls":[{"id":"call_1","type":"function","function":{"name":"web_search","arguments":"{\"query\":\"go\"}"}}]},
				"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":5,"total_tokens":10}}`), nil
		}
		if got := gjson.GetBytes(request, "messages.2.role").String(); got != "tool" {
			t.Fatalf("tool result not appended: %s", request)
		}
		return []byte(`{"id":"c2","object":"chat.completion","created":1,"model":"m",
			"choices":[{"index":0,"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":20,"completion_tokens":4,"total_tokens":24}}`), nil
	}
	body, errRun := Run(context.Background(), cfg, "openai", payload, exec)
	if errRun != nil {
		t.Fatalf("Run error = %v", errRun)
	}
	if got := gjson.GetBytes(body, "choices.0.message.content").String(); got != "answer" {
		t.Fatalf("final body = %s", body)
	}
	if got := gjson.GetBytes(body, "usage.total_tokens").Int(); got != 34 {
		t.Fatalf("total tokens = %d: %s", got, body)
	}
}

func TestRunResponsesLoop(t *testing.T) {
	cfg := searchStubConfig()
	payload := []byte(`{"model":"m","tools":[{"type":"web_search"}],"input":[{"role":"user","content":"hi"}]}`)
	calls := 0
	exec := func(_ context.Context, request []byte) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte(`{"id":"resp_1","object":"response","status":"completed","model":"m",
				"output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"web_search","arguments":"{\"query\":\"go\"}"}],
				"usage":{"input_tokens":5,"output_tokens":5,"total_tokens":10}}`), nil
		}
		types := gjson.GetBytes(request, "input.#.type").String()
		if !strings.Contains(types, "function_call_output") {
			t.Fatalf("call output not appended: %s", request)
		}
		return []byte(`{"id":"resp_2","object":"response","status":"completed","model":"m",
			"output":[{"type":"message","id":"msg_1","status":"completed","role":"assistant",
			"content":[{"type":"output_text","text":"answer","annotations":[]}]}],
			"usage":{"input_tokens":20,"output_tokens":4,"total_tokens":24}}`), nil
	}
	body, errRun := Run(context.Background(), cfg, "openai-response", payload, exec)
	if errRun != nil {
		t.Fatalf("Run error = %v", errRun)
	}
	if got := gjson.GetBytes(body, "output.0.content.0.text").String(); got != "answer" {
		t.Fatalf("final body = %s", body)
	}
}

func TestRunGeminiLoop(t *testing.T) {
	cfg := searchStubConfig()
	payload := []byte(`{"tools":[{"googleSearch":{}}],"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	calls := 0
	exec := func(_ context.Context, request []byte) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"web_search","args":{"query":"go"}}}]},
				"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":5,"totalTokenCount":10}}`), nil
		}
		parts := gjson.GetBytes(request, "contents.2.parts.0.functionResponse.name").String()
		if parts != "web_search" {
			t.Fatalf("function response not appended: %s", request)
		}
		return []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"answer"}]},
			"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":4,"totalTokenCount":24}}`), nil
	}
	body, errRun := Run(context.Background(), cfg, "gemini", payload, exec)
	if errRun != nil {
		t.Fatalf("Run error = %v", errRun)
	}
	if got := gjson.GetBytes(body, "candidates.0.content.parts.0.text").String(); got != "answer" {
		t.Fatalf("final body = %s", body)
	}
}

func TestSynthesizeClaudeStreamRoundTrip(t *testing.T) {
	body := []byte(`{"id":"msg_1","type":"message","role":"assistant","model":"m",
		"content":[{"type":"text","text":"Hello"},{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"x"}}],
		"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5}}`)
	chunks, errSynth := SynthesizeStream("claude", "m", body)
	if errSynth != nil {
		t.Fatalf("SynthesizeStream error = %v", errSynth)
	}
	joined := strings.Join(byteSlicesToStrings(chunks), "\n")
	for _, want := range []string{
		"event: message_start", `"id":"msg_1"`,
		"event: content_block_start", `"type":"text"`, `"text_delta"`, "Hello",
		`"type":"tool_use"`, `"input_json_delta"`, `partial_json`,
		"event: message_delta", `"stop_reason":"tool_use"`,
		"event: message_stop",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in:\n%s", want, joined)
		}
	}
	for _, chunk := range chunks {
		if !strings.HasSuffix(string(chunk), "\n\n") {
			t.Fatalf("chunk missing SSE framing: %q", chunk)
		}
	}
}

func TestSynthesizeChatStream(t *testing.T) {
	body := []byte(`{"id":"c1","object":"chat.completion","created":7,"model":"m",
		"choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}]}`)
	chunks, errSynth := SynthesizeStream("openai", "m", body)
	if errSynth != nil {
		t.Fatalf("SynthesizeStream error = %v", errSynth)
	}
	if len(chunks) != 3 {
		t.Fatalf("chunks = %d, want role+content+terminal", len(chunks))
	}
	if got := gjson.GetBytes(chunks[0], "choices.0.delta.role").String(); got != "assistant" {
		t.Fatalf("first chunk = %s", chunks[0])
	}
	if got := gjson.GetBytes(chunks[1], "choices.0.delta.content").String(); got != "Hi" {
		t.Fatalf("second chunk = %s", chunks[1])
	}
	if got := gjson.GetBytes(chunks[2], "choices.0.finish_reason").String(); got != "stop" {
		t.Fatalf("terminal chunk = %s", chunks[2])
	}
}

func TestSynthesizeResponsesStream(t *testing.T) {
	body := []byte(`{"id":"resp_1","object":"response","status":"completed","model":"m",
		"output":[{"type":"message","id":"msg_1","status":"completed","role":"assistant",
		"content":[{"type":"output_text","text":"Hi","annotations":[]}]}],
		"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`)
	chunks, errSynth := SynthesizeStream("openai-response", "m", body)
	if errSynth != nil {
		t.Fatalf("SynthesizeStream error = %v", errSynth)
	}
	joined := strings.Join(byteSlicesToStrings(chunks), "\n")
	for _, want := range []string{
		"event: response.created", "event: response.in_progress",
		"event: response.output_item.added", "event: response.content_part.added",
		"event: response.output_text.delta", `"delta":"Hi"`,
		"event: response.output_text.done", "event: response.content_part.done",
		"event: response.output_item.done", "event: response.completed",
		`"status":"completed"`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in:\n%s", want, joined)
		}
	}
	// Sequence numbers strictly increase.
	last := -1
	for _, chunk := range chunks {
		text := string(chunk)
		index := strings.Index(text, `"sequence_number":`)
		if index < 0 {
			continue
		}
		rest := text[index+len(`"sequence_number":`):]
		end := strings.IndexAny(rest, ",}")
		seq, errParse := strconv.Atoi(strings.TrimSpace(rest[:end]))
		if errParse != nil {
			t.Fatalf("parse seq: %v", errParse)
		}
		if seq <= last {
			t.Fatalf("sequence not increasing: %d after %d", seq, last)
		}
		last = seq
	}
}

func TestSynthesizeGeminiPassesThrough(t *testing.T) {
	body := []byte(`{"candidates":[{"content":{"parts":[{"text":"Hi"}]},"finishReason":"STOP"}]}`)
	chunks, errSynth := SynthesizeStream("gemini", "m", body)
	if errSynth != nil {
		t.Fatalf("SynthesizeStream error = %v", errSynth)
	}
	if len(chunks) != 1 || string(chunks[0]) != string(body) {
		t.Fatalf("chunks = %v", chunks)
	}
}

func TestSynthesizeRejectsInvalid(t *testing.T) {
	if _, errSynth := SynthesizeStream("claude", "m", []byte(`{bad`)); errSynth == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if _, errSynth := SynthesizeStream("interactions", "m", []byte(`{}`)); errSynth == nil {
		t.Fatal("expected error for unsupported format")
	}
}

func byteSlicesToStrings(chunks [][]byte) []string {
	out := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		out = append(out, string(chunk))
	}
	return out
}
