package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestValidateCodexResponsesInput_RejectsOutputTextInUserMessage(t *testing.T) {
	inputJSON := []byte("{\"input\":[{\"type\":\"message\",\"role\":\"user\",\"content\":[{\"type\":\"output_text\",\"text\":\"a\"},{\"type\":\"input_text\",\"text\":\"b\"}]}]}")
	verr := ValidateCodexResponsesInput(inputJSON)
	if verr == nil || verr.Param != "input[0].content[0]" {
		t.Fatalf("got %#v", verr)
	}
}

func TestValidateCodexResponsesInput_AllowsAssistantOutputText(t *testing.T) {
	inputJSON := []byte("{\"input\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}")
	if err := ValidateCodexResponsesInput(inputJSON); err != nil {
		t.Fatalf("got %v", err)
	}
}

func TestConvertOpenAIResponsesRequestToCodex_StripsUnsupportedTokenLimits(t *testing.T) {
	inputJSON := []byte("{\"model\":\"gpt-5.5\",\"max_output_tokens\":32,\"input\":[{\"type\":\"message\",\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"hi\"}]}]}")
	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.5", inputJSON, false)
	if err := ValidateCodexResponsesInput(output); err != nil {
		t.Fatalf("valid after convert: %v", err)
	}
	if gjson.GetBytes(output, "max_output_tokens").Exists() {
		t.Fatal("max_output_tokens should be stripped")
	}
}

func TestConvertOpenAIResponsesRequestToCodex_ValidateRejectsMixedUserMessageAfterConvert(t *testing.T) {
	inputJSON := []byte("{\"model\":\"gpt-5.5\",\"input\":[{\"type\":\"message\",\"role\":\"user\",\"content\":[{\"type\":\"output_text\",\"text\":\"a\"},{\"type\":\"input_text\",\"text\":\"b\"}]}]}")
	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.5", inputJSON, false)
	if err := ValidateCodexResponsesInput(output); err == nil {
		t.Fatal("expected validation error")
	}
}
