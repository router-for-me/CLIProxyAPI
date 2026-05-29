package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToGemini_SystemAndDeveloperRoles(t *testing.T) {
	// Test system role conversion
	systemInput := []byte(`{
		"instructions": "Be a helpful assistant",
		"input": [
			{
				"type": "message",
				"role": "system",
				"content": [
					{
						"type": "input_text",
						"text": "System message text"
					}
				]
			},
			{
				"type": "message",
				"role": "user",
				"content": [
					{
						"type": "input_text",
						"text": "Hello"
					}
				]
			}
		]
	}`)

	outSystem := ConvertOpenAIResponsesRequestToGemini("gemini-3.5-flash", systemInput, false)
	resSystem := gjson.ParseBytes(outSystem)

	systemInstruction := resSystem.Get("systemInstruction")
	if !systemInstruction.Exists() {
		t.Errorf("Expected systemInstruction field to exist")
	}
	parts := systemInstruction.Get("parts")
	if parts.Get("#").Int() != 2 {
		t.Errorf("Expected 2 parts in systemInstruction, got %d", parts.Get("#").Int())
	}
	if parts.Get("0.text").String() != "Be a helpful assistant" {
		t.Errorf("Expected first part to be 'Be a helpful assistant', got '%s'", parts.Get("0.text").String())
	}
	if parts.Get("1.text").String() != "System message text" {
		t.Errorf("Expected second part to be 'System message text', got '%s'", parts.Get("1.text").String())
	}

	// Test developer role conversion (which is the main bug we're addressing)
	developerInput := []byte(`{
		"instructions": "Be a helpful assistant",
		"input": [
			{
				"type": "message",
				"role": "developer",
				"content": [
					{
						"type": "input_text",
						"text": "Developer message text"
					}
				]
			},
			{
				"type": "message",
				"role": "user",
				"content": [
					{
						"type": "input_text",
						"text": "Hello"
					}
				]
			}
		]
	}`)

	outDev := ConvertOpenAIResponsesRequestToGemini("gemini-3.5-flash", developerInput, false)
	resDev := gjson.ParseBytes(outDev)

	systemInstructionDev := resDev.Get("systemInstruction")
	if !systemInstructionDev.Exists() {
		t.Errorf("Expected systemInstruction field to exist for developer role")
	}
	partsDev := systemInstructionDev.Get("parts")
	if partsDev.Get("#").Int() != 2 {
		t.Errorf("Expected 2 parts in systemInstruction for developer role, got %d", partsDev.Get("#").Int())
	}
	if partsDev.Get("0.text").String() != "Be a helpful assistant" {
		t.Errorf("Expected first part to be 'Be a helpful assistant', got '%s'", partsDev.Get("0.text").String())
	}
	if partsDev.Get("1.text").String() != "Developer message text" {
		t.Errorf("Expected second part to be 'Developer message text', got '%s'", partsDev.Get("1.text").String())
	}

	// Ensure role 'developer' is not sent inside contents array as a regular message
	contents := resDev.Get("contents")
	contents.ForEach(func(_, value gjson.Result) bool {
		role := value.Get("role").String()
		if role == "developer" {
			t.Errorf("Role 'developer' leaked into contents array")
		}
		return true
	})
}
