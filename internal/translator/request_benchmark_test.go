package translator

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	translatorapi "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/translator"
	"github.com/tidwall/gjson"
)

func BenchmarkRequestTranslationLargeHistory(b *testing.B) {
	benchmarkRequestTranslation(b, 64)
}

func BenchmarkRequestTranslationHistorySizes(b *testing.B) {
	for _, turns := range []int{0, 1, 4, 16, 64} {
		b.Run(fmt.Sprintf("turns_%d", turns), func(b *testing.B) {
			benchmarkRequestTranslation(b, turns)
		})
	}
}

func benchmarkRequestTranslation(b *testing.B, turns int) {
	requests := map[string][]byte{
		"claude":          benchmarkClaudeRequest(turns),
		"gemini":          benchmarkGeminiRequest(turns),
		"openai":          benchmarkOpenAIRequest(turns),
		"openai-response": benchmarkOpenAIResponsesRequest(turns),
		"interactions":    benchmarkInteractionsRequest(turns),
	}
	routes := []struct {
		source  string
		targets []string
	}{
		{source: "claude", targets: []string{"openai", "gemini", "codex", "interactions", "antigravity"}},
		{source: "gemini", targets: []string{"openai", "claude", "codex", "interactions", "antigravity", "gemini"}},
		{source: "openai", targets: []string{"claude", "gemini", "codex", "interactions", "antigravity"}},
		{source: "openai-response", targets: []string{"claude", "gemini", "codex", "interactions", "openai"}},
		{source: "interactions", targets: []string{"claude", "gemini", "codex", "openai", "openai-response", "antigravity"}},
	}

	for _, route := range routes {
		request := requests[route.source]
		for _, target := range route.targets {
			b.Run(route.source+"_to_"+target, func(b *testing.B) {
				if output := translatorapi.Request(route.source, target, "gemini-2.5-pro", request, true); !gjson.ValidBytes(output) {
					b.Fatalf("translator generated invalid JSON: %s", output)
				}
				b.ReportAllocs()
				b.SetBytes(int64(len(request)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					translatorapi.Request(route.source, target, "gemini-2.5-pro", request, true)
				}
			})
		}
	}
}

func benchmarkClaudeRequest(turns int) []byte {
	payload := strings.Repeat("x", 1024)
	messages := make([]any, 0, turns*2)
	for i := 0; i < turns; i++ {
		callID := fmt.Sprintf("call_%d", i)
		messages = append(messages,
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": payload},
				map[string]any{"type": "tool_use", "id": callID, "name": "lookup", "input": map[string]any{"query": payload}},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": callID, "content": []any{map[string]any{"type": "text", "text": payload}}},
			}},
		)
	}
	return benchmarkJSON(map[string]any{
		"system":   []any{map[string]any{"type": "text", "text": payload}},
		"messages": messages,
		"tools":    []any{map[string]any{"name": "lookup", "description": payload, "input_schema": benchmarkSchema()}},
	})
}

func benchmarkGeminiRequest(turns int) []byte {
	payload := strings.Repeat("x", 1024)
	contents := make([]any, 0, turns*2)
	for i := 0; i < turns; i++ {
		callID := fmt.Sprintf("call_%d", i)
		contents = append(contents,
			map[string]any{"role": "model", "parts": []any{
				map[string]any{"text": payload},
				map[string]any{"functionCall": map[string]any{"id": callID, "name": "lookup", "args": map[string]any{"query": payload}}},
			}},
			map[string]any{"role": "user", "parts": []any{
				map[string]any{"functionResponse": map[string]any{"id": callID, "name": "lookup", "response": map[string]any{"result": payload}}},
			}},
		)
	}
	return benchmarkJSON(map[string]any{
		"system_instruction": map[string]any{"parts": []any{map[string]any{"text": payload}}},
		"contents":           contents,
		"tools": []any{map[string]any{"functionDeclarations": []any{
			map[string]any{"name": "lookup", "description": payload, "parameters": benchmarkSchema()},
		}}},
	})
}

func benchmarkOpenAIRequest(turns int) []byte {
	payload := strings.Repeat("x", 1024)
	messages := make([]any, 0, turns*2+1)
	messages = append(messages, map[string]any{"role": "system", "content": payload})
	for i := 0; i < turns; i++ {
		callID := fmt.Sprintf("call_%d", i)
		messages = append(messages,
			map[string]any{"role": "assistant", "content": payload, "tool_calls": []any{
				map[string]any{"id": callID, "type": "function", "function": map[string]any{"name": "lookup", "arguments": `{"query":"value"}`}},
			}},
			map[string]any{"role": "tool", "tool_call_id": callID, "content": payload},
		)
	}
	return benchmarkJSON(map[string]any{
		"messages": messages,
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{
			"name": "lookup", "description": payload, "parameters": benchmarkSchema(),
		}}},
	})
}

func benchmarkOpenAIResponsesRequest(turns int) []byte {
	payload := strings.Repeat("x", 1024)
	input := make([]any, 0, turns*3)
	for i := 0; i < turns; i++ {
		callID := fmt.Sprintf("call_%d", i)
		input = append(input,
			map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": payload}}},
			map[string]any{"type": "function_call", "call_id": callID, "name": "lookup", "arguments": `{"query":"value"}`},
			map[string]any{"type": "function_call_output", "call_id": callID, "output": payload},
		)
	}
	return benchmarkJSON(map[string]any{
		"instructions": payload,
		"input":        input,
		"tools": []any{map[string]any{
			"type": "function", "name": "lookup", "description": payload, "parameters": benchmarkSchema(),
		}},
	})
}

func benchmarkInteractionsRequest(turns int) []byte {
	payload := strings.Repeat("x", 1024)
	input := make([]any, 0, turns*3)
	for i := 0; i < turns; i++ {
		callID := fmt.Sprintf("call_%d", i)
		input = append(input,
			map[string]any{"type": "model_output", "content": []any{map[string]any{"type": "text", "text": payload}}},
			map[string]any{"type": "function_call", "call_id": callID, "name": "lookup", "arguments": map[string]any{"query": payload}},
			map[string]any{"type": "function_result", "call_id": callID, "name": "lookup", "result": payload},
		)
	}
	return benchmarkJSON(map[string]any{
		"system_instruction": payload,
		"input":              input,
		"tools": []any{map[string]any{"function_declarations": []any{
			map[string]any{"name": "lookup", "description": payload, "parameters": benchmarkSchema()},
		}}},
	})
}

func benchmarkSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string"},
		},
	}
}

func benchmarkJSON(value any) []byte {
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		panic(errMarshal)
	}
	return raw
}
