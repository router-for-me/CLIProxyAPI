package util

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const openAIAgentToolUseInstruction = "Agent tool-use compatibility: When function tools are available and the next step requires inspecting files, editing files, running commands, or otherwise acting outside the conversation, call the appropriate tool in this response. Do not end a turn by only saying that you will use a tool or make a change. Cursor file/search tools: use only workspace roots or directories already observed from tool results; for Glob provide a real target_directory plus a specific glob_pattern. Never use Glob with **/* alone; use a file-type glob such as **/*.{js,ts,tsx,json,md} or use shell rg --files for broad listing. For rg/search, path must be a verified directory, not a file; use recursive globs such as **/*.tsx to limit files, or ReadFile for a specific file. Avoid inventing Windows absolute paths. Cursor Subagent: include cloud_base_branch only when environment is cloud; omit it for local/default environments."

// RequireOpenAIAgentFunctionToolChoice makes OpenAI-compatible agent tool turns
// deterministic for Codex upstream. Cursor often sends function tools with
// tool_choice=auto, but Codex may answer with promise-only text instead of a
// tool call. Requiring a function tool keeps those turns in Cursor's expected
// tool-call loop while preserving explicit "none" and specific function choices.
func RequireOpenAIAgentFunctionToolChoice(rawJSON []byte) []byte {
	if !hasOpenAIFunctionTools(rawJSON) || isOpenAIToolChoiceNone(rawJSON) || hasSpecificOpenAIFunctionToolChoice(rawJSON) {
		return rawJSON
	}

	toolChoice := gjson.GetBytes(rawJSON, "tool_choice")
	if lastInputIsOpenAIToolOutput(rawJSON) {
		return rawJSON
	}
	if toolChoice.Exists() && toolChoice.IsObject() && !isAutoOpenAIToolChoice(toolChoice) {
		return rawJSON
	}

	updated, err := sjson.SetBytes(rawJSON, "tool_choice", "required")
	if err != nil {
		return rawJSON
	}
	return updated
}

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

func hasSpecificOpenAIFunctionToolChoice(rawJSON []byte) bool {
	toolChoice := gjson.GetBytes(rawJSON, "tool_choice")
	if !toolChoice.IsObject() {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(toolChoice.Get("type").String()), "function") {
		return false
	}
	return strings.TrimSpace(toolChoice.Get("name").String()) != "" ||
		strings.TrimSpace(toolChoice.Get("function.name").String()) != ""
}

func isAutoOpenAIToolChoice(toolChoice gjson.Result) bool {
	if toolChoice.Type == gjson.String {
		return strings.EqualFold(strings.TrimSpace(toolChoice.String()), "auto")
	}
	if toolChoice.IsObject() {
		return strings.EqualFold(strings.TrimSpace(toolChoice.Get("type").String()), "auto")
	}
	return false
}

func lastInputIsOpenAIToolOutput(rawJSON []byte) bool {
	input := gjson.GetBytes(rawJSON, "input")
	if !input.IsArray() {
		return false
	}
	items := input.Array()
	if len(items) == 0 {
		return false
	}
	switch strings.TrimSpace(items[len(items)-1].Get("type").String()) {
	case "function_call_output", "custom_tool_call_output":
		return true
	default:
		return false
	}
}
