package util

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestAddOpenAIAgentToolUseInstruction_AppendsForFunctionTools(t *testing.T) {
	input := []byte(`{
		"instructions":"Keep answers concise.",
		"tools":[{"type":"function","name":"edit_file"}],
		"tool_choice":"auto"
	}`)

	out := AddOpenAIAgentToolUseInstruction(input)
	instructions := gjson.GetBytes(out, "instructions").String()

	if !strings.Contains(instructions, "Keep answers concise.") {
		t.Fatalf("existing instructions were not preserved: %s", instructions)
	}
	if !strings.Contains(instructions, "Do not end a turn by only saying") {
		t.Fatalf("agent tool-use instruction was not appended: %s", instructions)
	}
}

func TestAddOpenAIAgentToolUseInstruction_SkipsToolChoiceNone(t *testing.T) {
	input := []byte(`{
		"instructions":"Keep answers concise.",
		"tools":[{"type":"function","name":"edit_file"}],
		"tool_choice":"none"
	}`)

	out := AddOpenAIAgentToolUseInstruction(input)
	instructions := gjson.GetBytes(out, "instructions").String()

	if strings.Contains(instructions, "Do not end a turn by only saying") {
		t.Fatalf("agent tool-use instruction should be skipped for tool_choice none: %s", instructions)
	}
}

func TestAddOpenAIAgentToolUseInstruction_SkipsNonFunctionTools(t *testing.T) {
	input := []byte(`{
		"instructions":"Keep answers concise.",
		"tools":[{"type":"web_search"}],
		"tool_choice":"auto"
	}`)

	out := AddOpenAIAgentToolUseInstruction(input)
	instructions := gjson.GetBytes(out, "instructions").String()

	if strings.Contains(instructions, "Do not end a turn by only saying") {
		t.Fatalf("agent tool-use instruction should be skipped without function tools: %s", instructions)
	}
}

func TestRequireOpenAIAgentFunctionToolChoice_SetsRequiredForAuto(t *testing.T) {
	input := []byte(`{
		"tools":[{"type":"function","name":"edit_file"}],
		"tool_choice":"auto"
	}`)

	out := RequireOpenAIAgentFunctionToolChoice(input)

	if got := gjson.GetBytes(out, "tool_choice").String(); got != "required" {
		t.Fatalf("tool_choice = %q, want required; output=%s", got, string(out))
	}
}

func TestRequireOpenAIAgentFunctionToolChoice_SetsRequiredWhenMissing(t *testing.T) {
	input := []byte(`{"tools":[{"type":"function","name":"edit_file"}]}`)

	out := RequireOpenAIAgentFunctionToolChoice(input)

	if got := gjson.GetBytes(out, "tool_choice").String(); got != "required" {
		t.Fatalf("tool_choice = %q, want required; output=%s", got, string(out))
	}
}

func TestRequireOpenAIAgentFunctionToolChoice_AllowsFinalAfterToolOutput(t *testing.T) {
	input := []byte(`{
		"tools":[{"type":"function","name":"read_file"}],
		"tool_choice":"auto",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"read then summarize"}]},
			{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_1","output":"file content"}
		]
	}`)

	out := RequireOpenAIAgentFunctionToolChoice(input)

	if got := gjson.GetBytes(out, "tool_choice").String(); got != "auto" {
		t.Fatalf("tool_choice = %q, want auto after tool output; output=%s", got, string(out))
	}
}

func TestRequireOpenAIAgentFunctionToolChoice_LeavesMissingChoiceAfterToolOutput(t *testing.T) {
	input := []byte(`{
		"tools":[{"type":"function","name":"read_file"}],
		"input":[
			{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{}"},
			{"type":"custom_tool_call_output","call_id":"call_1","output":"ok"}
		]
	}`)

	out := RequireOpenAIAgentFunctionToolChoice(input)

	if gjson.GetBytes(out, "tool_choice").Exists() {
		t.Fatalf("tool_choice should remain missing after tool output; output=%s", string(out))
	}
}

func TestRequireOpenAIAgentFunctionToolChoice_PreservesNoneAndSpecificFunction(t *testing.T) {
	cases := []struct {
		name  string
		input []byte
		want  string
	}{
		{
			name:  "none",
			input: []byte(`{"tools":[{"type":"function","name":"edit_file"}],"tool_choice":"none"}`),
			want:  `"none"`,
		},
		{
			name:  "specific responses function",
			input: []byte(`{"tools":[{"type":"function","name":"edit_file"}],"tool_choice":{"type":"function","name":"edit_file"}}`),
			want:  `{"type":"function","name":"edit_file"}`,
		},
		{
			name:  "specific chat function",
			input: []byte(`{"tools":[{"type":"function","name":"edit_file"}],"tool_choice":{"type":"function","function":{"name":"edit_file"}}}`),
			want:  `{"type":"function","function":{"name":"edit_file"}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := RequireOpenAIAgentFunctionToolChoice(tc.input)
			if got := gjson.GetBytes(out, "tool_choice").Raw; got != tc.want {
				t.Fatalf("tool_choice = %s, want %s; output=%s", got, tc.want, string(out))
			}
		})
	}
}
