package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestNormalizeKimiToolMessageLinks_UsesCallIDFallback(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"assistant","tool_calls":[{"id":"list_directory:1","type":"function","function":{"name":"list_directory","arguments":"{}"}}]},
			{"role":"tool","call_id":"list_directory:1","content":"[]"}
		]
	}`)

	out, err := normalizeKimiToolMessageLinks(body)
	if err != nil {
		t.Fatalf("normalizeKimiToolMessageLinks() error = %v", err)
	}

	got := gjson.GetBytes(out, "messages.1.tool_call_id").String()
	if got != "list_directory:1" {
		t.Fatalf("messages.1.tool_call_id = %q, want %q", got, "list_directory:1")
	}
}

func TestNormalizeKimiToolMessageLinks_InferSinglePendingID(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"assistant","tool_calls":[{"id":"call_123","type":"function","function":{"name":"read_file","arguments":"{}"}}]},
			{"role":"tool","content":"file-content"}
		]
	}`)

	out, err := normalizeKimiToolMessageLinks(body)
	if err != nil {
		t.Fatalf("normalizeKimiToolMessageLinks() error = %v", err)
	}

	got := gjson.GetBytes(out, "messages.1.tool_call_id").String()
	if got != "call_123" {
		t.Fatalf("messages.1.tool_call_id = %q, want %q", got, "call_123")
	}
}

func TestSanitizeKimiOpenAICompatibleRequestBodyDropsOrphanReplyToolCall(t *testing.T) {
	body := []byte(`{
		"model":"kimi-k2.5",
		"messages":[
			{"role":"user","content":"start"},
			{"role":"assistant","content":"reply pending","tool_calls":[{"id":"reply:0","type":"function","function":{"name":"reply","arguments":"{}"}}]},
			{"role":"user","content":"continue"}
		]
	}`)

	out, err := sanitizeKimiOpenAICompatibleRequestBody(body)
	if err != nil {
		t.Fatalf("sanitizeKimiOpenAICompatibleRequestBody() error = %v", err)
	}

	if gjson.GetBytes(out, "messages.1.tool_calls").Exists() {
		t.Fatalf("orphan reply tool_call should be removed: %s", string(out))
	}
	if got := gjson.GetBytes(out, "messages.1.content").String(); got != "reply pending" {
		t.Fatalf("assistant content = %q, want preserved text: %s", got, string(out))
	}
}

