// Package util provides utility functions for the CLI Proxy API server.
package util

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var gjsonPathKeyReplacer = strings.NewReplacer(".", "\\.", "*", "\\*", "?", "\\?")

const placeholderReasonDescription = "Brief explanation of why you are calling this tool"

// Pass a single JSON schema to the functions below — never a whole request document.
//
// Cleaning walks every node and rewrites keys by name, and schema keywords such as "title",
// "format", "default" and "const" are also ordinary data keys. Handing these functions a request
// silently rewrites tool-call arguments inside the conversation history: the guard that protects
// a key under ".properties" does not apply to argument values, so the keys are deleted outright
// and replacements such as "enum" and "type" are fabricated. That regression reached production
// once already; scope every call site to the schema itself.

type jsonSchemaCleanOptions struct {
	addPlaceholder                    bool
	addMissingArrayItems              bool
	antigravitySemantics              bool
	removeToolTitle                   bool
	removeGeminiMetadata              bool
	flattenUnions                     bool
	forceEnumStringType               bool
	dropAllEnums                      bool
	dropBooleanEnums                  bool
	preserveAdditionalPropertiesFalse bool
}

// CleanJSONSchemaForAntigravity transforms a tool schema to be compatible with Antigravity API.
// It handles unsupported keywords, type flattening, and schema simplification while preserving
// semantic information as description hints and adding placeholders required by VALIDATED mode.
func CleanJSONSchemaForAntigravity(jsonStr string) string {
	return CleanJSONSchemaForAntigravityTool(jsonStr, true)
}

// CleanJSONSchemaForAntigravityTool transforms an Antigravity function schema. The private
// backend accepts enum members only as strings, but the declared type still controls the JSON
// type of generated function arguments, so numeric and boolean types must not be rewritten.
// requirePlaceholder is used only for Claude VALIDATED mode.
func CleanJSONSchemaForAntigravityTool(jsonStr string, requirePlaceholder bool) string {
	return cleanJSONSchema(jsonStr, jsonSchemaCleanOptions{
		addPlaceholder:       requirePlaceholder,
		addMissingArrayItems: true,
		antigravitySemantics: true,
		removeToolTitle:      !requirePlaceholder,
		flattenUnions:        true,
		dropAllEnums:         true,
	})
}

// CleanJSONSchemaForAntigravityResponse transforms a response schema without applying tool-only
// compatibility rewrites that would alter the client's structured output contract.
//
// Sanitization policy:
//   - Passthrough: type, properties, items, required, description, enum, nullable, and
//     additionalProperties: false (which Antigravity natively enforces for response schemas).
//   - Description hints + deletion: unsupported or accepted-but-ignored constraints.
//   - Flattened: allOf merged into properties/required.
//   - Projected: anyOf/oneOf select the strongest branch; null branches become nullable:true.
//   - Resolved: local $ref targets are inlined before $defs/definitions are removed.
//   - Dropped: unresolved $ref (after a hint), metadata, unsupported object-key constraints,
//     conditional keywords (after non-conflicting properties are retained), and x-* extensions.
func CleanJSONSchemaForAntigravityResponse(jsonStr string) string {
	return cleanJSONSchema(jsonStr, jsonSchemaCleanOptions{
		antigravitySemantics:              true,
		flattenUnions:                     true,
		dropBooleanEnums:                  true,
		preserveAdditionalPropertiesFalse: true,
	})
}

// CleanJSONSchemaForGemini transforms a JSON schema to be compatible with Gemini tool calling.
// It removes unsupported keywords and simplifies schemas, without adding empty-schema placeholders.
func CleanJSONSchemaForGemini(jsonStr string) string {
	return cleanJSONSchema(jsonStr, jsonSchemaCleanOptions{
		addMissingArrayItems: true,
		removeGeminiMetadata: true,
		flattenUnions:        true,
		forceEnumStringType:  true,
		dropBooleanEnums:     true,
	})
}

// cleanJSONSchema performs the core cleaning operations on the JSON schema.
func cleanJSONSchema(jsonStr string, options jsonSchemaCleanOptions) string {
	// Phase 0: Normalize malformed schemas (e.g. bare property maps and boolean required from MCP tools)
	jsonStr = normalizeMalformedSchemaObjects(jsonStr, options.addMissingArrayItems)

	// Phase 1: Resolve references and normalize constraints that need their original JSON types.
	if options.antigravitySemantics {
		jsonStr = inlineLocalRefs(jsonStr)
	}
	jsonStr = convertRefsToHints(jsonStr, options.antigravitySemantics)
	jsonStr = projectExclusiveBounds(jsonStr)
	jsonStr = convertConstToEnum(jsonStr)
	if options.antigravitySemantics {
		jsonStr = moveNotToDescription(jsonStr)
	}

	// Intersect allOf before enum conversion or boolean inference erases the members' original
	// types. Conditional properties are merged first so nested allOf schemas share this ordering.
	jsonStr = mergeConditionals(jsonStr)
	jsonStr = mergeAllOf(jsonStr)

	// Phase 2: Convert constraints to their upstream-compatible representation and add hints.
	jsonStr = convertEnumValuesToStrings(jsonStr, options.forceEnumStringType)
	jsonStr = addEnumHints(jsonStr)
	jsonStr = dropIgnoredEnumsToHints(jsonStr, options)
	if !options.preserveAdditionalPropertiesFalse {
		jsonStr = addAdditionalPropertiesHints(jsonStr)
	}
	// Preserve only the effective constraint after intersecting allOf branches. Moving constraints
	// before allOf flattening can leave a weaker branch's first-wins description hint behind.
	jsonStr = moveConstraintsToDescription(jsonStr, options)
	if options.flattenUnions {
		jsonStr = flattenAnyOfOneOf(jsonStr)
	}
	jsonStr = flattenTypeArrays(jsonStr, options.antigravitySemantics)

	// Phase 3: Cleanup
	jsonStr = removeUnsupportedKeywords(jsonStr, options)
	if options.removeGeminiMetadata {
		// Gemini schema cleanup: remove nullable/title and placeholder-only fields.
		jsonStr = removeKeywords(jsonStr, []string{"nullable", "title"})
		jsonStr = removePlaceholderFields(jsonStr)
	} else if options.removeToolTitle {
		// Legacy non-VALIDATED Antigravity requests used the Gemini cleaner, which drops title.
		// Keep that harmless metadata policy without losing Antigravity's native nullable support.
		jsonStr = removeKeywords(jsonStr, []string{"title"})
	}
	jsonStr = cleanupRequiredFields(jsonStr)
	// Phase 4: Add placeholder for empty object schemas (Claude VALIDATED mode requirement)
	if options.addPlaceholder {
		jsonStr = addEmptySchemaPlaceholder(jsonStr)
	}

	return jsonStr
}

// removeKeywords removes all occurrences of specified keywords from the JSON schema.
func removeKeywords(jsonStr string, keywords []string) string {
	deletePaths := make([]string, 0)
	pathsByField := findPathsByFields(jsonStr, keywords)
	for _, key := range keywords {
		for _, p := range pathsByField[key] {
			if isPropertyDefinition(trimSuffix(p, "."+key)) {
				continue
			}
			deletePaths = append(deletePaths, p)
		}
	}
	sortByDepth(deletePaths)
	for _, p := range deletePaths {
		jsonStr, _ = sjson.Delete(jsonStr, p)
	}
	return jsonStr
}

// removePlaceholderFields removes placeholder-only properties ("_" and "reason") and their required entries.
func removePlaceholderFields(jsonStr string) string {
	// Remove "_" placeholder properties.
	paths := findPropertyNamePaths(jsonStr, "_")
	sortByDepth(paths)
	for _, p := range paths {
		if !strings.HasSuffix(p, ".properties._") {
			continue
		}
		jsonStr, _ = sjson.Delete(jsonStr, p)
		parentPath := trimSuffix(p, ".properties._")
		reqPath := joinPath(parentPath, "required")
		req := gjson.Get(jsonStr, reqPath)
		if req.IsArray() {
			var filtered []string
			for _, r := range req.Array() {
				if r.String() != "_" {
					filtered = append(filtered, r.String())
				}
			}
			if len(filtered) == 0 {
				jsonStr, _ = sjson.Delete(jsonStr, reqPath)
			} else {
				updated, _ := sjson.SetBytes([]byte(jsonStr), reqPath, filtered)
				jsonStr = string(updated)
			}
		}
	}

	// Remove placeholder-only "reason" objects.
	reasonPaths := findPropertyNamePaths(jsonStr, "reason")
	sortByDepth(reasonPaths)
	for _, p := range reasonPaths {
		if !strings.HasSuffix(p, ".properties.reason") {
			continue
		}
		parentPath := trimSuffix(p, ".properties.reason")
		props := gjson.Get(jsonStr, joinPath(parentPath, "properties"))
		if !props.IsObject() || len(props.Map()) != 1 {
			continue
		}
		desc := gjson.Get(jsonStr, p+".description").String()
		if desc != placeholderReasonDescription {
			continue
		}
		jsonStr, _ = sjson.Delete(jsonStr, p)
		reqPath := joinPath(parentPath, "required")
		req := gjson.Get(jsonStr, reqPath)
		if req.IsArray() {
			var filtered []string
			for _, r := range req.Array() {
				if r.String() != "reason" {
					filtered = append(filtered, r.String())
				}
			}
			if len(filtered) == 0 {
				jsonStr, _ = sjson.Delete(jsonStr, reqPath)
			} else {
				updated, _ := sjson.SetBytes([]byte(jsonStr), reqPath, filtered)
				jsonStr = string(updated)
			}
		}
	}

	return jsonStr
}

