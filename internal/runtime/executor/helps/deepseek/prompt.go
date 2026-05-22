package deepseek

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/google/uuid"
)

type RequestSpec struct {
	Prompt          string
	ToolsRaw        any
	ToolNames       []string
	ModelType       string
	ThinkingEnabled bool
	SearchEnabled   bool
	RefFileIDs      []string
}

func BuildRequestSpec(payload []byte, model string) (RequestSpec, error) {
	var body map[string]any
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &body); err != nil {
			return RequestSpec{}, fmt.Errorf("deepseek: parse OpenAI payload: %w", err)
		}
	}
	modelType, searchEnabled, thinkingEnabled := ModelBehavior(model)
	if forced, ok := explicitThinkingEnabled(body); ok {
		thinkingEnabled = forced
	}

	messages, _ := body["messages"].([]any)
	toolsRaw := body["tools"]
	toolChoice := body["tool_choice"]
	promptText, toolNames := BuildOpenAIPrompt(messages, toolsRaw, toolChoice, thinkingEnabled)
	refFileIDs := extractRefFileIDs(body)
	return RequestSpec{
		Prompt:          promptText,
		ToolsRaw:        toolsRaw,
		ToolNames:       toolNames,
		ModelType:       modelType,
		ThinkingEnabled: thinkingEnabled,
		SearchEnabled:   searchEnabled,
		RefFileIDs:      refFileIDs,
	}, nil
}

func ModelBehavior(model string) (modelType string, searchEnabled bool, thinkingEnabled bool) {
	name := strings.ToLower(strings.TrimSpace(model))
	if strings.Contains(name, "(") {
		name = strings.TrimSpace(name[:strings.Index(name, "(")])
	}
	modelType = "default"
	if strings.Contains(name, "pro") || strings.Contains(name, "expert") {
		modelType = "expert"
	}
	searchEnabled = strings.Contains(name, "search")
	thinkingEnabled = true
	if strings.Contains(name, "nothinking") || strings.Contains(name, "no-thinking") {
		thinkingEnabled = false
	}
	return modelType, searchEnabled, thinkingEnabled
}

func explicitThinkingEnabled(body map[string]any) (bool, bool) {
	if body == nil {
		return false, false
	}
	for _, key := range []string{"thinking_enabled", "enable_thinking"} {
		if value, ok := boolFromAny(body[key]); ok {
			return value, true
		}
	}
	if effort := strings.ToLower(strings.TrimSpace(stringFromAny(body["reasoning_effort"]))); effort != "" {
		switch effort {
		case "none", "off", "disable", "disabled":
			return false, true
		default:
			return true, true
		}
	}
	if reasoning, ok := body["reasoning"].(map[string]any); ok {
		if value, okBool := boolFromAny(reasoning["enabled"]); okBool {
			return value, true
		}
		if effort := strings.ToLower(strings.TrimSpace(stringFromAny(reasoning["effort"]))); effort != "" {
			return effort != "none" && effort != "off" && effort != "disabled", true
		}
	}
	if thinking, ok := body["thinking"].(map[string]any); ok {
		if value, okBool := boolFromAny(thinking["enabled"]); okBool {
			return value, true
		}
		if typ := strings.ToLower(strings.TrimSpace(stringFromAny(thinking["type"]))); typ != "" {
			return typ != "disabled" && typ != "none", true
		}
	}
	return false, false
}

func BuildOpenAIPrompt(messagesRaw []any, toolsRaw any, toolChoice any, thinkingEnabled bool) (string, []string) {
	messages := normalizeOpenAIMessages(messagesRaw)
	messages, toolNames := injectToolPrompt(messages, toolsRaw, toolChoice)
	parts := make([]string, 0, len(messages))
	for _, msg := range messages {
		role := strings.ToLower(strings.TrimSpace(stringFromAny(msg["role"])))
		content := strings.TrimSpace(stringFromAny(msg["content"]))
		if content == "" {
			continue
		}
		switch role {
		case "system":
			parts = append(parts, "[system]\n"+content)
		case "assistant":
			parts = append(parts, "[assistant]\n"+content)
		case "tool":
			parts = append(parts, "[tool]\n"+content)
		default:
			parts = append(parts, "[user]\n"+content)
		}
	}
	if !thinkingEnabled {
		parts = append([]string{"Answer directly. Do not reveal chain-of-thought or internal reasoning."}, parts...)
	}
	return strings.Join(parts, "\n\n"), toolNames
}