func TestSanitizeKimiOpenAICompatibleRequestBodyRemovesMoonshotAnyOfParentSchema(t *testing.T) {
	body := []byte(`{
		"model":"kimi-k2.6",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{
			"name":"run_tool",
			"input_schema":{
				"type":"object",
				"properties":{
					"arguments":{
						"type":"object",
						"properties":{"command":{"type":"string"}},
						"required":["command"],
						"additionalProperties":false,
						"anyOf":[
							{"type":"object","properties":{"command":{"type":"string"}}},
							{"type":"string"}
						]
					}
				}
			},
			"strict":true
		}]
	}`)

	out, err := sanitizeKimiOpenAICompatibleRequestBody(body)
	if err != nil {
		t.Fatalf("sanitizeKimiOpenAICompatibleRequestBody() error = %v", err)
	}

	argumentsPath := "tools.0.function.parameters.properties.arguments"
	if got := gjson.GetBytes(out, "tools.0.type").String(); got != "function" {
		t.Fatalf("tool type = %q, want function: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.function.strict").Bool(); got {
		t.Fatalf("kimi strict should be disabled: %s", string(out))
	}
	if !gjson.GetBytes(out, argumentsPath+".anyOf").Exists() {
		t.Fatalf("arguments anyOf should be preserved: %s", string(out))
	}
	if gjson.GetBytes(out, argumentsPath+".properties").Exists() {
		t.Fatalf("arguments parent properties should be removed: %s", string(out))
	}
	if got := gjson.GetBytes(out, argumentsPath+".anyOf.0.properties.command.type").String(); got != "string" {
		t.Fatalf("anyOf command type = %q, want string: %s", got, string(out))
	}
}

func TestKimiExecutorHttpRequestSanitizesDirectChatBody(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		gotBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("Authorization = %q, want Bearer test-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"kimi-k2.5","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	payload := `{
		"model":"kimi-k2.5",
		"messages":[
			{"role":"assistant","content":"reply pending","tool_calls":[{"id":"reply:0","type":"function","function":{"name":"reply","arguments":"{}"}}]},
			{"role":"user","content":"continue"}
		]
	}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	executor := NewKimiExecutor(nil)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test-key"}}

	resp, err := executor.HttpRequest(context.Background(), auth, req)
	if err != nil {
		t.Fatalf("HttpRequest error: %v", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			t.Fatalf("close response body: %v", errClose)
		}
	}()

	if gjson.GetBytes(gotBody, "messages.0.tool_calls").Exists() {
		t.Fatalf("direct Kimi HttpRequest should remove orphan tool_calls: %s", string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "messages.0.content").String(); got != "reply pending" {
		t.Fatalf("assistant content = %q, want preserved text: %s", got, string(gotBody))
	}
}

func TestKimiExecutorExecuteClaudeSourceUsesChatCompletionsEndpoint(t *testing.T) {
	var gotPath string
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-test",
			"object":"chat.completion",
			"created":1,
			"model":"kimi-k2.6",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}
		}`))
	}))
	defer server.Close()

	executor := NewKimiExecutor(nil)
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL + "/coding/v1",
		},
	}
	body := []byte(`{
		"model":"kimi-k2.6",
		"messages":[{"role":"user","content":"hello"}]
	}`)
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "kimi-k2.6",
		Payload: body,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("claude"),
		OriginalRequest: body,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotPath != "/coding/v1/chat/completions" {
		t.Fatalf("upstream path = %q, want %q", gotPath, "/coding/v1/chat/completions")
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("Authorization = %q, want %q", gotAuth, "Bearer test-key")
	}
	if len(resp.Payload) == 0 {
		t.Fatal("expected translated response payload")
	}
}

func TestKimiExecutorExecuteStreamClaudeSourceUsesChatCompletionsEndpoint(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		lines := []string{
			`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,"model":"kimi-k2.6","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
			`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,"model":"kimi-k2.6","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`,
			`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,"model":"kimi-k2.6","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`,
			`data: [DONE]`,
		}
		for _, line := range lines {
			_, _ = io.WriteString(w, line+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer server.Close()

	executor := NewKimiExecutor(nil)
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL + "/coding/v1",
		},
	}
	body := []byte(`{
		"model":"kimi-k2.6",
		"messages":[{"role":"user","content":"hello"}]
	}`)
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "kimi-k2.6",
		Payload: body,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("claude"),
		OriginalRequest: body,
		Stream:          true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	var combined strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		combined.Write(chunk.Payload)
	}
	if gotPath != "/coding/v1/chat/completions" {
		t.Fatalf("upstream path = %q, want %q", gotPath, "/coding/v1/chat/completions")
	}
	if !strings.Contains(combined.String(), `"type":"message"`) && !strings.Contains(combined.String(), "message_stop") {
		t.Fatalf("expected translated stream output, got %q", combined.String())
	}
	if strings.Contains(combined.String(), "missing message_start") {
		t.Fatalf("unexpected missing_message_start failure in translated output: %q", combined.String())
	}
}

func TestNormalizeKimiToolMessageLinks_AmbiguousMissingIDIsNotInferred(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"assistant","tool_calls":[
				{"id":"call_1","type":"function","function":{"name":"list_directory","arguments":"{}"}},
				{"id":"call_2","type":"function","function":{"name":"read_file","arguments":"{}"}}
			]},
			{"role":"tool","content":"result-without-id"}
		]
	}`)

	out, err := normalizeKimiToolMessageLinks(body)
	if err != nil {
		t.Fatalf("normalizeKimiToolMessageLinks() error = %v", err)
	}

	if gjson.GetBytes(out, "messages.1.tool_call_id").Exists() {
		t.Fatalf("messages.1.tool_call_id should be absent for ambiguous case, got %q", gjson.GetBytes(out, "messages.1.tool_call_id").String())
	}
}

func TestNormalizeKimiToolMessageLinks_PreservesExistingToolCallID(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"list_directory","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","call_id":"different-id","content":"result"}
		]
	}`)

	out, err := normalizeKimiToolMessageLinks(body)
	if err != nil {
		t.Fatalf("normalizeKimiToolMessageLinks() error = %v", err)
	}

	got := gjson.GetBytes(out, "messages.1.tool_call_id").String()
	if got != "call_1" {
		t.Fatalf("messages.1.tool_call_id = %q, want %q", got, "call_1")
	}
}