// normalizeMalformedSchemaObjects normalizes malformed JSON schema nodes commonly produced by
// certain MCP tool definitions (e.g. Asana MCP server):
// 1. Bare property maps missing the "type": "object" and "properties": {...} wrappers are wrapped.
// 2. Boolean "required": true on property definitions are stripped and promoted to the parent's "required" array.
// 3. Tool array schemas missing "items" receive a string item schema required by Gemini and Antigravity.
func normalizeMalformedSchemaObjects(jsonStr string, addMissingArrayItems bool) string {
	if jsonStr == "" {
		return jsonStr
	}

	decoder := json.NewDecoder(strings.NewReader(jsonStr))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return jsonStr
	}

	rootMap, ok := root.(map[string]any)
	if !ok || isAPIRequestDocument(rootMap) {
		return jsonStr
	}

	// If wrapped in single-key {"schema": ...} by cleanNestedSchema, unwrap, repair, and re-wrap.
	if len(rootMap) == 1 {
		if innerSchema, ok := rootMap["schema"].(map[string]any); ok {
			repairedInner, modified := repairSchemaNode(innerSchema, addMissingArrayItems)
			if !modified {
				return jsonStr
			}
			out, err := marshalJSONNoHTMLEscape(map[string]any{"schema": repairedInner})
			if err != nil {
				return jsonStr
			}
			return string(out)
		}
	}

	repaired, modified := repairSchemaNode(rootMap, addMissingArrayItems)
	if !modified {
		return jsonStr
	}

	out, err := marshalJSONNoHTMLEscape(repaired)
	if err != nil {
		return jsonStr
	}
	return string(out)
}

func marshalJSONNoHTMLEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	b := buf.Bytes()
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	return b, nil
}

func isKnownSchemaKeywordOrExtension(key string) bool {
	if strings.HasPrefix(key, "x-") {
		return true
	}
	switch key {
	case "properties", "patternProperties", "additionalProperties", "items", "prefixItems",
		"$defs", "definitions", "dependentSchemas", "dependentRequired", "dependencies",
		"if", "then", "else", "not", "contains", "propertyNames",
		"unevaluatedProperties", "unevaluatedItems", "contentSchema", "additionalItems",
		"default", "const", "example", "examples", "discriminator", "xml", "externalDocs",
		"enumDescriptions", "enumTitles":
		return true
	}
	return false
}

func isNonObjectDeclaredType(t any) bool {
	if s, ok := t.(string); ok {
		return s != "" && s != "object"
	}
	if arr, ok := t.([]any); ok {
		for _, item := range arr {
			if s, ok := item.(string); ok && s == "object" {
				return false
			}
		}
		return len(arr) > 0
	}
	return false
}

func isArrayDeclaredType(t any) bool {
	switch typeValue := t.(type) {
	case string:
		return typeValue == "array"
	case []any:
		for _, item := range typeValue {
			if itemType, ok := item.(string); ok && itemType == "array" {
				return true
			}
		}
	}
	return false
}

func isAPIRequestDocument(m map[string]any) bool {
	if _, ok := m["tools"].([]any); ok {
		return true
	}
	if _, ok := m["contents"].([]any); ok {
		return true
	}
	if _, ok := m["messages"].([]any); ok {
		return true
	}
	if _, ok := m["functionDeclarations"].([]any); ok {
		return true
	}
	if _, ok := m["function_declarations"].([]any); ok {
		return true
	}
	if reqMap, ok := m["request"].(map[string]any); ok {
		if isAPIRequestDocument(reqMap) {
			return true
		}
	}
	return false
}

func repairSchemaNode(node map[string]any, addMissingArrayItems bool) (map[string]any, bool) {
	if node == nil {
		return nil, false
	}

	modified := false
	clone := make(map[string]any, len(node))
	for k, v := range node {
		clone[k] = v
	}

	// 1. If not declared as a primitive/array type, collect bare property definition maps
	if !isNonObjectDeclaredType(clone["type"]) {
		var bareProps map[string]any
		for k, v := range clone {
			if childMap, isMap := v.(map[string]any); isMap {
				if !isKnownSchemaKeywordOrExtension(k) {
					if bareProps == nil {
						bareProps = make(map[string]any)
					}
					bareProps[k] = childMap
				}
			}
		}

		if len(bareProps) > 0 {
			repairedProps, promotedReqs, _ := repairPropertyMap(bareProps, addMissingArrayItems)
			for k := range bareProps {
				delete(clone, k)
			}

			if existingProps, ok := clone["properties"].(map[string]any); ok {
				newProps := make(map[string]any, len(existingProps)+len(repairedProps))
				for k, v := range existingProps {
					newProps[k] = v
				}
				for k, v := range repairedProps {
					newProps[k] = v
				}
				clone["properties"] = newProps
			} else {
				clone["properties"] = repairedProps
				if _, hasType := clone["type"]; !hasType {
					clone["type"] = "object"
				}
			}

			if len(promotedReqs) > 0 {
				existingReqs := extractStringArray(clone["required"])
				merged := mergeStringSlices(existingReqs, promotedReqs)
				clone["required"] = merged
			}
			modified = true
		}
	}

	// 2. If node has a "properties" map, recursively repair all properties inside it
	if propsVal, ok := clone["properties"].(map[string]any); ok {
		repairedProps, promotedReqs, propsMod := repairPropertyMap(propsVal, addMissingArrayItems)
		if propsMod {
			clone["properties"] = repairedProps
			modified = true
		}
		if len(promotedReqs) > 0 {
			existingReqs := extractStringArray(clone["required"])
			merged := mergeStringSlices(existingReqs, promotedReqs)
			clone["required"] = merged
			modified = true
		}
	}

	// Gemini and Antigravity reject tool array schemas without an items definition.
	if addMissingArrayItems && isArrayDeclaredType(clone["type"]) {
		if _, hasItems := clone["items"]; !hasItems {
			clone["items"] = map[string]any{"type": "string"}
			modified = true
		}
	}

	// 3. Recurse into all other standard schema containers
	if itemsVal, ok := clone["items"].(map[string]any); ok {
		repairedItems, itemsMod := repairSchemaNode(itemsVal, addMissingArrayItems)
		if itemsMod {
			clone["items"] = repairedItems
			modified = true
		}
	} else if itemsList, ok := clone["items"].([]any); ok {
		repairedList, listMod := repairSchemaList(itemsList, addMissingArrayItems)
		if listMod {
			clone["items"] = repairedList
			modified = true
		}
	}

	if addProps, ok := clone["additionalProperties"].(map[string]any); ok {
		repairedAddProps, addPropsMod := repairSchemaNode(addProps, addMissingArrayItems)
		if addPropsMod {
			clone["additionalProperties"] = repairedAddProps
			modified = true
		}
	}

	if patProps, ok := clone["patternProperties"].(map[string]any); ok {
		repairedPatProps, _, patMod := repairPropertyMap(patProps, addMissingArrayItems)
		if patMod {
			clone["patternProperties"] = repairedPatProps
			modified = true
		}
	}

	for _, key := range []string{"if", "then", "else", "not", "contains", "propertyNames", "unevaluatedProperties", "unevaluatedItems", "contentSchema", "additionalItems"} {
		if subVal, ok := clone[key].(map[string]any); ok {
			repairedSub, subMod := repairSchemaNode(subVal, addMissingArrayItems)
			if subMod {
				clone[key] = repairedSub
				modified = true
			}
		}
	}

	for _, key := range []string{"anyOf", "oneOf", "allOf", "prefixItems"} {
		if listVal, ok := clone[key].([]any); ok {
			repairedList, listMod := repairSchemaList(listVal, addMissingArrayItems)
			if listMod {
				clone[key] = repairedList
				modified = true
			}
		}
	}

	// Legacy dependencies is a hybrid name map: object values are schemas, while array values are
	// property-name lists. The type assertion below deliberately repairs only its schema values.
	for _, key := range []string{"$defs", "definitions", "dependentSchemas", "dependencies"} {
		if defsVal, ok := clone[key].(map[string]any); ok {
			repairedDefs := make(map[string]any, len(defsVal))
			defsModified := false
			for dk, dv := range defsVal {
				if defMap, ok := dv.(map[string]any); ok {
					repairedDef, defMod := repairSchemaNode(defMap, addMissingArrayItems)
					repairedDefs[dk] = repairedDef
					if defMod {
						defsModified = true
						modified = true
					}
				} else {
					repairedDefs[dk] = dv
				}
			}
			if defsModified {
				clone[key] = repairedDefs
			}
		}
	}

	return clone, modified
}

