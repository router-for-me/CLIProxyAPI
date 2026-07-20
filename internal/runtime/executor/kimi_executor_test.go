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

func TestNewKimiExecutorInitializesDelegatedClaudeConfig(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: true}}
	executor := NewKimiExecutor(cfg)

	if executor.cfg != cfg {
		t.Fatal("Kimi executor config was not initialized")
	}
	if executor.ClaudeExecutor.cfg != cfg {
		t.Fatal("delegated Claude executor config was not initialized")
	}
}

func TestKimiExecutorClaudeRequestPreservesInternalModelSemantics(t *testing.T) {
	var upstreamBody []byte
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", kimiRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		var errRead error
		upstreamBody, errRead = io.ReadAll(req.Body)
		if errRead != nil {
			return nil, errRead
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"id":"msg_test","type":"message","role":"assistant","model":"k2.5","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`,
			)),
		}, nil
	}))

	executor := NewKimiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{},
		Metadata:   map[string]any{"access_token": "test-token"},
	}
	const model = "kimi-k2.5(max)"
	payload := []byte(`{"model":"kimi-k2.5(max)","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`)
	response, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		OriginalRequest: payload,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := gjson.GetBytes(upstreamBody, "model").String(); got != "k2.5" {
		t.Fatalf("upstream model = %q, want k2.5", got)
	}
	if got := gjson.GetBytes(upstreamBody, "output_config.effort").String(); got != "high" {
		t.Fatalf("upstream output_config.effort = %q, want high", got)
	}
	if got := gjson.GetBytes(response.Payload, "model").String(); got != model {
		t.Fatalf("response model = %q, want %q", got, model)
	}
}

func TestKimiExecutorCountTokensUsesCanonicalUpstreamModel(t *testing.T) {
	var upstreamRequest *http.Request
	var upstreamBody []byte
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", kimiRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		upstreamRequest = req.Clone(req.Context())
		var errRead error
		upstreamBody, errRead = io.ReadAll(req.Body)
		if errRead != nil {
			return nil, errRead
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"input_tokens":42}`)),
		}, nil
	}))

	executor := NewKimiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{},
		Metadata:   map[string]any{"access_token": "test-token"},
	}
	payload := []byte(`{"model":"kimi-k3[1m](high)","messages":[{"role":"user","content":"hello"}]}`)
	_, err := executor.CountTokens(ctx, auth, cliproxyexecutor.Request{
		Model:   "kimi-k3[1m](high)",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err != nil {
		t.Fatalf("CountTokens() error = %v", err)
	}
	if upstreamRequest == nil {
		t.Fatal("upstream request was not captured")
	}
	if got := upstreamRequest.URL.String(); got != "https://api.kimi.com/coding/v1/messages/count_tokens?beta=true" {
		t.Fatalf("upstream URL = %q, want Kimi count tokens endpoint", got)
	}
	if got := gjson.GetBytes(upstreamBody, "model").String(); got != "k3" {
		t.Fatalf("upstream model = %q, want k3", got)
	}
}

func TestKimiExecutorClaudeStreamForwardsAnthropicBetaAndLogsUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages?beta=true", nil)

	var upstreamRequest *http.Request
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	ctx = context.WithValue(ctx, "cliproxy.roundtripper", kimiRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		upstreamRequest = req.Clone(req.Context())
		upstreamRequest.Header = req.Header.Clone()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				"event: message_start\n" +
					`data: {"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","model":"k3","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}` + "\n\n" +
					"event: message_stop\n" +
					`data: {"type":"message_stop"}` + "\n\n",
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
	payload := []byte(`{"model":"kimi-k3","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`)
	result, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "kimi-k3",
		Payload: payload,
	}, cliproxyexecutor.Options{
		Stream:          true,
		SourceFormat:    sdktranslator.FormatClaude,
		OriginalRequest: payload,
		Headers: http.Header{
			"Anthropic-Beta": []string{"client-beta-one", "client-beta-two"},
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
	if !strings.Contains(output.String(), `"model":"kimi-k3"`) {
		t.Fatalf("stream output = %q, want requested model kimi-k3", output.String())
	}
	if upstreamRequest == nil {
		t.Fatal("upstream request was not captured")
	}
	if got := upstreamRequest.URL.String(); got != "https://api.kimi.com/coding/v1/messages?beta=true" {
		t.Fatalf("upstream URL = %q, want Kimi messages endpoint", got)
	}
	upstreamBetas := upstreamRequest.Header.Get("Anthropic-Beta")
	for _, beta := range []string{"client-beta-one", "client-beta-two", "oauth-2025-04-20", "interleaved-thinking-2025-05-14"} {
		if !strings.Contains(upstreamBetas, beta) {
			t.Fatalf("Anthropic-Beta = %q, want %q", upstreamBetas, beta)
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
		"Upstream URL: https://api.kimi.com/coding/v1/messages?beta=true",
		"Auth: provider=kimi",
		"Anthropic-Beta: " + upstreamBetas,
		`"model":"k3"`,
	} {
		if !strings.Contains(apiRequestText, want) {
			t.Fatalf("API_REQUEST = %q, want %q", apiRequestText, want)
		}
	}
	if strings.Contains(apiRequestText, "<missing>") {
		t.Fatalf("API_REQUEST = %q, want captured upstream request", apiRequestText)
	}

	rawAPIResponse, existsResponse := ginCtx.Get("API_RESPONSE")
	apiResponse, okResponse := rawAPIResponse.([]byte)
	if !existsResponse || !okResponse {
		t.Fatalf("API_RESPONSE = %#v, want captured bytes", rawAPIResponse)
	}
	apiResponseText := string(apiResponse)
	for _, want := range []string{"=== API RESPONSE 1 ===", "Status: 200", `data: {"type":"message_stop"}`} {
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

func TestNormalizeKimiUpstreamModel(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"kimi-k3[1m]", "k3"},
		{"kimi-k3", "k3"},
		{"Kimi-K3[1M]", "k3"},
		{"k3[1m]", "k3"},
		{"k3", "k3"},
		{"kimi-k2.6", "k2.6"},
		{"kimi-k2.6[1m]", "k2.6"},
		{"kimi-k3(1024)", "k3(1024)"},
		{"kimi-k3[1m](1024)", "k3(1024)"},
		{"kimi-k2.6(high)", "k2.6(high)"},
		{"kimi-k2.6[1m](high)", "k2.6(high)"},
	}

	for _, c := range cases {
		got := normalizeKimiUpstreamModel(c.in)
		if got != c.want {
			t.Errorf("normalizeKimiUpstreamModel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
