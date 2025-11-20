// Package claude provides request translation functionality for Claude Code API compatibility.
// It handles parsing and transforming Claude Code API requests into the internal client format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package also performs JSON data cleaning and transformation to ensure compatibility
// between Claude Code API format and the internal client's expected format.
package claude

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertClaudeRequestToCodex parses and transforms a Claude Code API request into the internal client format.
// It extracts the model name, system instruction, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the internal client.
// The function performs the following transformations:
// 1. Sets up a template with the model name and Codex instructions
// 2. Processes system messages and converts them to input content
// 3. Transforms message contents (text, tool_use, tool_result) to appropriate formats
// 4. Converts tools declarations to the expected format
// 5. Adds additional configuration parameters for the Codex API
// 6. Prepends a special instruction message to override system instructions
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - rawJSON: The raw JSON request data from the Claude Code API
//   - stream: A boolean indicating if the request is for a streaming response (unused in current implementation)
//
// Returns:
//   - []byte: The transformed request data in internal client format
func ConvertClaudeRequestToCodex(modelName string, inputRawJSON []byte, _ bool) []byte {
	rawJSON := bytes.Clone(inputRawJSON)

	template := `{"model":"","instructions":"","input":[]}`

	_, instructions := misc.CodexInstructionsForModel(modelName, "")
	template, _ = sjson.Set(template, "instructions", instructions)

	rootResult := gjson.ParseBytes(rawJSON)
	template, _ = sjson.Set(template, "model", modelName)

	// Process system messages and convert them to input content format.
	systemsResult := rootResult.Get("system")
	if systemsResult.IsArray() {
		systemResults := systemsResult.Array()
		message := `{"type":"message","role":"user","content":[]}`
		for i := 0; i < len(systemResults); i++ {
			systemResult := systemResults[i]
			systemTypeResult := systemResult.Get("type")
			if systemTypeResult.String() == "text" {
				message, _ = sjson.Set(message, fmt.Sprintf("content.%d.type", i), "input_text")
				message, _ = sjson.Set(message, fmt.Sprintf("content.%d.text", i), systemResult.Get("text").String())
			}
		}
		template, _ = sjson.SetRaw(template, "input.-1", message)
	}

	// Process messages and transform their contents to appropriate formats.
	messagesResult := rootResult.Get("messages")
	if messagesResult.IsArray() {
		messageResults := messagesResult.Array()

		for i := 0; i < len(messageResults); i++ {
			messageResult := messageResults[i]
			messageRole := messageResult.Get("role").String()

			newMessage := func() string {
				msg := `{"type": "message","role":"","content":[]}`
				msg, _ = sjson.Set(msg, "role", messageRole)
				return msg
			}

			message := newMessage()
			contentIndex := 0
			hasContent := false

			flushMessage := func() {
				if hasContent {
					template, _ = sjson.SetRaw(template, "input.-1", message)
					message = newMessage()
					contentIndex = 0
					hasContent = false
				}
			}

			appendTextContent := func(text string) {
				partType := "input_text"
				if messageRole == "assistant" {
					partType = "output_text"
				}
				message, _ = sjson.Set(message, fmt.Sprintf("content.%d.type", contentIndex), partType)
				message, _ = sjson.Set(message, fmt.Sprintf("content.%d.text", contentIndex), text)
				contentIndex++
				hasContent = true
			}

			appendImageContent := func(dataURL string) {
				message, _ = sjson.Set(message, fmt.Sprintf("content.%d.type", contentIndex), "input_image")
				message, _ = sjson.Set(message, fmt.Sprintf("content.%d.image_url", contentIndex), dataURL)
				contentIndex++
				hasContent = true
			}

			messageContentsResult := messageResult.Get("content")
			if messageContentsResult.IsArray() {
				messageContentResults := messageContentsResult.Array()
				for j := 0; j < len(messageContentResults); j++ {
					messageContentResult := messageContentResults[j]
					contentType := messageContentResult.Get("type").String()

					switch contentType {
					case "text":
						appendTextContent(messageContentResult.Get("text").String())
					case "image":
						sourceResult := messageContentResult.Get("source")
						if sourceResult.Exists() {
							data := sourceResult.Get("data").String()
							if data == "" {
								data = sourceResult.Get("base64").String()
							}
							if data != "" {
								mediaType := sourceResult.Get("media_type").String()
								if mediaType == "" {
									mediaType = sourceResult.Get("mime_type").String()
								}
								if mediaType == "" {
									mediaType = "application/octet-stream"
								}
								dataURL := fmt.Sprintf("data:%s;base64,%s", mediaType, data)
								appendImageContent(dataURL)
							}
						}
					case "tool_use":
						flushMessage()
						functionCallMessage := `{"type":"function_call"}`
						functionCallMessage, _ = sjson.Set(functionCallMessage, "call_id", messageContentResult.Get("id").String())
						{
							name := messageContentResult.Get("name").String()
							toolMap := buildReverseMapFromClaudeOriginalToShort(rawJSON)
							if short, ok := toolMap[name]; ok {
								name = short
							} else {
								name = shortenNameIfNeeded(name)
							}
							functionCallMessage, _ = sjson.Set(functionCallMessage, "name", name)
						}
						functionCallMessage, _ = sjson.Set(functionCallMessage, "arguments", messageContentResult.Get("input").Raw)
						template, _ = sjson.SetRaw(template, "input.-1", functionCallMessage)
					case "tool_result":
						flushMessage()
						functionCallOutputMessage := `{"type":"function_call_output"}`
						functionCallOutputMessage, _ = sjson.Set(functionCallOutputMessage, "call_id", messageContentResult.Get("tool_use_id").String())
						functionCallOutputMessage, _ = sjson.Set(functionCallOutputMessage, "output", messageContentResult.Get("content").String())
						template, _ = sjson.SetRaw(template, "input.-1", functionCallOutputMessage)
					}
				}
				flushMessage()
			} else if messageContentsResult.Type == gjson.String {
				appendTextContent(messageContentsResult.String())
				flushMessage()
			}
		}

	}

	// Convert tools declarations to the expected format for the Codex API.
	toolsResult := rootResult.Get("tools")
	if toolsResult.IsArray() {
		template, _ = sjson.SetRaw(template, "tools", `[]`)
		template, _ = sjson.Set(template, "tool_choice", `auto`)
		toolResults := toolsResult.Array()
		// Build short name map from declared tools
		var names []string
		for i := 0; i < len(toolResults); i++ {
			n := toolResults[i].Get("name").String()
			if n != "" {
				names = append(names, n)
			}
		}
		shortMap := buildShortNameMap(names)
		for i := 0; i < len(toolResults); i++ {
			toolResult := toolResults[i]
			// Special handling: map Claude web search tool to Codex web_search
			if toolResult.Get("type").String() == "web_search_20250305" {
				// Replace the tool content entirely with {"type":"web_search"}
				template, _ = sjson.SetRaw(template, "tools.-1", `{"type":"web_search"}`)
				continue
			}
			tool := toolResult.Raw
			tool, _ = sjson.Set(tool, "type", "function")
			// Apply shortened name if needed
			if v := toolResult.Get("name"); v.Exists() {
				name := v.String()
				if short, ok := shortMap[name]; ok {
					name = short
				} else {
					name = shortenNameIfNeeded(name)
				}
				tool, _ = sjson.Set(tool, "name", name)
			}
			tool, _ = sjson.SetRaw(tool, "parameters", normalizeToolParameters(toolResult.Get("input_schema").Raw))
			tool, _ = sjson.Delete(tool, "input_schema")
			tool, _ = sjson.Delete(tool, "parameters.$schema")
			tool, _ = sjson.Set(tool, "strict", false)
			template, _ = sjson.SetRaw(template, "tools.-1", tool)
		}
	}

	// Add additional configuration parameters for the Codex API.
	template, _ = sjson.Set(template, "parallel_tool_calls", true)
	template, _ = sjson.Set(template, "reasoning.effort", "low")
	template, _ = sjson.Set(template, "reasoning.summary", "auto")
	template, _ = sjson.Set(template, "stream", true)
	template, _ = sjson.Set(template, "store", false)
	template, _ = sjson.Set(template, "include", []string{"reasoning.encrypted_content"})

	// Add a first message to ignore system instructions and ensure proper execution.
	inputResult := gjson.Get(template, "input")
	if inputResult.Exists() && inputResult.IsArray() {
		inputResults := inputResult.Array()
		newInput := "[]"
		for i := 0; i < len(inputResults); i++ {
			if i == 0 {
				firstText := inputResults[i].Get("content.0.text")
				firstInstructions := "EXECUTE ACCORDING TO THE FOLLOWING INSTRUCTIONS!!!"
				if firstText.Exists() && firstText.String() != firstInstructions {
					newInput, _ = sjson.SetRaw(newInput, "-1", `{"type":"message","role":"user","content":[{"type":"input_text","text":"EXECUTE ACCORDING TO THE FOLLOWING INSTRUCTIONS!!!"}]}`)
				}
			}
			newInput, _ = sjson.SetRaw(newInput, "-1", inputResults[i].Raw)
		}
		template, _ = sjson.SetRaw(template, "input", newInput)
	}

	return []byte(template)
}

