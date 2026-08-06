package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestParseAuthEnvironmentReferenceDoesNotPersistSecret(t *testing.T) {
	t.Setenv("KIRO_TEST_KEY", "secret-test-value")
	request, _ := json.Marshal(pluginapi.AuthParseRequest{
		FileName: "kiro-pro.json",
		RawJSON:  []byte(`{"type":"kiro","api_key_env":"KIRO_TEST_KEY","region":"us-east-1","label":"pro"}`),
	})
	raw, errParse := parseAuth(request)
	if errParse != nil {
		t.Fatalf("parseAuth() error = %v", errParse)
	}
	var env envelope
	if errUnmarshal := json.Unmarshal(raw, &env); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if !env.OK {
		t.Fatalf("parseAuth() envelope = %#v", env)
	}
	var response pluginapi.AuthParseResponse
	if errUnmarshal := json.Unmarshal(env.Result, &response); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if !response.Handled || response.Auth.Provider != pluginIdentifier {
		t.Fatalf("ParseAuth response = %#v", response)
	}
	if strings.Contains(string(response.Auth.StorageJSON), "secret-test-value") {
		t.Fatal("StorageJSON contains resolved API key")
	}
	if response.Auth.Attributes["region"] != "us-east-1" {
		t.Fatalf("region = %q", response.Auth.Attributes["region"])
	}
}

func TestDecodeCredentialRejectsHostInjectionRegion(t *testing.T) {
	_, handled, errCredential := decodeCredential([]byte(`{"type":"kiro","api_key_env":"KEY","region":"us-east-1.kiro.dev.evil"}`))
	if !handled || errCredential == nil {
		t.Fatalf("decodeCredential() handled=%v error=%v", handled, errCredential)
	}
}

func TestModelsForAuthDiscoversPaginatesAndCaches(t *testing.T) {
	configureKiroTest(t, "models:\n  - '*'\n")
	t.Setenv("KIRO_TEST_KEY", "test-discovery-key")

	var requests []rpcHostHTTPRequest
	previous := invokeHost
	invokeHost = func(method string, payload any) (json.RawMessage, error) {
		if method != pluginabi.MethodHostHTTPDo {
			return nil, os.ErrInvalid
		}
		request := payload.(rpcHostHTTPRequest)
		requests = append(requests, request)
		if request.Headers.Get("Authorization") != "Bearer test-discovery-key" || request.Headers.Get("TokenType") != "API_KEY" {
			t.Fatalf("discovery headers = %#v", request.Headers)
		}
		parsed, errParse := url.Parse(request.URL)
		if errParse != nil {
			t.Fatal(errParse)
		}
		if parsed.Host != "codewhisperer.us-east-1.amazonaws.com" || parsed.Path != "/ListAvailableModels" {
			t.Fatalf("discovery URL = %q", request.URL)
		}
		var body []byte
		switch len(requests) {
		case 1:
			if parsed.Query().Get("nextToken") != "" {
				t.Fatalf("first nextToken = %q", parsed.Query().Get("nextToken"))
			}
			body = []byte(`{
				"models":[
					{"modelId":"claude-opus-5","modelName":"Claude Opus 5","description":"Large context","supportedInputTypes":["TEXT","IMAGE"],"tokenLimits":{"maxInputTokens":1000000,"maxOutputTokens":128000}},
					{"modelId":"","modelName":"invalid"}
				],
				"nextToken":"page +/="
			}`)
		case 2:
			if parsed.Query().Get("nextToken") != "page +/=" {
				t.Fatalf("second nextToken = %q", parsed.Query().Get("nextToken"))
			}
			body = []byte(`{
				"models":[
					{"modelId":"claude-opus-5","modelName":"duplicate"},
					{"modelId":"glm-5","modelName":"GLM 5","supportedInputTypes":["TEXT"],"tokenLimits":{"maxInputTokens":200000,"maxOutputTokens":64000}}
				],
				"nextToken":null
			}`)
		default:
			t.Fatalf("unexpected discovery request %d", len(requests))
		}
		return mustJSONRaw(pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: body}), nil
	}
	t.Cleanup(func() { invokeHost = previous })

	response := callModelsForAuthTest(t, "kiro-pro", "callback-1")
	if len(response.Models) != 2 {
		t.Fatalf("discovered models = %#v", response.Models)
	}
	if response.Models[0].ID != "claude-opus-5" || response.Models[0].InputTokenLimit != 1_000_000 || response.Models[0].OutputTokenLimit != 128_000 {
		t.Fatalf("first model = %#v", response.Models[0])
	}
	if strings.Join(response.Models[0].SupportedInputModalities, ",") != "text,image" {
		t.Fatalf("first model modalities = %#v", response.Models[0].SupportedInputModalities)
	}
	if response.Models[1].ID != "glm-5" || strings.Join(response.Models[1].SupportedInputModalities, ",") != "text" {
		t.Fatalf("second model = %#v", response.Models[1])
	}

	cached := callModelsForAuthTest(t, "kiro-pro", "callback-2")
	if len(cached.Models) != 2 || len(requests) != 2 {
		t.Fatalf("cached models=%d HTTP requests=%d", len(cached.Models), len(requests))
	}
}

