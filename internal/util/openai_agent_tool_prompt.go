package util

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const openAIAgentToolUseInstruction = "Agent tool-use compatibility: When function tools are available and the next step requires inspecting files, editing files, running commands, or otherwise acting outside the conversation, call the appropriate tool in this response. Do not end a turn by only saying that you will use a tool or make a change."

// AddOpenAIAgentToolUseInstruction appends a Codex-facing tool-use hint for
// OpenAI-compatible agent clients. It keeps final answer turns free to answer
// normally, while nudging action turns away from promise-only text.
func AddOpenAIAgentToolUseInstruction(rawJSON []byte) []byte {
	if !hasOpenAIFunctionTools(rawJSON) || isOpenAIToolChoiceNone(rawJSON) {
		return rawJSON
	}

	instructions := strings.TrimSpace(gjson.GetBytes(rawJSON, "instructions").String())
	if strings.Contains(instructions, openAIAgentToolUseInstruction) {
		return rawJSON
	}

	nextInstructions := openAIAgentToolUseInstruction
	if instructions != "" {
		nextInstructions = instructions + "\n\n" + nextInstructions
	}

	updated, err := sjson.SetBytes(rawJSON, "instructions", nextInstructions)
	if err != nil {
		return rawJSON
	}
	return updated
}

func hasOpenAIFunctionTools(rawJSON []byte) bool {
	tools := gjson.GetBytes(rawJSON, "tools")
	if !tools.IsArray() {
		return false
	}
	for _, tool := range tools.Array() {
		if tool.Get("type").String() == "function" {
			return true
		}
	}
	return false
}

func isOpenAIToolChoiceNone(rawJSON []byte) bool {
	toolChoice := gjson.GetBytes(rawJSON, "tool_choice")
	if !toolChoice.Exists() {
		return false
	}
	if toolChoice.Type == gjson.String {
		return strings.EqualFold(strings.TrimSpace(toolChoice.String()), "none")
	}
	if toolChoice.IsObject() {
		return strings.EqualFold(strings.TrimSpace(toolChoice.Get("type").String()), "none")
	}
	return false
}
