package util

import (
	"bytes"
	"sort"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
)

const propertyOrderKey = "x-cliproxy-property-order"

type orderedProperties struct {
	keys   []string
	values map[string]any
}

func (p orderedProperties) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, key := range p.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		name, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		value, err := json.Marshal(p.values[key])
		if err != nil {
			return nil, err
		}
		buf.Write(name)
		buf.WriteByte(':')
		buf.Write(value)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func recordPropertyOrder(node any, raw ojson.Result, inNameMap bool) {
	switch typed := node.(type) {
	case []any:
		index := 0
		raw.ForEach(func(_, item ojson.Result) bool {
			if index < len(typed) {
				recordPropertyOrder(typed[index], item, false)
			}
			index++
			return true
		})
	case map[string]any:
		if !inNameMap {
			if _, hasProperties := typed["properties"].(map[string]any); hasProperties {
				var keys []any
				raw.Get("properties").ForEach(func(key, _ ojson.Result) bool {
					keys = append(keys, key.String())
					return true
				})
				typed[propertyOrderKey] = keys
			}
		}
		raw.ForEach(func(key, value ojson.Result) bool {
			name := key.String()
			if !inNameMap && isLiteralValueKeyword(name) {
				return true
			}
			_, childIsNameMap := schemaNameMapKeywords[name]
			recordPropertyOrder(typed[name], value, !inNameMap && childIsNameMap)
			return true
		})
	}
}

func applyPropertyOrder(node any) any {
	switch typed := node.(type) {
	case []any:
		for i, item := range typed {
			typed[i] = applyPropertyOrder(item)
		}
	case map[string]any:
		for key, value := range typed {
			if key != propertyOrderKey {
				typed[key] = applyPropertyOrder(value)
			}
		}
		recorded, hasOrder := typed[propertyOrderKey].([]any)
		delete(typed, propertyOrderKey)
		properties, hasProperties := typed["properties"].(map[string]any)
		if hasOrder && hasProperties {
			typed["properties"] = orderedProperties{keys: propertyKeysInOrder(recorded, properties), values: properties}
		}
	}
	return node
}

func propertyKeysInOrder(recorded []any, properties map[string]any) []string {
	keys := make([]string, 0, len(properties))
	seen := make(map[string]bool, len(properties))
	for _, item := range recorded {
		key, ok := item.(string)
		if _, exists := properties[key]; ok && exists && !seen[key] {
			keys = append(keys, key)
			seen[key] = true
		}
	}
	var rest []string
	for key := range properties {
		if !seen[key] {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	return append(keys, rest...)
}
