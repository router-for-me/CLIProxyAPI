package util

import (
	"encoding/json"
	"reflect"
	"strings"
)

const emptyClaudeToolInputSchema = "{\"type\":\"object\",\"properties\":{}}"

// NormalizeClaudeToolInputSchema makes a JSON Schema compatible with Claude's
// requirement that a tool input schema is an object without root-level unions.
// Local $defs references and nested root unions are resolved before flattening
// so namespace tools retain their complete callable surface.
func NormalizeClaudeToolInputSchema(schema []byte) []byte {
	var root map[string]any
	if len(schema) == 0 || json.Unmarshal(schema, &root) != nil || root == nil {
		return []byte(emptyClaudeToolInputSchema)
	}

	flattenClaudeToolRootCombinators(root)
	root["type"] = "object"
	if _, ok := root["properties"].(map[string]any); !ok {
		root["properties"] = map[string]any{}
	}

	normalized, err := json.Marshal(root)
	if err != nil {
		return []byte(emptyClaudeToolInputSchema)
	}
	return normalized
}

// flattenClaudeToolRootCombinators converts root object unions into the flat
// object shape required by Anthropic tool input schemas. Combinators below a
// property are intentionally preserved because Anthropic accepts them there.
func flattenClaudeToolRootCombinators(schema map[string]any) {
	for _, keyword := range []string{"oneOf", "anyOf"} {
		if variants, ok := schema[keyword].([]any); ok {
			if mergeClaudeToolObjectUnion(schema, variants) {
				delete(schema, keyword)
			}
		}
	}

	if conjuncts, ok := schema["allOf"].([]any); ok {
		if mergeClaudeToolObjectConjunction(schema, conjuncts) {
			delete(schema, "allOf")
		}
	}

	// An unresolved root combinator would be rejected by Anthropic. Keep the
	// best-effort object shape even when a branch could not be represented.
	delete(schema, "oneOf")
	delete(schema, "anyOf")
	delete(schema, "allOf")
}

func mergeClaudeToolObjectUnion(root map[string]any, rawVariants []any) bool {
	if len(rawVariants) == 0 {
		return false
	}

	variants := make([]map[string]any, 0, len(rawVariants))
	for _, rawVariant := range rawVariants {
		variant, ok := resolveClaudeToolObjectSchema(root, rawVariant, map[string]bool{})
		if !ok {
			continue
		}
		if _, hasDefs := variant["$defs"]; !hasDefs {
			variant["$defs"] = cloneClaudeToolJSON(root["$defs"])
		}
		flattenClaudeToolRootCombinators(variant)
		variants = append(variants, variant)
	}
	if len(variants) == 0 {
		return false
	}

	properties := cloneClaudeToolProperties(root["properties"])
	for _, variant := range variants {
		variantProperties, _ := variant["properties"].(map[string]any)
		for name, propertySchema := range variantProperties {
			if existing, exists := properties[name]; exists {
				properties[name] = mergeClaudeToolPropertyUnion(existing, propertySchema)
			} else {
				properties[name] = cloneClaudeToolJSON(propertySchema)
			}
		}
	}
	root["properties"] = properties
	root["type"] = "object"

	required := claudeToolStringList(root["required"])
	variantRequired := claudeToolStringList(variants[0]["required"])
	for _, variant := range variants[1:] {
		variantRequired = intersectClaudeToolStrings(variantRequired, claudeToolStringList(variant["required"]))
	}
	required = appendUniqueClaudeToolStrings(required, variantRequired...)
	if len(required) > 0 {
		root["required"] = required
	} else {
		delete(root, "required")
	}

	if _, exists := root["additionalProperties"]; !exists && allClaudeToolSchemasDisallowAdditionalProperties(variants) {
		root["additionalProperties"] = false
	}
	return true
}

