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
	if value.IsArray() {
		for i, item := range value.Array() {
			walkCodexSchemaNullArrayFields(item, joinCodexSchemaPath(path, strconv.Itoa(i)), arrayFields, paths)
		}
		return
	}
	if !value.IsObject() {
		return
	}

	value.ForEach(func(key, child gjson.Result) bool {
		keyString := key.String()
		childPath := joinCodexSchemaPath(path, escapeCodexSchemaPathKey(keyString))
		if _, ok := arrayFields[keyString]; ok && child.Type == gjson.Null {
			*paths = append(*paths, childPath)
			return true
		}
		walkCodexSchemaNullArrayFields(child, childPath, arrayFields, paths)
		return true
	})
}

func joinCodexSchemaPath(base, suffix string) string {
	if base == "" {
		return suffix
	}
	return base + "." + suffix
}

func escapeCodexSchemaPathKey(key string) string {
	if !strings.ContainsAny(key, ".*?") {
		return key
	}
	return strings.NewReplacer(".", "\\.", "*", "\\*", "?", "\\?").Replace(key)
}
