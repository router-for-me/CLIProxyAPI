package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestNewKimiExecutorInitializesClaudeTokenCounterConfig(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: true}}
	executor := NewKimiExecutor(cfg)

	if executor.cfg != cfg {
		t.Fatal("Kimi executor config was not initialized")
	}
	if executor.ClaudeExecutor.cfg != cfg {
		t.Fatal("Claude token counter config was not initialized")
	}
}

func TestKimiExecutorClaudeRequestUsesChatCompletionsPath(t *testing.T) {
	var upstreamRequest *http.Request
	var upstreamBody []byte
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", kimiRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		upstreamRequest = req.Clone(req.Context())
		upstreamRequest.Header = req.Header.Clone()
		var errRead error
		upstreamBody, errRead = io.ReadAll(req.Body)
		if errRead != nil {
			return nil, errRead
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"id":"chatcmpl_test","object":"chat.completion","created":1,"model":"k2.5","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
			)),
		}, nil
	}))

	executor := NewKimiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{},
		Metadata:   map[string]any{"access_token": "test-token"},
	}
	payload := []byte(`{"model":"kimi-k2.5","max_tokens":32,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	response, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "kimi-k2.5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		OriginalRequest: payload,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if upstreamRequest == nil {
		t.Fatal("upstream request was not captured")
	}
	if got := upstreamRequest.URL.String(); got != "https://api.kimi.com/coding/v1/chat/completions" {
		t.Fatalf("upstream URL = %q, want Kimi chat completions endpoint", got)
	}
	if got := upstreamRequest.Header.Get("Authorization"); got != "Bearer test-token" {
		t.Fatalf("Authorization = %q, want Kimi bearer token", got)
	}
	if got := gjson.GetBytes(upstreamBody, "model").String(); got != "k2.5" {
		t.Fatalf("upstream model = %q, want stripped Kimi model", got)
	}
	if got := gjson.GetBytes(upstreamBody, "messages.0.content.0.text").String(); got != "hello" {
		t.Fatalf("upstream message text = %q, want translated Claude request", got)
	}
	if got := gjson.GetBytes(response.Payload, "type").String(); got != "message" {
		t.Fatalf("response type = %q, want Claude message", got)
	}
	if got := gjson.GetBytes(response.Payload, "content.0.text").String(); got != "hello" {
		t.Fatalf("response text = %q, want translated Claude response", got)
	}
}

func TestKimiExecutorClaudeStreamUsesChatCompletionsPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages?beta=true", nil)

	var upstreamRequest *http.Request
	var upstreamBody []byte
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	ctx = context.WithValue(ctx, "cliproxy.roundtripper", kimiRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		upstreamRequest = req.Clone(req.Context())
		upstreamRequest.Header = req.Header.Clone()
		var errRead error
		upstreamBody, errRead = io.ReadAll(req.Body)
		if errRead != nil {
			return nil, errRead
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				`data: {"id":"chatcmpl_test","object":"chat.completion.chunk","created":1,"model":"k2.5","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":null}]}` + "\n\n" +
					`data: {"id":"chatcmpl_test","object":"chat.completion.chunk","created":1,"model":"k2.5","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}` + "\n\n",
			)),
		}, nil
	}))

	cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: true}}
	executor := NewKimiExecutor(cfg)
	auth := &cliproxyauth.Auth{
		ID:         "kimi-test-auth",
		Attributes: map[string]string{},
		Metadata:   map[string]any{"access_token": "test-token"},
	}
	payload := []byte(`{"model":"kimi-k2.5","max_tokens":32,"stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	result, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "kimi-k2.5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		Stream:          true,
		SourceFormat:    sdktranslator.FormatClaude,
		OriginalRequest: payload,
		Headers: http.Header{
			"Anthropic-Beta": []string{"client-beta"},
		},
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	var output strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		output.Write(chunk.Payload)
	}
	if upstreamRequest == nil {
		t.Fatal("upstream request was not captured")
	}
	if got := upstreamRequest.URL.String(); got != "https://api.kimi.com/coding/v1/chat/completions" {
		t.Fatalf("upstream URL = %q, want Kimi chat completions endpoint", got)
	}
	if got := upstreamRequest.Header.Get("Anthropic-Beta"); got != "" {
		t.Fatalf("Anthropic-Beta = %q, want header omitted from Kimi chat completions request", got)
	}
	if got := gjson.GetBytes(upstreamBody, "model").String(); got != "k2.5" {
		t.Fatalf("upstream model = %q, want stripped Kimi model", got)
	}
	if !gjson.GetBytes(upstreamBody, "stream").Bool() {
		t.Fatal("upstream stream = false, want true")
	}
	if !gjson.GetBytes(upstreamBody, "stream_options.include_usage").Bool() {
		t.Fatal("upstream stream_options.include_usage = false, want true")
	}
	for _, want := range []string{`event: message_start`, `"type":"text_delta","text":"hello"`, `event: message_stop`} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("stream output = %q, want %q", output.String(), want)
		}
	}

	rawAPIRequest, existsRequest := ginCtx.Get("API_REQUEST")
	apiRequest, okRequest := rawAPIRequest.([]byte)
	if !existsRequest || !okRequest {
		t.Fatalf("API_REQUEST = %#v, want captured bytes", rawAPIRequest)
	}
	apiRequestText := string(apiRequest)
	for _, want := range []string{
		"=== API REQUEST 1 ===",
		"Upstream URL: https://api.kimi.com/coding/v1/chat/completions",
		"Auth: provider=kimi",
		`"model":"k2.5"`,
	} {
		if !strings.Contains(apiRequestText, want) {
			t.Fatalf("API_REQUEST = %q, want %q", apiRequestText, want)
		}
	}

	rawAPIResponse, existsResponse := ginCtx.Get("API_RESPONSE")
	apiResponse, okResponse := rawAPIResponse.([]byte)
	if !existsResponse || !okResponse {
		t.Fatalf("API_RESPONSE = %#v, want captured bytes", rawAPIResponse)
	}
	apiResponseText := string(apiResponse)
	for _, want := range []string{"=== API RESPONSE 1 ===", "Status: 200", `"object":"chat.completion.chunk"`} {
		if !strings.Contains(apiResponseText, want) {
			t.Fatalf("API_RESPONSE = %q, want %q", apiResponseText, want)
		}
	}
}

type kimiRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f kimiRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

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

func TestSanitizeKimiToolSchemas_CoercesBooleanSubschemaInProperties(t *testing.T) {
	// Reproduces the real oh-my-pi `task` tool schema whose nested
	// `outputSchema: true` boolean subschema makes Kimi 400 the whole turn with
	// "tools.function.parameters is not a valid moonshot flavored json schema".
	body := []byte(`{
		"tools":[
			{"type":"function","function":{"name":"task","parameters":{
				"type":"object",
				"properties":{
					"tasks":{"type":"array","items":{
						"type":"object",
						"properties":{"task":{"type":"string"},"outputSchema":true},
						"required":["task"]
					}}
				},
				"required":["tasks"]
			}}}
		]
	}`)

	out, err := sanitizeKimiToolSchemas(body)
	if err != nil {
		t.Fatalf("sanitizeKimiToolSchemas() error = %v", err)
	}

	node := gjson.GetBytes(out, "tools.0.function.parameters.properties.tasks.items.properties.outputSchema")
	if !node.IsObject() {
		t.Fatalf("outputSchema should be coerced to an object, got %q", node.Raw)
	}
	if len(node.Map()) != 0 {
		t.Fatalf("outputSchema `true` should coerce to `{}`, got %q", node.Raw)
	}
}

func TestSanitizeKimiToolSchemas_CoercesFalseToNot(t *testing.T) {
	body := []byte(`{
		"tools":[
			{"type":"function","function":{"name":"f","parameters":{
				"type":"object",
				"properties":{"blocked":false}
			}}}
		]
	}`)

	out, err := sanitizeKimiToolSchemas(body)
	if err != nil {
		t.Fatalf("sanitizeKimiToolSchemas() error = %v", err)
	}

	node := gjson.GetBytes(out, "tools.0.function.parameters.properties.blocked")
	if !node.Get("not").IsObject() {
		t.Fatalf("`false` subschema should coerce to {\"not\":{}}, got %q", node.Raw)
	}
}

func TestSanitizeKimiToolSchemas_PreservesLegalBooleanKeywords(t *testing.T) {
	// `additionalProperties: false` is native MFJS and must NOT be coerced.
	body := []byte(`{
		"tools":[
			{"type":"function","function":{"name":"f","parameters":{
				"type":"object",
				"properties":{"name":{"type":"string"}},
				"additionalProperties":false
			}}}
		]
	}`)

	out, err := sanitizeKimiToolSchemas(body)
	if err != nil {
		t.Fatalf("sanitizeKimiToolSchemas() error = %v", err)
	}

	node := gjson.GetBytes(out, "tools.0.function.parameters.additionalProperties")
	if node.Type != gjson.False {
		t.Fatalf("additionalProperties:false must stay a boolean, got %q", node.Raw)
	}
}

func TestSanitizeKimiToolSchemas_CoercesBooleanInCombinatorsAndItems(t *testing.T) {
	body := []byte(`{
		"tools":[
			{"type":"function","function":{"name":"f","parameters":{
				"type":"object",
				"properties":{
					"a":{"anyOf":[true,{"type":"string"}]},
					"b":{"type":"array","items":true},
					"c":{"type":"array","items":[true,{"type":"string"}]}
				}
			}}}
		]
	}`)

	out, err := sanitizeKimiToolSchemas(body)
	if err != nil {
		t.Fatalf("sanitizeKimiToolSchemas() error = %v", err)
	}

	if got := gjson.GetBytes(out, "tools.0.function.parameters.properties.a.anyOf.0"); !got.IsObject() {
		t.Fatalf("anyOf[0] boolean should be coerced to object, got %q", got.Raw)
	}
	if got := gjson.GetBytes(out, "tools.0.function.parameters.properties.b.items"); !got.IsObject() {
		t.Fatalf("items boolean should be coerced to object, got %q", got.Raw)
	}
	if got := gjson.GetBytes(out, "tools.0.function.parameters.properties.c.items.0"); !got.IsObject() {
		t.Fatalf("tuple items[0] boolean should be coerced to object, got %q", got.Raw)
	}
	if got := gjson.GetBytes(out, "tools.0.function.parameters.properties.c.items.1.type"); got.String() != "string" {
		t.Fatalf("tuple items[1] should be preserved, got %q", got.Raw)
	}
}

func TestSanitizeKimiToolSchemas_PreservesExactNumbersDuringCoercion(t *testing.T) {
	// A boolean subschema forces a rewrite; unrelated numeric literals — incl.
	// values above 2^53 — must survive the round-trip byte-exact rather than
	// being rounded through float64.
	body := []byte(`{
		"tools":[
			{"type":"function","function":{"name":"f","parameters":{
				"type":"object",
				"properties":{
					"open":true,
					"n":{"type":"integer","const":9007199254740993},
					"m":{"type":"integer","enum":[9007199254740993,1]}
				}
			}}}
		]
	}`)

	out, err := sanitizeKimiToolSchemas(body)
	if err != nil {
		t.Fatalf("sanitizeKimiToolSchemas() error = %v", err)
	}

	if got := gjson.GetBytes(out, "tools.0.function.parameters.properties.open"); !got.IsObject() {
		t.Fatalf("open `true` should be coerced to object, got %q", got.Raw)
	}
	if got := gjson.GetBytes(out, "tools.0.function.parameters.properties.n.const").Raw; got != "9007199254740993" {
		t.Fatalf("const should stay exact, got %q", got)
	}
	if got := gjson.GetBytes(out, "tools.0.function.parameters.properties.m.enum.0").Raw; got != "9007199254740993" {
		t.Fatalf("enum[0] should stay exact, got %q", got)
	}
}

func TestSanitizeKimiToolSchemas_NoToolsIsNoOp(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	out, err := sanitizeKimiToolSchemas(body)
	if err != nil {
		t.Fatalf("sanitizeKimiToolSchemas() error = %v", err)
	}
	if string(out) != string(body) {
		t.Fatalf("body without tools should be unchanged, got %s", string(out))
	}
}

func TestSanitizeKimiToolSchemas_CleanSchemaUnchanged(t *testing.T) {
	body := []byte(`{"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object","properties":{"x":{"type":"string"}}}}}]}`)
	out, err := sanitizeKimiToolSchemas(body)
	if err != nil {
		t.Fatalf("sanitizeKimiToolSchemas() error = %v", err)
	}
	if string(out) != string(body) {
		t.Fatalf("clean schema should be byte-identical, got %s", string(out))
	}
}
