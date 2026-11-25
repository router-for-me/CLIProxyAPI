package util

import (
	"slices"
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

func restorePropertyOrder(original, resolved string) string {
	orders := make(map[string][]string)
	ambiguous := make(map[string]bool)
	walkSchemaProperties(ojson.Parse(original), "", false, func(_ string, properties ojson.Result) {
		keys := objectKeys(properties)
		signature := propertySignature(keys)
		if known, seen := orders[signature]; seen && !slices.Equal(known, keys) {
			ambiguous[signature] = true
			return
		}
		orders[signature] = keys
	})

	var paths []string
	walkSchemaProperties(ojson.Parse(resolved), "", false, func(path string, _ ojson.Result) {
		paths = append(paths, path)
	})
	if len(paths) == 0 {
		return resolved
	}

	doc := ojson.NewDocString(resolved)
	for _, path := range paths {
		current := doc.Get(path)
		keys := objectKeys(current)
		signature := propertySignature(keys)
		want, known := orders[signature]
		if !known || ambiguous[signature] || slices.Equal(want, keys) {
			continue
		}
		ordered := ojson.NewDocString("{}")
		for _, key := range want {
			escaped := escapeWritePathKey(key)
			_ = ordered.SetRaw(escaped, current.Get(escaped).Raw)
		}
		_ = doc.SetRaw(path, ordered.String())
	}
	return doc.String()
}

func walkSchemaProperties(value ojson.Result, path string, inNameMap bool, visit func(path string, properties ojson.Result)) {
	if value.IsArray() {
		index := 0
		value.ForEach(func(_, item ojson.Result) bool {
			walkSchemaProperties(item, joinPath(path, strconv.Itoa(index)), false, visit)
			index++
			return true
		})
		return
	}
	if !value.IsObject() {
		return
	}
	value.ForEach(func(key, child ojson.Result) bool {
		name := key.String()
		childPath := joinPath(path, escapeWritePathKey(name))
		if inNameMap {
			walkSchemaProperties(child, childPath, false, visit)
			return true
		}
		if isLiteralValueKeyword(name) {
			return true
		}
		if name == "properties" && child.IsObject() {
			visit(childPath, child)
		}
		_, childIsNameMap := schemaNameMapKeywords[name]
		walkSchemaProperties(child, childPath, childIsNameMap, visit)
		return true
	})
}

func objectKeys(object ojson.Result) []string {
	var keys []string
	object.ForEach(func(key, _ ojson.Result) bool {
		keys = append(keys, key.String())
		return true
	})
	return keys
}

func propertySignature(keys []string) string {
	sorted := slices.Clone(keys)
	slices.Sort(sorted)
	return strings.Join(sorted, "\x00")
}

func escapeWritePathKey(key string) string {
	return strings.ReplaceAll(escapeGJSONPathKey(key), ":", `\:`)
}