func TestModelsForAuthAppliesConfiguredAllowList(t *testing.T) {
	configureKiroTest(t, "models:\n  - claude-sonnet-4.5\n")
	t.Setenv("KIRO_TEST_KEY", "test-allow-list-key")

	previous := invokeHost
	invokeHost = func(method string, payload any) (json.RawMessage, error) {
		if method != pluginabi.MethodHostHTTPDo {
			return nil, os.ErrInvalid
		}
		body := []byte(`{"models":[{"modelId":"claude-sonnet-4.5","modelName":"Sonnet"},{"modelId":"claude-opus-5","modelName":"Opus"}]}`)
		return mustJSONRaw(pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: body}), nil
	}
	t.Cleanup(func() { invokeHost = previous })

	response := callModelsForAuthTest(t, "kiro-allow-list", "callback-allow-list")
	if len(response.Models) != 1 || response.Models[0].ID != "claude-sonnet-4.5" {
		t.Fatalf("allow-listed models = %#v", response.Models)
	}
}

func TestModelsForAuthUsesConfiguredFallbackOnColdStartFailure(t *testing.T) {
	configureKiroTest(t, "models:\n  - '*'\n")
	t.Setenv("KIRO_TEST_KEY", "test-fallback-key")

	previous := invokeHost
	invokeHost = func(method string, payload any) (json.RawMessage, error) {
		switch method {
		case pluginabi.MethodHostHTTPDo:
			return mustJSONRaw(pluginapi.HTTPResponse{StatusCode: http.StatusServiceUnavailable, Body: []byte(`{"message":"temporary"}`)}), nil
		case pluginabi.MethodHostLog:
			return json.RawMessage(`{}`), nil
		default:
			return nil, os.ErrInvalid
		}
	}
	t.Cleanup(func() { invokeHost = previous })

	response := callModelsForAuthTest(t, "kiro-fallback", "callback-fallback")
	if len(response.Models) != 2 || response.Models[0].ID != "claude-sonnet-4.5" || response.Models[1].ID != "claude-haiku-4.5" {
		t.Fatalf("fallback models = %#v", response.Models)
	}
	for _, model := range response.Models {
		if model.ID == "*" {
			t.Fatal("wildcard was registered as a model")
		}
	}
}

func TestModelsForAuthUsesStaleCacheOnRefreshFailure(t *testing.T) {
	configureKiroTest(t, "models:\n  - '*'\n")
	t.Setenv("KIRO_TEST_KEY", "test-stale-key")
	cacheKey := modelCacheKey("kiro-stale", "us-east-1", "test-stale-key")
	modelCacheMu.Lock()
	modelCache[cacheKey] = modelCacheEntry{
		Models:    []pluginapi.ModelInfo{{ID: "claude-opus-5", Name: "Claude Opus 5"}},
		FetchedAt: time.Now().Add(-modelCacheTTL - time.Second),
	}
	modelCacheMu.Unlock()

	previous := invokeHost
	invokeHost = func(method string, payload any) (json.RawMessage, error) {
		switch method {
		case pluginabi.MethodHostHTTPDo:
			return nil, os.ErrDeadlineExceeded
		case pluginabi.MethodHostLog:
			return json.RawMessage(`{}`), nil
		default:
			return nil, os.ErrInvalid
		}
	}
	t.Cleanup(func() { invokeHost = previous })

	response := callModelsForAuthTest(t, "kiro-stale", "callback-stale")
	if len(response.Models) != 1 || response.Models[0].ID != "claude-opus-5" {
		t.Fatalf("stale models = %#v", response.Models)
	}
}

