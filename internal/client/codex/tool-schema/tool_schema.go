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

// Keys are explicit schema paths relative to parameters.properties, not recursive field names.
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
		"sleep_before_ms":                 struct{}{},
		"sleep_after_ms":                  struct{}{},
		"participants":                    struct{}{},
		"timeout_ms":                      struct{}{},
		"barrier.properties.participants": struct{}{},
		"barrier.properties.timeout_ms":   struct{}{},
	},
	"create_goal": {
		"token_budget": struct{}{},
	},
	"get_channels": {
		"limit": struct{}{},
	},
	"list_threads": {
		"limit":              struct{}{},
		"max_chars_per_post": struct{}{},
	},
	"search_posts": {
		"limit":              struct{}{},
		"max_chars_per_post": struct{}{},
	},
	"read_thread": {
		"limit":              struct{}{},
		"max_chars_per_post": struct{}{},
	},
	"read_post": {
		"offset_chars": struct{}{},
		"limit_chars":  struct{}{},
	},
	"memories__list": {
		"max_results": struct{}{},
	},
	"memories__read": {
		"line_offset": struct{}{},
		"max_lines":   struct{}{},
	},
	"memories__search": {
		"context_lines": struct{}{},
		"max_results":   struct{}{},
	},
	"history__list_windows": {
		"limit": struct{}{},
	},
	"history__list_items": {
		"limit":              struct{}{},
		"max_chars_per_item": struct{}{},
	},
	"history__read_item": {
		"offset_chars": struct{}{},
		"limit_chars":  struct{}{},
	},
	"history__search_contents": {
		"limit": struct{}{},
	},
	"notes__list_files_by_prefix": {
		"max_results": struct{}{},
	},
	"notes__read_file": {
		"start_line": struct{}{},
		"stop_line":  struct{}{},
		// Codex declares signed line numbers in the first nullable union branch.
		"start_line.anyOf.0": struct{}{},
		"stop_line.anyOf.0":  struct{}{},
	},
	"notes__search_contents": {
		"max_matches_per_file": struct{}{},
		"max_files":            struct{}{},
	},
	"image_gen__imagegen": {
		"num_last_images_to_include": struct{}{},
	},
	"web__run": {
		"search_query.items.properties.recency": struct{}{},
		"image_query.items.properties.recency":  struct{}{},
		"open.items.properties.lineno":          struct{}{},
		"click.items.properties.id":             struct{}{},
		"screenshot.items.properties.pageno":    struct{}{},
		"weather.items.properties.duration":     struct{}{},
		"sports.items.properties.num_games":     struct{}{},
	},
}

func matchCodexTargetTool(toolName string) map[string]struct{} {
	baseName := strings.TrimSpace(toolName)
	if strings.HasPrefix(baseName, "functions__") {
		baseName = strings.TrimPrefix(baseName, "functions__")
	} else if strings.HasPrefix(baseName, "collab__") {
		baseName = strings.TrimPrefix(baseName, "collab__")
	}
	switch baseName {
	case "multi_agent_v1__wait_agent", "collaboration__wait_agent":
		baseName = "wait_agent"
	case "collaboration__get_channels", "collaboration__list_threads", "collaboration__search_posts",
		"collaboration__read_thread", "collaboration__read_post":
		baseName = strings.TrimPrefix(baseName, "collaboration__")
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
		typePath := "properties." + fieldName + ".type"
		if typeVal.Type == gjson.String && typeVal.String() == "number" {
			if updated, errSet := sjson.SetBytes(rawParams, typePath, "integer"); errSet == nil {
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
				if updated, errSet := sjson.SetBytes(rawParams, typePath, newTypes); errSet == nil {
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
		if updated, ok := normalizeToolIntegerTypesInArray(toolsResult, ""); ok {
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
					if updated, ok := normalizeToolIntegerTypesInArray(addTools, ""); ok {
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

func normalizeToolIntegerTypesInArray(tools gjson.Result, namespace string) ([]byte, bool) {
	if !tools.IsArray() {
		return nil, false
	}
	var out []byte
	offset := 0
	tools.ForEach(func(_, tool gjson.Result) bool {
		updated, changed := normalizeToolIntegerTypesInElement(tool, namespace)
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

func normalizeToolIntegerTypesInElement(tool gjson.Result, namespace string) ([]byte, bool) {
	toolRaw := []byte(tool.Raw)
	changed := false

	// Handle namespace tools
	if tool.Get("type").String() == "namespace" {
		if namespace != "" {
			return nil, false
		}
		namespace = tool.Get("name").String()
		if namespace == "" {
			return nil, false
		}
		nested := tool.Get("tools")
		if nested.IsArray() {
			if updated, ok := normalizeToolIntegerTypesInArray(nested, namespace); ok {
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
			if updated, ok := normalizeToolIntegerTypesInArray(decls, namespace); ok {
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

	if namespace != "" {
		toolName = namespace + "__" + toolName
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