func TestNormalizeKimiToolMessageLinks_InheritsPreviousReasoningForAssistantToolCalls(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"assistant","content":"plan","reasoning_content":"previous reasoning"},
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"list_directory","arguments":"{}"}}]}
		]
	}`)

	out, err := normalizeKimiToolMessageLinks(body)
	if err != nil {
		t.Fatalf("normalizeKimiToolMessageLinks() error = %v", err)
	}

	got := gjson.GetBytes(out, "messages.1.reasoning_content").String()
	if got != "previous reasoning" {
		t.Fatalf("messages.1.reasoning_content = %q, want %q", got, "previous reasoning")
	}
}

func TestNormalizeKimiToolMessageLinks_InsertsFallbackReasoningWhenMissing(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"list_directory","arguments":"{}"}}]}
		]
	}`)

	out, err := normalizeKimiToolMessageLinks(body)
	if err != nil {
		t.Fatalf("normalizeKimiToolMessageLinks() error = %v", err)
	}

	reasoning := gjson.GetBytes(out, "messages.0.reasoning_content")
	if !reasoning.Exists() {
		t.Fatalf("messages.0.reasoning_content should exist")
	}
	if reasoning.String() != "[reasoning unavailable]" {
		t.Fatalf("messages.0.reasoning_content = %q, want %q", reasoning.String(), "[reasoning unavailable]")
	}
}

func TestNormalizeKimiToolMessageLinks_UsesContentAsReasoningFallback(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"assistant","content":[{"type":"text","text":"first line"},{"type":"text","text":"second line"}],"tool_calls":[{"id":"call_1","type":"function","function":{"name":"list_directory","arguments":"{}"}}]}
		]
	}`)

	out, err := normalizeKimiToolMessageLinks(body)
	if err != nil {
		t.Fatalf("normalizeKimiToolMessageLinks() error = %v", err)
	}

	got := gjson.GetBytes(out, "messages.0.reasoning_content").String()
	if got != "first line\nsecond line" {
		t.Fatalf("messages.0.reasoning_content = %q, want %q", got, "first line\nsecond line")
	}
}

func TestNormalizeKimiToolMessageLinks_ReplacesEmptyReasoningContent(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"assistant","content":"assistant summary","tool_calls":[{"id":"call_1","type":"function","function":{"name":"list_directory","arguments":"{}"}}],"reasoning_content":""}
		]
	}`)

	out, err := normalizeKimiToolMessageLinks(body)
	if err != nil {
		t.Fatalf("normalizeKimiToolMessageLinks() error = %v", err)
	}

	got := gjson.GetBytes(out, "messages.0.reasoning_content").String()
	if got != "assistant summary" {
		t.Fatalf("messages.0.reasoning_content = %q, want %q", got, "assistant summary")
	}
}

func TestNormalizeKimiToolMessageLinks_PreservesExistingAssistantReasoning(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"list_directory","arguments":"{}"}}],"reasoning_content":"keep me"}
		]
	}`)

	out, err := normalizeKimiToolMessageLinks(body)
	if err != nil {
		t.Fatalf("normalizeKimiToolMessageLinks() error = %v", err)
	}

	got := gjson.GetBytes(out, "messages.0.reasoning_content").String()
	if got != "keep me" {
		t.Fatalf("messages.0.reasoning_content = %q, want %q", got, "keep me")
	}
}

func TestNormalizeKimiToolMessageLinks_RepairsIDsAndReasoningTogether(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"list_directory","arguments":"{}"}}],"reasoning_content":"r1"},
			{"role":"tool","call_id":"call_1","content":"[]"},
			{"role":"assistant","tool_calls":[{"id":"call_2","type":"function","function":{"name":"read_file","arguments":"{}"}}]},
			{"role":"tool","call_id":"call_2","content":"file"}
		]
	}`)

	out, err := normalizeKimiToolMessageLinks(body)
	if err != nil {
		t.Fatalf("normalizeKimiToolMessageLinks() error = %v", err)
	}

	if got := gjson.GetBytes(out, "messages.1.tool_call_id").String(); got != "call_1" {
		t.Fatalf("messages.1.tool_call_id = %q, want %q", got, "call_1")
	}
	if got := gjson.GetBytes(out, "messages.3.tool_call_id").String(); got != "call_2" {
		t.Fatalf("messages.3.tool_call_id = %q, want %q", got, "call_2")
	}
	if got := gjson.GetBytes(out, "messages.2.reasoning_content").String(); got != "r1" {
		t.Fatalf("messages.2.reasoning_content = %q, want %q", got, "r1")
	}
}

