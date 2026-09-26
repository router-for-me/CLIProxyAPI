package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	clauderesponses "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/claude/openai/responses"
	geminiresponses "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/gemini/openai/responses"
	openairesponses "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/openai/responses"
)

type bridgeIntegrationCase struct {
	name              string
	toFormat          string
	model             string
	translateRequest  func(string, []byte, bool) []byte
	translateResponse func([]byte, []byte, []byte) []byte
	searchResponse    func() []byte
	activatedResponse func(string) []byte
	finalResponse     func() []byte
}

func TestBridgeClosesLoopAcrossTranslatedProviders(t *testing.T) {
	t.Cleanup(clearRequestStates)

	cases := []bridgeIntegrationCase{
		{
			name:     "chat",
			toFormat: "openai",
			model:    "bridge-chat",
			translateRequest: func(model string, body []byte, stream bool) []byte {
				return openairesponses.ConvertOpenAIResponsesRequestToOpenAIChatCompletions(model, body, stream)
			},
			translateResponse: func(originalRequest, upstreamRequest, raw []byte) []byte {
				return openairesponses.ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(
					context.Background(), "bridge-chat", originalRequest, upstreamRequest, raw, nil)
			},
			searchResponse: func() []byte {
				return []byte(`{"id":"chatcmpl-search","object":"chat.completion","created":1,"model":"bridge-chat","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"search-1","type":"function","function":{"name":"tool_search","arguments":"{\"query\":\"weather\"}"}}]},"finish_reason":"tool_calls"}]}`)
			},
			activatedResponse: func(name string) []byte {
				return []byte(fmt.Sprintf(`{"id":"chatcmpl-call","object":"chat.completion","created":1,"model":"bridge-chat","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call-lookup","type":"function","function":{"name":%q,"arguments":"{\"city\":\"Paris\"}"}}]},"finish_reason":"tool_calls"}]}`, name))
			},
			finalResponse: func() []byte {
				return []byte(`{"id":"chatcmpl-final","object":"chat.completion","created":1,"model":"bridge-chat","choices":[{"index":0,"message":{"role":"assistant","content":"temperature=21"},"finish_reason":"stop"}]}`)
			},
		},
		{
			name:     "claude",
			toFormat: "claude",
			model:    "bridge-claude",
			translateRequest: func(model string, body []byte, stream bool) []byte {
				return clauderesponses.ConvertOpenAIResponsesRequestToClaude(model, body, stream)
			},
			translateResponse: func(originalRequest, upstreamRequest, raw []byte) []byte {
				return clauderesponses.ConvertClaudeResponseToOpenAIResponsesNonStream(
					context.Background(), "bridge-claude", originalRequest, upstreamRequest, raw, nil)
			},
			searchResponse: func() []byte {
				return []byte(strings.Join([]string{
					`data: {"type":"message_start","message":{"id":"msg-search","usage":{"input_tokens":1,"output_tokens":1}}}`,
					`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"search-1","name":"tool_search","input":{}}}`,
					`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"weather\"}"}}`,
					`data: {"type":"content_block_stop","index":0}`,
					`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":1}}`,
					`data: {"type":"message_stop"}`,
				}, "\n"))
			},
			activatedResponse: func(name string) []byte {
				return []byte(strings.Join([]string{
					`data: {"type":"message_start","message":{"id":"msg-call","usage":{"input_tokens":1,"output_tokens":1}}}`,
					fmt.Sprintf(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call-lookup","name":%q,"input":{}}}`, name),
					`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"Paris\"}"}}`,
					`data: {"type":"content_block_stop","index":0}`,
					`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":1}}`,
					`data: {"type":"message_stop"}`,
				}, "\n"))
			},
			finalResponse: func() []byte {
				return []byte(strings.Join([]string{
					`data: {"type":"message_start","message":{"id":"msg-final","usage":{"input_tokens":1,"output_tokens":1}}}`,
					`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
					`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"temperature=21"}}`,
					`data: {"type":"content_block_stop","index":0}`,
					`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
					`data: {"type":"message_stop"}`,
				}, "\n"))
			},
		},
		{
			name:     "gemini",
			toFormat: "gemini",
			model:    "bridge-gemini",
			translateRequest: func(model string, body []byte, stream bool) []byte {
				return geminiresponses.ConvertOpenAIResponsesRequestToGemini(model, body, stream)
			},
			translateResponse: func(originalRequest, upstreamRequest, raw []byte) []byte {
				return geminiresponses.ConvertGeminiResponseToOpenAIResponsesNonStream(
					context.Background(), "bridge-gemini", originalRequest, upstreamRequest, raw, nil)
			},
			searchResponse: func() []byte {
				return []byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"tool_search","args":{"query":"weather"}}}]},"finishReason":"STOP"}],"responseId":"search-1"}`)
			},
			activatedResponse: func(name string) []byte {
				return []byte(fmt.Sprintf(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":%q,"args":{"city":"Paris"}}}]},"finishReason":"STOP"}],"responseId":"call-lookup"}`, name))
			},
			finalResponse: func() []byte {
				return []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"temperature=21"}]},"finishReason":"STOP"}],"responseId":"final"}`)
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			firstRequest := bridgeIntegrationFirstRequest()
			firstPluginBody := runBridgeIntegrationRequest(t, testCase.name+"-first", testCase.toFormat, firstRequest)
			firstUpstreamBody := testCase.translateRequest(testCase.model, firstPluginBody, false)
			assertBridgeIntegrationUpstreamTools(t, firstUpstreamBody, []string{toolSearchName, "exec_command"}, []string{"lookup_weather"}, true)

			firstClientResponse := runBridgeIntegrationProviderResponse(
				t, testCase.name+"-first", firstPluginBody, firstUpstreamBody, testCase.searchResponse(), testCase.translateResponse)
			assertBridgeIntegrationToolCall(t, firstClientResponse, "tool_search_call", "", "query", "weather")
			if firstOutput := bridgeIntegrationClientOutput(t, firstClientResponse); strings.Contains(string(firstOutput), "lookup_schema_marker") {
				t.Fatalf("search response leaked a deferred schema: %s", firstOutput)
			}

			secondRequest := bridgeIntegrationDiscoveryRequest()
			secondPluginBody := runBridgeIntegrationRequest(t, testCase.name+"-second", testCase.toFormat, secondRequest)
			identity := toolIdentity{Namespace: "mcp__weather", Name: "lookup_weather", Kind: "function"}
			activatedAlias := extractToolCatalog(secondRequest).aliasByID[identity]
			if activatedAlias == "" {
				t.Fatal("activated tool alias is empty")
			}
			secondUpstreamBody := testCase.translateRequest(testCase.model, secondPluginBody, false)
			assertBridgeIntegrationUpstreamTools(t, secondUpstreamBody, []string{toolSearchName, "exec_command", activatedAlias}, []string{"lookup_weather"}, false)
			if !strings.Contains(string(secondUpstreamBody), "lookup_schema_marker") {
				t.Fatalf("activated schema is missing upstream: %s", secondUpstreamBody)
			}

			secondClientResponse := runBridgeIntegrationProviderResponse(
				t, testCase.name+"-second", secondPluginBody, secondUpstreamBody, testCase.activatedResponse(activatedAlias), testCase.translateResponse)
			assertBridgeIntegrationToolCall(t, secondClientResponse, "function_call", "mcp__weather", "city", "Paris")
			if secondOutput := bridgeIntegrationClientOutput(t, secondClientResponse); strings.Contains(string(secondOutput), activatedAlias) {
				t.Fatalf("activated alias leaked to the client output: %s", secondOutput)
			}

			thirdRequest := bridgeIntegrationToolResultRequest()
			thirdPluginBody := runBridgeIntegrationRequest(t, testCase.name+"-third", testCase.toFormat, thirdRequest)
			thirdUpstreamBody := testCase.translateRequest(testCase.model, thirdPluginBody, false)
			assertBridgeIntegrationUpstreamTools(t, thirdUpstreamBody, []string{toolSearchName, "exec_command", activatedAlias}, []string{"lookup_weather"}, false)
			if strings.Contains(string(thirdUpstreamBody), "mcp__weather__lookup_weather") {
				t.Fatalf("activated tool history did not use the upstream alias: %s", thirdUpstreamBody)
			}
			if strings.Count(string(thirdUpstreamBody), activatedAlias) < 2 {
				t.Fatalf("alias is missing from upstream history: %s", thirdUpstreamBody)
			}
			if !strings.Contains(string(thirdUpstreamBody), "temperature=21") {
				t.Fatalf("tool result did not reach the upstream: %s", thirdUpstreamBody)
			}

			finalClientResponse := runBridgeIntegrationProviderResponse(
				t, testCase.name+"-third", thirdPluginBody, thirdUpstreamBody, testCase.finalResponse(), testCase.translateResponse)
			finalOutput := bridgeIntegrationClientOutput(t, finalClientResponse)
			if !strings.Contains(string(finalOutput), "temperature=21") {
				t.Fatalf("final answer changed: %s", finalClientResponse)
			}
			if strings.Contains(string(finalOutput), `"type":"tool_search_call"`) {
				t.Fatalf("final answer was rewritten as a search call: %s", finalClientResponse)
			}
		})
	}
}

func TestBridgeFirstPacketWith500DeferredToolsUsesRealTranslators(t *testing.T) {
	t.Cleanup(clearRequestStates)

	original := bridgeIntegrationLargeRequest(500)
	pluginBody := runBridgeIntegrationRequest(t, "large-catalog", "openai", original)
	if strings.Contains(string(pluginBody), "deferred_schema_marker") {
		t.Fatalf("deferred schema leaked through the plugin: %d bytes", len(pluginBody))
	}
	if len(pluginBody) >= len(original) {
		t.Fatalf("plugin did not reduce the first packet: original=%d plugin=%d", len(original), len(pluginBody))
	}

	translatorCases := []struct {
		name      string
		toFormat  string
		translate func(string, []byte, bool) []byte
	}{
		{"chat", "openai", openairesponses.ConvertOpenAIResponsesRequestToOpenAIChatCompletions},
		{"claude", "claude", clauderesponses.ConvertOpenAIResponsesRequestToClaude},
		{"gemini", "gemini", geminiresponses.ConvertOpenAIResponsesRequestToGemini},
	}
	for _, translatorCase := range translatorCases {
		t.Run(translatorCase.name, func(t *testing.T) {
			upstream := translatorCase.translate("large-"+translatorCase.name, pluginBody, false)
			names := bridgeIntegrationToolNames(t, upstream)
			bridgeIntegrationRequireNames(t, names, []string{toolSearchName, "exec_command"})
			for index := 0; index < 500; index++ {
				if names["deferred_"+fmt.Sprintf("%03d", index)] {
					t.Fatalf("deferred tool %d reached the upstream", index)
				}
			}
			if strings.Contains(string(upstream), "deferred_schema_marker") {
				t.Fatalf("deferred schema reached the upstream: %d bytes", len(upstream))
			}
			t.Logf("bytes original=%d plugin=%d upstream=%d", len(original), len(pluginBody), len(upstream))
		})
	}
}

func bridgeIntegrationFirstRequest() []byte {
	return []byte(`{
		"model":"bridge-integration",
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"check weather"}]}],
		"tools":[
			{"type":"tool_search","execution":"client","description":"search deferred tools","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}},
			{"type":"function","name":"exec_command","description":"run a command","parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}},
			{"type":"namespace","name":"mcp__weather","tools":[
				{"type":"function","name":"lookup_weather","defer_loading":true,"description":"lookup_schema_marker","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}
			]}
		]
	}`)
}

func bridgeIntegrationDiscoveryRequest() []byte {
	return []byte(`{
		"model":"bridge-integration",
		"tools":[
			{"type":"tool_search","execution":"client","description":"search deferred tools","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}},
			{"type":"function","name":"exec_command","description":"run a command","parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}},
			{"type":"namespace","name":"mcp__weather","tools":[
				{"type":"function","name":"lookup_weather","defer_loading":true,"description":"lookup_schema_marker","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}
			]}
		],
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"check weather"}]},
			{"type":"tool_search_call","call_id":"search-1","execution":"client","status":"completed","arguments":{"query":"weather"}},
			{"type":"tool_search_output","call_id":"search-1","execution":"client","status":"completed","tools":[
				{"type":"namespace","name":"mcp__weather","tools":[
					{"type":"function","name":"lookup_weather","description":"lookup_schema_marker","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}
				]}
			]}
		]
	}`)
}

func bridgeIntegrationToolResultRequest() []byte {
	return []byte(`{
		"model":"bridge-integration",
		"tools":[
			{"type":"tool_search","execution":"client","description":"search deferred tools","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}},
			{"type":"function","name":"exec_command","description":"run a command","parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}},
			{"type":"namespace","name":"mcp__weather","tools":[
				{"type":"function","name":"lookup_weather","defer_loading":true,"description":"lookup_schema_marker","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}
			]}
		],
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"check weather"}]},
			{"type":"tool_search_call","call_id":"search-1","execution":"client","status":"completed","arguments":{"query":"weather"}},
			{"type":"tool_search_output","call_id":"search-1","execution":"client","status":"completed","tools":[
				{"type":"namespace","name":"mcp__weather","tools":[
					{"type":"function","name":"lookup_weather","description":"lookup_schema_marker","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}
				]}
			]},
			{"type":"function_call","call_id":"call-lookup","namespace":"mcp__weather","name":"lookup_weather","arguments":"{\"city\":\"Paris\"}"},
			{"type":"function_call_output","call_id":"call-lookup","output":"temperature=21"}
		]
	}`)
}

func bridgeIntegrationLargeRequest(count int) []byte {
	tools := []map[string]any{
		{"type": "tool_search", "execution": "client", "description": "search deferred tools", "parameters": map[string]any{"type": "object"}},
		{"type": "function", "name": "exec_command", "description": "run a command", "parameters": map[string]any{"type": "object"}},
	}
	children := make([]map[string]any, 0, count)
	for index := 0; index < count; index++ {
		name := "deferred_" + fmt.Sprintf("%03d", index)
		children = append(children, map[string]any{
			"type":          "function",
			"name":          name,
			"defer_loading": true,
			"description":   strings.Repeat("deferred_schema_marker ", 80),
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"argument": map[string]any{"type": "string"}},
				"required":   []any{"argument"},
			},
		})
	}
	tools = append(tools, map[string]any{
		"type":  "namespace",
		"name":  "mcp__large",
		"tools": children,
	})
	encoded, errMarshal := json.Marshal(map[string]any{
		"model": "bridge-integration",
		"tools": tools,
	})
	if errMarshal != nil {
		panic(errMarshal)
	}
	return encoded
}

func runBridgeIntegrationRequest(t *testing.T, requestID, toFormat string, body []byte) []byte {
	t.Helper()
	raw, errIntercept := interceptRequest("request_after", mustJSON(t, map[string]any{
		"RequestID":    requestID,
		"SourceFormat": "openai-response",
		"ToFormat":     toFormat,
		"Model":        "bridge-integration",
		"Body":         body,
	}))
	if errIntercept != nil {
		t.Fatalf("interceptRequest() error = %v", errIntercept)
	}
	result := decodeRequestInterceptResult(t, raw)
	if result.Terminate {
		t.Fatalf("interceptRequest() terminated: %#v", result)
	}
	if len(result.Body) > 0 {
		return result.Body
	}
	return body
}

func runBridgeIntegrationProviderResponse(
	t *testing.T,
	requestID string,
	originalRequest []byte,
	upstreamRequest []byte,
	rawProviderResponse []byte,
	translate func([]byte, []byte, []byte) []byte,
) []byte {
	t.Helper()
	translated := translate(originalRequest, upstreamRequest, rawProviderResponse)
	raw, errResponse := interceptResponse(mustJSON(t, map[string]any{
		"RequestID":       requestID,
		"SourceFormat":    "openai-response",
		"OriginalRequest": originalRequest,
		"RequestBody":     upstreamRequest,
		"Body":            translated,
	}))
	if errResponse != nil {
		t.Fatalf("interceptResponse() error = %v", errResponse)
	}
	rewritten := decodeResponseBodyResult(t, raw)
	if len(rewritten) > 0 {
		return rewritten
	}
	return translated
}

func assertBridgeIntegrationUpstreamTools(t *testing.T, body []byte, required []string, forbidden []string, requireDeferredMarker bool) {
	t.Helper()
	names := bridgeIntegrationToolNames(t, body)
	bridgeIntegrationRequireNames(t, names, required)
	for _, name := range forbidden {
		if names[name] {
			t.Fatalf("deferred local tool name reached upstream tools: %s", name)
		}
	}
	hasMarker := strings.Contains(string(body), "lookup_schema_marker")
	if requireDeferredMarker && hasMarker {
		t.Fatalf("deferred schema reached upstream: %s", body)
	}
	if !requireDeferredMarker && !hasMarker {
		t.Fatalf("expected schema marker is missing upstream: %s", body)
	}
}

func bridgeIntegrationToolNames(t *testing.T, body []byte) map[string]bool {
	t.Helper()
	var root map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if errDecode := decoder.Decode(&root); errDecode != nil {
		t.Fatalf("decode upstream request: %v", errDecode)
	}
	names := make(map[string]bool)
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			if name, ok := typed["name"].(string); ok {
				names[name] = true
			}
			for key, child := range typed {
				if key == "input" || key == "messages" || key == "contents" {
					continue
				}
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(root["tools"])
	return names
}

func bridgeIntegrationRequireNames(t *testing.T, names map[string]bool, required []string) {
	t.Helper()
	for _, name := range required {
		if !names[name] {
			t.Fatalf("required upstream tool %q missing; names=%v", name, bridgeIntegrationNameList(names))
		}
	}
}

func bridgeIntegrationNameList(names map[string]bool) []string {
	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	return out
}

func assertBridgeIntegrationToolCall(t *testing.T, body []byte, wantType, wantNamespace, argumentKey, argumentValue string) {
	t.Helper()
	var root struct {
		Output []map[string]any `json:"output"`
	}
	if errUnmarshal := json.Unmarshal(body, &root); errUnmarshal != nil {
		t.Fatalf("decode client response: %v; body=%s", errUnmarshal, body)
	}
	var call map[string]any
	for _, item := range root.Output {
		if item["type"] == wantType {
			call = item
			break
		}
	}
	if call == nil {
		t.Fatalf("%s missing from client response: %s", wantType, body)
	}
	if got := call["namespace"]; wantNamespace == "" {
		if got != nil {
			t.Fatalf("namespace = %#v, want absent", got)
		}
	} else if got, _ := call["namespace"].(string); got != wantNamespace {
		t.Fatalf("namespace = %#v, want %q", call["namespace"], wantNamespace)
	}
	if wantType == "tool_search_call" {
		if got := call["execution"]; got != "client" {
			t.Fatalf("execution = %#v, want client", got)
		}
	} else if name, _ := call["name"].(string); name != "lookup_weather" {
		t.Fatalf("name = %#v, want lookup_weather", call["name"])
	}
	var arguments map[string]any
	switch value := call["arguments"].(type) {
	case map[string]any:
		arguments = value
	case string:
		if errUnmarshal := json.Unmarshal([]byte(value), &arguments); errUnmarshal != nil {
			t.Fatalf("decode function arguments: %v; value=%q", errUnmarshal, value)
		}
	default:
		t.Fatalf("arguments = %#v, want object or JSON string", call["arguments"])
	}
	if got := arguments[argumentKey]; got != argumentValue {
		t.Fatalf("argument %s = %#v, want %q", argumentKey, got, argumentValue)
	}
}

func bridgeIntegrationClientOutput(t *testing.T, body []byte) []byte {
	t.Helper()
	var root struct {
		Output json.RawMessage `json:"output"`
	}
	if errUnmarshal := json.Unmarshal(body, &root); errUnmarshal != nil {
		t.Fatalf("decode client output: %v; body=%s", errUnmarshal, body)
	}
	if len(root.Output) == 0 {
		t.Fatalf("client output is empty: %s", body)
	}
	return root.Output
}