func repairSchemaList(list []any, addMissingArrayItems bool) ([]any, bool) {
	var repairedList []any
	listModified := false
	for _, item := range list {
		if itemMap, ok := item.(map[string]any); ok {
			repairedItem, itemMod := repairSchemaNode(itemMap, addMissingArrayItems)
			repairedList = append(repairedList, repairedItem)
			if itemMod {
				listModified = true
			}
		} else {
			repairedList = append(repairedList, item)
		}
	}
	return repairedList, listModified
}

func repairPropertyMap(props map[string]any, addMissingArrayItems bool) (map[string]any, []string, bool) {
	out := make(map[string]any, len(props))
	var promotedReqs []string
	modified := false

	for k, v := range props {
		childMap, isMap := v.(map[string]any)
		if !isMap {
			out[k] = v
			continue
		}

		childClone := make(map[string]any, len(childMap))
		for ck, cv := range childMap {
			childClone[ck] = cv
		}

		if reqBool, isBool := childClone["required"].(bool); isBool {
			delete(childClone, "required")
			modified = true
			if reqBool {
				promotedReqs = append(promotedReqs, k)
			}
		}

		repairedChild, childMod := repairSchemaNode(childClone, addMissingArrayItems)
		if childMod {
			modified = true
		}
		out[k] = repairedChild
	}

	sort.Strings(promotedReqs)
	return out, promotedReqs, modified
}

func extractStringArray(val any) []string {
	if val == nil {
		return nil
	}
	arr, ok := val.([]any)
	if !ok {
		if strArr, ok := val.([]string); ok {
			return strArr
		}
		return nil
	}
	var res []string
	for _, item := range arr {
		if s, ok := item.(string); ok {
			res = append(res, s)
		}
	}
	return res
}

func mergeStringSlices(existing, promoted []string) []string {
	seen := make(map[string]bool)
	var res []string
	for _, s := range existing {
		if !seen[s] && s != "" {
			seen[s] = true
			res = append(res, s)
		}
	}
	for _, s := range promoted {
		if !seen[s] && s != "" {
			seen[s] = true
			res = append(res, s)
		}
	}
	return res
}

// InlineLocalRefs resolves JSON Pointer references against the original schema before definition
// containers are stripped. Each expansion receives its own copy, sibling keywords override the
// referenced definition, and cycles terminate as a typed hint instead of recursing forever.
func InlineLocalRefs(jsonStr string) string {
	return inlineLocalRefs(jsonStr)
}

// inlineLocalRefs resolves JSON Pointer references against the original schema before definition
// containers are stripped. Each expansion receives its own copy, sibling keywords override the
// referenced definition, and cycles terminate as a typed hint instead of recursing forever.
func inlineLocalRefs(jsonStr string) string {
	if !strings.Contains(jsonStr, `"$ref"`) {
		return jsonStr
	}

	decoder := json.NewDecoder(strings.NewReader(jsonStr))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return jsonStr
	}

	resolved := resolveLocalRefs(root, root, make(map[string]bool), "")
	out, err := json.Marshal(resolved)
	if err != nil {
		return jsonStr
	}
	return string(out)
}

func resolveLocalRefs(root, value any, active map[string]bool, path string) any {
	switch node := value.(type) {
	case []any:
		out := make([]any, len(node))
		for i, item := range node {
			out[i] = resolveLocalRefs(root, item, active, joinPath(path, strconv.Itoa(i)))
		}
		return out
	case map[string]any:
		ref, hasRef := node["$ref"].(string)
		if hasRef && strings.HasPrefix(ref, "#/") {
			if target, ok := resolveJSONPointer(root, ref); ok {
				if active[ref] {
					return cyclicRefFallback(node, target, ref)
				}
				active[ref] = true
				resolvedTarget := resolveLocalRefs(root, target, active, path)
				delete(active, ref)
				if targetMap, okTarget := resolvedTarget.(map[string]any); okTarget {
					out := make(map[string]any, len(targetMap)+len(node))
					for key, item := range targetMap {
						out[key] = item
					}
					for key, item := range node {
						if key == "$ref" {
							continue
						}
						if isOpaqueSchemaValue(path, key) {
							out[key] = item
							continue
						}
						out[key] = resolveLocalRefs(root, item, active, joinPath(path, escapeGJSONPathKey(key)))
					}
					return out
				}
			}
		}

		out := make(map[string]any, len(node))
		for key, item := range node {
			if isOpaqueSchemaValue(path, key) {
				out[key] = item
				continue
			}
			out[key] = resolveLocalRefs(root, item, active, joinPath(path, escapeGJSONPathKey(key)))
		}
		return out
	default:
		return value
	}
}

func resolveJSONPointer(root any, ref string) (any, bool) {
	current := root
	for _, rawPart := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		part := strings.ReplaceAll(strings.ReplaceAll(rawPart, "~1", "/"), "~0", "~")
		switch node := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = node[part]
			if !ok {
				return nil, false
			}
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(node) {
				return nil, false
			}
			current = node[index]
		default:
			return nil, false
		}
	}
	return current, true
}

func cyclicRefFallback(node map[string]any, target any, ref string) map[string]any {
	out := make(map[string]any, len(node)+2)
	if targetMap, ok := target.(map[string]any); ok {
		for _, key := range []string{"type", "nullable", "description"} {
			if value, exists := targetMap[key]; exists {
				out[key] = value
			}
		}
	}
	for key, value := range node {
		if key != "$ref" {
			out[key] = value
		}
	}
	name := refName(ref)
	hint := "See: " + name
	if description, _ := out["description"].(string); description != "" {
		out["description"] = mergeHint(description, hint)
	} else {
		out["description"] = hint
	}
	return out
}

func refName(ref string) string {
	if index := strings.LastIndex(ref, "/"); index >= 0 && index+1 < len(ref) {
		return strings.ReplaceAll(strings.ReplaceAll(ref[index+1:], "~1", "/"), "~0", "~")
	}
	return ref
}

// convertRefsToHints retains sibling keywords and converts only unresolved or external references
// to descriptions. Local references have already been expanded by inlineLocalRefs.
func convertRefsToHints(jsonStr string, preserveSiblings bool) string {
	paths := findPaths(jsonStr, "$ref")
	sortByDepth(paths)

	for _, p := range paths {
		refVal := gjson.Get(jsonStr, p).String()
		defName := refName(refVal)

		parentPath := trimSuffix(p, ".$ref")
		hint := fmt.Sprintf("See: %s", defName)
		if !preserveSiblings {
			if existing := gjson.Get(jsonStr, descriptionPath(parentPath)).String(); existing != "" {
				hint = fmt.Sprintf("%s (%s)", existing, hint)
			}
			replacement := `{"type":"object","description":""}`
			replacementBytes, _ := sjson.SetBytes([]byte(replacement), "description", hint)
			jsonStr = setRawAt(jsonStr, parentPath, string(replacementBytes))
			continue
		}
		jsonStr, _ = sjson.Delete(jsonStr, p)
		jsonStr = appendHint(jsonStr, parentPath, hint)
	}
	return jsonStr
}