func TestDropUnansweredClaudeToolUses_RemovesMissingResults(t *testing.T) {
	body := []byte(`{
		"model":"kimi-k2.6",
		"messages":[
			{"role":"user","content":[{"type":"text","text":"start"}]},
			{"role":"assistant","content":[
				{"type":"text","text":"reading files"},
				{"type":"tool_use","id":"read_file:1","name":"read_file","input":{"path":"a.go"}},
				{"type":"tool_use","id":"read_file:2","name":"read_file","input":{"path":"b.go"}},
				{"type":"tool_use","id":"read_file:3","name":"read_file","input":{"path":"c.go"}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"read_file:1","content":"a"},
				{"type":"tool_result","tool_use_id":"read_file:2","content":"b"},
				{"type":"text","text":"continue"}
			]}
		]
	}`)

	out, removed, err := dropUnansweredClaudeToolUses(body)
	if err != nil {
		t.Fatalf("dropUnansweredClaudeToolUses() error = %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}

	content := gjson.GetBytes(out, "messages.1.content")
	if len(content.Array()) != 3 {
		t.Fatalf("assistant content length = %d, want 3: %s", len(content.Array()), content.Raw)
	}
	if gjson.GetBytes(out, `messages.1.content.#(id=="read_file:3")`).Exists() {
		t.Fatalf("unanswered tool_use read_file:3 should be removed: %s", content.Raw)
	}
	if !gjson.GetBytes(out, `messages.1.content.#(id=="read_file:1")`).Exists() {
		t.Fatalf("answered tool_use read_file:1 should be kept: %s", content.Raw)
	}
	if !gjson.GetBytes(out, `messages.1.content.#(id=="read_file:2")`).Exists() {
		t.Fatalf("answered tool_use read_file:2 should be kept: %s", content.Raw)
	}
}

func TestDropUnansweredClaudeToolUses_DropsToolOnlyAssistantMessage(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"read_file:3","name":"read_file","input":{}}]},
			{"role":"user","content":[{"type":"text","text":"no tool result"}]}
		]
	}`)

	out, removed, err := dropUnansweredClaudeToolUses(body)
	if err != nil {
		t.Fatalf("dropUnansweredClaudeToolUses() error = %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}

	msgs := gjson.GetBytes(out, "messages").Array()
	if len(msgs) != 1 {
		t.Fatalf("messages length = %d, want 1: %s", len(msgs), gjson.GetBytes(out, "messages").Raw)
	}
	if got := msgs[0].Get("role").String(); got != "user" {
		t.Fatalf("remaining role = %q, want user", got)
	}
}

func TestRepairClaudeToolUseHistory_CoalescesSplitToolResults(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"assistant","content":[
				{"type":"tool_use","id":"call_1","name":"read_file","input":{}},
				{"type":"tool_use","id":"call_2","name":"search_file_content","input":{}}
			]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"a"}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_2","content":"b"}]}
		]
	}`)

	out, err := repairClaudeToolUseHistory(body, "test")
	if err != nil {
		t.Fatalf("repairClaudeToolUseHistory() error = %v", err)
	}

	msgs := gjson.GetBytes(out, "messages").Array()
	if len(msgs) != 2 {
		t.Fatalf("messages length = %d, want 2: %s", len(msgs), gjson.GetBytes(out, "messages").Raw)
	}
	if got := len(msgs[0].Get("content").Array()); got != 2 {
		t.Fatalf("assistant content length = %d, want 2: %s", got, msgs[0].Raw)
	}
	if got := len(msgs[1].Get("content").Array()); got != 2 {
		t.Fatalf("tool result content length = %d, want 2: %s", got, msgs[1].Raw)
	}
	if !gjson.GetBytes(out, `messages.0.content.#(id=="call_2")`).Exists() {
		t.Fatalf("call_2 tool_use should be preserved: %s", out)
	}
	if !gjson.GetBytes(out, `messages.1.content.#(tool_use_id=="call_2")`).Exists() {
		t.Fatalf("call_2 tool_result should be preserved: %s", out)
	}
}