func TestClaudeToKiroTransformsSystemToolsAndResults(t *testing.T) {
	raw := []byte(`{
		"model":"claude-sonnet-4-5",
		"max_tokens":128,
		"system":"Be concise.",
		"tools":[{"name":"math.add/unsafe","description":"Add values","input_schema":{"type":"object","properties":{"a":{"type":"number"}}}}],
		"messages":[
			{"role":"user","content":"Add 2 and 3"},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"math.add/unsafe","input":{"a":2}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"5"}]}
		]
	}`)
	payload, request, errTranslate := claudeToKiro(raw, "")
	if errTranslate != nil {
		t.Fatalf("claudeToKiro() error = %v", errTranslate)
	}
	current := payload.ConversationState.CurrentMessage.UserInputMessage
	if current.ModelID != "claude-sonnet-4.5" {
		t.Fatalf("model ID = %q", current.ModelID)
	}
	if current.Origin != "KIRO_CLI" {
		t.Fatalf("origin = %q", current.Origin)
	}
	if request.MaxTokens != 128 || payload.InferenceConfig.MaxTokens != 128 {
		t.Fatalf("max tokens were not preserved")
	}
	if len(payload.ConversationState.History) < 4 {
		t.Fatalf("history = %#v", payload.ConversationState.History)
	}
	context := current.UserInputMessageContext
	if context == nil || len(context.Tools) != 1 || len(context.ToolResults) != 1 {
		t.Fatalf("current context = %#v", context)
	}
	if context.ToolResults[0].Status != "success" {
		t.Fatalf("tool result status = %q, want success", context.ToolResults[0].Status)
	}
	toolName := context.Tools[0].ToolSpecification.Name
	if toolName != "math_add_unsafe" || payload.ToolNameMap[toolName] != "math.add/unsafe" {
		t.Fatalf("tool name=%q map=%#v", toolName, payload.ToolNameMap)
	}
	if !strings.Contains(current.Content, "Tool results:") {
		t.Fatalf("current content = %q", current.Content)
	}
}

func TestClaudeToKiroDisambiguatesHistoricalToolNames(t *testing.T) {
	payload, _, errTranslate := claudeToKiro([]byte(`{
		"model":"claude-sonnet-4-5",
		"tools":[
			{"name":"foo","input_schema":{"type":"object"}},
			{"name":"foo_","input_schema":{"type":"object"}}
		],
		"messages":[
			{"role":"user","content":"Use the second tool"},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"foo_","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"done"}]}
		]
	}`), "")
	if errTranslate != nil {
		t.Fatalf("claudeToKiro() error = %v", errTranslate)
	}
	context := payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext
	if context == nil || len(context.Tools) != 2 {
		t.Fatalf("current context = %#v, want two tools", context)
	}
	if got := context.Tools[1].ToolSpecification.Name; got != "foo_2" {
		t.Fatalf("second declared tool name = %q, want foo_2", got)
	}
	if len(payload.ConversationState.History) != 2 || len(payload.ConversationState.History[1].AssistantResponseMessage.ToolUses) != 1 {
		t.Fatalf("history = %#v, want assistant tool call", payload.ConversationState.History)
	}
	if got := payload.ConversationState.History[1].AssistantResponseMessage.ToolUses[0].Name; got != "foo_2" {
		t.Fatalf("historical tool name = %q, want foo_2", got)
	}
	if got := payload.ToolNameMap["foo_2"]; got != "foo_" {
		t.Fatalf("response tool name mapping = %q, want foo_", got)
	}
}