// projectExclusiveBounds rewrites integer exclusiveMinimum / exclusiveMaximum into equivalent
// inclusive bounds. Continuous number bounds use the closest supported inclusive bound and retain
// their exact exclusivity as a description hint. Property names that collide with these keywords
// are preserved.
func projectExclusiveBounds(jsonStr string) string {
	pathsByField := findPathsByFields(jsonStr, []string{"exclusiveMinimum", "exclusiveMaximum"})
	integerSchemaPaths := findIntegerSchemaPaths(jsonStr)
	for _, key := range []string{"exclusiveMinimum", "exclusiveMaximum"} {
		inclusive := "minimum"
		exclusiveMaximum := key == "exclusiveMaximum"
		if exclusiveMaximum {
			inclusive = "maximum"
		}
		for _, p := range pathsByField[key] {
			parentPath := trimSuffix(p, "."+key)
			if isPropertyDefinition(parentPath) {
				continue
			}
			val := gjson.Get(jsonStr, p)
			if !val.Exists() {
				continue
			}

			incPath := joinPath(parentPath, inclusive)
			integerSchema := effectiveSchemaType(gjson.Get(jsonStr, joinPath(parentPath, "type"))) == "integer"
			if !integerSchema {
				_, integerSchema = integerSchemaPaths[logicalAllOfSchemaPath(parentPath)]
			}
			switch {
			case val.Type == gjson.Number && integerSchema:
				if projected, ok := projectIntegerExclusiveBound(val.Raw, exclusiveMaximum); ok {
					var represented bool
					jsonStr, represented = setStricterNumericBound(jsonStr, incPath, projected, exclusiveMaximum)
					if !represented {
						jsonStr = appendHint(jsonStr, parentPath, fmt.Sprintf("%s: %s", key, val.Raw))
					}
				} else {
					jsonStr = appendHint(jsonStr, parentPath, fmt.Sprintf("%s: %s", key, val.Raw))
				}
			case val.Type == gjson.Number:
				jsonStr, _ = setStricterNumericBound(jsonStr, incPath, val.Raw, exclusiveMaximum)
				jsonStr = appendHint(jsonStr, parentPath, fmt.Sprintf("%s: %s", key, val.Raw))
			case (val.Type == gjson.True || val.Type == gjson.False) && val.Bool():
				// Draft-04 represents exclusivity as a boolean flag on minimum / maximum.
				bound := gjson.Get(jsonStr, incPath)
				if integerSchema && bound.Type == gjson.Number {
					if projected, ok := projectIntegerExclusiveBound(bound.Raw, exclusiveMaximum); ok {
						updated, _ := sjson.SetRawBytes([]byte(jsonStr), incPath, []byte(projected))
						jsonStr = string(updated)
					} else {
						jsonStr = appendHint(jsonStr, parentPath, key+": true")
					}
				} else if bound.Exists() {
					jsonStr = appendHint(jsonStr, parentPath, key+": true")
				}
			}
			jsonStr, _ = sjson.Delete(jsonStr, p)
		}
	}
	return jsonStr
}

// findIntegerSchemaPaths records types by their logical location after allOf flattening. An allOf
// branch inherits the intersection's integer domain even when its own bound-only branch omits type.
func findIntegerSchemaPaths(jsonStr string) map[string]struct{} {
	paths := make(map[string]struct{})
	for _, typePath := range findPaths(jsonStr, "type") {
		if effectiveSchemaType(gjson.Get(jsonStr, typePath)) != "integer" {
			continue
		}
		parentPath := trimSuffix(typePath, ".type")
		paths[logicalAllOfSchemaPath(parentPath)] = struct{}{}
	}
	return paths
}

func logicalAllOfSchemaPath(path string) string {
	parts := splitGJSONPath(path)
	logical := make([]string, 0, len(parts))
	for index := 0; index < len(parts); index++ {
		if parts[index] == "allOf" && index+1 < len(parts) && isDecimalPathIndex(parts[index+1]) {
			index++
			continue
		}
		logical = append(logical, parts[index])
	}
	return strings.Join(logical, ".")
}