func TestRepairClaudeToolUseHistory_DropsOrphanToolResults(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"read_file","input":{}}]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"call_1","content":"a"},
				{"type":"tool_result","tool_use_id":"call_missing","content":"orphan"}
			]}
		]
	}`)

	out, err := repairClaudeToolUseHistory(body, "test")
	if err != nil {
		t.Fatalf("repairClaudeToolUseHistory() error = %v", err)
	}

	if gjson.GetBytes(out, `messages.1.content.#(tool_use_id=="call_missing")`).Exists() {
		t.Fatalf("orphan tool_result should be removed: %s", out)
	}
	if !gjson.GetBytes(out, `messages.1.content.#(tool_use_id=="call_1")`).Exists() {
		t.Fatalf("matched tool_result should be preserved: %s", out)
	}
}

func TestRepairClaudeToolUseHistory_DropsDelayedToolResults(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"read_file","input":{}}]},
			{"role":"user","content":[{"type":"text","text":"not a tool result"}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"late"}]}
		]
	}`)

	out, err := repairClaudeToolUseHistory(body, "test")
	if err != nil {
		t.Fatalf("repairClaudeToolUseHistory() error = %v", err)
	}

	if strings.Contains(string(out), `"tool_use_id":"call_1"`) {
		t.Fatalf("delayed tool_result should be removed: %s", out)
	}
	if strings.Contains(string(out), `"id":"call_1"`) {
		t.Fatalf("unanswered tool_use should be removed: %s", out)
	}
}

func TestRepairClaudeToolUseHistory_DeduplicatesToolResultsKeepingLatest(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"read_file","input":{}}]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"call_1","content":"old"},
				{"type":"tool_result","tool_use_id":"call_1","content":"latest"}
			]}
		]
	}`)

	out, stats, err := repairClaudeToolUseHistoryWithStats(body)
	if err != nil {
		t.Fatalf("repairClaudeToolUseHistoryWithStats() error = %v", err)
	}
	if stats.dedupedToolResults != 1 {
		t.Fatalf("dedupedToolResults = %d, want 1", stats.dedupedToolResults)
	}

	results := gjson.GetBytes(out, "messages.1.content").Array()
	if len(results) != 1 {
		t.Fatalf("tool_result count = %d, want 1: %s", len(results), gjson.GetBytes(out, "messages.1.content").Raw)
	}
	if got := results[0].Get("content").String(); got != "latest" {
		t.Fatalf("kept tool_result content = %q, want latest: %s", got, out)
	}
}

