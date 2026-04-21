package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToGemini_DeveloperBecomesSystemInstruction(t *testing.T) {
	in := []byte(`{"input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"stay strict"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	out := ConvertOpenAIResponsesRequestToGemini("gemini-3-flash-preview", in, false)

	if got := gjson.GetBytes(out, "systemInstruction.parts.0.text").String(); got != "stay strict" {
		t.Fatalf("unexpected system instruction text: %q", got)
	}
	if got := gjson.GetBytes(out, "contents.0.role").String(); got != "user" {
		t.Fatalf("unexpected first content role: %q", got)
	}
	if got := gjson.GetBytes(out, "contents.0.parts.0.text").String(); got != "hi" {
		t.Fatalf("unexpected first content text: %q", got)
	}
	if gjson.GetBytes(out, "contents.#(role=developer)").Exists() {
		t.Fatalf("developer role leaked into gemini payload: %s", string(out))
	}
}