func isDecimalPathIndex(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

// effectiveSchemaType mirrors flattenTypeArrays: for a type union, the first
// non-null member is the scalar type that survives cleaning.
func effectiveSchemaType(schemaType gjson.Result) string {
	if !schemaType.IsArray() {
		return schemaType.String()
	}
	for _, item := range schemaType.Array() {
		if value := item.String(); value != "" && value != "null" {
			return value
		}
	}
	return ""
}

const (
	// Bounds larger than these limits cannot be represented usefully by the upstream schema APIs.
	// Keeping both the literal and exponent bounded also prevents big.Rat from expanding a compact
	// value such as 1e1000000000 into an enormous integer.
	maxSchemaBoundLiteralLength = 4096
	maxSchemaBoundExponent      = 4096
)

func parseSchemaNumericBound(raw string) (*big.Rat, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxSchemaBoundLiteralLength {
		return nil, false
	}

	if exponentIndex := strings.IndexAny(raw, "eE"); exponentIndex >= 0 {
		exponent := raw[exponentIndex+1:]
		if exponent == "" {
			return nil, false
		}
		if exponent[0] == '+' || exponent[0] == '-' {
			exponent = exponent[1:]
		}
		if exponent == "" {
			return nil, false
		}
		magnitude := 0
		for i := 0; i < len(exponent); i++ {
			if exponent[i] < '0' || exponent[i] > '9' {
				return nil, false
			}
			digit := int(exponent[i] - '0')
			if magnitude > (maxSchemaBoundExponent-digit)/10 {
				return nil, false
			}
			magnitude = magnitude*10 + digit
		}
	}

	value, ok := new(big.Rat).SetString(raw)
	return value, ok
}

func projectIntegerExclusiveBound(raw string, exclusiveMaximum bool) (string, bool) {
	value, ok := parseSchemaNumericBound(raw)
	if !ok {
		return "", false
	}

	// Div rounds toward negative infinity because the denominator is positive.
	projected := new(big.Int).Div(value.Num(), value.Denom())
	if exclusiveMaximum {
		if value.IsInt() {
			projected.Sub(projected, big.NewInt(1))
		}
	} else {
		projected.Add(projected, big.NewInt(1))
	}
	return projected.String(), true
}

// setStricterNumericBound returns whether the incoming bound is represented either by the
// existing stricter bound or by a successful write. A capped numeric sibling cannot be compared
// safely, so callers can preserve the incoming constraint as an exact description hint.
func setStricterNumericBound(jsonStr, path, projected string, maximum bool) (string, bool) {
	existing := gjson.Get(jsonStr, path)
	if existing.Exists() {
		projectedValue, projectedOK := parseSchemaNumericBound(projected)
		if !projectedOK {
			return jsonStr, false
		}
		if existing.Type != gjson.Number {
			updated, _ := sjson.SetRawBytes([]byte(jsonStr), path, []byte(projected))
			return string(updated), true
		}
		existingValue, existingOK := parseSchemaNumericBound(existing.Raw)
		if !existingOK {
			return jsonStr, false
		}
		comparison := projectedValue.Cmp(existingValue)
		if (maximum && comparison >= 0) || (!maximum && comparison <= 0) {
			return jsonStr, true
		}
	}
	updated, _ := sjson.SetRawBytes([]byte(jsonStr), path, []byte(projected))
	return string(updated), true
}

func convertConstToEnum(jsonStr string) string {
	for _, p := range findPaths(jsonStr, "const") {
		val := gjson.Get(jsonStr, p)
		if !val.Exists() {
			continue
		}
		enumPath := joinPath(trimSuffix(p, ".const"), "enum")
		if !gjson.Get(jsonStr, enumPath).Exists() {
			updated, _ := sjson.SetRawBytes([]byte(jsonStr), enumPath, []byte("["+val.Raw+"]"))
			jsonStr = string(updated)
		}
	}
	return jsonStr
}

// convertEnumValuesToStrings ensures all enum values use the string representation required by
// Gemini's proto schema. The declared type remains independent: Antigravity uses it to choose the
// emitted JSON type on both response and function-argument paths.
func convertEnumValuesToStrings(jsonStr string, forceStringType bool) string {
	for _, p := range findPaths(jsonStr, "enum") {
		arr := gjson.Get(jsonStr, p)
		if !arr.IsArray() {
			continue
		}
		parentPath := trimSuffix(p, ".enum")
		if isBooleanEnumSchema(jsonStr, parentPath, arr) {
			continue
		}

		var stringVals []string
		for _, item := range arr.Array() {
			stringVals = append(stringVals, item.String())
		}

		updated, _ := sjson.SetBytes([]byte(jsonStr), p, stringVals)
		jsonStr = string(updated)
		if forceStringType {
			updated, _ = sjson.SetBytes([]byte(jsonStr), joinPath(parentPath, "type"), "string")
			jsonStr = string(updated)
		}
	}
	return jsonStr
}

func isBooleanEnumSchema(jsonStr, parentPath string, arr gjson.Result) bool {
	if !arr.IsArray() {
		return false
	}
	hasBoolean := false
	for _, item := range arr.Array() {
		switch item.Type {
		case gjson.True, gjson.False:
			hasBoolean = true
		case gjson.Null:
			// An untyped boolean enum can express nullability through a null member.
		default:
			return false
		}
	}
	schemaType := gjson.Get(jsonStr, joinPath(parentPath, "type"))
	if schemaType.Exists() {
		// An explicitly typed nullable boolean may be constrained to null alone. The member
		// validation above still rejects string or numeric enums on boolean-containing unions.
		return schemaTypeIncludes(schemaType, "boolean")
	}
	return hasBoolean
}

func schemaTypeIncludes(schemaType gjson.Result, target string) bool {
	if !schemaType.IsArray() {
		return schemaType.String() == target
	}
	for _, item := range schemaType.Array() {
		if item.String() == target {
			return true
		}
	}
	return false
}

func enumContainsNull(arr gjson.Result) bool {
	for _, item := range arr.Array() {
		if item.Type == gjson.Null {
			return true
		}
	}
	return false
}

func enumHintValue(value gjson.Result) string {
	if value.Type == gjson.Null {
		return "null"
	}
	return value.String()
}

func addEnumHints(jsonStr string) string {
	for _, p := range findPaths(jsonStr, "enum") {
		arr := gjson.Get(jsonStr, p)
		if !arr.IsArray() {
			continue
		}
		items := arr.Array()
		if len(items) <= 1 || len(items) > 10 {
			continue
		}

		var vals []string
		for _, item := range items {
			vals = append(vals, enumHintValue(item))
		}
		jsonStr = appendHint(jsonStr, trimSuffix(p, ".enum"), "Allowed: "+strings.Join(vals, ", "))
	}
	return jsonStr
}

// Antigravity does not enforce enum on function arguments and ignores boolean response enums.
// Preserve the advisory values in description, but do not leave an unenforced constraint in the
// schema contract. Response enums for string, number, and integer remain native constraints.
func dropIgnoredEnumsToHints(jsonStr string, options jsonSchemaCleanOptions) string {
	for _, path := range findPaths(jsonStr, "enum") {
		parentPath := trimSuffix(path, ".enum")
		enum := gjson.Get(jsonStr, path)
		booleanEnum := isBooleanEnumSchema(jsonStr, parentPath, enum)
		shouldDrop := options.dropAllEnums || (options.dropBooleanEnums && booleanEnum)
		if !shouldDrop {
			continue
		}
		if booleanEnum {
			typePath := joinPath(parentPath, "type")
			declaredType := gjson.Get(jsonStr, typePath)
			normalizedType := any("boolean")
			if enumContainsNull(enum) && (!declaredType.Exists() || schemaTypeIncludes(declaredType, "null")) {
				normalizedType = []string{"boolean", "null"}
			}
			updated, _ := sjson.SetBytes([]byte(jsonStr), typePath, normalizedType)
			jsonStr = string(updated)
		}
		if enum.IsArray() && len(enum.Array()) == 1 {
			jsonStr = appendHint(jsonStr, parentPath, "Allowed: "+enumHintValue(enum.Array()[0]))
		}
		jsonStr, _ = sjson.Delete(jsonStr, path)
	}
	return jsonStr
}

func addAdditionalPropertiesHints(jsonStr string) string {
	for _, p := range findPaths(jsonStr, "additionalProperties") {
		if gjson.Get(jsonStr, p).Type == gjson.False {
			jsonStr = appendHint(jsonStr, trimSuffix(p, ".additionalProperties"), "No extra properties allowed")
		}
	}
	return jsonStr
}

var unsupportedConstraints = []string{
	"minLength", "maxLength",
	"pattern", "minItems", "maxItems", "minProperties", "maxProperties", "uniqueItems", "contains", "format",
	"default", "examples", // Claude rejects these in VALIDATED mode
}

func constraintKeywords(options jsonSchemaCleanOptions) []string {
	keywords := append([]string(nil), unsupportedConstraints...)
	if options.antigravitySemantics {
		keywords = append(keywords, "minimum", "maximum", "multipleOf")
	}
	return keywords
}

func moveConstraintsToDescription(jsonStr string, options jsonSchemaCleanOptions) string {
	constraints := constraintKeywords(options)
	pathsByField := findPathsByFields(jsonStr, constraints)
	for _, key := range constraints {
		for _, p := range pathsByField[key] {
			val := gjson.Get(jsonStr, p)
			if !val.Exists() {
				continue
			}
			parentPath := trimSuffix(p, "."+key)
			if isPropertyDefinition(parentPath) {
				continue
			}
			jsonStr = appendConstraintHint(jsonStr, parentPath, key, val)
		}
	}
	return jsonStr
}

func appendConstraintHint(jsonStr, parentPath, field string, value gjson.Result) string {
	if value.IsObject() || value.IsArray() {
		return appendHint(jsonStr, parentPath, fmt.Sprintf("%s: %s", field, value.Raw))
	}
	return appendHint(jsonStr, parentPath, fmt.Sprintf("%s: %s", field, value.String()))
}

func moveNotToDescription(jsonStr string) string {
	for _, path := range findPaths(jsonStr, "not") {
		value := gjson.Get(jsonStr, path)
		if !value.Exists() || isPropertyDefinition(trimSuffix(path, ".not")) {
			continue
		}
		jsonStr = appendHint(jsonStr, trimSuffix(path, ".not"), "not: "+value.Raw)
	}
	return jsonStr
}

func mergeConditionals(jsonStr string) string {
	pathsByField := findPathsByFields(jsonStr, []string{"then", "else"})
	var paths []string
	for _, key := range []string{"then", "else"} {
		for _, p := range pathsByField[key] {
			parentPath := trimSuffix(p, "."+key)
			if isPropertyDefinition(parentPath) {
				continue
			}
			paths = append(paths, p)
		}
	}
	sortByDepth(paths)

	for _, p := range paths {
		props := gjson.Get(jsonStr, joinPath(p, "properties"))
		if !props.IsObject() {
			continue
		}
		var parentPath string
		if strings.HasSuffix(p, ".then") {
			parentPath = trimSuffix(p, ".then")
		} else if strings.HasSuffix(p, ".else") {
			parentPath = trimSuffix(p, ".else")
		} else if p == "then" || p == "else" {
			parentPath = ""
		} else {
			continue
		}

		props.ForEach(func(key, value gjson.Result) bool {
			destPath := joinPath(parentPath, "properties."+escapeGJSONPathKey(key.String()))
			if !gjson.Get(jsonStr, destPath).Exists() {
				updated, _ := sjson.SetRawBytes([]byte(jsonStr), destPath, []byte(value.Raw))
				jsonStr = string(updated)
			}
			return true
		})
	}
	return jsonStr
}

func mergeAllOf(jsonStr string) string {
	paths := findPaths(jsonStr, "allOf")
	sortByDepth(paths)

	for _, p := range paths {
		allOf := gjson.Get(jsonStr, p)
		if !allOf.IsArray() {
			continue
		}
		parentPath := trimSuffix(p, ".allOf")

		for _, item := range allOf.Array() {
			if !item.IsObject() {
				continue
			}
			item.ForEach(func(key, value gjson.Result) bool {
				field := key.String()
				switch field {
				case "required":
					if !value.IsArray() {
						return true
					}
					reqPath := joinPath(parentPath, "required")
					current := getStrings(jsonStr, reqPath)
					for _, required := range value.Array() {
						if name := required.String(); !contains(current, name) {
							current = append(current, name)
						}
					}
					updated, _ := sjson.SetBytes([]byte(jsonStr), reqPath, current)
					jsonStr = string(updated)
				case "if", "then", "else", "allOf":
					// Conditional applicability cannot be represented by the upstream schema.
				default:
					destination := joinPath(parentPath, escapeGJSONPathKey(field))
					jsonStr = mergeAllOfSchemaAtPath(jsonStr, destination, field, value)
				}
				return true
			})
		}
		jsonStr, _ = sjson.Delete(jsonStr, p)
	}
	return jsonStr
}

// mergeAllOfSchemaAtPath preserves parent-first schema shape while intersecting the constraints
// that have safe projections. The rule is recursive and local to allOf rather than changing the
// merger also used for lossy anyOf projections.
func mergeAllOfSchemaAtPath(jsonStr, destination, field string, incoming gjson.Result) string {
	parentPath := trimSuffix(destination, "."+escapeGJSONPathKey(field))
	propertyDefinition := isPropertyDefinition(parentPath)
	if field == "required" && incoming.IsArray() && !propertyDefinition {
		current := getStrings(jsonStr, destination)
		for _, required := range incoming.Array() {
			if name := required.String(); !contains(current, name) {
				current = append(current, name)
			}
		}
		updated, _ := sjson.SetBytes([]byte(jsonStr), destination, current)
		return string(updated)
	}
	if field == "enum" && incoming.IsArray() && !propertyDefinition {
		if existing := gjson.Get(jsonStr, destination); existing.IsArray() {
			updated, _ := sjson.SetRawBytes([]byte(jsonStr), destination, []byte(intersectEnumValues(existing, incoming)))
			return string(updated)
		}
	}
	if field == "additionalProperties" && !propertyDefinition {
		existing := gjson.Get(jsonStr, destination)
		switch {
		case existing.Type == gjson.False:
			return jsonStr
		case incoming.Type == gjson.False:
			updated, _ := sjson.SetBytes([]byte(jsonStr), destination, false)
			return string(updated)
		case existing.Type == gjson.True && incoming.IsObject():
			updated, _ := sjson.SetRawBytes([]byte(jsonStr), destination, []byte(incoming.Raw))
			return string(updated)
		}
	}
	if shouldPreserveAllOfConstraintHints(field) && !propertyDefinition && gjson.Get(jsonStr, destination).Exists() {
		return appendConstraintHint(jsonStr, parentPath, field, incoming)
	}
	if incoming.Type == gjson.Number {
		if maximum, ok := allOfBoundDirection(field); ok {
			updated, represented := setStricterNumericBound(jsonStr, destination, incoming.Raw, maximum)
			if !represented {
				return appendConstraintHint(updated, parentPath, field, incoming)
			}
			return updated
		}
	}
	if field == "description" && incoming.Type == gjson.String {
		existing := gjson.Get(jsonStr, destination)
		if existing.Type == gjson.String {
			updated, _ := sjson.SetBytes([]byte(jsonStr), destination, mergeHint(existing.String(), incoming.String()))
			return string(updated)
		}
	}

	existing := gjson.Get(jsonStr, destination)
	if !existing.Exists() {
		updated, _ := sjson.SetRawBytes([]byte(jsonStr), destination, []byte(incoming.Raw))
		return string(updated)
	}
	if !existing.IsObject() || !incoming.IsObject() {
		return jsonStr
	}
	incoming.ForEach(func(key, value gjson.Result) bool {
		field := key.String()
		child := joinPath(destination, escapeGJSONPathKey(field))
		jsonStr = mergeAllOfSchemaAtPath(jsonStr, child, field, value)
		return true
	})
	return jsonStr
}

func intersectEnumValues(existing, incoming gjson.Result) string {
	incomingValues := incoming.Array()
	allowed := make(map[string]struct{}, len(incomingValues))
	for _, value := range incomingValues {
		allowed[enumValueKey(value)] = struct{}{}
	}
	emitted := make(map[string]struct{})
	var result bytes.Buffer
	result.WriteByte('[')
	first := true
	for _, candidate := range existing.Array() {
		key := enumValueKey(candidate)
		if _, ok := allowed[key]; !ok {
			continue
		}
		if _, duplicate := emitted[key]; duplicate {
			continue
		}
		emitted[key] = struct{}{}
		if !first {
			result.WriteByte(',')
		}
		result.WriteString(candidate.Raw)
		first = false
	}
	result.WriteByte(']')
	return result.String()
}

func enumValueKey(value gjson.Result) string {
	var key strings.Builder
	appendEnumValueKey(&key, value)
	return key.String()
}

func appendEnumValueKey(key *strings.Builder, value gjson.Result) {
	switch value.Type {
	case gjson.String:
		key.WriteString("string:")
		key.WriteString(strconv.Quote(value.String()))
	case gjson.Number:
		if number, ok := parseSchemaNumericBound(value.Raw); ok {
			key.WriteString("number:")
			key.WriteString(number.RatString())
			return
		}
		key.WriteString("number-raw:")
		key.WriteString(value.Raw)
	case gjson.JSON:
		switch {
		case value.IsArray():
			key.WriteString("array:[")
			for index, item := range value.Array() {
				if index > 0 {
					key.WriteByte(',')
				}
				appendEnumValueKey(key, item)
			}
			key.WriteByte(']')
		case value.IsObject():
			type objectMember struct {
				name  string
				value gjson.Result
			}
			members := make([]objectMember, 0)
			value.ForEach(func(memberName, memberValue gjson.Result) bool {
				members = append(members, objectMember{
					name:  memberName.String(),
					value: memberValue,
				})
				return true
			})
			sort.SliceStable(members, func(left, right int) bool {
				return members[left].name < members[right].name
			})
			key.WriteString("object:{")
			for index, member := range members {
				if index > 0 {
					key.WriteByte(',')
				}
				key.WriteString(strconv.Quote(member.name))
				key.WriteByte(':')
				appendEnumValueKey(key, member.value)
			}
			key.WriteByte('}')
		default:
			key.WriteString("json-raw:")
			key.WriteString(value.Raw)
		}
	case gjson.True:
		key.WriteString("boolean:true")
	case gjson.False:
		key.WriteString("boolean:false")
	case gjson.Null:
		key.WriteString("null")
	default:
		fmt.Fprintf(key, "unknown:%d:%s", value.Type, value.Raw)
	}
}

func allOfBoundDirection(field string) (maximum bool, ok bool) {
	switch field {
	case "minimum", "minLength", "minItems", "minProperties":
		return false, true
	case "maximum", "maxLength", "maxItems", "maxProperties":
		return true, true
	default:
		return false, false
	}
}

func shouldPreserveAllOfConstraintHints(field string) bool {
	switch field {
	case "pattern", "format", "contains", "uniqueItems", "multipleOf":
		return true
	default:
		return false
	}
}

// mergeMissingSchemaAtPath recursively fills absent fields without replacing any existing
// definition. A parent schema is the canonical definition; allOf and conditional branches may
// enrich gaps in it, but can never replace it with a narrower branch shell.
func mergeMissingSchemaAtPath(jsonStr, destination string, incoming gjson.Result) string {
	existing := gjson.Get(jsonStr, destination)
	if !existing.Exists() {
		updated, _ := sjson.SetRawBytes([]byte(jsonStr), destination, []byte(incoming.Raw))
		return string(updated)
	}
	if !existing.IsObject() || !incoming.IsObject() {
		return jsonStr
	}
	incoming.ForEach(func(key, value gjson.Result) bool {
		child := joinPath(destination, escapeGJSONPathKey(key.String()))
		jsonStr = mergeMissingSchemaAtPath(jsonStr, child, value)
		return true
	})
	return jsonStr
}

func flattenAnyOfOneOf(jsonStr string) string {
	for _, key := range []string{"anyOf", "oneOf"} {
		paths := findPaths(jsonStr, key)
		sortByDepth(paths)

		for _, p := range paths {
			arr := gjson.Get(jsonStr, p)
			if !arr.IsArray() || len(arr.Array()) == 0 {
				continue
			}

			parentPath := trimSuffix(p, "."+key)
			parent := gjson.Get(jsonStr, parentPath)
			if parentPath == "" {
				parent = gjson.Parse(jsonStr)
			}

			items := arr.Array()

			// If the parent already defines properties (e.g. an object schema with anyOf/oneOf constraints),
			// do not replace the parent with a single branch. Instead, merge any branch properties
			// into the parent and delete the union keyword.
			if parentProps := parent.Get("properties"); parentProps.IsObject() {
				hasNull := false
				for _, item := range items {
					if item.Get("type").String() == "null" {
						hasNull = true
					}
					if branchProps := item.Get("properties"); branchProps.IsObject() {
						branchProps.ForEach(func(propKey, propVal gjson.Result) bool {
							destPath := joinPath(parentPath, "properties."+escapeGJSONPathKey(propKey.String()))
							jsonStr = mergeMissingSchemaAtPath(jsonStr, destPath, propVal)
							return true
						})
					}
				}
				if hasNull {
					updated, _ := sjson.SetBytes([]byte(jsonStr), joinPath(parentPath, "nullable"), true)
					jsonStr = string(updated)
				}
				jsonStr, _ = sjson.Delete(jsonStr, p)
				continue
			}

			parentDesc := gjson.Get(jsonStr, descriptionPath(parentPath)).String()
			bestIdx, allTypes := selectBest(items)
			selected := items[bestIdx].Raw
			hasNull := false
			for _, item := range items {
				if item.Get("type").String() == "null" {
					hasNull = true
					break
				}
			}
			if hasNull && items[bestIdx].Get("type").String() != "null" {
				updated, _ := sjson.SetBytes([]byte(selected), "nullable", true)
				selected = string(updated)
			}

			if parentDesc != "" {
				selected = mergeDescriptionRaw(selected, parentDesc)
			}

			if len(allTypes) > 1 {
				hint := "Accepts: " + strings.Join(allTypes, " | ")
				selected = appendHintRaw(selected, hint)
			}

			jsonStr = setRawAt(jsonStr, parentPath, selected)
		}
	}
	return jsonStr
}

func selectBest(items []gjson.Result) (bestIdx int, types []string) {
	bestScore := -1
	for i, item := range items {
		t := item.Get("type").String()
		score := 0

		switch {
		case t == "object" || item.Get("properties").Exists():
			score, t = 3, orDefault(t, "object")
		case t == "array" || item.Get("items").Exists():
			score, t = 2, orDefault(t, "array")
		case t != "" && t != "null":
			score = 1
		case t == "null":
			score, t = 0, "null"
		default:
			score, t = 0, ""
		}

		if t != "" {
			types = append(types, t)
		}
		if score > bestScore {
			bestScore, bestIdx = score, i
		}
	}
	return
}

func flattenTypeArrays(jsonStr string, preserveNativeNullable bool) string {
	paths := findPaths(jsonStr, "type")
	sortByDepth(paths)

	nullableFields := make(map[string][]string)

	for _, p := range paths {
		res := gjson.Get(jsonStr, p)
		if !res.IsArray() || len(res.Array()) == 0 {
			continue
		}

		hasNull := false
		var nonNullTypes []string
		for _, item := range res.Array() {
			s := item.String()
			if s == "null" {
				hasNull = true
			} else if s != "" {
				nonNullTypes = append(nonNullTypes, s)
			}
		}

		firstType := "string"
		if len(nonNullTypes) > 0 {
			firstType = nonNullTypes[0]
		}

		updated, _ := sjson.SetBytes([]byte(jsonStr), p, firstType)
		jsonStr = string(updated)

		parentPath := trimSuffix(p, ".type")
		if len(nonNullTypes) > 1 {
			hint := "Accepts: " + strings.Join(nonNullTypes, " | ")
			jsonStr = appendHint(jsonStr, parentPath, hint)
		}

		if hasNull {
			if preserveNativeNullable {
				updated, _ = sjson.SetBytes([]byte(jsonStr), joinPath(parentPath, "nullable"), true)
				jsonStr = string(updated)
				jsonStr = appendHint(jsonStr, parentPath, "(nullable)")
				continue
			}

			parts := splitGJSONPath(p)
			if len(parts) >= 3 && parts[len(parts)-3] == "properties" {
				fieldNameEscaped := parts[len(parts)-2]
				fieldName := unescapeGJSONPathKey(fieldNameEscaped)
				objectPath := strings.Join(parts[:len(parts)-3], ".")
				nullableFields[objectPath] = append(nullableFields[objectPath], fieldName)
				jsonStr = appendHint(jsonStr, joinPath(objectPath, "properties."+fieldNameEscaped), "(nullable)")
			}
		}
	}

	for objectPath, fields := range nullableFields {
		reqPath := joinPath(objectPath, "required")
		req := gjson.Get(jsonStr, reqPath)
		if !req.IsArray() {
			continue
		}

		var filtered []string
		for _, required := range req.Array() {
			if !contains(fields, required.String()) {
				filtered = append(filtered, required.String())
			}
		}
		if len(filtered) == 0 {
			jsonStr, _ = sjson.Delete(jsonStr, reqPath)
		} else {
			updated, _ := sjson.SetBytes([]byte(jsonStr), reqPath, filtered)
			jsonStr = string(updated)
		}
	}
	return jsonStr
}

func removeUnsupportedKeywords(jsonStr string, options jsonSchemaCleanOptions) string {
	keywords := append(constraintKeywords(options),
		"$schema", "$defs", "definitions", "const", "$ref", "$id", "additionalProperties",
		"propertyNames", "patternProperties", // Gemini doesn't support these schema keywords
		"if", "then", "else",
		"$comment", "enumDescriptions", "enumTitles", "prefill", "deprecated", "encrypted", // Schema metadata fields unsupported by Gemini
	)
	if options.antigravitySemantics {
		keywords = append(keywords, "not")
	}

	deletePaths := make([]string, 0)
	pathsByField := findPathsByFields(jsonStr, keywords)
	for _, key := range keywords {
		for _, p := range pathsByField[key] {
			if isPropertyDefinition(trimSuffix(p, "."+key)) {
				continue
			}
			if options.preserveAdditionalPropertiesFalse && key == "additionalProperties" {
				if gjson.Get(jsonStr, p).Type == gjson.False {
					continue
				}
			}
			deletePaths = append(deletePaths, p)
		}
	}
	sortByDepth(deletePaths)
	for _, p := range deletePaths {
		jsonStr, _ = sjson.Delete(jsonStr, p)
	}
	// Remove x-* extension fields (e.g., x-google-enum-descriptions) that are not supported by Gemini API
	jsonStr = removeExtensionFields(jsonStr)
	return jsonStr
}

// removeExtensionFields removes all x-* extension fields from the JSON schema.
// These are OpenAPI/JSON Schema extension fields that Google APIs don't recognize.
func removeExtensionFields(jsonStr string) string {
	var paths []string
	walkForExtensions(gjson.Parse(jsonStr), "", &paths)
	// walkForExtensions returns paths in a way that deeper paths are added before their ancestors
	// when they are not deleted wholesale, but since we skip children of deleted x-* nodes,
	// any collected path is safe to delete. We still use DeleteBytes for efficiency.

	b := []byte(jsonStr)
	for _, p := range paths {
		b, _ = sjson.DeleteBytes(b, p)
	}
	return string(b)
}

func walkForExtensions(value gjson.Result, path string, paths *[]string) {
	if value.IsArray() {
		arr := value.Array()
		for i := len(arr) - 1; i >= 0; i-- {
			walkForExtensions(arr[i], joinPath(path, strconv.Itoa(i)), paths)
		}
		return
	}

	if value.IsObject() {
		value.ForEach(func(key, val gjson.Result) bool {
			keyStr := key.String()
			safeKey := escapeGJSONPathKey(keyStr)
			childPath := joinPath(path, safeKey)

			// If it's an extension field, we delete it and don't need to look at its children.
			if strings.HasPrefix(keyStr, "x-") && !isPropertyDefinition(path) {
				*paths = append(*paths, childPath)
				return true
			}
			if isOpaqueSchemaValue(path, keyStr) {
				return true
			}

			walkForExtensions(val, childPath, paths)
			return true
		})
	}
}

func cleanupRequiredFields(jsonStr string) string {
	for _, p := range findPaths(jsonStr, "required") {
		parentPath := trimSuffix(p, ".required")
		propsPath := joinPath(parentPath, "properties")

		req := gjson.Get(jsonStr, p)
		props := gjson.Get(jsonStr, propsPath)
		if !req.IsArray() {
			continue
		}
		if !props.IsObject() {
			jsonStr, _ = sjson.Delete(jsonStr, p)
			continue
		}

		var valid []string
		for _, r := range req.Array() {
			key := r.String()
			if props.Get(escapeGJSONPathKey(key)).Exists() {
				valid = append(valid, key)
			}
		}

		if len(valid) != len(req.Array()) {
			if len(valid) == 0 {
				jsonStr, _ = sjson.Delete(jsonStr, p)
			} else {
				updated, _ := sjson.SetBytes([]byte(jsonStr), p, valid)
				jsonStr = string(updated)
			}
		}
	}
	return jsonStr
}

// addEmptySchemaPlaceholder adds a placeholder "reason" property to empty object schemas.
// Claude VALIDATED mode requires at least one required property in tool schemas.
func addEmptySchemaPlaceholder(jsonStr string) string {
	// Find all "type" fields
	paths := findPaths(jsonStr, "type")

	// Process from deepest to shallowest (to handle nested objects properly)
	sortByDepth(paths)

	for _, p := range paths {
		typeVal := gjson.Get(jsonStr, p)
		if typeVal.String() != "object" {
			continue
		}

		// Get the parent path (the object containing "type")
		parentPath := trimSuffix(p, ".type")

		// Check if properties exists and is empty or missing
		propsPath := joinPath(parentPath, "properties")
		propsVal := gjson.Get(jsonStr, propsPath)
		reqPath := joinPath(parentPath, "required")
		reqVal := gjson.Get(jsonStr, reqPath)
		hasRequiredProperties := reqVal.IsArray() && len(reqVal.Array()) > 0

		needsPlaceholder := false
		if !propsVal.Exists() {
			// No properties field at all
			needsPlaceholder = true
		} else if propsVal.IsObject() && len(propsVal.Map()) == 0 {
			// Empty properties object
			needsPlaceholder = true
		}

		if needsPlaceholder {
			// Add placeholder "reason" property
			reasonPath := joinPath(propsPath, "reason")
			updated, _ := sjson.SetBytes([]byte(jsonStr), reasonPath+".type", "string")
			jsonStr = string(updated)
			updated, _ = sjson.SetBytes([]byte(jsonStr), reasonPath+".description", placeholderReasonDescription)
			jsonStr = string(updated)

			// Add to required array
			updated, _ = sjson.SetBytes([]byte(jsonStr), reqPath, []string{"reason"})
			jsonStr = string(updated)
			continue
		}

		// If schema has properties but none are required, add a minimal placeholder.
		if propsVal.IsObject() && !hasRequiredProperties {
			// DO NOT add placeholder if it's a top-level schema (parentPath is empty)
			// or if we've already added a placeholder reason above.
			if parentPath == "" {
				continue
			}
			placeholderPath := joinPath(propsPath, "_")
			if !gjson.Get(jsonStr, placeholderPath).Exists() {
				updated, _ := sjson.SetBytes([]byte(jsonStr), placeholderPath+".type", "boolean")
				jsonStr = string(updated)
			}
			updated, _ := sjson.SetBytes([]byte(jsonStr), reqPath, []string{"_"})
			jsonStr = string(updated)
		}
	}

	return jsonStr
}

// --- Helpers ---

func findPaths(jsonStr, field string) []string {
	pathsByField := findPathsByFields(jsonStr, []string{field})
	return pathsByField[field]
}

// findPropertyNamePaths is used only when the caller intentionally searches author-selected
// names in a schema name map (for example, cleanup of generated placeholder properties). It still
// observes opaque literal boundaries, so a matching object key inside enum/const/default data is
// never returned.
func findPropertyNamePaths(jsonStr, field string) []string {
	pathsByField := findPathsByFieldsWithPropertyNames(jsonStr, []string{field}, true)
	return pathsByField[field]
}

func findPathsByFields(jsonStr string, fields []string) map[string][]string {
	return findPathsByFieldsWithPropertyNames(jsonStr, fields, false)
}

func findPathsByFieldsWithPropertyNames(jsonStr string, fields []string, includePropertyNames bool) map[string][]string {
	set := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		set[field] = struct{}{}
	}
	paths := make(map[string][]string, len(set))
	walkForFields(gjson.Parse(jsonStr), "", set, paths, includePropertyNames)
	return paths
}

