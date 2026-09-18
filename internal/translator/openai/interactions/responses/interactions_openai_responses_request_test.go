package responses

import (
	"os"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToInteractions(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-test",
		"instructions":"be brief",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"},{"type":"input_image","image_url":"data:image/png;base64,aGVsbG8="}]},
			{"type":"function_call","name":"lookup","call_id":"call_1","arguments":"{\"q\":\"x\"}"},
			{"type":"function_call_output","call_id":"call_1","output":{"ok":true}}
		],
		"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],
		"tool_choice":"auto",
		"reasoning":{"effort":"high","summary":"auto"},
		"response_format":{"type":"json_object"},
		"stream":true
	}`)
	out := ConvertOpenAIResponsesRequestToInteractions("gpt-test", raw, true)
	if got := gjson.GetBytes(out, "input.0.type").String(); got != "user_input" {
		t.Fatalf("input.0.type = %q, want user_input. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.0.content.0.type").String(); got != "text" {
		t.Fatalf("content.0.type = %q, want text. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.0.content.0.text").String(); got != "hi" {
		t.Fatalf("input text = %q, want hi. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.0.content.1.mime_type").String(); got != "image/png" {
		t.Fatalf("image mime_type = %q, want image/png. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.1.call_id").String(); got != "call_1" {
		t.Fatalf("function call_id = %q, want call_1. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.2.type").String(); got != "function_result" {
		t.Fatalf("function result type = %q, want function_result. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.2.name").String(); got != "lookup" {
		t.Fatalf("function result name = %q, want lookup. Output: %s", got, string(out))
	}
	sys := gjson.GetBytes(out, "system_instruction")
	if sys.Type != gjson.String {
		t.Fatalf("system_instruction type = %v, want string. Output: %s", sys.Type, string(out))
	}
	if got := sys.String(); got != "be brief" {
		t.Fatalf("system_instruction = %q, want be brief. Output: %s", got, string(out))
	}
	if gjson.GetBytes(out, "system_instruction.parts").Exists() {
		t.Fatalf("system_instruction.parts should not be forwarded. Output: %s", string(out))
	}
	if got := gjson.GetBytes(out, "generation_config.thinking_level").String(); got != "high" {
		t.Fatalf("thinking_level = %q, want high. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "lookup" {
		t.Fatalf("tool name = %q, want lookup. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "generation_config.tool_choice").String(); got != "auto" {
		t.Fatalf("tool_choice = %q, want auto. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "response_format.type").String(); got != "json_object" {
		t.Fatalf("response_format.type = %q, want json_object. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToInteractionsPreservesRequestStream(t *testing.T) {
	out := ConvertOpenAIResponsesRequestToInteractions("gpt-test", []byte(`{"model":"gpt-test","input":"hi","stream":true}`), false)
	if got := gjson.GetBytes(out, "stream").Bool(); !got {
		t.Fatalf("stream = %v, want true. Output: %s", got, string(out))
	}

	out = ConvertOpenAIResponsesRequestToInteractions("gpt-test", []byte(`{"model":"gpt-test","input":"hi","stream":false}`), true)
	if got := gjson.GetBytes(out, "stream").Bool(); got {
		t.Fatalf("stream = %v, want false. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToInteractionsPreservesPreviousResponseID(t *testing.T) {
	out := ConvertOpenAIResponsesRequestToInteractions("gpt-test", []byte(`{"model":"gpt-test","input":"hi","previous_response_id":"resp_123"}`), false)
	if got := gjson.GetBytes(out, "previous_interaction_id").String(); got != "resp_123" {
		t.Fatalf("previous_interaction_id = %q, want resp_123. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsRequestToOpenAIResponsesWithToolMessages(t *testing.T) {
	raw := []byte(`{"model":"gpt-test","input":[{"type":"user_input","content":[{"type":"text","text":"hi"}]},{"type":"function_call","name":"lookup","call_id":"call_1","arguments":{"q":"x"}},{"type":"function_result","name":"lookup","call_id":"call_1","result":{"ok":true}}]}`)
	out := ConvertInteractionsRequestToOpenAIResponses("gpt-test", raw, false)

	foundFunctionCall := false
	foundFunctionOutput := false
	gjson.GetBytes(out, "input").ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == "function_call" {
			foundFunctionCall = true
			if item.Get("name").String() != "lookup" {
				t.Fatalf("name = %q, want lookup", item.Get("name").String())
			}
		}
		if item.Get("type").String() == "function_call_output" {
			foundFunctionOutput = true
		}
		return true
	})
	if !foundFunctionCall {
		t.Fatal("function_call input not found")
	}
	if !foundFunctionOutput {
		t.Fatal("function_call_output input not found")
	}
}

func TestConvertInteractionsRequestToOpenAIResponsesPreservesStringSystemAndThinkingConfig(t *testing.T) {
	raw := []byte(`{"model":"gpt-test","system_instruction":"You are a helpful assistant.","input":[{"type":"user_input","content":[{"type":"text","text":"hi"}]}],"tools":[{"name":"lookup","type":"function","parameters":{"type":"object"}}],"generation_config":{"tool_choice":"auto","thinking_level":"high","thinking_summaries":"auto"},"stream":true}`)
	out := ConvertInteractionsRequestToOpenAIResponses("gpt-test", raw, true)
	if got := gjson.GetBytes(out, "instructions").String(); got != "You are a helpful assistant." {
		t.Fatalf("instructions = %q, want system instruction. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tool_choice").String(); got != "auto" {
		t.Fatalf("tool_choice = %q, want auto. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "reasoning.effort").String(); got != "high" {
		t.Fatalf("reasoning.effort = %q, want high. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "reasoning.summary").String(); got != "auto" {
		t.Fatalf("reasoning.summary = %q, want auto. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsRequestToOpenAIResponsesPreservesInteractionStream(t *testing.T) {
	out := ConvertInteractionsRequestToOpenAIResponses("gpt-test", []byte(`{"model":"gpt-test","input":"hi","stream":true}`), false)
	if got := gjson.GetBytes(out, "stream").Bool(); !got {
		t.Fatalf("stream = %v, want true. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsRequestToOpenAIResponsesPreservesPreviousInteractionID(t *testing.T) {
	out := ConvertInteractionsRequestToOpenAIResponses("gpt-test", []byte(`{"model":"gpt-test","input":"hi","previous_interaction_id":"interaction_123"}`), false)
	if got := gjson.GetBytes(out, "previous_response_id").String(); got != "interaction_123" {
		t.Fatalf("previous_response_id = %q, want interaction_123. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsRequestToOpenAIResponsesPreservesToolCallID(t *testing.T) {
	out := ConvertInteractionsRequestToOpenAIResponses("gpt-test", []byte(`{"model":"gpt-test","input":[{"type":"function_call","name":"lookup","call_id":"call_gateway","arguments":{"q":"x"}},{"type":"function_result","name":"lookup","call_id":"call_gateway","result":{"ok":true}}]}`), false)

	foundFunctionCall := false
	foundFunctionOutput := false
	gjson.GetBytes(out, "input").ForEach(func(_, item gjson.Result) bool {
		switch item.Get("type").String() {
		case "function_call":
			foundFunctionCall = true
			if got := item.Get("call_id").String(); got != "call_gateway" {
				t.Fatalf("function_call call_id = %q, want call_gateway. Output: %s", got, string(out))
			}
		case "function_call_output":
			foundFunctionOutput = true
			if got := item.Get("call_id").String(); got != "call_gateway" {
				t.Fatalf("function_call_output call_id = %q, want call_gateway. Output: %s", got, string(out))
			}
		}
		return true
	})
	if !foundFunctionCall {
		t.Fatal("function_call input not found")
	}
	if !foundFunctionOutput {
		t.Fatal("function_call_output input not found")
	}
}

func TestConvertInteractionsRequestToOpenAIResponsesConvertsSimpleTools(t *testing.T) {
	out := ConvertInteractionsRequestToOpenAIResponses("gpt-test", []byte(`{"model":"gpt-test","tools":[{"name":"lookup","description":"Find data","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}],"input":"hi"}`), false)
	if got := gjson.GetBytes(out, "tools.0.type").String(); got != "function" {
		t.Fatalf("tools.0.type = %q, want function. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "lookup" {
		t.Fatalf("tools.0.name = %q, want lookup. Output: %s", got, string(out))
	}
	if gjson.GetBytes(out, "tools.0.function").Exists() {
		t.Fatalf("tools.0.function should not be forwarded. Output: %s", string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.parameters.properties.q.type").String(); got != "string" {
		t.Fatalf("tools.0.parameters.properties.q.type = %q, want string. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsRequestToOpenAIResponsesConvertsFunctionDeclarationsTools(t *testing.T) {
	out := ConvertInteractionsRequestToOpenAIResponses("gpt-test", []byte(`{"model":"gpt-test","tools":[{"function_declarations":[{"name":"lookup","description":"Find data","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}]}],"input":"hi"}`), false)
	if got := gjson.GetBytes(out, "tools.0.type").String(); got != "function" {
		t.Fatalf("tools.0.type = %q, want function. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "lookup" {
		t.Fatalf("tools.0.name = %q, want lookup. Output: %s", got, string(out))
	}
	if gjson.GetBytes(out, "tools.0.function_declarations").Exists() {
		t.Fatalf("tools.0.function_declarations should not be forwarded. Output: %s", string(out))
	}
}

func TestConvertInteractionsRequestToOpenAIResponsesWithImageContent(t *testing.T) {
	raw := []byte(`{"model":"gpt-test","input":[{"type":"user_input","content":[{"type":"text","text":"describe"},{"type":"image","mime_type":"image/png","data":"aGVsbG8="}]}]}`)
	out := ConvertInteractionsRequestToOpenAIResponses("gpt-test", raw, false)
	if got := gjson.GetBytes(out, "input.0.content.1.type").String(); got != "input_image" {
		t.Fatalf("content.1.type = %q, want input_image", got)
	}
	if got := gjson.GetBytes(out, "input.0.content.1.image_url").String(); got != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("image_url = %q, want data URL", got)
	}
}

func TestConvertInteractionsRequestToOpenAIResponsesPreservesNonImageMediaContent(t *testing.T) {
	out := ConvertInteractionsRequestToOpenAIResponses("gpt-test", []byte(`{"model":"gpt-test","input":[{"type":"model_output","content":[{"type":"audio","mime_type":"audio/wav","data":"UklGRg=="},{"type":"video","mime_type":"video/mp4","data":"AAAAIGZ0eXA="},{"type":"document","mime_type":"application/pdf","data":"JVBERi0="}]}]}`), false)

	if got := gjson.GetBytes(out, "input.0.content.0.type").String(); got != "output_text" {
		t.Fatalf("audio fallback type = %q, want output_text. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.0.content.1.type").String(); got != "output_file" {
		t.Fatalf("video type = %q, want output_file. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.0.content.2.type").String(); got != "output_file" {
		t.Fatalf("document type = %q, want output_file. Output: %s", got, string(out))
	}
	if gjson.GetBytes(out, "input.0.content.#(type==\"output_image\")").Exists() {
		t.Fatalf("non-image media must not be converted to output_image. Output: %s", string(out))
	}
}

func TestConvertInteractionsRequestToOpenAIResponsesWithAssistantTextContent(t *testing.T) {
	raw := []byte(`{"model":"gpt-test","input":[{"type":"model_output","content":[{"type":"text","text":"hello"}]}]}`)
	out := ConvertInteractionsRequestToOpenAIResponses("gpt-test", raw, false)
	if got := gjson.GetBytes(out, "input.0.content.0.type").String(); got != "output_text" {
		t.Fatalf("content.0.type = %q, want output_text", got)
	}
	if got := gjson.GetBytes(out, "input.0.content.0.text").String(); got != "hello" {
		t.Fatalf("content.0.text = %q, want hello", got)
	}
}

func TestConvertInteractionsRequestToOpenAIResponsesWithUserObjectContent(t *testing.T) {
	raw := []byte(`{"model":"gpt-test","input":[{"type":"user_input","content":[{"type":"text","text":"hi"}]}]}`)
	out := ConvertInteractionsRequestToOpenAIResponses("gpt-test", raw, false)
	if got := gjson.GetBytes(out, "input.0.content.0.type").String(); got != "input_text" {
		t.Fatalf("content.0.type = %q, want input_text", got)
	}
	if got := gjson.GetBytes(out, "input.0.content.0.text").String(); got != "hi" {
		t.Fatalf("content.0.text = %q, want hi", got)
	}
}

func TestConvertInteractionsRequestToOpenAIResponsesWithStringFunctionArguments(t *testing.T) {
	raw := []byte(`{"model":"gpt-test","input":[{"type":"function_call","name":"lookup","call_id":"call_1","arguments":{"q":"x"}},{"type":"function_result","name":"lookup","call_id":"call_1","result":{"ok":true}}]}`)
	out := ConvertInteractionsRequestToOpenAIResponses("gpt-test", raw, false)

	found := false
	gjson.GetBytes(out, "input").ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == "function_call" {
			found = true
			if item.Get("arguments").Type != gjson.String {
				t.Fatalf("arguments should be string, got %v", item.Get("arguments").Type)
			}
			if got := item.Get("arguments").String(); got != `{"q":"x"}` {
				t.Fatalf("arguments = %q, want {\"q\":\"x\"}", got)
			}
		}
		return true
	})
	if !found {
		t.Fatal("function_call input not found")
	}
}

func TestConvertInteractionsRequestToOpenAIResponsesPreservesExpressibleFields(t *testing.T) {
	out := ConvertInteractionsRequestToOpenAIResponses("gpt-test", []byte(`{"model":"gpt-test","tool_choice":{"type":"function","function":{"name":"lookup"}},"response_modalities":["text","image"],"service_tier":"priority","store":true,"background":true,"webhook_config":{"url":"https://example.com"},"input":"hi"}`), false)
	if got := gjson.GetBytes(out, "tool_choice.type").String(); got != "function" {
		t.Fatalf("tool_choice.type = %q, want function. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tool_choice.function.name").String(); got != "lookup" {
		t.Fatalf("tool_choice.function.name = %q, want lookup. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "modalities.0").String(); got != "text" {
		t.Fatalf("modalities.0 = %q, want text. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "modalities.1").String(); got != "image" {
		t.Fatalf("modalities.1 = %q, want image. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "service_tier").String(); got != "priority" {
		t.Fatalf("service_tier = %q, want priority. Output: %s", got, string(out))
	}
	for _, path := range []string{"store", "background", "webhook_config"} {
		if gjson.GetBytes(out, path).Exists() {
			t.Fatalf("%s should not be forwarded. Output: %s", path, string(out))
		}
	}
}

func TestConvertOpenAIResponsesRequestToInteractions_PreservesEnvironmentID(t *testing.T) {
	out := ConvertOpenAIResponsesRequestToInteractions("gpt-test", []byte(`{"model":"gpt-test","input":"hi","previous_response_id":"resp_123","environment_id":"env_abc456"}`), false)
	if got := gjson.GetBytes(out, "previous_interaction_id").String(); got != "resp_123" {
		t.Fatalf("previous_interaction_id = %q, want resp_123. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "environment_id").String(); got != "env_abc456" {
		t.Fatalf("environment_id = %q, want env_abc456. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsRequestToOpenAIResponses_PreservesEnvironmentID(t *testing.T) {
	out := ConvertInteractionsRequestToOpenAIResponses("gpt-test", []byte(`{"model":"gpt-test","input":"hi","previous_interaction_id":"interaction_123","environment_id":"env_abc456"}`), false)
	if got := gjson.GetBytes(out, "previous_response_id").String(); got != "interaction_123" {
		t.Fatalf("previous_response_id = %q, want interaction_123. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "environment_id").String(); got != "env_abc456" {
		t.Fatalf("environment_id = %q, want env_abc456. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToInteractions_AntigravitySanitizesGenerationConfigAndSetsAgentConfig(t *testing.T) {
	raw := []byte(`{
		"model":"antigravity-preview-05-2026",
		"input":"Search the web",
		"previous_response_id":"v1_Chd3...",
		"environment_id":"env_789",
		"max_output_tokens":2048,
		"temperature":0.7,
		"top_p":0.95,
		"tools":[{"type":"function","name":"web_search","parameters":{"type":"object"}}]
	}`)
	out := ConvertOpenAIResponsesRequestToInteractions("antigravity-preview-05-2026", raw, false)
	if got := gjson.GetBytes(out, "previous_interaction_id").String(); got != "v1_Chd3..." {
		t.Fatalf("previous_interaction_id = %q, want v1_Chd3.... Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "environment_id").String(); got != "env_789" {
		t.Fatalf("environment_id = %q, want env_789. Output: %s", got, string(out))
	}
	// temperature, top_p, max_output_tokens should be stripped from generation_config for Antigravity models
	for _, knob := range []string{"temperature", "top_p", "top_k", "stop_sequences", "max_output_tokens"} {
		if gjson.GetBytes(out, "generation_config."+knob).Exists() {
			t.Fatalf("generation_config.%s should be stripped for antigravity model. Output: %s", knob, string(out))
		}
	}
	// max_output_tokens should be mapped to agent_config.max_total_tokens
	if got := gjson.GetBytes(out, "agent_config.max_total_tokens").Int(); got != 2048 {
		t.Fatalf("agent_config.max_total_tokens = %d, want 2048. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToInteractionsRenamesConflictingAntigravityTools(t *testing.T) {
	raw := []byte(`{
		"model":"antigravity-preview-05-2026",
		"input":[{"type":"message","role":"user","content":"read file"}],
		"tools":[
			{"type":"function","name":"read_file","parameters":{"type":"object"}},
			{"type":"function","name":"write_file","parameters":{"type":"object"}},
			{"type":"function","name":"execute_code","parameters":{"type":"object"}},
			{"type":"function","name":"web_search","parameters":{"type":"object"}}
		]
	}`)
	out := ConvertOpenAIResponsesRequestToInteractions("antigravity-preview-05-2026", raw, false)
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "external_read_file" {
		t.Fatalf("tools.0.name = %q, want external_read_file. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.1.name").String(); got != "external_write_file" {
		t.Fatalf("tools.1.name = %q, want external_write_file. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.2.name").String(); got != "external_execute_code" {
		t.Fatalf("tools.2.name = %q, want external_execute_code. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.3.name").String(); got != "web_search" {
		t.Fatalf("tools.3.name = %q, want web_search (unchanged). Output: %s", got, string(out))
	}
	outNonAnti := ConvertOpenAIResponsesRequestToInteractions("gemini-3.1-flash-lite", raw, false)
	if got := gjson.GetBytes(outNonAnti, "tools.0.name").String(); got != "read_file" {
		t.Fatalf("gemini tools.0.name = %q, want read_file. Output: %s", got, string(outNonAnti))
	}
}

func TestConvertOpenAIResponsesRequestToInteractionsRenamesConflictingAntigravityToolCallsAndResultsInHistory(t *testing.T) {
	raw := []byte(`{
		"model":"antigravity-preview-05-2026",
		"input":[
			{"type":"message","role":"user","content":"read file"},
			{"type":"function_call","name":"read_file","call_id":"call_1","arguments":"{\"path\":\"/tmp/x\"}"},
			{"type":"function_call_output","name":"read_file","call_id":"call_1","output":"hello file"}
		],
		"tools":[
			{"type":"function","name":"read_file","parameters":{"type":"object"}}
		]
	}`)
	out := ConvertOpenAIResponsesRequestToInteractions("antigravity-preview-05-2026", raw, false)
	if got := gjson.GetBytes(out, "input.1.name").String(); got != "external_read_file" {
		t.Fatalf("input.1.name = %q, want external_read_file. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.2.name").String(); got != "external_read_file" {
		t.Fatalf("input.2.name = %q, want external_read_file. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToInteractionsRenamesConflictingToolChoice(t *testing.T) {
	raw := []byte(`{
		"model":"antigravity-preview-05-2026",
		"input":[{"type":"message","role":"user","content":"read file"}],
		"tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}],
		"tool_choice":{"type":"function","name":"read_file"}
	}`)
	out := ConvertOpenAIResponsesRequestToInteractions("antigravity-preview-05-2026", raw, false)
	if got := gjson.GetBytes(out, "generation_config.tool_choice.name").String(); got != "external_read_file" {
		t.Fatalf("generation_config.tool_choice.name = %q, want external_read_file. Output: %s", got, string(out))
	}
	outNonAnti := ConvertOpenAIResponsesRequestToInteractions("gemini-3.1-flash-lite", raw, false)
	if got := gjson.GetBytes(outNonAnti, "generation_config.tool_choice.name").String(); got != "read_file" {
		t.Fatalf("gemini tool_choice.name = %q, want read_file (no rename). Output: %s", got, string(outNonAnti))
	}
}

func TestConvertOpenAIResponsesRequestToInteractions_NonAntigravityGenerationConfig(t *testing.T) {
	raw := []byte(`{
		"model":"devin/swe-2",
		"input":[{"type":"message","role":"user","content":"write a poem"}],
		"max_output_tokens":400,
		"temperature":0.7,
		"top_p":0.95
	}`)
	out := ConvertOpenAIResponsesRequestToInteractions("devin/swe-2", raw, false)
	if got := gjson.GetBytes(out, "generation_config.max_output_tokens").Int(); got != 400 {
		t.Fatalf("generation_config.max_output_tokens = %d, want 400. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "generation_config.temperature").Float(); got != 0.7 {
		t.Fatalf("generation_config.temperature = %f, want 0.7. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "generation_config.top_p").Float(); got != 0.95 {
		t.Fatalf("generation_config.top_p = %f, want 0.95. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToInteractions_FlattensNamespaceTools(t *testing.T) {
	raw := []byte(`{
		"model": "devin/gemini-3-7-flash",
		"tools": [
			{
				"type": "function",
				"name": "exec_command",
				"description": "Execute a command",
				"parameters": {
					"type": "object",
					"properties": {"cmd": {"type": "string"}},
					"required": ["cmd"]
				}
			},
			{
				"type": "namespace",
				"name": "multi_agent_v1",
				"description": "Multi agent tools",
				"tools": [
					{
						"type": "function",
						"name": "close_agent",
						"description": "Close an agent",
						"parameters": {
							"type": "object",
							"properties": {"target": {"type": "string"}},
							"required": ["target"]
						}
					},
					{
						"type": "function",
						"name": "resume_agent",
						"description": "Resume an agent",
						"parameters": {
							"type": "object",
							"properties": {"id": {"type": "string"}},
							"required": ["id"]
						}
					}
				]
			},
			{
				"type": "namespace",
				"name": "functions",
				"tools": [
					{
						"type": "custom",
						"name": "exec",
						"description": "Run custom command"
					}
				]
			}
		],
		"tool_choice": {
			"type": "function",
			"name": "close_agent",
			"namespace": "multi_agent_v1"
		}
	}`)

	out := ConvertOpenAIResponsesRequestToInteractions("devin/gemini-3-7-flash", raw, false)
	tools := gjson.GetBytes(out, "tools").Array()
	if len(tools) != 4 {
		t.Fatalf("expected 4 flattened tools, got %d. Output: %s", len(tools), string(out))
	}

	// Tool 0: exec_command
	if got := tools[0].Get("name").String(); got != "exec_command" {
		t.Errorf("tools[0].name = %q, want exec_command", got)
	}
	if got := tools[0].Get("type").String(); got != "function" {
		t.Errorf("tools[0].type = %q, want function", got)
	}

	// Tool 1: multi_agent_v1__close_agent
	if got := tools[1].Get("name").String(); got != "multi_agent_v1__close_agent" {
		t.Errorf("tools[1].name = %q, want multi_agent_v1__close_agent", got)
	}
	if got := tools[1].Get("description").String(); got != "Close an agent" {
		t.Errorf("tools[1].description = %q, want Close an agent", got)
	}
	if got := tools[1].Get("parameters.properties.target.type").String(); got != "string" {
		t.Errorf("tools[1].parameters.properties.target.type = %q, want string", got)
	}

	// Tool 2: multi_agent_v1__resume_agent
	if got := tools[2].Get("name").String(); got != "multi_agent_v1__resume_agent" {
		t.Errorf("tools[2].name = %q, want multi_agent_v1__resume_agent", got)
	}

	// Tool 3: functions__exec (custom tool)
	if got := tools[3].Get("name").String(); got != "functions__exec" {
		t.Errorf("tools[3].name = %q, want functions__exec", got)
	}
	if got := tools[3].Get("parameters.properties.input.type").String(); got != "string" {
		t.Errorf("tools[3].parameters.properties.input.type = %q, want string", got)
	}

	// Check tool_choice was qualified
	if got := gjson.GetBytes(out, "generation_config.tool_choice.name").String(); got != "multi_agent_v1__close_agent" {
		t.Errorf("tool_choice.name = %q, want multi_agent_v1__close_agent", got)
	}
}

func TestConvertOpenAIResponsesRequestToInteractions_HistoryNamespaceAndCustomTools(t *testing.T) {
	raw := []byte(`{
		"model": "devin/gemini-3-7-flash",
		"input": [
			{
				"type": "function_call",
				"call_id": "call_1",
				"name": "close_agent",
				"namespace": "multi_agent_v1",
				"arguments": "{\"target\":\"agent_123\"}"
			},
			{
				"type": "function_call_output",
				"call_id": "call_1",
				"name": "close_agent",
				"namespace": "multi_agent_v1",
				"output": "{\"status\":\"closed\"}"
			},
			{
				"type": "custom_tool_call",
				"call_id": "call_2",
				"name": "exec",
				"namespace": "functions",
				"input": "ls -la"
			},
			{
				"type": "custom_tool_call_output",
				"call_id": "call_2",
				"output": "file1.txt"
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToInteractions("devin/gemini-3-7-flash", raw, false)

	// Step 0: function_call with qualified name
	if got := gjson.GetBytes(out, "input.0.name").String(); got != "multi_agent_v1__close_agent" {
		t.Errorf("input.0.name = %q, want multi_agent_v1__close_agent", got)
	}
	if got := gjson.GetBytes(out, "input.0.call_id").String(); got != "call_1" {
		t.Errorf("input.0.call_id = %q, want call_1", got)
	}

	// Step 1: function_result with qualified name
	if got := gjson.GetBytes(out, "input.1.name").String(); got != "multi_agent_v1__close_agent" {
		t.Errorf("input.1.name = %q, want multi_agent_v1__close_agent", got)
	}

	// Step 2: custom_tool_call with qualified name and input in arguments
	if got := gjson.GetBytes(out, "input.2.name").String(); got != "functions__exec" {
		t.Errorf("input.2.name = %q, want functions__exec", got)
	}
	if got := gjson.GetBytes(out, "input.2.arguments.input").String(); got != "ls -la" {
		t.Errorf("input.2.arguments.input = %q, want ls -la", got)
	}

	// Step 3: custom_tool_call_output resolves name from functionNamesByCallID
	if got := gjson.GetBytes(out, "input.3.name").String(); got != "functions__exec" {
		t.Errorf("input.3.name = %q, want functions__exec", got)
	}
	if got := gjson.GetBytes(out, "input.3.result").String(); got != "file1.txt" {
		t.Errorf("input.3.result = %q, want file1.txt", got)
	}
}

func TestConvertOpenAIResponsesRequestToInteractions_AntigravityCustomToolRenamed(t *testing.T) {
	raw := []byte(`{
		"model": "antigravity-preview-05-2026",
		"tools": [
			{"type": "custom", "name": "read_file", "description": "Read file"}
		]
	}`)
	out := ConvertOpenAIResponsesRequestToInteractions("antigravity-preview-05-2026", raw, false)
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "external_read_file" {
		t.Fatalf("tools.0.name = %q, want external_read_file", got)
	}
}

func TestConvertOpenAIResponsesRequestToInteractions_FixedFixtureFullNamespaceExpansion(t *testing.T) {
	fixture := `{
		"model": "devin/gemini-3-7-flash",
		"tools": [
			{"type": "function", "name": "top_fn_1"},
			{"type": "function", "name": "top_fn_2"},
			{
				"type": "namespace",
				"name": "ns_a",
				"tools": [
					{"type": "function", "name": "child_1"},
					{"type": "function", "name": "child_2"}
				]
			},
			{
				"type": "namespace",
				"name": "ns_b",
				"tools": [
					{"type": "custom", "name": "custom_1"},
					{"type": "function", "name": "child_3"}
				]
			},
			{"type": "function", "name": "top_fn_3"}
		]
	}`

	out := ConvertOpenAIResponsesRequestToInteractions("devin/gemini-3-7-flash", []byte(fixture), false)
	tools := gjson.GetBytes(out, "tools").Array()
	expectedNames := []string{
		"top_fn_1",
		"top_fn_2",
		"ns_a__child_1",
		"ns_a__child_2",
		"ns_b__custom_1",
		"ns_b__child_3",
		"top_fn_3",
	}
	if len(tools) != len(expectedNames) {
		t.Fatalf("expected %d flattened tools, got %d. Output: %s", len(expectedNames), len(tools), string(out))
	}
	for i, want := range expectedNames {
		if got := tools[i].Get("name").String(); got != want {
			t.Errorf("tools[%d].name = %q, want %q", i, got, want)
		}
	}
}

func TestConvertOpenAIResponsesRequestToInteractions_LogFilePayload(t *testing.T) {
	var reqBody string
	logPath := "../../../../../logs/log/v1-responses-2026-09-17T113605-d90227fd.log"
	if data, err := os.ReadFile(logPath); err == nil {
		content := string(data)
		startMarker := "=== REQUEST BODY ==="
		endMarker := "=== API REQUEST 1 ==="
		sIdx := strings.Index(content, startMarker)
		eIdx := strings.Index(content, endMarker)
		if sIdx != -1 && eIdx != -1 {
			reqBody = strings.TrimSpace(content[sIdx+len(startMarker) : eIdx])
		}
	}

	if reqBody == "" {
		// Fallback fixture matching the 19 tool structure from the log
		reqBody = `{
			"model": "devin/gemini-3-7-flash",
			"tools": [
				{"type": "function", "name": "t0"},
				{"type": "function", "name": "t1"},
				{"type": "function", "name": "t2"},
				{"type": "function", "name": "t3"},
				{"type": "function", "name": "t4"},
				{"type": "function", "name": "t5"},
				{"type": "function", "name": "t6"},
				{"type": "function", "name": "t7"},
				{"type": "namespace", "name": "ns8", "tools": [{"type": "function", "name": "c1"}, {"type": "function", "name": "c2"}]},
				{"type": "namespace", "name": "ns9", "tools": [{"type": "function", "name": "c1"}]},
				{"type": "function", "name": "t10"}
			]
		}`
	}

	out := ConvertOpenAIResponsesRequestToInteractions("devin/gemini-3-7-flash", []byte(reqBody), false)

	tools := gjson.GetBytes(out, "tools").Array()
	if len(tools) == 0 {
		t.Fatalf("expected tools in output, got 0")
	}

	// Verify all tools are flattened and have non-empty names
	for i, tool := range tools {
		name := tool.Get("name").String()
		if name == "" {
			t.Errorf("tool[%d] has empty name: %s", i, tool.Raw)
		}
		if tool.Get("function_declarations").Exists() {
			t.Errorf("tool[%d] unexpectedly contains function_declarations: %s", i, tool.Raw)
		}
	}
}

func TestConvertOpenAIResponsesRequestToInteractions_DevinToolsFilterAndObfuscate(t *testing.T) {
	responsesPayload := []byte(`{
		"model": "devin/swe-2",
		"tools": [
			{
				"type": "namespace",
				"name": "mcp__codex_app",
				"description": "Codex App tools",
				"tools": [
					{
						"type": "function",
						"name": "automation_update",
						"description": "Recurring automations",
						"parameters": {"type": "object", "properties": {"id": {"type": "string"}}}
					},
					{
						"type": "function",
						"name": "read_resource",
						"description": "Read a resource",
						"parameters": {"type": "object", "properties": {"uri": {"type": "string"}}}
					}
				]
			},
			{
				"type": "function",
				"name": "exec_command",
				"description": "Runs a command in a bash shell, returning output or a session ID for ongoing interaction.",
				"parameters": {
					"type": "object",
					"properties": {"cmd": {"type": "string"}},
					"required": ["cmd"]
				}
			},
			{
				"type": "function",
				"name": "write_stdin",
				"description": "Writes characters to an existing unified exec session and returns recent output.",
				"parameters": {
					"type": "object",
					"properties": {"session_id": {"type": "string"}},
					"required": ["session_id"]
				}
			},
			{
				"type": "function",
				"name": "other_func",
				"description": "Regular function description",
				"parameters": {"type": "object"}
			}
		],
		"tool_choice": {
			"type": "function",
			"name": "automation_update",
			"namespace": "mcp__codex_app"
		},
		"input": [
			{"type": "message", "role": "user", "content": "hello"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToInteractions("devin/swe-2", responsesPayload, false)

	tools := gjson.GetBytes(out, "tools").Array()
	toolNames := make([]string, 0, len(tools))
	toolDescs := make(map[string]string)
	for _, tool := range tools {
		name := tool.Get("name").String()
		toolNames = append(toolNames, name)
		toolDescs[name] = tool.Get("description").String()
	}

	// 1. automation_update in mcp__codex_app must be filtered out
	for _, name := range toolNames {
		if strings.Contains(name, "automation_update") {
			t.Fatalf("unexpected automation_update tool found in devin tools: %q", name)
		}
	}

	// 2. other tool in mcp__codex_app must be preserved
	if _, exists := toolDescs["mcp__codex_app__read_resource"]; !exists {
		t.Fatalf("expected mcp__codex_app__read_resource in devin tools, got %v", toolNames)
	}

	// 3. exec_command description must have intentional grammar error "an session ID"
	execDesc, ok := toolDescs["exec_command"]
	if !ok {
		t.Fatalf("expected exec_command in devin tools, got %v", toolNames)
	}
	const wantExecDesc = "Runs a command in a bash shell, returning output or an session ID for ongoing interaction."
	if execDesc != wantExecDesc {
		t.Fatalf("exec_command description = %q, want %q", execDesc, wantExecDesc)
	}

	// 4. write_stdin description must have intentional grammar error "to a existing"
	stdinDesc, ok := toolDescs["write_stdin"]
	if !ok {
		t.Fatalf("expected write_stdin in devin tools, got %v", toolNames)
	}
	const wantStdinDesc = "Writes characters to a existing unified exec session and returns recent output."
	if stdinDesc != wantStdinDesc {
		t.Fatalf("write_stdin description = %q, want %q", stdinDesc, wantStdinDesc)
	}

	// 5. other_func description must remain untouched
	otherDesc, ok := toolDescs["other_func"]
	if !ok || otherDesc != "Regular function description" {
		t.Fatalf("other_func description = %q, want Regular function description", otherDesc)
	}

	// 6. tool_choice targeting the filtered automation_update should not be set
	if tc := gjson.GetBytes(out, "generation_config.tool_choice"); tc.Exists() {
		t.Fatalf("expected generation_config.tool_choice to be omitted for filtered tool, got %s", tc.Raw)
	}
}

func TestConvertOpenAIResponsesRequestToInteractions_AntigravityDoesNotFilterDevinTools(t *testing.T) {
	responsesPayload := []byte(`{
		"model": "antigravity-preview-05-2026",
		"tools": [
			{
				"type": "namespace",
				"name": "mcp__codex_app",
				"tools": [
					{
						"type": "function",
						"name": "automation_update",
						"description": "Recurring automations",
						"parameters": {"type": "object"}
					}
				]
			},
			{
				"type": "function",
				"name": "exec_command",
				"description": "Runs a command in a bash shell, returning output or a session ID for ongoing interaction."
			}
		],
		"input": "hello"
	}`)

	out := ConvertOpenAIResponsesRequestToInteractions("antigravity-preview-05-2026", responsesPayload, false)

	// In Antigravity, mcp__codex_app__automation_update should NOT be filtered by Devin rules
	foundAuto := false
	gjson.GetBytes(out, "tools").ForEach(func(_, tool gjson.Result) bool {
		if tool.Get("name").String() == "mcp__codex_app__automation_update" {
			foundAuto = true
		}
		if tool.Get("name").String() == "exec_command" {
			if strings.Contains(tool.Get("description").String(), "an session ID") {
				t.Fatalf("exec_command description should not have 'an session ID' in antigravity: %s", tool.Get("description").String())
			}
		}
		return true
	})
	if !foundAuto {
		t.Fatalf("expected mcp__codex_app__automation_update to be kept in antigravity output: %s", string(out))
	}
}
