package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToGemini_CleansToolSchema(t *testing.T) {
	input := []byte(`{
		"model":"gemini-test",
		"input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}],
		"tools":[
			{
				"type":"function",
				"name":"wander_story_payload",
				"description":"test",
				"parameters":{
					"type":"object",
					"properties":{
						"rewardTitleEffects":{
							"type":"array",
							"items":{
								"oneOf":[
									{"type":"string"},
									{"type":"object","properties":{"title":{"type":"string"}}}
								]
							}
						}
					}
				}
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToGemini("gemini-test", input, false)
	result := string(out)

	if gjson.Get(result, "tools.0.functionDeclarations.0.parametersJsonSchema.properties.rewardTitleEffects.items.oneOf").Exists() {
		t.Fatalf("oneOf should be removed: %s", result)
	}
	if got := gjson.Get(result, "tools.0.functionDeclarations.0.parametersJsonSchema.properties.rewardTitleEffects.items.type").String(); got == "" {
		t.Fatalf("items.type should exist after cleaning: %s", result)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_NormalizesClaudeToolBlocks(t *testing.T) {
	input := []byte(`{
		"model":"gemini-test",
		"input":[
			{
				"role":"assistant",
				"content":[
					{"type":"text","text":"checking"},
					{"type":"tool_use","id":"call_1","name":"sessions_list","input":{"limit":10}}
				]
			},
			{
				"role":"user",
				"content":[
					{"type":"tool_result","tool_use_id":"call_1","content":"ok"}
				]
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToGemini("gemini-test", input, false)
	result := string(out)
	if got := gjson.Get(result, "contents.1.role").String(); got != "model" {
		t.Fatalf("expected function call content role=model, got %q: %s", got, result)
	}
	if got := gjson.Get(result, "contents.1.parts.0.functionCall.name").String(); got != "sessions_list" {
		t.Fatalf("expected normalized functionCall, got: %s", result)
	}
	if got := gjson.Get(result, "contents.2.parts.0.functionResponse.name").String(); got != "sessions_list" {
		t.Fatalf("expected paired functionResponse name, got: %s", result)
	}
}