func walkForFields(value gjson.Result, path string, fields map[string]struct{}, paths map[string][]string, includePropertyNames bool) {
	switch value.Type {
	case gjson.JSON:
		value.ForEach(func(key, val gjson.Result) bool {
			keyStr := key.String()
			safeKey := escapeGJSONPathKey(keyStr)

			var childPath string
			if path == "" {
				childPath = safeKey
			} else {
				childPath = path + "." + safeKey
			}

			if _, ok := fields[keyStr]; ok && (includePropertyNames || !isPropertyDefinition(path)) {
				paths[keyStr] = append(paths[keyStr], childPath)
			}
			if isOpaqueSchemaValue(path, keyStr) {
				return true
			}

			walkForFields(val, childPath, fields, paths, includePropertyNames)
			return true
		})
	case gjson.String, gjson.Number, gjson.True, gjson.False, gjson.Null:
		// Terminal types - no further traversal needed
	}
}

// isOpaqueSchemaValue identifies schema keywords whose values are instance data or annotations,
// not nested schemas. A matching name directly under properties/$defs is author-selected and its
// value remains a schema, so property-name collision handling takes precedence.
func isOpaqueSchemaValue(parentPath, field string) bool {
	if isPropertyDefinition(parentPath) {
		return false
	}
	switch field {
	case "enum", "const", "default", "example", "examples",
		"enumDescriptions", "enumTitles", "required", "dependentRequired",
		"discriminator", "xml", "externalDocs":
		return true
	default:
		return false
	}
}