func normalizeOpenAIMessages(raw []any) []map[string]any {
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(stringFromAny(msg["role"])))
		if role == "developer" {
			role = "system"
		}
		content := normalizeOpenAIContent(msg["content"])
		if role == "assistant" {
			reasoning := normalizeOpenAIContent(msg["reasoning_content"])
			if reasoning == "" {
				reasoning = extractReasoningFromContent(msg["content"])
			}
			toolHistory := formatToolHistory(msg["tool_calls"])
			segments := make([]string, 0, 3)
			if reasoning != "" {
				segments = append(segments, "[reasoning_content]\n"+reasoning+"\n[/reasoning_content]")
			}
			if content != "" {
				segments = append(segments, content)
			}
			if toolHistory != "" {
				segments = append(segments, toolHistory)
			}
			content = strings.Join(segments, "\n\n")
		}
		if role == "tool" || role == "function" {
			if content == "" {
				content = "null"
			}
			role = "tool"
		}
		if role == "" {
			role = "user"
		}
		if strings.TrimSpace(content) == "" {
			continue
		}
		out = append(out, map[string]any{"role": role, "content": content})
	}
	return out
}

func normalizeOpenAIContent(v any) string {
	switch value := v.(type) {
	case nil:
		return ""
	case string:
		return value
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			part, ok := item.(map[string]any)
			if !ok {
				if s := strings.TrimSpace(stringFromAny(item)); s != "" {
					parts = append(parts, s)
				}
				continue
			}
			typ := strings.ToLower(strings.TrimSpace(stringFromAny(part["type"])))
			switch typ {
			case "text", "input_text", "":
				if s := strings.TrimSpace(stringFromAny(part["text"])); s != "" {
					parts = append(parts, s)
				}
			case "image_url", "input_image":
				if imageURL, ok := part["image_url"].(map[string]any); ok {
					if url := strings.TrimSpace(stringFromAny(imageURL["url"])); url != "" {
						parts = append(parts, "[image: "+url+"]")
					}
				} else if url := strings.TrimSpace(stringFromAny(part["image_url"])); url != "" {
					parts = append(parts, "[image: "+url+"]")
				}
			case "reasoning", "thinking":
				if s := strings.TrimSpace(stringFromAny(part["text"])); s != "" {
					parts = append(parts, "[reasoning_content]\n"+s+"\n[/reasoning_content]")
				}
			default:
				if s := strings.TrimSpace(stringFromAny(part["text"])); s != "" {
					parts = append(parts, s)
				}
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		if s := strings.TrimSpace(stringFromAny(value["text"])); s != "" {
			return s
		}
		if s := strings.TrimSpace(stringFromAny(value["content"])); s != "" {
			return s
		}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func extractReasoningFromContent(v any) string {
	items, ok := v.([]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0)
	for _, item := range items {
		part, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(stringFromAny(part["type"]))) {
		case "reasoning", "thinking":
			if text := strings.TrimSpace(stringFromAny(part["text"])); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func formatToolHistory(v any) string {
	calls, ok := v.([]any)
	if !ok || len(calls) == 0 {
		return ""
	}
	parts := make([]string, 0, len(calls))
	for _, item := range calls {
		call, ok := item.(map[string]any)
		if !ok {
			continue
		}
		fn, _ := call["function"].(map[string]any)
		name := strings.TrimSpace(stringFromAny(fn["name"]))
		args := strings.TrimSpace(stringFromAny(fn["arguments"]))
		if name == "" {
			continue
		}
		parts = append(parts, "Tool call: "+name+"\nArguments: "+args)
	}
	return strings.Join(parts, "\n\n")
}

func injectToolPrompt(messages []map[string]any, toolsRaw any, toolChoice any) ([]map[string]any, []string) {
	if toolChoiceNone(toolChoice) {
		return messages, nil
	}
	tools, ok := toolsRaw.([]any)
	if !ok || len(tools) == 0 {
		return messages, nil
	}
	names := make([]string, 0, len(tools))
	schemas := make([]string, 0, len(tools))
	for _, item := range tools {
		tool, ok := item.(map[string]any)
		if !ok {
			continue
		}
		fn, _ := tool["function"].(map[string]any)
		if len(fn) == 0 && strings.EqualFold(stringFromAny(tool["type"]), "function") {
			fn = tool
		}
		name := strings.TrimSpace(stringFromAny(fn["name"]))
		if name == "" {
			continue
		}
		names = append(names, name)
		desc := strings.TrimSpace(stringFromAny(fn["description"]))
		if desc == "" {
			desc = "No description available"
		}
		params := fn["parameters"]
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		paramJSON, _ := json.Marshal(params)
		schemas = append(schemas, fmt.Sprintf("Tool: %s\nDescription: %s\nParameters: %s", name, desc, string(paramJSON)))
	}
	if len(schemas) == 0 {
		return messages, names
	}
	toolPrompt := "You have access to these tools:\n\n" + strings.Join(schemas, "\n\n") + "\n\n" + toolCallInstructions(names, toolChoice)
	for i := range messages {
		if strings.EqualFold(stringFromAny(messages[i]["role"]), "system") {
			old := strings.TrimSpace(stringFromAny(messages[i]["content"]))
			messages[i]["content"] = strings.TrimSpace(old + "\n\n" + toolPrompt)
			return messages, names
		}
	}
	return append([]map[string]any{{"role": "system", "content": toolPrompt}}, messages...), names
}

func toolChoiceNone(choice any) bool {
	switch v := choice.(type) {
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "none")
	case map[string]any:
		return strings.EqualFold(strings.TrimSpace(stringFromAny(v["type"])), "none")
	default:
		return false
	}
}

func toolCallInstructions(toolNames []string, toolChoice any) string {
	var b strings.Builder
	b.WriteString(`TOOL CALL FORMAT — FOLLOW EXACTLY:

<|DSML|tool_calls>
  <|DSML|invoke name="TOOL_NAME_HERE">
    <|DSML|parameter name="PARAMETER_NAME"><![CDATA[PARAMETER_VALUE]]></|DSML|parameter>
  </|DSML|invoke>
</|DSML|tool_calls>

Rules:
1) If a tool is needed, output only one <|DSML|tool_calls> block.
2) Use one or more <|DSML|invoke> entries.
3) Use only available tool names and schema parameter names.
4) Do not wrap tool XML in Markdown fences.
`)
	if forced := forcedToolName(toolChoice); forced != "" {
		b.WriteString("5) You must call exactly this tool: " + forced + "\n")
	} else if requiredToolChoice(toolChoice) {
		b.WriteString("5) You must call at least one tool.\n")
	}
	if len(toolNames) > 0 {
		b.WriteString("\nAvailable tool names: " + strings.Join(toolNames, ", ") + "\n")
	}
	return b.String()
}

func forcedToolName(choice any) string {
	m, ok := choice.(map[string]any)
	if !ok {
		return ""
	}
	if !strings.EqualFold(strings.TrimSpace(stringFromAny(m["type"])), "function") {
		return ""
	}
	fn, _ := m["function"].(map[string]any)
	return strings.TrimSpace(stringFromAny(fn["name"]))
}

func requiredToolChoice(choice any) bool {
	if s, ok := choice.(string); ok {
		return strings.EqualFold(strings.TrimSpace(s), "required")
	}
	return false
}

func ParseToolCalls(text string, availableToolNames []string) ([]map[string]any, string) {
	block, start, end := findToolCallBlock(text)
	if block == "" {
		return nil, text
	}
	cleaned := strings.TrimSpace(text[:start] + text[end:])
	calls := parseToolCallBlock(block, availableToolNames)
	return calls, cleaned
}

func findToolCallBlock(text string) (string, int, int) {
	re := regexp.MustCompile(`(?is)<(?:\|DSML\|)?tool_calls\b[^>]*>.*?</(?:\|DSML\|)?tool_calls>`)
	locs := re.FindAllStringIndex(text, -1)
	if len(locs) == 0 {
		return "", -1, -1
	}
	loc := locs[len(locs)-1]
	return text[loc[0]:loc[1]], loc[0], loc[1]
}

type xmlToolCalls struct {
	Invokes []xmlInvoke `xml:"invoke"`
}

type xmlInvoke struct {
	Name   string     `xml:"name,attr"`
	Params []xmlParam `xml:"parameter"`
}

type xmlParam struct {
	Name  string `xml:"name,attr"`
	Inner string `xml:",innerxml"`
}

func parseToolCallBlock(block string, availableToolNames []string) []map[string]any {
	normalized := normalizeToolXML(block)
	var root xmlToolCalls
	if err := xml.Unmarshal([]byte(normalized), &root); err != nil {
		return nil
	}
	allowed := make(map[string]struct{}, len(availableToolNames))
	for _, name := range availableToolNames {
		name = strings.TrimSpace(name)
		if name != "" {
			allowed[name] = struct{}{}
		}
	}
	out := make([]map[string]any, 0, len(root.Invokes))
	for _, invoke := range root.Invokes {
		name := strings.TrimSpace(invoke.Name)
		if name == "" {
			continue
		}
		if len(allowed) > 0 {
			if _, ok := allowed[name]; !ok {
				continue
			}
		}
		args := make(map[string]any)
		for _, param := range invoke.Params {
			paramName := strings.TrimSpace(param.Name)
			if paramName == "" {
				continue
			}
			args[paramName] = parseToolParamValue(param.Inner)
		}
		argsJSON, _ := json.Marshal(args)
		out = append(out, map[string]any{
			"id":   "call_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
			"type": "function",
			"function": map[string]any{
				"name":      name,
				"arguments": string(argsJSON),
			},
		})
	}
	return out
}

func normalizeToolXML(block string) string {
	replacements := map[string]string{
		"<|DSML|tool_calls":   "<tool_calls",
		"</|DSML|tool_calls>": "</tool_calls>",
		"<|DSML|invoke":       "<invoke",
		"</|DSML|invoke>":     "</invoke>",
		"<|DSML|parameter":    "<parameter",
		"</|DSML|parameter>":  "</parameter>",
	}
	out := block
	for old, newValue := range replacements {
		out = strings.ReplaceAll(out, old, newValue)
	}
	return out
}

func parseToolParamValue(inner string) any {
	text := strings.TrimSpace(stripXMLTags(inner))
	text = html.UnescapeString(text)
	if text == "" {
		return ""
	}
	var decoded any
	if (strings.HasPrefix(text, "{") && strings.HasSuffix(text, "}")) || (strings.HasPrefix(text, "[") && strings.HasSuffix(text, "]")) {
		if err := json.Unmarshal([]byte(text), &decoded); err == nil {
			return decoded
		}
	}
	if strings.EqualFold(text, "true") {
		return true
	}
	if strings.EqualFold(text, "false") {
		return false
	}
	if strings.EqualFold(text, "null") {
		return nil
	}
	if n, err := strconv.ParseFloat(text, 64); err == nil && numericText(text) {
		return n
	}
	return text
}

func stripXMLTags(s string) string {
	re := regexp.MustCompile(`(?s)<[^>]+>`)
	return re.ReplaceAllString(s, "")
}

func numericText(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if unicode.IsDigit(r) || r == '.' {
			continue
		}
		if i == 0 && (r == '-' || r == '+') {
			continue
		}
		return false
	}
	return true
}

func extractRefFileIDs(body map[string]any) []string {
	if body == nil {
		return nil
	}
	keys := []string{"ref_file_ids", "ref_files", "file_ids"}
	out := make([]string, 0)
	for _, key := range keys {
		switch value := body[key].(type) {
		case []any:
			for _, item := range value {
				if s := strings.TrimSpace(stringFromAny(item)); s != "" {
					out = append(out, s)
				}
			}
		case []string:
			for _, item := range value {
				if s := strings.TrimSpace(item); s != "" {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

func boolFromAny(v any) (bool, bool) {
	switch value := v.(type) {
	case bool:
		return value, true
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "true", "1", "yes", "on", "enabled":
			return true, true
		case "false", "0", "no", "off", "disabled":
			return false, true
		}
	}
	return false, false
}

func stringFromAny(v any) string {
	switch value := v.(type) {
	case nil:
		return ""
	case string:
		return value
	case fmt.Stringer:
		return value.String()
	default:
		return fmt.Sprintf("%v", value)
	}
}
