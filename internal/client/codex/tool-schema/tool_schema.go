package toolschema

import (
	"fmt"
	"net/http"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// IsCodexUserAgent reports whether headers contain a User-Agent indicating a Codex client.
func IsCodexUserAgent(headers http.Header) bool {
	if headers == nil {
		return false
	}
	ua := headerValueCaseInsensitive(headers, "User-Agent")
	if ua == "" {
		return false
	}
	return strings.Contains(strings.ToLower(ua), "codex")
}

func headerValueCaseInsensitive(headers http.Header, name string) string {
	for k, values := range headers {
		if strings.EqualFold(k, name) {
			for _, v := range values {
				if trimmed := strings.TrimSpace(v); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	return ""
}

var codexClientToolIntegerFields = map[string]map[string]struct{}{
	"exec_command": {
		"yield_time_ms":     struct{}{},
		"max_output_tokens": struct{}{},
		"timeout_ms":        struct{}{},
	},
	"write_stdin": {
		"session_id":        struct{}{},
		"yield_time_ms":     struct{}{},
		"max_output_tokens": struct{}{},
	},
	"sleep": {
		"duration_ms": struct{}{},
	},
	"wait_agent": {
		"timeout_ms": struct{}{},
	},
	"wait": {
		"yield_time_ms": struct{}{},
		"max_tokens":    struct{}{},
	},
	"tool_search": {
		"limit": struct{}{},
	},
	"test_sync_tool": {
		"sleep_before_ms": struct{}{},
		"sleep_after_ms":  struct{}{},
		"participants":    struct{}{},
		"timeout_ms":      struct{}{},
	},
}

func matchCodexTargetTool(toolName string) map[string]struct{} {
	baseName := strings.TrimSpace(toolName)
	if strings.HasPrefix(baseName, "functions__") {
		baseName = strings.TrimPrefix(baseName, "functions__")
	} else if strings.HasPrefix(baseName, "collab__") {
		baseName = strings.TrimPrefix(baseName, "collab__")
	}
	return codexClientToolIntegerFields[baseName]
}

func normalizeCodexToolFieldTypes(rawParams []byte, targetFields map[string]struct{}) ([]byte, bool) {
	if len(targetFields) == 0 || len(rawParams) == 0 {
		return rawParams, false
	}
	params := gjson.ParseBytes(rawParams)
	properties := params.Get("properties")
	if !properties.Exists() || !properties.IsObject() {
		return rawParams, false
	}
	changed := false
	for fieldName := range targetFields {
		prop := properties.Get(fieldName)
		if !prop.Exists() {
			continue
		}
		typeVal := prop.Get("type")
		if !typeVal.Exists() {
			continue
		}
		escapedKey := escapeCodexSjsonKey(fieldName)
		if typeVal.Type == gjson.String && typeVal.String() == "number" {
			if updated, errSet := sjson.SetBytes(rawParams, "properties."+escapedKey+".type", "integer"); errSet == nil {
				rawParams = updated
				changed = true
			}
		} else if typeVal.IsArray() {
			arrayItems := typeVal.Array()
			hasNumber := false
			seenTypes := make(map[string]struct{}, len(arrayItems))
			newTypes := make([]string, 0, len(arrayItems))
			for _, item := range arrayItems {
				itemStr := item.String()
				if itemStr == "number" {
					hasNumber = true
					itemStr = "integer"
				}
				if _, seen := seenTypes[itemStr]; !seen {
					seenTypes[itemStr] = struct{}{}
					newTypes = append(newTypes, itemStr)
				}
			}
			if hasNumber {
				if updated, errSet := sjson.SetBytes(rawParams, "properties."+escapedKey+".type", newTypes); errSet == nil {
					rawParams = updated
					changed = true
				}
			}
		}
	}
	return rawParams, changed
}

// NormalizeCodexToolIntegerTypes normalizes specified tool parameter declarations
// from number to integer for Codex clients across supported tool formats (OpenAI,
// Claude input_schema, Gemini function_declarations, and namespace tools).
func NormalizeCodexToolIntegerTypes(body []byte, headers http.Header) []byte {
	if len(body) == 0 || !IsCodexUserAgent(headers) {
		return body
	}

	changed := false

	// 1. Process top-level tools
	toolsResult := gjson.GetBytes(body, "tools")
	if toolsResult.IsArray() {
		if updated, ok := normalizeToolIntegerTypesInArray(toolsResult); ok {
			if out, errSet := sjson.SetRawBytes(body, "tools", updated); errSet == nil {
				body = out
				changed = true
			}
		}
	}

	// 2. Process input[].additional_tools
	inputResult := gjson.GetBytes(body, "input")
	if inputResult.IsArray() {
		for idx, item := range inputResult.Array() {
			if item.Get("type").String() == "additional_tools" {
				addTools := item.Get("tools")
				if addTools.IsArray() {
					if updated, ok := normalizeToolIntegerTypesInArray(addTools); ok {
						path := fmt.Sprintf("input.%d.tools", idx)
						if out, errSet := sjson.SetRawBytes(body, path, updated); errSet == nil {
							body = out
							changed = true
						}
					}
				}
			}
		}
	}

	if changed {
		log.Debugf("codex: normalized target tool number types to integer")
	}
	return body
}

func normalizeToolIntegerTypesInArray(tools gjson.Result) ([]byte, bool) {
	if !tools.IsArray() {
		return nil, false
	}
	var out []byte
	offset := 0
	tools.ForEach(func(_, tool gjson.Result) bool {
		updated, changed := normalizeToolIntegerTypesInElement(tool)
		if !changed {
			return true
		}
		if out == nil {
			out = make([]byte, 0, len(tools.Raw))
		}
		start := tool.Index - tools.Index
		out = append(out, tools.Raw[offset:start]...)
		out = append(out, updated...)
		offset = start + len(tool.Raw)
		return true
	})
	if out == nil {
		return nil, false
	}
	return append(out, tools.Raw[offset:]...), true
}

func normalizeToolIntegerTypesInElement(tool gjson.Result) ([]byte, bool) {
	toolRaw := []byte(tool.Raw)
	changed := false

	// Handle namespace tools
	if tool.Get("type").String() == "namespace" {
		nested := tool.Get("tools")
		if nested.IsArray() {
			if updated, ok := normalizeToolIntegerTypesInArray(nested); ok {
				if out, errSet := sjson.SetRawBytes(toolRaw, "tools", updated); errSet == nil {
					toolRaw = out
					changed = true
				}
			}
		}
		return toolRaw, changed
	}

	// Handle Gemini function declarations
	for _, declKey := range []string{"function_declarations", "functionDeclarations"} {
		decls := tool.Get(declKey)
		if decls.IsArray() {
			if updated, ok := normalizeToolIntegerTypesInArray(decls); ok {
				if out, errSet := sjson.SetRawBytes(toolRaw, declKey, updated); errSet == nil {
					toolRaw = out
					changed = true
				}
			}
			return toolRaw, changed
		}
	}

	// Standard function/custom tool or Claude tool
	toolName := tool.Get("name").String()
	paramPath := "parameters"
	params := tool.Get("parameters")

	if !params.Exists() || !params.IsObject() {
		if fnParams := tool.Get("function.parameters"); fnParams.Exists() && fnParams.IsObject() {
			paramPath = "function.parameters"
			params = fnParams
			if toolName == "" {
				toolName = tool.Get("function.name").String()
			}
		} else if inputSchema := tool.Get("input_schema"); inputSchema.Exists() && inputSchema.IsObject() {
			paramPath = "input_schema"
			params = inputSchema
		} else if jsonSchema := tool.Get("parametersJsonSchema"); jsonSchema.Exists() && jsonSchema.IsObject() {
			paramPath = "parametersJsonSchema"
			params = jsonSchema
		} else {
			return nil, false
		}
	}

	targetFields := matchCodexTargetTool(toolName)
	if len(targetFields) == 0 {
		return nil, false
	}

	updatedParams, paramsChanged := normalizeCodexToolFieldTypes([]byte(params.Raw), targetFields)
	if !paramsChanged {
		return nil, false
	}

	updatedTool, errSet := sjson.SetRawBytes(toolRaw, paramPath, updatedParams)
	if errSet != nil {
		return nil, false
	}
	return updatedTool, true
}

func escapeCodexSjsonKey(key string) string {
	key = strings.ReplaceAll(key, `\`, `\\`)
	key = strings.ReplaceAll(key, `.`, `\.`)
	key = strings.ReplaceAll(key, `:`, `\:`)
	return key
}