func mergeClaudeToolObjectConjunction(root map[string]any, rawConjuncts []any) bool {
	if len(rawConjuncts) == 0 {
		return false
	}

	properties := cloneClaudeToolProperties(root["properties"])
	required := claudeToolStringList(root["required"])
	mergedAny := len(properties) > 0
	for _, rawConjunct := range rawConjuncts {
		conjunct, ok := resolveClaudeToolObjectSchema(root, rawConjunct, map[string]bool{})
		if !ok {
			continue
		}
		if _, hasDefs := conjunct["$defs"]; !hasDefs {
			conjunct["$defs"] = cloneClaudeToolJSON(root["$defs"])
		}
		flattenClaudeToolRootCombinators(conjunct)
		conjunctProperties, _ := conjunct["properties"].(map[string]any)
		for name, propertySchema := range conjunctProperties {
			if existing, exists := properties[name]; exists && !reflect.DeepEqual(existing, propertySchema) {
				properties[name] = map[string]any{"allOf": []any{existing, cloneClaudeToolJSON(propertySchema)}}
			} else if !exists {
				properties[name] = cloneClaudeToolJSON(propertySchema)
			}
		}
		required = appendUniqueClaudeToolStrings(required, claudeToolStringList(conjunct["required"])...)
		mergedAny = mergedAny || len(conjunctProperties) > 0
	}
	if !mergedAny {
		return false
	}

	root["properties"] = properties
	root["type"] = "object"
	if len(required) > 0 {
		root["required"] = required
	}
	return true
}

func resolveClaudeToolObjectSchema(root map[string]any, rawSchema any, seenRefs map[string]bool) (map[string]any, bool) {
	schema, ok := cloneClaudeToolJSON(rawSchema).(map[string]any)
	if !ok {
		return nil, false
	}

	if ref, okRef := schema["$ref"].(string); okRef {
		if seenRefs[ref] {
			return nil, false
		}
		target, found := resolveClaudeToolLocalRef(root, ref)
		if !found {
			return nil, false
		}
		seenRefs[ref] = true
		resolved, resolvedOK := resolveClaudeToolObjectSchema(root, target, seenRefs)
		delete(seenRefs, ref)
		if !resolvedOK {
			return nil, false
		}
		delete(schema, "$ref")
		mergeClaudeToolSchemaSiblings(resolved, schema)
		schema = resolved
	}

	if schemaType, exists := schema["type"]; exists && !claudeToolTypeIncludesObject(schemaType) {
		return nil, false
	}
	if _, hasProperties := schema["properties"]; !hasProperties && !hasClaudeToolRootCombinator(schema) {
		return nil, false
	}
	return schema, true
}

func claudeToolTypeIncludesObject(raw any) bool {
	if value, ok := raw.(string); ok {
		return value == "object"
	}
	if values, ok := raw.([]any); ok {
		for _, value := range values {
			if value == "object" {
				return true
			}
		}
	}
	return false
}

func resolveClaudeToolLocalRef(root map[string]any, ref string) (any, bool) {
	const defsPrefix = "#/$defs/"
	if !strings.HasPrefix(ref, defsPrefix) {
		return nil, false
	}
	defs, ok := root["$defs"].(map[string]any)
	if !ok {
		return nil, false
	}
	name := strings.TrimPrefix(ref, defsPrefix)
	name = strings.ReplaceAll(strings.ReplaceAll(name, "~1", "/"), "~0", "~")
	value, ok := defs[name]
	return value, ok
}

func mergeClaudeToolSchemaSiblings(target, siblings map[string]any) {
	for key, value := range siblings {
		switch key {
		case "properties":
			properties := cloneClaudeToolProperties(target["properties"])
			for name, propertySchema := range cloneClaudeToolProperties(value) {
				properties[name] = propertySchema
			}
			target[key] = properties
		case "required":
			target[key] = appendUniqueClaudeToolStrings(claudeToolStringList(target[key]), claudeToolStringList(value)...)
		default:
			target[key] = cloneClaudeToolJSON(value)
		}
	}
}