func sortByDepth(paths []string) {
	sort.SliceStable(paths, func(i, j int) bool {
		return len(splitGJSONPath(paths[i])) > len(splitGJSONPath(paths[j]))
	})
}

func trimSuffix(path, suffix string) string {
	if path == strings.TrimPrefix(suffix, ".") {
		return ""
	}
	return strings.TrimSuffix(path, suffix)
}

func joinPath(base, suffix string) string {
	if base == "" {
		return suffix
	}
	return base + "." + suffix
}

func setRawAt(jsonStr, path, value string) string {
	if path == "" {
		return value
	}
	result, _ := sjson.SetRawBytes([]byte(jsonStr), path, []byte(value))
	return string(result)
}

// schemaNameMapKeywords are the schema keywords whose value maps author-chosen names to
// subschemas. Legacy dependencies may also map a name to a string array. A key directly under any
// of these maps is a name, never a schema keyword.
var schemaNameMapKeywords = map[string]struct{}{
	"properties":        {},
	"patternProperties": {},
	"dependentSchemas":  {},
	"dependencies":      {},
	"$defs":             {},
	"definitions":       {},
}

// isPropertyDefinition reports whether path points at a map whose keys are names chosen by the
// tool author, so a key spelled like a schema keyword there must be preserved.
//
// A trailing ".properties" is not enough to tell: a tool may declare a property named
// "properties", and the schema for that property then sits at a path ending in ".properties" while
// being an ordinary schema node. Classifying it as a name map skipped every cleaning pass inside
// it, so unsupported keywords such as "propertyNames" reached the private Gemini backend, which
// rejects unknown fields with a 400.
//
// Each name-map keyword at the end of the path therefore flips the answer, because the node it
// names is a map only when its own parent is a schema: "properties" is a map,
// "properties.properties" the schema of a property named "properties", and
// "properties.properties.properties" that schema's own map. Only the trailing run matters, so any
// prefix the caller nests the schema under is ignored.
func isPropertyDefinition(path string) bool {
	segments := splitGJSONPath(path)
	trailing := 0
	for i := len(segments) - 1; i >= 0; i-- {
		if _, ok := schemaNameMapKeywords[unescapeGJSONPathKey(segments[i])]; !ok {
			break
		}
		trailing++
	}
	return trailing%2 == 1
}