func TestTruncatePayloadEvictsOldestConversationTurn(t *testing.T) {
	payload := &kiroPayload{}
	payload.ConversationState.History = []kiroHistoryMessage{
		{UserInputMessage: &kiroUserInputMessage{Content: strings.Repeat("x", maxKiroPayloadBytes), ModelID: "model", Origin: "KIRO_CLI"}},
		{AssistantResponseMessage: &kiroAssistantResponseMessage{Content: "old response"}},
		{UserInputMessage: &kiroUserInputMessage{Content: "new request", ModelID: "model", Origin: "KIRO_CLI"}},
		{AssistantResponseMessage: &kiroAssistantResponseMessage{Content: "new response"}},
	}

	truncatePayload(payload, false)

	if len(payload.ConversationState.History) != 2 {
		t.Fatalf("history length = %d, want newest turn only", len(payload.ConversationState.History))
	}
	if got := payload.ConversationState.History[0].UserInputMessage.Content; got != "new request" {
		t.Fatalf("oldest retained request = %q, want newest request", got)
	}
	raw, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if len(raw) > maxKiroPayloadBytes {
		t.Fatalf("truncated payload size = %d, limit = %d", len(raw), maxKiroPayloadBytes)
	}
}

func TestClaudeToKiroPreservesExplicitZeroSamplingValues(t *testing.T) {
	payload, _, errTranslate := claudeToKiro([]byte(`{
		"model":"claude-sonnet-4-5",
		"messages":[{"role":"user","content":"hello"}],
		"temperature":0,
		"top_p":0
	}`), "")
	if errTranslate != nil {
		t.Fatalf("claudeToKiro() error = %v", errTranslate)
	}
	if payload.InferenceConfig == nil || payload.InferenceConfig.Temperature == nil || payload.InferenceConfig.TopP == nil {
		t.Fatalf("inference config = %#v, want explicit sampling values", payload.InferenceConfig)
	}

	raw, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if !bytes.Contains(raw, []byte(`"temperature":0`)) || !bytes.Contains(raw, []byte(`"topP":0`)) {
		t.Fatalf("Kiro payload = %s, want explicit zero sampling values", raw)
	}
}

func TestClaudeToKiroPreservesThinkingBudget(t *testing.T) {
	payload, _, errTranslate := claudeToKiro([]byte(`{
		"model":"claude-sonnet-4-5",
		"messages":[{"role":"user","content":"hello"}],
		"thinking":{"type":"enabled","budget_tokens":1024}
	}`), "")
	if errTranslate != nil {
		t.Fatalf("claudeToKiro() error = %v", errTranslate)
	}
	if len(payload.ConversationState.History) < 1 || payload.ConversationState.History[0].UserInputMessage == nil {
		t.Fatalf("history = %#v, want thinking system prompt", payload.ConversationState.History)
	}
	content := payload.ConversationState.History[0].UserInputMessage.Content
	if !strings.Contains(content, "<max_thinking_length>1024</max_thinking_length>") {
		t.Fatalf("thinking system prompt = %q, want requested budget", content)
	}
}

