package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	openairesponses "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/openai/responses"
)

func TestBridgeFiftyRoundsKeepBudgetAndCallHistory(t *testing.T) {
	t.Cleanup(func() { _ = applyPluginConfig(nil) })
	t.Cleanup(clearRequestStates)
	if errConfigure := applyPluginConfig([]byte(
		"max_active_tool_bytes: 8192\n" +
			"max_request_states: 64\n" +
			"max_state_bytes: 4194304\n",
	)); errConfigure != nil {
		t.Fatalf("applyPluginConfig() error = %v", errConfigure)
	}

	const roundCount = 50
	requestID := "fifty-round-bridge"
	children := make([]map[string]any, 0, roundCount)
	for index := 0; index < roundCount; index++ {
		children = append(children, map[string]any{
			"type":          "function",
			"name":          fmt.Sprintf("round_tool_%02d", index),
			"defer_loading": true,
			"description":   fmt.Sprintf("round %02d deferred tool", index),
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"value": map[string]any{"type": "string"}},
				"required":   []any{"value"},
			},
		})
	}
	tools := []any{
		map[string]any{
			"type":        "tool_search",
			"execution":   "client",
			"description": "search deferred tools",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"query": map[string]any{"type": "string"}},
				"required":   []any{"query"},
			},
		},
		map[string]any{
			"type":        "function",
			"name":        "exec_command",
			"description": "run a command",
			"parameters":  map[string]any{"type": "object"},
		},
		map[string]any{
			"type":  "namespace",
			"name":  "mcp__round",
			"tools": children,
		},
	}
	input := []any{
		map[string]any{
			"type":    "message",
			"role":    "user",
			"content": []any{map[string]any{"type": "input_text", "text": "exercise fifty rounds"}},
		},
	}
	var lastUpstreamBody []byte
	var lastOriginalBody []byte

	for index := 0; index < roundCount; index++ {
		toolName := fmt.Sprintf("round_tool_%02d", index)
		searchID := fmt.Sprintf("search-%02d", index)
		callID := fmt.Sprintf("call-%02d", index)
		input = append(input,
			map[string]any{
				"type":      "tool_search_call",
				"call_id":   searchID,
				"execution": "client",
				"status":    "completed",
				"arguments": map[string]any{"query": toolName},
			},
			map[string]any{
				"type":      "tool_search_output",
				"call_id":   searchID,
				"execution": "client",
				"status":    "completed",
				"tools": []any{
					map[string]any{
						"type":  "namespace",
						"name":  "mcp__round",
						"tools": []any{children[index]},
					},
				},
			},
			map[string]any{
				"type":      "function_call",
				"call_id":   callID,
				"namespace": "mcp__round",
				"name":      toolName,
				"arguments": map[string]any{"value": fmt.Sprintf("round-%02d", index)},
			},
			map[string]any{
				"type":    "function_call_output",
				"call_id": callID,
				"output":  fmt.Sprintf("round-%02d-ok", index),
			},
		)

		originalBody := mustJSON(t, map[string]any{
			"model": "bridge-fifty",
			"tools": tools,
			"input": input,
		})
		pluginBody := runBridgeIntegrationRequest(t, requestID, "openai", originalBody)
		activeBytes, errActive := activeToolArraysBytesForTest(t, pluginBody)
		if errActive != nil {
			t.Fatalf("round %d active tool bytes: %v", index, errActive)
		}
		if activeBytes > 8192 {
			t.Fatalf("round %d active tool bytes = %d, want <= 8192", index, activeBytes)
		}

		catalog := extractToolCatalog(originalBody)
		identity := toolIdentity{Namespace: "mcp__round", Name: toolName, Kind: "function"}
		alias := catalog.aliasByID[identity]
		if alias == "" {
			t.Fatalf("round %d alias is empty", index)
		}
		upstreamBody := openairesponses.ConvertOpenAIResponsesRequestToOpenAIChatCompletions(
			"bridge-fifty",
			pluginBody,
			false,
		)
		names := bridgeIntegrationToolNames(t, upstreamBody)
		if !names[alias] {
			t.Fatalf("round %d activated alias %q missing upstream; names=%v", index, alias, bridgeIntegrationNameList(names))
		}
		if names[toolName] {
			t.Fatalf("round %d deferred original name %q reached upstream", index, toolName)
		}
		for previous := 0; previous <= index; previous++ {
			for _, callID := range []string{
				fmt.Sprintf("search-%02d", previous),
				fmt.Sprintf("call-%02d", previous),
			} {
				if !bytes.Contains(upstreamBody, []byte(callID)) {
					t.Fatalf("round %d lost historical call_id %q", index, callID)
				}
			}
		}

		lastOriginalBody = originalBody
		lastUpstreamBody = upstreamBody
	}

	finalResponse := []byte(`{"id":"chatcmpl-fifty","object":"chat.completion","created":1,"model":"bridge-fifty","choices":[{"index":0,"message":{"role":"assistant","content":"round-49-ok"},"finish_reason":"stop"}]}`)
	clientResponse := runBridgeIntegrationProviderResponse(
		t,
		requestID,
		lastOriginalBody,
		lastUpstreamBody,
		finalResponse,
		func(originalRequest, upstreamRequest, raw []byte) []byte {
			return openairesponses.ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(
				context.Background(),
				"bridge-fifty",
				originalRequest,
				upstreamRequest,
				raw,
				nil,
			)
		},
	)
	if finalOutput := bridgeIntegrationClientOutput(t, clientResponse); !strings.Contains(string(finalOutput), "round-49-ok") {
		t.Fatalf("final functional result missing: %s", clientResponse)
	}

	entries, stateBytes := requestStateUsage()
	if entries != 1 || stateBytes <= 0 || stateBytes > 4194304 {
		t.Fatalf("state usage after fifty rounds = entries:%d bytes:%d", entries, stateBytes)
	}
	stateBytesBeforeCompletion := stateBytes
	completeStateRequest(t, requestID, "success")
	entries, stateBytes = requestStateUsage()
	if entries != 0 || stateBytes != 0 {
		t.Fatalf("state usage after completion = entries:%d bytes:%d", entries, stateBytes)
	}
	t.Logf("fifty rounds: active_bytes_max<=8192 state_bytes=%d call_ids=%d", stateBytesBeforeCompletion, roundCount*2)
}

func activeToolArraysBytesForTest(t *testing.T, body []byte) (int, error) {
	t.Helper()
	var root map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if errDecode := decoder.Decode(&root); errDecode != nil {
		return 0, errDecode
	}
	return activeToolArraysBytes(root)
}
