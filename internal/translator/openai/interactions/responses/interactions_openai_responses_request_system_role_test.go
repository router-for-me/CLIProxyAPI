package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

const testSystemText = "You are a strict coding agent. Never edit files without reading them first."

func inputStepTypes(out []byte) []string {
	var types []string
	gjson.GetBytes(out, "input").ForEach(func(_, step gjson.Result) bool {
		types = append(types, step.Get("type").String())
		return true
	})
	return types
}

func assertSystemAndSteps(t *testing.T, out []byte, wantSystem string, wantSteps []string) {
	t.Helper()
	if got := gjson.GetBytes(out, "system_instruction").String(); got != wantSystem {
		t.Fatalf("system_instruction = %q, want %q. Output: %s", got, wantSystem, out)
	}
	got := inputStepTypes(out)
	if len(got) != len(wantSteps) {
		t.Fatalf("input steps = %v, want %v. Output: %s", got, wantSteps, out)
	}
	for i := range got {
		if got[i] != wantSteps[i] {
			t.Fatalf("input steps = %v, want %v. Output: %s", got, wantSteps, out)
		}
	}
}

// A leading input item with role system must become system_instruction, not a
// user turn, regardless of whether "type":"message" is spelled out.
func TestConvertOpenAIResponsesRequestToInteractions_HoistsLeadingSystemMessage(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"typed message", `{"model":"devin/swe-2","input":[{"type":"message","role":"system","content":"` + testSystemText + `"},{"type":"message","role":"user","content":"hi"}]}`},
		{"shorthand", `{"model":"devin/swe-2","input":[{"role":"system","content":"` + testSystemText + `"},{"role":"user","content":"hi"}]}`},
		{"developer role", `{"model":"devin/swe-2","input":[{"role":"developer","content":"` + testSystemText + `"},{"role":"user","content":"hi"}]}`},
		{"content parts", `{"model":"devin/swe-2","input":[{"role":"system","content":[{"type":"input_text","text":"` + testSystemText + `"}]},{"role":"user","content":"hi"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := ConvertOpenAIResponsesRequestToInteractions("devin/swe-2", []byte(tc.raw), false)
			assertSystemAndSteps(t, out, testSystemText, []string{"user_input"})
			if got := gjson.GetBytes(out, "input.0.content.0.text").String(); got != "hi" {
				t.Fatalf("remaining user turn = %q, want hi. Output: %s", got, out)
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToInteractions_MergesInstructionsAndLeadingSystem(t *testing.T) {
	raw := `{"model":"devin/swe-2","instructions":"Top-level instructions.","input":[{"role":"system","content":"First system."},{"role":"developer","content":"Second developer."},{"role":"user","content":"hi"}]}`
	out := ConvertOpenAIResponsesRequestToInteractions("devin/swe-2", []byte(raw), false)
	assertSystemAndSteps(t, out, "Top-level instructions.\n\nFirst system.\n\nSecond developer.", []string{"user_input"})
}

// Developer messages that appear after the conversation has started stay in
// place as user turns (matching the Gemini translator, #5490) so history and
// caching are not disturbed.
func TestConvertOpenAIResponsesRequestToInteractions_MidConversationDeveloperStaysInPlace(t *testing.T) {
	raw := `{"model":"devin/swe-2","input":[{"role":"system","content":"Sys."},{"role":"user","content":"q1"},{"role":"assistant","content":"a1"},{"role":"developer","content":"mid-session note"},{"role":"user","content":"q2"}]}`
	out := ConvertOpenAIResponsesRequestToInteractions("devin/swe-2", []byte(raw), false)
	assertSystemAndSteps(t, out, "Sys.", []string{"user_input", "model_output", "user_input", "user_input"})
	if got := gjson.GetBytes(out, "input.2.content.0.text").String(); got != "mid-session note" {
		t.Fatalf("mid-session developer text = %q. Output: %s", got, out)
	}
}

// Shorthand assistant items (no "type") must map to model_output, not user_input.
func TestConvertOpenAIResponsesRequestToInteractions_ShorthandAssistantIsModelOutput(t *testing.T) {
	raw := `{"model":"devin/swe-2","input":[{"role":"user","content":"q"},{"role":"assistant","content":"a"},{"role":"user","content":"q2"}]}`
	out := ConvertOpenAIResponsesRequestToInteractions("devin/swe-2", []byte(raw), false)
	assertSystemAndSteps(t, out, "", []string{"user_input", "model_output", "user_input"})
	if gjson.GetBytes(out, "system_instruction").Exists() {
		t.Fatalf("system_instruction must be absent when nothing provides it. Output: %s", out)
	}
}

func TestConvertOpenAIResponsesRequestToInteractions_TopLevelInstructionsUnchanged(t *testing.T) {
	raw := `{"model":"devin/swe-2","instructions":"` + testSystemText + `","input":[{"role":"user","content":"hi"}]}`
	out := ConvertOpenAIResponsesRequestToInteractions("devin/swe-2", []byte(raw), false)
	assertSystemAndSteps(t, out, testSystemText, []string{"user_input"})
}

func TestConvertOpenAIResponsesRequestToInteractions_SingleSystemObjectInput(t *testing.T) {
	raw := `{"model":"devin/swe-2","input":{"role":"system","content":"Only system."}}`
	out := ConvertOpenAIResponsesRequestToInteractions("devin/swe-2", []byte(raw), false)
	if got := gjson.GetBytes(out, "system_instruction").String(); got != "Only system." {
		t.Fatalf("system_instruction = %q. Output: %s", got, out)
	}
	if gjson.GetBytes(out, "input.#").Int() != 0 {
		t.Fatalf("no input steps expected. Output: %s", out)
	}
}
