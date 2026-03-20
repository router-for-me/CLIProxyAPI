package responses

import (
	"encoding/json"
	"fmt"
	"testing"
)

func BenchmarkConvertOpenAIResponsesRequestToCodex_LargeInput(b *testing.B) {
	rawJSON := benchmarkResponsesRequestJSON(128, 5)
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		output := ConvertOpenAIResponsesRequestToCodex("gpt-5.2", rawJSON, false)
		if len(output) == 0 {
			b.Fatal("expected non-empty translated payload")
		}
	}
}

func BenchmarkConvertOpenAIResponsesRequestToCodex_LargeInput_NoSystemMessages(b *testing.B) {
	rawJSON := benchmarkResponsesRequestJSON(128, 0)
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		output := ConvertOpenAIResponsesRequestToCodex("gpt-5.2", rawJSON, false)
		if len(output) == 0 {
			b.Fatal("expected non-empty translated payload")
		}
	}
}

func benchmarkResponsesRequestJSON(messageCount int, systemEvery int) []byte {
	input := make([]any, 0, messageCount)
	for i := 0; i < messageCount; i++ {
		role := "user"
		if systemEvery > 0 && i%systemEvery == 0 {
			role = "system"
		}
		input = append(input, map[string]any{
			"type": "message",
			"role": role,
			"content": []any{
				map[string]any{
					"type": "input_text",
					"text": fmt.Sprintf("message-%d-%s", i, role),
				},
			},
		})
	}

	payload := map[string]any{
		"model":                 "gpt-5.2",
		"stream":                false,
		"store":                 true,
		"parallel_tool_calls":   false,
		"service_tier":          "standard",
		"max_output_tokens":     4096,
		"max_completion_tokens": 4096,
		"temperature":           0.2,
		"top_p":                 0.95,
		"truncation":            "auto",
		"user":                  "benchmark-user",
		"context_management": map[string]any{
			"type": "compaction",
		},
		"input": input,
	}

	rawJSON, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return rawJSON
}
