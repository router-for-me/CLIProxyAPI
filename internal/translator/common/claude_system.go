package common

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
)

const (
	claudeSystemReminderStart = "<system-reminder>"
	claudeSystemReminderEnd   = "</system-reminder>"
)

// ClaudeMessageSystemReminderText converts a Claude message-level system value
// into ordinary user-visible reminder text for non-Claude upstream formats.
func ClaudeMessageSystemReminderText(content gjson.Result) (string, bool) {
	parts := claudeSystemTextParts(content)
	if len(parts) == 0 {
		return "", false
	}
	text := strings.Join(parts, "\n")
	if strings.TrimSpace(text) == "" {
		return "", false
	}
	return claudeSystemReminderStart + "\n" + text + "\n" + claudeSystemReminderEnd, true
}

func claudeSystemTextParts(content gjson.Result) []string {
	if !content.Exists() {
		return nil
	}
	if content.Type == gjson.String {
		text := content.String()
		if text == "" || util.IsClaudeCodeAttributionSystemText(text) {
			return nil
		}
		return []string{text}
	}
	if !content.IsArray() {
		return nil
	}
	parts := make([]string, 0)
	content.ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() != "text" {
			return true
		}
		text := item.Get("text").String()
		if text == "" || util.IsClaudeCodeAttributionSystemText(text) {
			return true
		}
		parts = append(parts, text)
		return true
	})
	return parts
}

// BuildClaudeStructuredOutputInstruction formats structured output settings
// (from Chat Completions response_format or Responses text.format) as explicit
// instructions to inject into Claude's system prompt.
func BuildClaudeStructuredOutputInstruction(format gjson.Result) string {
	if !format.Exists() {
		return ""
	}

	formatType := strings.ToLower(strings.TrimSpace(format.Get("type").String()))
	switch formatType {
	case "json_object":
		return "You must format your entire response as a valid JSON object. Do not include any explanations, markdown code blocks (such as ```json), or any text outside of the JSON object."
	case "json_schema":
		jsonSchema := format.Get("json_schema")
		schema := jsonSchema.Get("schema")
		if !schema.Exists() {
			schema = format.Get("schema")
		}
		if !schema.Exists() {
			return "You must format your entire response as a valid JSON object. Do not include any explanations, markdown code blocks (such as ```json), or any text outside of the JSON object."
		}

		var builder strings.Builder
		builder.WriteString("You must format your entire response as valid JSON that conforms strictly to the following JSON schema:\n")
		name := strings.TrimSpace(jsonSchema.Get("name").String())
		if name == "" {
			name = strings.TrimSpace(format.Get("name").String())
		}
		if name != "" {
			builder.WriteString("Schema Name: ")
			builder.WriteString(name)
			builder.WriteString("\n")
		}
		desc := strings.TrimSpace(jsonSchema.Get("description").String())
		if desc == "" {
			desc = strings.TrimSpace(format.Get("description").String())
		}
		if desc != "" {
			builder.WriteString("Schema Description: ")
			builder.WriteString(desc)
			builder.WriteString("\n")
		}
		builder.WriteString("JSON Schema:\n")
		builder.WriteString(schema.Raw)
		builder.WriteString("\nDo not include any explanations, markdown code blocks (such as ```json), or any text outside of the JSON object.")
		return builder.String()
	default:
		return ""
	}
}