func descriptionPath(parentPath string) string {
	if parentPath == "" || parentPath == "@this" {
		return "description"
	}
	return parentPath + ".description"
}

// mergeHint combines an existing description with a hint. Cleaning is not always a single pass:
// a schema may be cleaned by a translator and again by an executor, so an already-present hint is
// kept as-is instead of being appended a second time.
func mergeHint(existing, hint string) string {
	if existing == "" {
		return hint
	}
	// A hint added to an empty description is stored bare and later hints are appended after it, so
	// the bare form may sit alone, lead the description, or appear parenthesised further along.
	if existing == hint ||
		strings.HasPrefix(existing, hint+" (") ||
		strings.Contains(existing, fmt.Sprintf("(%s)", hint)) {
		return existing
	}
	return fmt.Sprintf("%s (%s)", existing, hint)
}

func appendHint(jsonStr, parentPath, hint string) string {
	descPath := parentPath + ".description"
	if parentPath == "" || parentPath == "@this" {
		descPath = "description"
	}
	merged := mergeHint(gjson.Get(jsonStr, descPath).String(), hint)
	updated, _ := sjson.SetBytes([]byte(jsonStr), descPath, merged)
	jsonStr = string(updated)
	return jsonStr
}

func appendHintRaw(jsonRaw, hint string) string {
	merged := mergeHint(gjson.Get(jsonRaw, "description").String(), hint)
	updated, _ := sjson.SetBytes([]byte(jsonRaw), "description", merged)
	jsonRaw = string(updated)
	return jsonRaw
}

func getStrings(jsonStr, path string) []string {
	var result []string
	if arr := gjson.Get(jsonStr, path); arr.IsArray() {
		for _, r := range arr.Array() {
			result = append(result, r.String())
		}
	}
	return result
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func orDefault(val, def string) string {
	if val == "" {
		return def
	}
	return val
}

func escapeGJSONPathKey(key string) string {
	if strings.IndexAny(key, ".*?") == -1 {
		return key
	}
	return gjsonPathKeyReplacer.Replace(key)
}

func unescapeGJSONPathKey(key string) string {
	if !strings.Contains(key, "\\") {
		return key
	}
	var b strings.Builder
	b.Grow(len(key))
	for i := 0; i < len(key); i++ {
		if key[i] == '\\' && i+1 < len(key) {
			i++
			b.WriteByte(key[i])
			continue
		}
		b.WriteByte(key[i])
	}
	return b.String()
}

func splitGJSONPath(path string) []string {
	if path == "" {
		return nil
	}

	parts := make([]string, 0, strings.Count(path, ".")+1)
	var b strings.Builder
	b.Grow(len(path))

	for i := 0; i < len(path); i++ {
		c := path[i]
		if c == '\\' && i+1 < len(path) {
			b.WriteByte('\\')
			i++
			b.WriteByte(path[i])
			continue
		}
		if c == '.' {
			parts = append(parts, b.String())
			b.Reset()
			continue
		}
		b.WriteByte(c)
	}
	parts = append(parts, b.String())
	return parts
}

func mergeDescriptionRaw(schemaRaw, parentDesc string) string {
	childDesc := gjson.Get(schemaRaw, "description").String()
	switch {
	case childDesc == "":
		updated, _ := sjson.SetBytes([]byte(schemaRaw), "description", parentDesc)
		return string(updated)
	case childDesc == parentDesc:
		return schemaRaw
	default:
		combined := fmt.Sprintf("%s (%s)", parentDesc, childDesc)
		updated, _ := sjson.SetBytes([]byte(schemaRaw), "description", combined)
		return string(updated)
	}
}
