package util

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// NormalizeCodexToolSchema removes nullable array-valued schema keywords that
// Codex rejects while preserving valid schema structure.
func NormalizeCodexToolSchema(raw string) []byte {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" || !gjson.Valid(raw) {
		return []byte(`{"type":"object","properties":{}}`)
	}

	schema := []byte(raw)
	for _, path := range codexSchemaNullArrayFieldPaths(raw) {
		schema, _ = sjson.DeleteBytes(schema, path)
	}
	return schema
}

func codexSchemaNullArrayFieldPaths(raw string) []string {
	arrayFields := map[string]struct{}{
		"allOf":    {},
		"anyOf":    {},
		"enum":     {},
		"oneOf":    {},
		"required": {},
	}

	var paths []string
	walkCodexSchemaNullArrayFields(gjson.Parse(raw), "", arrayFields, &paths)
	return paths
}

func walkCodexSchemaNullArrayFields(value gjson.Result, path string, arrayFields map[string]struct{}, paths *[]string) {
	type walkNode struct {
		value gjson.Result
		path  string
	}

	stack := []walkNode{{value: value, path: path}}
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if node.value.IsArray() {
			items := node.value.Array()
			for i := len(items) - 1; i >= 0; i-- {
				stack = append(stack, walkNode{
					value: items[i],
					path:  joinCodexSchemaPath(node.path, strconv.Itoa(i)),
				})
			}
			continue
		}
		if !node.value.IsObject() {
			continue
		}

		children := make([]walkNode, 0)
		node.value.ForEach(func(key, child gjson.Result) bool {
			keyString := key.String()
			childPath := joinCodexSchemaPath(node.path, escapeCodexSchemaPathKey(keyString))
			if _, ok := arrayFields[keyString]; ok && child.Type == gjson.Null {
				*paths = append(*paths, childPath)
				return true
			}
			children = append(children, walkNode{value: child, path: childPath})
			return true
		})

		for i := len(children) - 1; i >= 0; i-- {
			stack = append(stack, children[i])
		}
	}
}

func joinCodexSchemaPath(base, suffix string) string {
	if base == "" {
		return suffix
	}
	return base + "." + suffix
}

var codexPathReplacer = strings.NewReplacer(
	"\\", "\\\\",
	".", "\\.",
	"*", "\\*",
	"?", "\\?",
	":", "\\:",
)

func escapeCodexSchemaPathKey(key string) string {
	forceObjectKey := isCodexNumericPathKey(key)
	if !forceObjectKey && !strings.ContainsAny(key, "\\.*?:") {
		return key
	}
	escaped := codexPathReplacer.Replace(key)
	if forceObjectKey {
		return ":" + escaped
	}
	return escaped
}

func isCodexNumericPathKey(key string) bool {
	if key == "" {
		return false
	}
	for i := 0; i < len(key); i++ {
		if key[i] < '0' || key[i] > '9' {
			return false
		}
	}
	return true
}
