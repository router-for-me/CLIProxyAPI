package responses

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

var benchmarkOpenAIResponsesRequest = []byte(`{
  "model": "gpt-5.4",
  "input": [
    {
      "type": "message",
      "role": "system",
      "content": [
        {
          "type": "input_text",
          "text": "You are a careful coding assistant. Prefer minimal diffs and preserve behavior."
        }
      ]
    },
    {
      "type": "message",
      "role": "user",
      "content": [
        {
          "type": "input_text",
          "text": "Refactor this handler and keep compatibility with the existing streaming contract."
        }
      ]
    }
  ],
  "tools": [
    {
      "type": "function",
      "name": "lookup_issue",
      "description": "Lookup an issue by id",
      "parameters": {
        "type": "object",
        "properties": {
          "id": {
            "type": "string"
          }
        },
        "required": [
          "id"
        ]
      }
    }
  ],
  "text": {
    "format": {
      "name": "json_schema",
      "schema": {
        "type": "object",
        "properties": {
          "result": {
            "type": "string"
          }
        },
        "required": [
          "result"
        ]
      }
    }
  },
  "service_tier": "priority",
  "max_output_tokens": 8192,
  "temperature": 0.2,
  "top_p": 0.95,
  "truncation": "disabled",
  "context_management": [
    {
      "type": "compaction",
      "compact_threshold": 12000
    }
  ],
  "user": "bench-user"
}`)

var benchmarkOpenAIResponsesLongRequest = mustMarshalBenchmarkPayload(map[string]any{
	"model": "gpt-5.4",
	"input": []any{
		map[string]any{
			"type": "message",
			"role": "system",
			"content": []any{
				map[string]any{
					"type": "input_text",
					"text": strings.Repeat("long benchmark system guidance ", 4096),
				},
			},
		},
		map[string]any{
			"type": "message",
			"role": "user",
			"content": []any{
				map[string]any{
					"type": "input_text",
					"text": "marker=benchmark-long\n" + strings.Repeat("long benchmark request segment;", 4096),
				},
			},
		},
	},
	"tools": []any{
		map[string]any{
			"type":        "function",
			"name":        "lookup_issue",
			"description": strings.Repeat("Lookup issue details ", 1024),
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "string",
						"description": strings.Repeat("Issue identifier ", 1024),
					},
				},
				"required": []string{"id"},
			},
		},
	},
	"text": map[string]any{
		"format": map[string]any{
			"name": "json_schema",
			"schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"result": map[string]any{
						"type":        "string",
						"description": strings.Repeat("Final result field ", 1024),
					},
				},
				"required": []string{"result"},
			},
		},
	},
	"service_tier":      "priority",
	"max_output_tokens": 8192,
	"temperature":       0.2,
	"top_p":             0.95,
	"truncation":        "disabled",
	"context_management": []any{
		map[string]any{
			"type":              "compaction",
			"compact_threshold": 12000,
		},
	},
	"user": "bench-user-long",
})

func BenchmarkConvertOpenAIResponsesRequestToCodex(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		out := ConvertOpenAIResponsesRequestToCodex("gpt-5.4", benchmarkOpenAIResponsesRequest, false)
		if len(out) == 0 {
			b.Fatal("empty output")
		}
	}
}

func BenchmarkConvertOpenAIResponsesRequestToCodexLegacy(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		out := convertOpenAIResponsesRequestToCodexLegacy(benchmarkOpenAIResponsesRequest)
		if len(out) == 0 {
			b.Fatal("empty output")
		}
	}
}

func BenchmarkConvertOpenAIResponsesRequestToCodexLong(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		out := ConvertOpenAIResponsesRequestToCodex("gpt-5.4", benchmarkOpenAIResponsesLongRequest, false)
		if len(out) == 0 {
			b.Fatal("empty output")
		}
	}
}

func BenchmarkConvertOpenAIResponsesRequestToCodexLegacyLong(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		out := convertOpenAIResponsesRequestToCodexLegacy(benchmarkOpenAIResponsesLongRequest)
		if len(out) == 0 {
			b.Fatal("empty output")
		}
	}
}

func mustMarshalBenchmarkPayload(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

func convertOpenAIResponsesRequestToCodexLegacy(inputRawJSON []byte) []byte {
	if len(inputRawJSON) == 0 {
		return inputRawJSON
	}

	decoder := json.NewDecoder(bytes.NewReader(inputRawJSON))
	decoder.UseNumber()

	payload := make(map[string]any)
	if err := decoder.Decode(&payload); err != nil {
		return inputRawJSON
	}

	switch input := payload["input"].(type) {
	case string:
		payload["input"] = []map[string]any{
			{
				"type": "message",
				"role": "user",
				"content": []map[string]any{
					{
						"type": "input_text",
						"text": input,
					},
				},
			},
		}
	case []any:
		for _, rawItem := range input {
			item, ok := rawItem.(map[string]any)
			if !ok {
				continue
			}
			role, ok := item["role"].(string)
			if !ok || role != "system" {
				continue
			}
			item["role"] = "developer"
		}
	}

	payload["stream"] = true
	payload["store"] = false
	payload["parallel_tool_calls"] = true
	payload["include"] = []string{"reasoning.encrypted_content"}

	delete(payload, "max_output_tokens")
	delete(payload, "max_completion_tokens")
	delete(payload, "temperature")
	delete(payload, "top_p")
	delete(payload, "truncation")
	delete(payload, "context_management")
	delete(payload, "user")

	if tier, ok := payload["service_tier"].(string); !ok || tier != "priority" {
		delete(payload, "service_tier")
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return inputRawJSON
	}
	return encoded
}