func TestClaudeToKiroRejectsPayloadAboveSizeLimit(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{
			name: "oversized current message",
			raw:  []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"` + strings.Repeat("x", maxKiroPayloadBytes) + `"}]}`),
		},
		{
			name: "oversized protected system prompt",
			raw:  []byte(`{"model":"claude-sonnet-4-5","system":"` + strings.Repeat("x", maxKiroPayloadBytes) + `","messages":[{"role":"user","content":"hello"}]}`),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, errTranslate := claudeToKiro(test.raw, ""); errTranslate == nil || !strings.Contains(errTranslate.Error(), "limit is") {
				t.Fatalf("claudeToKiro() error = %v, want local size-limit error", errTranslate)
			}
		})
	}
}

func TestTruncatePayloadPreservesSyntheticSystemPair(t *testing.T) {
	payload := &kiroPayload{}
	payload.ConversationState.History = []kiroHistoryMessage{
		{UserInputMessage: &kiroUserInputMessage{Content: "system prompt", ModelID: "model", Origin: "KIRO_CLI"}},
		{AssistantResponseMessage: &kiroAssistantResponseMessage{Content: "I will follow these instructions."}},
		{UserInputMessage: &kiroUserInputMessage{Content: strings.Repeat("x", maxKiroPayloadBytes), ModelID: "model", Origin: "KIRO_CLI"}},
		{AssistantResponseMessage: &kiroAssistantResponseMessage{Content: "old response"}},
		{UserInputMessage: &kiroUserInputMessage{Content: "new request", ModelID: "model", Origin: "KIRO_CLI"}},
		{AssistantResponseMessage: &kiroAssistantResponseMessage{Content: "new response"}},
	}

	truncatePayload(payload, true)

	if len(payload.ConversationState.History) != 4 {
		t.Fatalf("history length = %d, want system pair plus newest turn", len(payload.ConversationState.History))
	}
	if got := payload.ConversationState.History[0].UserInputMessage.Content; got != "system prompt" {
		t.Fatalf("first history request = %q, want preserved system prompt", got)
	}
	if got := payload.ConversationState.History[2].UserInputMessage.Content; got != "new request" {
		t.Fatalf("retained conversation request = %q, want newest request", got)
	}
}

func TestClaudeToKiroPreservesToolResultErrorStatus(t *testing.T) {
	raw := []byte(`{
		"model":"claude-sonnet-4-5",
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"lookup","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"lookup failed","is_error":true}]}
		]
	}`)
	payload, _, errTranslate := claudeToKiro(raw, "")
	if errTranslate != nil {
		t.Fatalf("claudeToKiro() error = %v", errTranslate)
	}
	context := payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext
	if context == nil || len(context.ToolResults) != 1 {
		t.Fatalf("current context = %#v", context)
	}
	if context.ToolResults[0].Status != "error" {
		t.Fatalf("tool result status = %q, want error", context.ToolResults[0].Status)
	}
}

func TestClaudeToKiroRejectsAssistantPrefill(t *testing.T) {
	raw := []byte(`{
		"model":"claude-sonnet-4-5",
		"messages":[
			{"role":"user","content":"Complete this sentence"},
			{"role":"assistant","content":"The answer starts with"}
		]
	}`)
	if _, _, errTranslate := claudeToKiro(raw, ""); errTranslate == nil || !strings.Contains(errTranslate.Error(), "assistant prefills") {
		t.Fatalf("claudeToKiro() error = %v, want unsupported assistant prefill error", errTranslate)
	}
}

func TestNormalizeKiroModelDoesNotRewriteDatedSnapshotAsDecimal(t *testing.T) {
	if got := normalizeKiroModel("claude-sonnet-4-20250514"); got != "claude-sonnet-4" {
		t.Fatalf("normalizeKiroModel() = %q", got)
	}
	if got := normalizeKiroModel("claude-haiku-4-5"); got != "claude-haiku-4.5" {
		t.Fatalf("normalizeKiroModel() = %q", got)
	}
}

func TestEventStreamDecoderHandlesSplitFramesAndCRC(t *testing.T) {
	frame := testEventFrame(t, "assistantResponseEvent", map[string]any{"content": "hello"})
	decoder := &eventStreamDecoder{}
	first, errFirst := decoder.Feed(frame[:7])
	if errFirst != nil || len(first) != 0 {
		t.Fatalf("first Feed() events=%d error=%v", len(first), errFirst)
	}
	events, errSecond := decoder.Feed(frame[7:])
	if errSecond != nil || len(events) != 1 || events[0].Type != "assistantResponseEvent" {
		t.Fatalf("second Feed() events=%#v error=%v", events, errSecond)
	}
	corrupt := append([]byte(nil), frame...)
	corrupt[len(corrupt)-1] ^= 0xff
	if _, errCorrupt := (&eventStreamDecoder{}).Feed(corrupt); errCorrupt == nil {
		t.Fatal("corrupt frame was accepted")
	}
}

func TestPendingToolUseAdoptsLateUpstreamID(t *testing.T) {
	pending := &pendingToolUses{}
	tools, errFirst := pending.accept(map[string]any{"name": "lookup", "input": `{"q":`})
	if errFirst != nil || len(tools) != 0 {
		t.Fatalf("first tool fragment = %#v, %v", tools, errFirst)
	}
	tools, errSecond := pending.accept(map[string]any{"toolUseId": "toolu_real", "name": "lookup", "input": `"x"}`, "stop": true})
	if errSecond != nil || len(tools) != 1 {
		t.Fatalf("second tool fragment = %#v, %v", tools, errSecond)
	}
	if tools[0].ToolUseID != "toolu_real" || tools[0].Input["q"] != "x" {
		t.Fatalf("tool = %#v", tools[0])
	}
}

func TestExecuteUsesHostTransportAndReturnsClaudeResponse(t *testing.T) {
	t.Setenv("KIRO_TEST_KEY", "test-key")
	frames := append(testEventFrame(t, "assistantResponseEvent", map[string]any{"content": "391", "usage": map[string]any{"inputTokens": 12, "outputTokens": 1}}), testEventFrame(t, "metadataEvent", map[string]any{"stopReason": "end_turn"})...)
	mock := newHostMock(frames)
	previous := invokeHost
	invokeHost = mock.call
	t.Cleanup(func() { invokeHost = previous })

	storage := []byte(`{"type":"kiro","api_key_env":"KIRO_TEST_KEY","region":"us-east-1"}`)
	requestRaw, _ := json.Marshal(rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{
		Model:       "claude-sonnet-4.5",
		Payload:     []byte(`{"model":"claude-sonnet-4.5","max_tokens":32,"messages":[{"role":"user","content":"Calculate 17 * 23. Return only the number."}]}`),
		StorageJSON: storage,
	}})
	raw, errExecute := execute(requestRaw)
	if errExecute != nil {
		t.Fatalf("execute() error = %v", errExecute)
	}
	var env envelope
	_ = json.Unmarshal(raw, &env)
	if !env.OK {
		t.Fatalf("execute() envelope = %#v", env)
	}
	var executorResponse pluginapi.ExecutorResponse
	_ = json.Unmarshal(env.Result, &executorResponse)
	var response claudeResponse
	if errUnmarshal := json.Unmarshal(executorResponse.Payload, &response); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if len(response.Content) != 1 || response.Content[0].Text != "391" || response.Usage.InputTokens != 12 || response.Usage.OutputTokens != 1 {
		t.Fatalf("response = %#v", response)
	}
	if got := mock.request.Headers.Get("Authorization"); got != "Bearer test-key" {
		t.Fatalf("Authorization = %q", got)
	}
	if strings.Contains(string(mock.request.Body), "test-key") {
		t.Fatal("upstream body contains API key")
	}
}

func TestExecuteStreamEmitsAnthropicSSE(t *testing.T) {
	t.Setenv("KIRO_TEST_KEY", "test-key")
	frames := append(testEventFrame(t, "assistantResponseEvent", map[string]any{"content": "hello"}), testEventFrame(t, "assistantResponseEvent", map[string]any{"content": " world"})...)
	frames = append(frames, testEventFrame(t, "metadataEvent", map[string]any{"stopReason": "end_turn"})...)
	mock := newHostMock(frames)
	previous := invokeHost
	invokeHost = mock.call
	t.Cleanup(func() { invokeHost = previous })

	requestRaw, _ := json.Marshal(rpcExecutorRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:       "claude-sonnet-4.5",
			Payload:     []byte(`{"model":"claude-sonnet-4.5","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":true}`),
			StorageJSON: []byte(`{"type":"kiro","api_key_env":"KIRO_TEST_KEY","region":"us-east-1"}`),
		},
		StreamID: "plugin-stream-1",
	})
	if _, errStream := executeStream(requestRaw); errStream != nil {
		t.Fatal(errStream)
	}
	select {
	case <-mock.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not close")
	}
	joined := string(mock.emittedBytes())
	for _, expected := range []string{"event: message_start", "event: content_block_delta", `"text":"hello"`, `"text":" world"`, `"input_tokens":`, `"output_tokens":3`, "event: message_stop"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("stream missing %q: %s", expected, joined)
		}
	}
	if got := strings.Count(joined, `"type":"content_block_start"`); got != 1 {
		t.Fatalf("content block starts = %d, want 1: %s", got, joined)
	}
	if got := strings.Count(joined, `"type":"content_block_stop"`); got != 1 {
		t.Fatalf("content block stops = %d, want 1: %s", got, joined)
	}
}

func TestAccumulatorMergesAdjacentFragments(t *testing.T) {
	payload := &kiroPayload{}
	payload.ConversationState.CurrentMessage.UserInputMessage.ModelID = "claude-sonnet-4.5"
	accumulator := newAccumulator(payload)

	events := []kiroEvent{
		{Type: "reasoningContentEvent", Payload: map[string]any{"text": "Think"}},
		{Type: "reasoningContentEvent", Payload: map[string]any{"text": " first."}},
		{Type: "assistantResponseEvent", Payload: map[string]any{"content": "Hello"}},
		{Type: "assistantResponseEvent", Payload: map[string]any{"content": " world."}},
	}
	for _, event := range events {
		if _, errAccept := accumulator.accept(event); errAccept != nil {
			t.Fatal(errAccept)
		}
	}
	if len(accumulator.Blocks) != 2 {
		t.Fatalf("content blocks = %#v, want one thinking and one text block", accumulator.Blocks)
	}
	if accumulator.Blocks[0].Thinking != "Think first." || accumulator.Blocks[1].Text != "Hello world." {
		t.Fatalf("merged content blocks = %#v", accumulator.Blocks)
	}
}

func TestAccumulatorEstimatesUsageWhenUpstreamOmitsTokens(t *testing.T) {
	payload, _, errTranslate := claudeToKiro([]byte(`{
		"model":"claude-sonnet-4.5",
		"max_tokens":32,
		"messages":[{"role":"user","content":"Calculate 17 * 23. Return only the number."}]
	}`), "")
	if errTranslate != nil {
		t.Fatal(errTranslate)
	}
	accumulator := newAccumulator(payload)
	if _, errAccept := accumulator.accept(kiroEvent{Type: "assistantResponseEvent", Payload: map[string]any{"content": "391"}}); errAccept != nil {
		t.Fatal(errAccept)
	}
	if errFinish := accumulator.finish(); errFinish != nil {
		t.Fatal(errFinish)
	}
	if accumulator.InputTokens <= 0 || accumulator.OutputTokens <= 0 {
		t.Fatalf("estimated usage = input:%d output:%d, want both positive", accumulator.InputTokens, accumulator.OutputTokens)
	}
}

func TestAccumulatorPrefersContextUsageOverInputEstimate(t *testing.T) {
	payload := &kiroPayload{EstimatedInputTokens: 7}
	payload.ConversationState.CurrentMessage.UserInputMessage.ModelID = "claude-sonnet-4.5"
	accumulator := newAccumulator(payload)
	_, _ = accumulator.accept(kiroEvent{Type: "contextUsageEvent", Payload: map[string]any{"contextUsagePercentage": 10.5}})
	_, _ = accumulator.accept(kiroEvent{Type: "assistantResponseEvent", Payload: map[string]any{"content": "ok"}})
	if errFinish := accumulator.finish(); errFinish != nil {
		t.Fatal(errFinish)
	}
	if accumulator.InputTokens != 21_000 {
		t.Fatalf("input tokens = %d, want 21000 from context usage", accumulator.InputTokens)
	}
}

func TestUpdateUsageReadsNestedCacheBuckets(t *testing.T) {
	event := map[string]any{"metrics": map[string]any{"usage": map[string]any{
		"uncachedInputTokens":   "5",
		"cacheReadInputTokens":  4.0,
		"cacheWriteInputTokens": 3.0,
		"totalOutputTokens":     2.0,
	}}}
	inputTokens, outputTokens := updateUsage(event, 0, 0)
	if inputTokens != 12 || outputTokens != 2 {
		t.Fatalf("usage = input:%d output:%d, want input:12 output:2", inputTokens, outputTokens)
	}
}

func configureKiroTest(t *testing.T, configYAML string) {
	t.Helper()
	request, errMarshal := json.Marshal(lifecycleRequest{ConfigYAML: []byte(configYAML)})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if errConfigure := configure(request); errConfigure != nil {
		t.Fatal(errConfigure)
	}
	t.Cleanup(func() {
		if errConfigure := configure(nil); errConfigure != nil {
			t.Errorf("restore default configuration: %v", errConfigure)
		}
	})
}

func callModelsForAuthTest(t *testing.T, authID, hostCallbackID string) pluginapi.ModelResponse {
	t.Helper()
	rawRequest, errMarshal := json.Marshal(rpcAuthModelRequest{
		AuthModelRequest: pluginapi.AuthModelRequest{
			AuthID:      authID,
			StorageJSON: []byte(`{"type":"kiro","api_key_env":"KIRO_TEST_KEY","region":"us-east-1"}`),
		},
		HostCallbackID: hostCallbackID,
	})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	rawResponse, errModels := modelsForAuth(rawRequest)
	if errModels != nil {
		t.Fatal(errModels)
	}
	var env envelope
	if errUnmarshal := json.Unmarshal(rawResponse, &env); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if !env.OK {
		t.Fatalf("modelsForAuth() envelope = %#v", env)
	}
	var response pluginapi.ModelResponse
	if errUnmarshal := json.Unmarshal(env.Result, &response); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	return response
}

type hostMock struct {
	mu      sync.Mutex
	chunks  [][]byte
	request rpcHostHTTPRequest
	emitted [][]byte
	closed  chan struct{}
	once    sync.Once
}

func newHostMock(stream []byte) *hostMock {
	middle := len(stream) / 2
	return &hostMock{chunks: [][]byte{stream[:middle], stream[middle:]}, closed: make(chan struct{})}
}

func (m *hostMock) call(method string, payload any) (json.RawMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch method {
	case pluginabi.MethodHostHTTPDoStream:
		m.request = payload.(rpcHostHTTPRequest)
		return mustJSONRaw(rpcHostHTTPStreamResponse{StatusCode: http.StatusOK, StreamID: "upstream-1"}), nil
	case pluginabi.MethodHostHTTPStreamRead:
		if len(m.chunks) == 0 {
			return mustJSONRaw(rpcHostHTTPStreamReadResponse{Done: true}), nil
		}
		chunk := m.chunks[0]
		m.chunks = m.chunks[1:]
		return mustJSONRaw(rpcHostHTTPStreamReadResponse{Payload: chunk, Done: len(m.chunks) == 0}), nil
	case pluginabi.MethodHostHTTPStreamClose:
		return json.RawMessage(`{}`), nil
	case pluginabi.MethodHostStreamEmit:
		request := payload.(rpcStreamEmitRequest)
		m.emitted = append(m.emitted, append([]byte(nil), request.Payload...))
		return json.RawMessage(`{}`), nil
	case pluginabi.MethodHostStreamClose:
		m.once.Do(func() { close(m.closed) })
		return json.RawMessage(`{}`), nil
	default:
		return nil, os.ErrInvalid
	}
}

func (m *hostMock) emittedBytes() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return []byte(strings.Join(byteStrings(m.emitted), ""))
}

func byteStrings(values [][]byte) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, string(value))
	}
	return out
}

func mustJSONRaw(value any) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}

func testEventFrame(t *testing.T, eventType string, payload any) []byte {
	t.Helper()
	headers := append(testEventHeader(":message-type", "event"), testEventHeader(":event-type", eventType)...)
	body, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	totalLength := 16 + len(headers) + len(body)
	frame := make([]byte, totalLength)
	binary.BigEndian.PutUint32(frame[0:4], uint32(totalLength))
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(headers)))
	binary.BigEndian.PutUint32(frame[8:12], crc32.ChecksumIEEE(frame[:8]))
	copy(frame[12:], headers)
	copy(frame[12+len(headers):], body)
	binary.BigEndian.PutUint32(frame[totalLength-4:], crc32.ChecksumIEEE(frame[:totalLength-4]))
	return frame
}

func testEventHeader(name, value string) []byte {
	header := []byte{byte(len(name))}
	header = append(header, name...)
	header = append(header, 7, byte(len(value)>>8), byte(len(value)))
	header = append(header, value...)
	return header
}