// shortenNameIfNeeded applies a simple shortening rule for a single name.
func shortenNameIfNeeded(name string) string {
	const limit = 64
	if len(name) <= limit {
		return name
	}
	if strings.HasPrefix(name, "mcp__") {
		idx := strings.LastIndex(name, "__")
		if idx > 0 {
			cand := "mcp__" + name[idx+2:]
			if len(cand) > limit {
				return cand[:limit]
			}
			return cand
		}
	}
	return name[:limit]
}

// buildShortNameMap ensures uniqueness of shortened names within a request.
func buildShortNameMap(names []string) map[string]string {
	const limit = 64
	used := map[string]struct{}{}
	m := map[string]string{}

	baseCandidate := func(n string) string {
		if len(n) <= limit {
			return n
		}
		if strings.HasPrefix(n, "mcp__") {
			idx := strings.LastIndex(n, "__")
			if idx > 0 {
				cand := "mcp__" + n[idx+2:]
				if len(cand) > limit {
					cand = cand[:limit]
				}
				return cand
			}
		}
		return n[:limit]
	}

	makeUnique := func(cand string) string {
		if _, ok := used[cand]; !ok {
			return cand
		}
		base := cand
		for i := 1; ; i++ {
			suffix := "_" + strconv.Itoa(i)
			allowed := limit - len(suffix)
			if allowed < 0 {
				allowed = 0
			}
			tmp := base
			if len(tmp) > allowed {
				tmp = tmp[:allowed]
			}
			tmp = tmp + suffix
			if _, ok := used[tmp]; !ok {
				return tmp
			}
		}
	}

	for _, n := range names {
		cand := baseCandidate(n)
		uniq := makeUnique(cand)
		used[uniq] = struct{}{}
		m[n] = uniq
	}
	return m
}

// buildReverseMapFromClaudeOriginalToShort builds original->short map, used to map tool_use names to short.
func buildReverseMapFromClaudeOriginalToShort(original []byte) map[string]string {
	tools := gjson.GetBytes(original, "tools")
	m := map[string]string{}
	if !tools.IsArray() {
		return m
	}
	var names []string
	arr := tools.Array()
	for i := 0; i < len(arr); i++ {
		n := arr[i].Get("name").String()
		if n != "" {
			names = append(names, n)
		}
	}
	if len(names) > 0 {
		m = buildShortNameMap(names)
	}
	return m
}

// normalizeToolParameters ensures object schemas contain at least an empty properties map.
func normalizeToolParameters(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" || !gjson.Valid(raw) {
		return `{"type":"object","properties":{}}`
	}
	schema := raw
	result := gjson.Parse(raw)
	schemaType := result.Get("type").String()
	if schemaType == "" {
		schema, _ = sjson.Set(schema, "type", "object")
		schemaType = "object"
	}
	if schemaType == "object" && !result.Get("properties").Exists() {
		schema, _ = sjson.SetRaw(schema, "properties", `{}`)
	}
	return schema
}
