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
