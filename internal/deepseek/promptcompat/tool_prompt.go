package promptcompat

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/deepseek/toolcall"
)

const CurrentToolsContextFilename = "DS2API_TOOLS.txt"

const toolsTranscriptTitle = "# DS2API_TOOLS.txt"
const toolsTranscriptSummary = "Available tool descriptions and parameter schemas for this request."

type toolPromptParts struct {
	Descriptions string
	Instructions string
	Names        []string
}

func injectToolPrompt(messages []map[string]any, tools []any, policy ToolChoicePolicy) ([]map[string]any, []string) {
	return injectToolPromptWithDescriptions(messages, tools, policy, true)
}

func injectToolPromptInstructionsOnly(messages []map[string]any, tools []any, policy ToolChoicePolicy) ([]map[string]any, []string) {
	return injectToolPromptWithDescriptions(messages, tools, policy, false)
}

func injectToolPromptWithDescriptions(messages []map[string]any, tools []any, policy ToolChoicePolicy, includeDescriptions bool) ([]map[string]any, []string) {
	if policy.IsNone() {
		return messages, nil
	}
	parts := buildToolPromptParts(tools, policy)
	if parts.Instructions == "" {
		return messages, parts.Names
	}
	toolPrompt := parts.Instructions
	if includeDescriptions && parts.Descriptions != "" {
		toolPrompt = parts.Descriptions + "\n\n" + toolPrompt
	} else if !includeDescriptions && parts.Descriptions != "" {
		toolPrompt = "Available tool descriptions and parameter schemas are attached in DS2API_TOOLS.txt. Treat DS2API_TOOLS.txt as the authoritative list of callable tools and schemas; use only tools and parameters listed there.\n\n" + toolPrompt
	}

	for i := range messages {
		if messages[i]["role"] == "system" {
			old, _ := messages[i]["content"].(string)
			messages[i]["content"] = strings.TrimSpace(old + "\n\n" + toolPrompt)
			return messages, parts.Names
		}
	}
	messages = append([]map[string]any{{"role": "system", "content": toolPrompt}}, messages...)
	return messages, parts.Names
}

func buildToolPromptParts(tools []any, policy ToolChoicePolicy) toolPromptParts {
	toolSchemas := make([]string, 0, len(tools))
	names := make([]string, 0, len(tools))
	isAllowed := func(name string) bool {
		if strings.TrimSpace(name) == "" {
			return false
		}
		if len(policy.Allowed) == 0 {
			return true
		}
		_, ok := policy.Allowed[name]
		return ok
	}

	for _, t := range tools {
		tool, ok := t.(map[string]any)
		if !ok {
			continue
		}
		name, desc, schema := toolcall.ExtractToolMeta(tool)
		name = strings.TrimSpace(name)
		if !isAllowed(name) {
			continue
		}
		names = append(names, name)
		if desc == "" {
			desc = "No description available"
		}
		b, _ := json.Marshal(schema)
		toolSchemas = append(toolSchemas, fmt.Sprintf("Tool: %s\nDescription: %s\nParameters: %s", name, desc, string(b)))
	}
	if len(toolSchemas) == 0 {
		return toolPromptParts{Names: names}
	}
	descriptions := "You have access to these tools:\n\n" + strings.Join(toolSchemas, "\n\n")
	instructions := toolcall.BuildToolCallInstructions(names)
	if hasReadLikeTool(names) {
		instructions += "\n\nRead-tool cache guard: If a Read/read_file-style tool result says the file is unchanged, already available in history, should be referenced from previous context, or otherwise provides no file body, treat that result as missing content. Do not repeatedly call the same read request for that missing body. Request a full-content read if the tool supports it, or tell the user that the file contents need to be provided again."
	}
	if policy.Mode == ToolChoiceRequired {
		instructions += "\n7) For this response, you MUST call at least one tool from the allowed list."
	}
	if policy.Mode == ToolChoiceForced && strings.TrimSpace(policy.ForcedName) != "" {
		instructions += "\n7) For this response, you MUST call exactly this tool name: " + strings.TrimSpace(policy.ForcedName)
		instructions += "\n8) Do not call any other tool."
	}
	return toolPromptParts{
		Descriptions: descriptions,
		Instructions: instructions,
		Names:        names,
	}
}

func BuildOpenAIToolsContextTranscript(toolsRaw any, policy ToolChoicePolicy) (string, []string) {
	if policy.IsNone() {
		return "", nil
	}
	tools, ok := toolsRaw.([]any)
	if !ok || len(tools) == 0 {
		return "", nil
	}
	parts := buildToolPromptParts(tools, policy)
	if strings.TrimSpace(parts.Descriptions) == "" {
		return "", parts.Names
	}
	var b strings.Builder
	b.WriteString(toolsTranscriptTitle)
	b.WriteString("\n")
	b.WriteString(toolsTranscriptSummary)
	b.WriteString("\n\n")
	b.WriteString(parts.Descriptions)
	b.WriteString("\n")
	return b.String(), parts.Names
}

func hasReadLikeTool(names []string) bool {
	for _, name := range names {
		switch normalizeToolNameForGuard(name) {
		case "read", "readfile":
			return true
		}
	}
	return false
}

func normalizeToolNameForGuard(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
