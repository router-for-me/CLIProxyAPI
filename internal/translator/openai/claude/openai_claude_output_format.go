package claude

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// claudeStructuredOutputName is the Chat Completions json_schema name used when
// Claude output_config.format does not carry one. OpenAI requires a name.
// The Codex text.format leg uses the same default.
const claudeStructuredOutputName = "cli_proxy_structured_output"

// claudeOutputConfigFormatToResponseFormat maps Claude output_config.format
// onto a Chat Completions response_format object.
//
// json_schema (with an object schema) becomes response_format.json_schema.
// Strict defaults to true unless the caller set false, and is downgraded when
// the schema leaves a declared property out of required: OpenAI strict mode
// rejects that with HTTP 400. json_object maps to response_format type
// json_object. Anything else, including effort-only output_config, returns nil
// so the translator does not invent a format.
func claudeOutputConfigFormatToResponseFormat(format gjson.Result) []byte {
	if !format.IsObject() {
		return nil
	}
	switch format.Get("type").String() {
	case "json_object":
		return []byte(`{"type":"json_object"}`)
	case "json_schema":
		schema := format.Get("schema")
		if !schema.IsObject() {
			return nil
		}
		name := claudeStructuredOutputName
		if n := format.Get("name").String(); n != "" {
			name = n
		}
		strict := true
		if s := format.Get("strict"); s.Exists() && s.Type == gjson.False {
			strict = false
		}
		if strict && chatSchemaMissesRequired(schema) {
			strict = false
		}
		responseFormat := []byte(`{"type":"json_schema","json_schema":{"name":"","strict":true,"schema":{}}}`)
		responseFormat, _ = sjson.SetBytes(responseFormat, "json_schema.name", name)
		responseFormat, _ = sjson.SetBytes(responseFormat, "json_schema.strict", strict)
		if desc := format.Get("description"); desc.Type == gjson.String {
			if text := strings.TrimSpace(desc.String()); text != "" {
				responseFormat, _ = sjson.SetBytes(responseFormat, "json_schema.description", text)
			}
		}
		responseFormat, _ = sjson.SetRawBytes(responseFormat, "json_schema.schema", []byte(schema.Raw))
		return responseFormat
	default:
		return nil
	}
}

// chatSchemaMissesRequired reports whether a JSON Schema has any declared
// property missing from its sibling required list (recursively). OpenAI
// strict mode rejects such schemas with HTTP 400, so callers downgrade
// response_format.json_schema.strict instead of emitting an unsatisfiable schema.
func chatSchemaMissesRequired(schema gjson.Result) bool {
	if !schema.IsObject() {
		if schema.IsArray() {
			miss := false
			schema.ForEach(func(_, child gjson.Result) bool {
				if chatSchemaMissesRequired(child) {
					miss = true
					return false
				}
				return true
			})
			return miss
		}
		return false
	}
	if properties := schema.Get("properties"); properties.IsObject() {
		required := schema.Get("required")
		if !required.IsArray() {
			return len(properties.Map()) > 0
		}
		names := make(map[string]struct{}, len(required.Array()))
		for _, item := range required.Array() {
			if item.Type == gjson.String {
				names[item.String()] = struct{}{}
			}
		}
		for name := range properties.Map() {
			if _, ok := names[name]; !ok {
				return true
			}
		}
	}
	for _, keyword := range util.SchemaMapKeywords {
		children := schema.Get(keyword)
		if !children.IsObject() {
			continue
		}
		miss := false
		children.ForEach(func(_, child gjson.Result) bool {
			if chatSchemaMissesRequired(child) {
				miss = true
				return false
			}
			return true
		})
		if miss {
			return true
		}
	}
	for _, keyword := range util.SchemaValueKeywords {
		if child := schema.Get(keyword); child.Exists() && chatSchemaMissesRequired(child) {
			return true
		}
	}
	return false
}