func mergeClaudeToolPropertyUnion(left, right any) any {
	if reflect.DeepEqual(left, right) {
		return left
	}
	leftSchema, leftValues, leftOK := claudeToolEnumLikeSchema(left)
	rightSchema, rightValues, rightOK := claudeToolEnumLikeSchema(right)
	if leftOK && rightOK && reflect.DeepEqual(leftSchema, rightSchema) {
		leftSchema["enum"] = appendUniqueClaudeToolValues(leftValues, rightValues...)
		return leftSchema
	}

	variants := []any{cloneClaudeToolJSON(left)}
	if existing, ok := left.(map[string]any); ok {
		if anyOf, okAnyOf := existing["anyOf"].([]any); okAnyOf && len(existing) == 1 {
			variants = cloneClaudeToolJSON(anyOf).([]any)
		}
	}
	for _, variant := range variants {
		if reflect.DeepEqual(variant, right) {
			return map[string]any{"anyOf": variants}
		}
	}
	return map[string]any{"anyOf": append(variants, cloneClaudeToolJSON(right))}
}

func claudeToolEnumLikeSchema(raw any) (map[string]any, []any, bool) {
	schema, ok := cloneClaudeToolJSON(raw).(map[string]any)
	if !ok {
		return nil, nil, false
	}
	if value, exists := schema["const"]; exists {
		delete(schema, "const")
		return schema, []any{value}, true
	}
	if values, exists := schema["enum"].([]any); exists && len(values) > 0 {
		delete(schema, "enum")
		return schema, values, true
	}
	return nil, nil, false
}

func hasClaudeToolRootCombinator(schema map[string]any) bool {
	for _, keyword := range []string{"oneOf", "anyOf", "allOf"} {
		if _, exists := schema[keyword]; exists {
			return true
		}
	}
	return false
}

func cloneClaudeToolProperties(raw any) map[string]any {
	properties, _ := cloneClaudeToolJSON(raw).(map[string]any)
	if properties == nil {
		return map[string]any{}
	}
	return properties
}

func cloneClaudeToolJSON(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		cloned := make(map[string]any, len(typed))
		for key, child := range typed {
			cloned[key] = cloneClaudeToolJSON(child)
		}
		return cloned
	case []any:
		cloned := make([]any, len(typed))
		for i, child := range typed {
			cloned[i] = cloneClaudeToolJSON(child)
		}
		return cloned
	default:
		return value
	}
}

func claudeToolStringList(raw any) []string {
	values, ok := raw.([]any)
	if !ok {
		if stringsValue, okStrings := raw.([]string); okStrings {
			return append([]string(nil), stringsValue...)
		}
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if text, okText := value.(string); okText {
			result = appendUniqueClaudeToolStrings(result, text)
		}
	}
	return result
}

func appendUniqueClaudeToolStrings(values []string, additions ...string) []string {
	for _, addition := range additions {
		found := false
		for _, value := range values {
			if value == addition {
				found = true
				break
			}
		}
		if !found {
			values = append(values, addition)
		}
	}
	return values
}

func intersectClaudeToolStrings(left, right []string) []string {
	result := make([]string, 0, len(left))
	for _, leftValue := range left {
		for _, rightValue := range right {
			if leftValue == rightValue {
				result = append(result, leftValue)
				break
			}
		}
	}
	return result
}

func appendUniqueClaudeToolValues(values []any, additions ...any) []any {
	for _, addition := range additions {
		found := false
		for _, value := range values {
			if reflect.DeepEqual(value, addition) {
				found = true
				break
			}
		}
		if !found {
			values = append(values, addition)
		}
	}
	return values
}

func allClaudeToolSchemasDisallowAdditionalProperties(schemas []map[string]any) bool {
	for _, schema := range schemas {
		value, ok := schema["additionalProperties"].(bool)
		if !ok || value {
			return false
		}
	}
	return len(schemas) > 0
}
