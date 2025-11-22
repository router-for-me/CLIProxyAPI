package responses

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func ConvertOpenAIResponsesRequestToCodex(modelName string, inputRawJSON []byte, _ bool) []byte {
	rawJSON := bytes.Clone(inputRawJSON)

	rawJSON, _ = sjson.SetBytes(rawJSON, "stream", true)
	rawJSON, _ = sjson.SetBytes(rawJSON, "store", false)
	rawJSON, _ = sjson.SetBytes(rawJSON, "parallel_tool_calls", true)
	rawJSON, _ = sjson.SetBytes(rawJSON, "include", []string{"reasoning.encrypted_content"})
	// Codex Responses rejects token limit fields, so strip them out before forwarding.
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "max_output_tokens")
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "max_completion_tokens")
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "temperature")
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "top_p")
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "service_tier")

	originalInstructions := ""
	originalInstructionsText := ""
	originalInstructionsResult := gjson.GetBytes(rawJSON, "instructions")
	if originalInstructionsResult.Exists() {
		originalInstructions = originalInstructionsResult.Raw
		originalInstructionsText = originalInstructionsResult.String()
	}

	hasOfficialInstructions, instructions := misc.CodexInstructionsForModel(modelName, originalInstructionsResult.String())

	inputResult := gjson.GetBytes(rawJSON, "input")
	var inputResults []gjson.Result
	if inputResult.Exists() {
		if inputResult.IsArray() {
			inputResults = inputResult.Array()
		} else if inputResult.Type == gjson.String {
			newInput := `[{"type":"message","role":"user","content":[{"type":"input_text","text":""}]}]`
			newInput, _ = sjson.SetRaw(newInput, "0.content.0.text", inputResult.Raw)
			inputResults = gjson.Parse(newInput).Array()
		}
	} else {
		inputResults = []gjson.Result{}
	}

	extractedSystemInstructions := false
	if originalInstructions == "" && len(inputResults) > 0 {
		for _, item := range inputResults {
			if strings.EqualFold(item.Get("role").String(), "system") {
				var builder strings.Builder
				if content := item.Get("content"); content.Exists() && content.IsArray() {
					content.ForEach(func(_, contentItem gjson.Result) bool {
						text := contentItem.Get("text").String()
						if builder.Len() > 0 && text != "" {
							builder.WriteByte('\n')
						}
						builder.WriteString(text)
						return true
					})
				}
				originalInstructionsText = builder.String()
				originalInstructions = strconv.Quote(originalInstructionsText)
				extractedSystemInstructions = true
				break
			}
		}
	}

	if hasOfficialInstructions {
		return rawJSON
	}
	// log.Debugf("instructions not matched, %s\n", originalInstructions)

	if len(inputResults) > 0 {
		newInput := "[]"
		firstMessageHandled := false
		for _, item := range inputResults {
			if extractedSystemInstructions && strings.EqualFold(item.Get("role").String(), "system") {
				continue
			}
			if !firstMessageHandled {
				firstText := item.Get("content.0.text")
				firstInstructions := "EXECUTE ACCORDING TO THE FOLLOWING INSTRUCTIONS!!!"
				if firstText.Exists() && firstText.String() != firstInstructions {
					firstTextTemplate := `{"type":"message","role":"user","content":[{"type":"input_text","text":"EXECUTE ACCORDING TO THE FOLLOWING INSTRUCTIONS!!!"}]}`
					firstTextTemplate, _ = sjson.Set(firstTextTemplate, "content.1.text", originalInstructionsText)
					firstTextTemplate, _ = sjson.Set(firstTextTemplate, "content.1.type", "input_text")
					newInput, _ = sjson.SetRaw(newInput, "-1", firstTextTemplate)
				}
				firstMessageHandled = true
			}
			newInput, _ = sjson.SetRaw(newInput, "-1", item.Raw)
		}
		rawJSON, _ = sjson.SetRawBytes(rawJSON, "input", []byte(newInput))
	}

	rawJSON, _ = sjson.SetBytes(rawJSON, "instructions", instructions)

	return rawJSON
}