func TestRepairClaudeToolUseHistory_ReordersToolResultsBeforeUserText(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"assistant","content":[
				{"type":"tool_use","id":"call_1","name":"read_file","input":{}},
				{"type":"tool_use","id":"call_2","name":"grep","input":{}}
			]},
			{"role":"user","content":[
				{"type":"text","text":"new instruction"},
				{"type":"tool_result","tool_use_id":"call_2","content":"grep ok"},
				{"type":"tool_result","tool_use_id":"call_1","content":"read ok"}
			]}
		]
	}`)

	out, stats, err := repairClaudeToolUseHistoryWithStats(body)
	if err != nil {
		t.Fatalf("repairClaudeToolUseHistoryWithStats() error = %v", err)
	}
	if stats.reorderedToolResults != 1 {
		t.Fatalf("reorderedToolResults = %d, want 1", stats.reorderedToolResults)
	}

	content := gjson.GetBytes(out, "messages.1.content").Array()
	if len(content) != 3 {
		t.Fatalf("content length = %d, want 3: %s", len(content), gjson.GetBytes(out, "messages.1.content").Raw)
	}
	if got := content[0].Get("tool_use_id").String(); got != "call_1" {
		t.Fatalf("first tool_use_id = %q, want call_1: %s", got, out)
	}
	if got := content[1].Get("tool_use_id").String(); got != "call_2" {
		t.Fatalf("second tool_use_id = %q, want call_2: %s", got, out)
	}
	if got := content[2].Get("type").String(); got != "text" {
		t.Fatalf("third part type = %q, want text: %s", got, out)
	}
}

func TestRepairKimiClaudeToolUseRequest_RepairsPayloadAndOriginal(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"assistant","content":[
				{"type":"tool_use","id":"read_file:1","name":"read_file","input":{}},
				{"type":"tool_use","id":"read_file:2","name":"read_file","input":{}}
			]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"read_file:1","content":"ok"}]}
		]
	}`)
	req := cliproxyexecutor.Request{Payload: body}
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("claude"),
		OriginalRequest: body,
	}

	repairedReq, repairedOpts, err := repairKimiClaudeToolUseRequest(req, opts)
	if err != nil {
		t.Fatalf("repairKimiClaudeToolUseRequest() error = %v", err)
	}

	if gjson.GetBytes(repairedReq.Payload, `messages.0.content.#(id=="read_file:2")`).Exists() {
		t.Fatalf("payload still has unanswered read_file:2: %s", repairedReq.Payload)
	}
	if gjson.GetBytes(repairedOpts.OriginalRequest, `messages.0.content.#(id=="read_file:2")`).Exists() {
		t.Fatalf("original request still has unanswered read_file:2: %s", repairedOpts.OriginalRequest)
	}
}

func TestNormalizeKimiToolMessageLinks_DropsEmptyAssistantWithoutToolLink(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"user","content":"start"},
			{"role":"assistant","content":""},
			{"role":"assistant","content":"   "},
			{"role":"assistant","content":"","tool_calls":null},
			{"role":"assistant","content":[{"type":"text","text":"  "}]},
			{"role":"assistant"},
			{"role":"assistant","content":"keep"},
			{"role":"user","content":"next"}
		]
	}`)

	out, err := normalizeKimiToolMessageLinks(body)
	if err != nil {
		t.Fatalf("normalizeKimiToolMessageLinks() error = %v", err)
	}

	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 3 {
		t.Fatalf("messages length = %d, want 3, raw = %s", len(messages), gjson.GetBytes(out, "messages").Raw)
	}
	if got := messages[0].Get("content").String(); got != "start" {
		t.Fatalf("messages.0.content = %q, want %q", got, "start")
	}
	if got := messages[1].Get("content").String(); got != "keep" {
		t.Fatalf("messages.1.content = %q, want %q", got, "keep")
	}
	if got := messages[2].Get("content").String(); got != "next" {
		t.Fatalf("messages.2.content = %q, want %q", got, "next")
	}
}

func TestNormalizeKimiToolMessageLinks_PreservesAssistantWithToolLinkOrReasoning(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"list_directory","arguments":"{}"}}]},
			{"role":"assistant","content":"","function_call":{"name":"legacy_call","arguments":"{}"}},
			{"role":"assistant","content":"","reasoning_content":"thought"},
			{"role":"assistant","content":[{"type":"text","text":" visible "}]}
		]
	}`)

	out, err := normalizeKimiToolMessageLinks(body)
	if err != nil {
		t.Fatalf("normalizeKimiToolMessageLinks() error = %v", err)
	}

	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 4 {
		t.Fatalf("messages length = %d, want 4, raw = %s", len(messages), gjson.GetBytes(out, "messages").Raw)
	}
	if !messages[0].Get("tool_calls").Exists() {
		t.Fatalf("messages.0.tool_calls should exist")
	}
	if !messages[1].Get("function_call").Exists() {
		t.Fatalf("messages.1.function_call should exist")
	}
	if got := messages[2].Get("reasoning_content").String(); got != "thought" {
		t.Fatalf("messages.2.reasoning_content = %q, want %q", got, "thought")
	}
	if got := messages[3].Get("content.0.text").String(); got != " visible " {
		t.Fatalf("messages.3.content.0.text = %q, want %q", got, " visible ")
	}
}
