package common

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

const (
	claudeSystemReminderStart = "<system-reminder>"
	claudeSystemReminderEnd   = "</system-reminder>"
)

// SystemReminderText wraps text in the <system-reminder> envelope so non-Claude
// upstream formats treat demoted mid-session system or developer instructions
// as system directives rather than user speech.
func SystemReminderText(text string) string {
	return claudeSystemReminderStart + "\n" + text + "\n" + claudeSystemReminderEnd
}

// ClaudeMessageSystemReminderText converts a Claude message-level system value
// into ordinary user-visible reminder text for non-Claude upstream formats.
func ClaudeMessageSystemReminderText(content ojson.Result) (string, bool) {
	parts := claudeSystemTextParts(content)
	if len(parts) == 0 {
		return "", false
	}
	text := strings.Join(parts, "\n")
	if strings.TrimSpace(text) == "" {
		return "", false
	}
	return SystemReminderText(text), true
}

func claudeSystemTextParts(content ojson.Result) []string {
	if !content.Exists() {
		return nil
	}
	if content.Type == ojson.String {
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
	content.ForEach(func(_, item ojson.Result) bool {
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

var claudeStructuredOutputLegacyPrefixes = []string{"claude-3", "claude-2", "claude-instant"}

var claudeStructuredOutputLegacyModels = map[string]struct{}{
	"claude-opus-4":            {},
	"claude-opus-4-0":          {},
	"claude-opus-4-20250514":   {},
	"claude-opus-4-latest":     {},
	"claude-opus-4-1":          {},
	"claude-opus-4-1-20250805": {},
	"claude-opus-4-1-latest":   {},
	"claude-sonnet-4":          {},
	"claude-sonnet-4-0":        {},
	"claude-sonnet-4-20250514": {},
	"claude-sonnet-4-latest":   {},
}

// claudeModelLacksNativeStructuredOutputs reports whether modelName identifies
// an older Claude model that does not support native output_config.format.
// Unknown and future model IDs are treated as supported.
func claudeModelLacksNativeStructuredOutputs(modelName string) bool {
	model := strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(modelName).ModelName))
	if slash := strings.LastIndexByte(model, '/'); slash >= 0 {
		model = model[slash+1:]
	}
	if bracket := strings.IndexByte(model, '['); bracket >= 0 {
		model = model[:bracket]
	}
	if at := strings.IndexByte(model, '@'); at >= 0 {
		model = model[:at]
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	normalized := strings.TrimSuffix(model, "-thinking")
	if _, legacy := claudeStructuredOutputLegacyModels[normalized]; legacy {
		return true
	}
	for _, prefix := range claudeStructuredOutputLegacyPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

// ClaudeOutputFormatSchema returns the schema object of a json_schema
// structured output setting (from Chat Completions response_format or
// Responses text.format) so the caller can set Claude's output_config.format
// directly on its *ojson.Doc. It returns false for non-json_schema formats,
// missing/non-object schemas, and legacy Claude models without native
// structured outputs, which keep the system prompt instruction fallback.
func ClaudeOutputFormatSchema(modelName string, format ojson.Result) (ojson.Result, bool) {
	if !strings.EqualFold(strings.TrimSpace(format.Get("type").String()), "json_schema") {
		return ojson.Result{}, false
	}
	if claudeModelLacksNativeStructuredOutputs(modelName) {
		return ojson.Result{}, false
	}
	schema := format.Get("json_schema.schema")
	if !schema.Exists() {
		schema = format.Get("schema")
	}
	if !schema.IsObject() {
		return ojson.Result{}, false
	}
	return schema, true
}

// BuildClaudeOutputFormatDescription builds a short system prompt instruction
// preserving the schema name and description when output_config.format carries
// the JSON schema natively. It returns "" when no description is present.
func BuildClaudeOutputFormatDescription(format ojson.Result) string {
	name, desc := claudeStructuredOutputNameAndDescription(format)
	if desc == "" {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("Your response must follow the JSON schema given as the output format.\n")
	if name != "" {
		builder.WriteString("Schema Name: ")
		builder.WriteString(name)
		builder.WriteString("\n")
	}
	builder.WriteString("Schema Description: ")
	builder.WriteString(desc)
	return builder.String()
}

func claudeStructuredOutputNameAndDescription(format ojson.Result) (string, string) {
	jsonSchema := format.Get("json_schema")
	name := strings.TrimSpace(jsonSchema.Get("name").String())
	if name == "" {
		name = strings.TrimSpace(format.Get("name").String())
	}
	desc := strings.TrimSpace(jsonSchema.Get("description").String())
	if desc == "" {
		desc = strings.TrimSpace(format.Get("description").String())
	}
	return name, desc
}

// BuildClaudeStructuredOutputInstruction formats structured output settings
// (from Chat Completions response_format or Responses text.format) as explicit
// instructions to inject into Claude's system prompt.
func BuildClaudeStructuredOutputInstruction(format ojson.Result) string {
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
		name, desc := claudeStructuredOutputNameAndDescription(format)
		if name != "" {
			builder.WriteString("Schema Name: ")
			builder.WriteString(name)
			builder.WriteString("\n")
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
