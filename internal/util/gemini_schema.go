package util

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// The functions below implement JSON Schema transformations for Gemini and Antigravity APIs.
//
// Background:
// Antigravity models have strict JSON Schema requirements. They reject keywords like
// $schema, additionalProperties: false, and minLength/maxLength. This package cleans and converts
// standard JSON Schemas to be compatible while preserving semantic information.
//
// Reference:
// - Gemini API docs: Function calling schema requirements
// - Antigravity API: VALIDATED mode requires specific schema structure
//
// A critical design constraint on this file is that its transformations must ONLY be run against
// schema definitions, never against payload bodies containing actual arguments. The cleaner
// silently rewrites tool-call arguments inside the conversation history: the guard that protects
// a key under ".properties" does not apply to argument values, so the keys are deleted outright
// and replacements such as "enum" and "type" are fabricated. That regression reached production
// once already; scope every call site to the schema itself.

type jsonSchemaCleanOptions struct {
	addPlaceholder                    bool
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
		removeGeminiMetadata: true,
		flattenUnions:        true,
		forceEnumStringType:  true,
	})
}

// cleanJSONSchema performs the core cleaning operations on the JSON schema.
func cleanJSONSchema(jsonStr string, options jsonSchemaCleanOptions) string {
	trimmed := strings.TrimSpace(jsonStr)
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		jsonStr = `{"type":"object","properties":{}}`
	}
	// Phase 1: Convert and add hints
	if options.antigravitySemantics {
		jsonStr = inlineLocalRefs(jsonStr)
	}
	jsonStr = convertRefsToHints(jsonStr, options.antigravitySemantics)
	jsonStr = convertConstToEnum(jsonStr)
	jsonStr = convertEnumValuesToStrings(jsonStr, options.forceEnumStringType)
	jsonStr = addEnumHints(jsonStr)
	jsonStr = dropIgnoredEnumsToHints(jsonStr, options)
	if !options.preserveAdditionalPropertiesFalse {
		jsonStr = addAdditionalPropertiesHints(jsonStr)
	}
	jsonStr = moveConstraintsToDescription(jsonStr, options)
	if options.antigravitySemantics {
		jsonStr = moveNotToDescription(jsonStr)
	}

	// Phase 2: Flatten complex structures
	jsonStr = mergeConditionals(jsonStr)
	jsonStr = mergeAllOf(jsonStr)
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
	for _, kw := range keywords {
		paths := findPaths(jsonStr, kw)
		sortByDepth(paths)
		for _, p := range paths {
			jsonStr, _ = sjson.Delete(jsonStr, p)
		}
	}
	return jsonStr
}

func removePlaceholderFields(jsonStr string) string {
	paths := findPaths(jsonStr, "properties.reason")
	sortByDepth(paths)
	for _, p := range paths {
		if !strings.HasSuffix(p, ".properties.reason") && p != "properties.reason" {
			continue
		}
		parentPath := trimSuffix(p, ".properties.reason")
		reasonDesc := gjson.Get(jsonStr, joinPath(p, "description")).String()
		if !strings.Contains(reasonDesc, "placeholder") && !strings.Contains(reasonDesc, "Brief explanation of why you are calling this tool") {
			continue
		}

		propsPath := joinPath(parentPath, "properties")
		propsVal := gjson.Get(jsonStr, propsPath)
		if len(propsVal.Map()) != 1 {
			continue
		}

		jsonStr, _ = sjson.Delete(jsonStr, p)
		reqPath := joinPath(parentPath, "required")
		reqVal := gjson.Get(jsonStr, reqPath)
		if reqVal.IsArray() {
			var newReq []string
			for _, item := range reqVal.Array() {
				if item.String() != "reason" {
					newReq = append(newReq, item.String())
				}
			}
			if len(newReq) == 0 {
				jsonStr, _ = sjson.Delete(jsonStr, reqPath)
			} else {
				updated, _ := sjson.SetBytes([]byte(jsonStr), reqPath, newReq)
				jsonStr = string(updated)
			}
		}
	}
	return jsonStr
}

// convertRefsToHints handles $ref keywords in schemas.
// When preserveLocalDefs is true ($defs present), it only converts external $refs to hints
// and preserves internal $refs so local definitions can be resolved.
func convertRefsToHints(jsonStr string, preserveLocalDefs bool) string {
	hasLocalDefs := preserveLocalDefs && (gjson.Get(jsonStr, "$defs").Exists() || gjson.Get(jsonStr, "definitions").Exists())

	paths := findPaths(jsonStr, "$ref")
	sortByDepth(paths)

	for _, p := range paths {
		refVal := gjson.Get(jsonStr, p).String()

		// If we have local defs and this is a local ref (starts with #), preserve it for inlining
		if hasLocalDefs && strings.HasPrefix(refVal, "#") {
			continue
		}

		// Extract reference name from URL/path
		refName := refVal
		if lastSlash := strings.LastIndex(refVal, "/"); lastSlash != -1 {
			refName = refVal[lastSlash+1:]
		}

		// Get parent path (the object containing $ref)
		parentPath := trimSuffix(p, ".$ref")
		descPath := joinPath(parentPath, "description")
		currentDesc := gjson.Get(jsonStr, descPath).String()

		hint := fmt.Sprintf("(see %s)", refName)
		newDesc := appendHint(currentDesc, hint)

		// Set default type to string for unresolved refs
		typePath := joinPath(parentPath, "type")
		if !gjson.Get(jsonStr, typePath).Exists() {
			jsonStr, _ = sjson.Set(jsonStr, typePath, "string")
		}

		jsonStr, _ = sjson.Set(jsonStr, descPath, newDesc)
		jsonStr, _ = sjson.Delete(jsonStr, p)
	}

	return jsonStr
}

// inlineLocalRefs resolves internal #/$defs/... and #/definitions/... references by copying definition contents.
func inlineLocalRefs(jsonStr string) string {
	defs := gjson.Get(jsonStr, "$defs")
	if !defs.Exists() {
		defs = gjson.Get(jsonStr, "definitions")
	}
	if !defs.Exists() {
		return jsonStr
	}

	// Maximum resolution depth to prevent infinite loops from circular refs
	const maxDepth = 10

	for depth := 0; depth < maxDepth; depth++ {
		paths := findPaths(jsonStr, "$ref")
		if len(paths) == 0 {
			break
		}

		hasLocalRef := false
		sortByDepth(paths)

		for _, p := range paths {
			refVal := gjson.Get(jsonStr, p).String()
			if !strings.HasPrefix(refVal, "#/") {
				continue
			}

			hasLocalRef = true
			targetPath := gjsonPathFromJSONPointer(refVal)
			targetVal := gjson.Get(jsonStr, targetPath)

			parentPath := trimSuffix(p, ".$ref")

			if targetVal.Exists() {
				// Inline the target definition properties into the parent object
				targetJSON := targetVal.Raw
				targetObj := gjson.Parse(targetJSON)

				// Delete the $ref first
				jsonStr, _ = sjson.Delete(jsonStr, p)

				// Copy all fields from target to parent (preserving existing parent fields)
				targetObj.ForEach(func(key, val gjson.Result) bool {
					destPath := joinPath(parentPath, key.String())
					if !gjson.Get(jsonStr, destPath).Exists() {
						jsonStr, _ = sjson.SetRaw(jsonStr, destPath, val.Raw)
					}
					return true
				})
			} else {
				// Target not found, convert to description hint
				refName := refVal[strings.LastIndex(refVal, "/")+1:]
				descPath := joinPath(parentPath, "description")
				currentDesc := gjson.Get(jsonStr, descPath).String()
				jsonStr, _ = sjson.Set(jsonStr, descPath, appendHint(currentDesc, fmt.Sprintf("(see %s)", refName)))

				typePath := joinPath(parentPath, "type")
				if !gjson.Get(jsonStr, typePath).Exists() {
					jsonStr, _ = sjson.Set(jsonStr, typePath, "string")
				}
				jsonStr, _ = sjson.Delete(jsonStr, p)
			}
		}

		if !hasLocalRef {
			break
		}
	}

	return jsonStr
}

// convertConstToEnum converts "const": "value" to "enum": ["value"].
func convertConstToEnum(jsonStr string) string {
	paths := findPaths(jsonStr, "const")
	sortByDepth(paths)

	for _, p := range paths {
		constVal := gjson.Get(jsonStr, p)
		parentPath := trimSuffix(p, ".const")
		enumPath := joinPath(parentPath, "enum")

		// Create single-element array with the const value
		var enumArr []any
		if constVal.Type == gjson.String {
			enumArr = []any{constVal.String()}
		} else if constVal.Type == gjson.Number {
			enumArr = []any{constVal.Num}
		} else if constVal.Type == gjson.True || constVal.Type == gjson.False {
			enumArr = []any{constVal.Bool()}
		} else {
			enumArr = []any{constVal.Value()}
		}

		jsonStr, _ = sjson.Set(jsonStr, enumPath, enumArr)
		jsonStr, _ = sjson.Delete(jsonStr, p)
	}

	return jsonStr
}

// convertEnumValuesToStrings ensures all enum values are strings for Antigravity API.
func convertEnumValuesToStrings(jsonStr string, forceEnumStringType bool) string {
	paths := findPaths(jsonStr, "enum")
	sortByDepth(paths)

	for _, p := range paths {
		enumVal := gjson.Get(jsonStr, p)
		if !enumVal.IsArray() {
			continue
		}

		var strEnum []string
		hasNonString := false

		for _, item := range enumVal.Array() {
			if item.Type != gjson.String {
				hasNonString = true
			}
			strEnum = append(strEnum, item.String())
		}

		if hasNonString {
			jsonStr, _ = sjson.Set(jsonStr, p, strEnum)

			if forceEnumStringType {
				// Also update type to string if it was something else
				parentPath := trimSuffix(p, ".enum")
				typePath := joinPath(parentPath, "type")
				jsonStr, _ = sjson.Set(jsonStr, typePath, "string")
			}
		}
	}

	return jsonStr
}

// addEnumHints adds "Allowed: [val1, val2]" to descriptions of enum fields.
func addEnumHints(jsonStr string) string {
	paths := findPaths(jsonStr, "enum")
	sortByDepth(paths)

	for _, p := range paths {
		enumVal := gjson.Get(jsonStr, p)
		if !enumVal.IsArray() {
			continue
		}

		var values []string
		for _, item := range enumVal.Array() {
			values = append(values, item.String())
		}

		parentPath := trimSuffix(p, ".enum")
		descPath := joinPath(parentPath, "description")
		currentDesc := gjson.Get(jsonStr, descPath).String()

		hint := fmt.Sprintf("(Allowed: %s)", strings.Join(values, ", "))
		newDesc := appendHint(currentDesc, hint)

		jsonStr, _ = sjson.Set(jsonStr, descPath, newDesc)
	}

	return jsonStr
}

func dropIgnoredEnumsToHints(jsonStr string, options jsonSchemaCleanOptions) string {
	paths := findPaths(jsonStr, "enum")
	sortByDepth(paths)

	for _, p := range paths {
		enumVal := gjson.Get(jsonStr, p)
		if !enumVal.IsArray() {
			continue
		}

		parentPath := trimSuffix(p, ".enum")
		typeVal := gjson.Get(jsonStr, joinPath(parentPath, "type")).String()
		shouldDrop := options.dropAllEnums || (options.dropBooleanEnums && typeVal == "boolean")
		if !shouldDrop {
			continue
		}

		jsonStr, _ = sjson.Delete(jsonStr, p)
	}

	return jsonStr
}

// addAdditionalPropertiesHints adds hints for additionalProperties constraints.
func addAdditionalPropertiesHints(jsonStr string) string {
	paths := findPaths(jsonStr, "additionalProperties")
	sortByDepth(paths)

	for _, p := range paths {
		addPropVal := gjson.Get(jsonStr, p)
		parentPath := trimSuffix(p, ".additionalProperties")
		descPath := joinPath(parentPath, "description")
		currentDesc := gjson.Get(jsonStr, descPath).String()

		var hint string
		if addPropVal.Type == gjson.False {
			hint = "(No extra properties allowed)"
		} else if addPropVal.IsObject() {
			schemaType := addPropVal.Get("type").String()
			if schemaType != "" {
				hint = fmt.Sprintf("(Additional properties type: %s)", schemaType)
			}
		}

		if hint != "" {
			newDesc := appendHint(currentDesc, hint)
			jsonStr, _ = sjson.Set(jsonStr, descPath, newDesc)
		}
	}

	return jsonStr
}

// moveConstraintsToDescription converts numeric/string/array constraints to description hints.
func moveConstraintsToDescription(jsonStr string, options jsonSchemaCleanOptions) string {
	constraintDefs := []struct {
		keyword string
		format  string
	}{
		{"minLength", "minLength: %v"},
		{"maxLength", "maxLength: %v"},
		{"minimum", "minimum: %v"},
		{"maximum", "maximum: %v"},
		{"exclusiveMinimum", "exclusiveMinimum: %v"},
		{"exclusiveMaximum", "exclusiveMaximum: %v"},
		{"minItems", "minItems: %v"},
		{"maxItems", "maxItems: %v"},
		{"pattern", "pattern: %v"},
		{"format", "format: %v"},
		{"default", "default: %v"},
	}

	for _, cd := range constraintDefs {
		paths := findPaths(jsonStr, cd.keyword)
		sortByDepth(paths)

		for _, p := range paths {
			val := gjson.Get(jsonStr, p)
			parentPath := trimSuffix(p, "."+cd.keyword)
			descPath := joinPath(parentPath, "description")
			currentDesc := gjson.Get(jsonStr, descPath).String()

			hint := fmt.Sprintf("("+cd.format+")", val.Value())
			newDesc := appendHint(currentDesc, hint)

			jsonStr, _ = sjson.Set(jsonStr, descPath, newDesc)
		}
	}

	return jsonStr
}

// moveNotToDescription converts "not" schemas to description hints.
func moveNotToDescription(jsonStr string) string {
	paths := findPaths(jsonStr, "not")
	sortByDepth(paths)

	for _, p := range paths {
		notVal := gjson.Get(jsonStr, p)
		parentPath := trimSuffix(p, ".not")
		descPath := joinPath(parentPath, "description")
		currentDesc := gjson.Get(jsonStr, descPath).String()

		var hint string
		if notVal.IsObject() {
			if notType := notVal.Get("type").String(); notType != "" {
				hint = fmt.Sprintf("(Must NOT be type: %s)", notType)
			} else if notEnum := notVal.Get("enum"); notEnum.IsArray() {
				var values []string
				for _, item := range notEnum.Array() {
					values = append(values, item.String())
				}
				hint = fmt.Sprintf("(Must NOT be: %s)", strings.Join(values, ", "))
			}
		}

		if hint != "" {
			newDesc := appendHint(currentDesc, hint)
			jsonStr, _ = sjson.Set(jsonStr, descPath, newDesc)
		}
		jsonStr, _ = sjson.Delete(jsonStr, p)
	}

	return jsonStr
}

// mergeConditionals flattens if/then/else structures by preserving properties from then/else branches.
func mergeConditionals(jsonStr string) string {
	paths := findPaths(jsonStr, "if")
	sortByDepth(paths)

	for _, p := range paths {
		parentPath := trimSuffix(p, ".if")

		thenPath := joinPath(parentPath, "then")
		elsePath := joinPath(parentPath, "else")

		thenVal := gjson.Get(jsonStr, thenPath)
		elseVal := gjson.Get(jsonStr, elsePath)

		// Merge properties from "then" branch
		if thenVal.Exists() && thenVal.IsObject() {
			thenProps := thenVal.Get("properties")
			if thenProps.Exists() && thenProps.IsObject() {
				thenProps.ForEach(func(key, val gjson.Result) bool {
					destPath := joinPath(parentPath, "properties."+escapeGJSONPathKey(key.String()))
					if !gjson.Get(jsonStr, destPath).Exists() {
						jsonStr, _ = sjson.SetRaw(jsonStr, destPath, val.Raw)
					}
					return true
				})
			}
		}

		// Merge properties from "else" branch
		if elseVal.Exists() && elseVal.IsObject() {
			elseProps := elseVal.Get("properties")
			if elseProps.Exists() && elseProps.IsObject() {
				elseProps.ForEach(func(key, val gjson.Result) bool {
					destPath := joinPath(parentPath, "properties."+escapeGJSONPathKey(key.String()))
					if !gjson.Get(jsonStr, destPath).Exists() {
						jsonStr, _ = sjson.SetRaw(jsonStr, destPath, val.Raw)
					}
					return true
				})
			}
		}

		// Delete if/then/else keywords
		jsonStr, _ = sjson.Delete(jsonStr, p)
		jsonStr, _ = sjson.Delete(jsonStr, thenPath)
		jsonStr, _ = sjson.Delete(jsonStr, elsePath)
	}

	return jsonStr
}

// mergeAllOf merges allOf schemas into the parent schema.
func mergeAllOf(jsonStr string) string {
	paths := findPaths(jsonStr, "allOf")
	sortByDepth(paths)

	for _, p := range paths {
		allOfVal := gjson.Get(jsonStr, p)
		if !allOfVal.IsArray() {
			continue
		}

		parentPath := trimSuffix(p, ".allOf")

		for _, branch := range allOfVal.Array() {
			if !branch.IsObject() {
				continue
			}

			// Merge properties
			props := branch.Get("properties")
			if props.Exists() && props.IsObject() {
				props.ForEach(func(key, val gjson.Result) bool {
					destPath := joinPath(parentPath, "properties."+escapeGJSONPathKey(key.String()))
					if !gjson.Get(jsonStr, destPath).Exists() {
						jsonStr, _ = sjson.SetRaw(jsonStr, destPath, val.Raw)
					}
					return true
				})
			}

			// Merge required
			req := branch.Get("required")
			if req.Exists() && req.IsArray() {
				parentReqPath := joinPath(parentPath, "required")
				parentReq := gjson.Get(jsonStr, parentReqPath)

				var mergedReq []string
				if parentReq.Exists() && parentReq.IsArray() {
					for _, item := range parentReq.Array() {
						mergedReq = append(mergedReq, item.String())
					}
				}
				for _, item := range req.Array() {
					if !contains(mergedReq, item.String()) {
						mergedReq = append(mergedReq, item.String())
					}
				}
				jsonStr, _ = sjson.Set(jsonStr, parentReqPath, mergedReq)
			}

			// Copy type if parent doesn't have one
			parentTypePath := joinPath(parentPath, "type")
			if !gjson.Get(jsonStr, parentTypePath).Exists() {
				branchType := branch.Get("type")
				if branchType.Exists() {
					jsonStr, _ = sjson.Set(jsonStr, parentTypePath, branchType.String())
				}
			}
		}

		jsonStr, _ = sjson.Delete(jsonStr, p)
	}

	return jsonStr
}

// flattenAnyOfOneOf handles anyOf and oneOf by selecting the best branch and adding hints.
func flattenAnyOfOneOf(jsonStr string) string {
	for _, unionKey := range []string{"anyOf", "oneOf"} {
		paths := findPaths(jsonStr, unionKey)
		sortByDepth(paths)

		for _, p := range paths {
			unionVal := gjson.Get(jsonStr, p)
			if !unionVal.IsArray() {
				continue
			}

			parentPath := trimSuffix(p, "."+unionKey)
			branches := unionVal.Array()

			// Check for nullable pattern: [T, null]
			hasNull := false
			var nonNullBranches []gjson.Result
			for _, b := range branches {
				if b.Get("type").String() == "null" {
					hasNull = true
				} else {
					nonNullBranches = append(nonNullBranches, b)
				}
			}

			if hasNull && len(nonNullBranches) == 1 {
				// Simple nullable: set nullable=true and use the non-null branch
				target := nonNullBranches[0]
				jsonStr, _ = sjson.Delete(jsonStr, p)

				target.ForEach(func(key, val gjson.Result) bool {
					destPath := joinPath(parentPath, key.String())
					jsonStr, _ = sjson.SetRaw(jsonStr, destPath, val.Raw)
					return true
				})

				jsonStr, _ = sjson.Set(jsonStr, joinPath(parentPath, "nullable"), true)
				descPath := joinPath(parentPath, "description")
				currentDesc := gjson.Get(jsonStr, descPath).String()
				jsonStr, _ = sjson.Set(jsonStr, descPath, appendHint(currentDesc, "(nullable)"))
				continue
			}

			// Multi-branch union: pick the best branch and add hints about alternatives
			bestBranch := selectBestUnionBranch(branches)
			var altTypes []string
			for _, b := range branches {
				t := b.Get("type").String()
				if t != "" && !contains(altTypes, t) {
					altTypes = append(altTypes, t)
				}
			}

			jsonStr, _ = sjson.Delete(jsonStr, p)

			if bestBranch.Exists() && bestBranch.IsObject() {
				bestBranch.ForEach(func(key, val gjson.Result) bool {
					destPath := joinPath(parentPath, key.String())
					if !gjson.Get(jsonStr, destPath).Exists() {
						jsonStr, _ = sjson.SetRaw(jsonStr, destPath, val.Raw)
					}
					return true
				})
			}

			if len(altTypes) > 1 {
				descPath := joinPath(parentPath, "description")
				currentDesc := gjson.Get(jsonStr, descPath).String()
				hint := fmt.Sprintf("(Accepts: %s)", strings.Join(altTypes, " | "))
				jsonStr, _ = sjson.Set(jsonStr, descPath, appendHint(currentDesc, hint))
			}
		}
	}

	return jsonStr
}

// selectBestUnionBranch picks the most informative branch from an anyOf/oneOf array.
// Priority: object with properties > object > array > string > number > boolean > null.
func selectBestUnionBranch(branches []gjson.Result) gjson.Result {
	if len(branches) == 0 {
		return gjson.Result{}
	}

	bestScore := -1
	var best gjson.Result

	for _, b := range branches {
		score := scoreBranch(b)
		if score > bestScore {
			bestScore = score
			best = b
		}
	}

	return best
}

func scoreBranch(b gjson.Result) int {
	if !b.IsObject() {
		return 0
	}

	t := b.Get("type").String()
	switch t {
	case "object":
		if props := b.Get("properties"); props.Exists() && len(props.Map()) > 0 {
			return 100 + len(props.Map())
		}
		return 90
	case "array":
		if items := b.Get("items"); items.Exists() {
			return 80
		}
		return 70
	case "string":
		if b.Get("enum").Exists() {
			return 65
		}
		return 60
	case "number", "integer":
		return 50
	case "boolean":
		return 40
	case "null":
		return 10
	default:
		// Untyped object with properties
		if props := b.Get("properties"); props.Exists() && len(props.Map()) > 0 {
			return 95 + len(props.Map())
		}
		return 30
	}
}

// flattenTypeArrays converts "type": ["string", "null"] to "type": "string", "nullable": true.
func flattenTypeArrays(jsonStr string, preserveNullable bool) string {
	paths := findPaths(jsonStr, "type")
	sortByDepth(paths)

	for _, p := range paths {
		typeVal := gjson.Get(jsonStr, p)
		if !typeVal.IsArray() {
			continue
		}

		types := typeVal.Array()
		hasNull := false
		var nonNullTypes []string

		for _, t := range types {
			if t.String() == "null" {
				hasNull = true
			} else {
				nonNullTypes = append(nonNullTypes, t.String())
			}
		}

		parentPath := trimSuffix(p, ".type")
		primaryType := "string" // safe default
		if len(nonNullTypes) > 0 {
			primaryType = nonNullTypes[0]
		}

		jsonStr, _ = sjson.Set(jsonStr, p, primaryType)

		if hasNull {
			if preserveNullable {
				jsonStr, _ = sjson.Set(jsonStr, joinPath(parentPath, "nullable"), true)
			}
			descPath := joinPath(parentPath, "description")
			currentDesc := gjson.Get(jsonStr, descPath).String()
			jsonStr, _ = sjson.Set(jsonStr, descPath, appendHint(currentDesc, "(nullable)"))
		}

		if len(nonNullTypes) > 1 {
			descPath := joinPath(parentPath, "description")
			currentDesc := gjson.Get(jsonStr, descPath).String()
			hint := fmt.Sprintf("(Accepts: %s)", strings.Join(nonNullTypes, " | "))
			jsonStr, _ = sjson.Set(jsonStr, descPath, appendHint(currentDesc, hint))
		}
	}

	return jsonStr
}

// removeUnsupportedKeywords strips keywords not supported by Antigravity/Gemini APIs.
func removeUnsupportedKeywords(jsonStr string, options jsonSchemaCleanOptions) string {
	unsupported := []string{
		"$schema", "$id", "$comment", "$defs", "definitions",
		"patternProperties", "propertyNames", "dependencies",
		"dependentRequired", "dependentSchemas",
		"unevaluatedProperties", "unevaluatedItems",
		"contains", "minContains", "maxContains",
		"uniqueItems", "prefixItems",
		"readOnly", "writeOnly", "examples",
	}
	if !options.preserveAdditionalPropertiesFalse {
		unsupported = append(unsupported, "additionalProperties")
	}

	for _, kw := range unsupported {
		paths := findPaths(jsonStr, kw)
		sortByDepth(paths)
		for _, p := range paths {
			jsonStr, _ = sjson.Delete(jsonStr, p)
		}
	}

	// Remove x-* custom extensions
	jsonStr = removeCustomExtensions(jsonStr)

	return jsonStr
}

// removeCustomExtensions removes properties starting with "x-".
func removeCustomExtensions(jsonStr string) string {
	var walk func(path string, val gjson.Result)
	var pathsToDelete []string

	walk = func(path string, val gjson.Result) {
		if val.IsObject() {
			val.ForEach(func(key, child gjson.Result) bool {
				k := key.String()
				currentPath := joinPath(path, escapeGJSONPathKey(k))
				if strings.HasPrefix(k, "x-") {
					pathsToDelete = append(pathsToDelete, currentPath)
				} else {
					walk(currentPath, child)
				}
				return true
			})
		} else if val.IsArray() {
			val.ForEach(func(idx, child gjson.Result) bool {
				currentPath := joinPath(path, idx.String())
				walk(currentPath, child)
				return true
			})
		}
	}

	root := gjson.Parse(jsonStr)
	walk("", root)

	sortByDepth(pathsToDelete)
	for _, p := range pathsToDelete {
		jsonStr, _ = sjson.Delete(jsonStr, p)
	}

	return jsonStr
}

// cleanupRequiredFields removes required entries that don't exist in properties.
func cleanupRequiredFields(jsonStr string) string {
	paths := findPaths(jsonStr, "required")
	sortByDepth(paths)

	for _, p := range paths {
		req := gjson.Get(jsonStr, p)
		if !req.IsArray() {
			continue
		}

		parentPath := trimSuffix(p, ".required")
		propsPath := joinPath(parentPath, "properties")
		props := gjson.Get(jsonStr, propsPath)

		if !props.Exists() || !props.IsObject() {
			// No properties, remove required entirely
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

		// If properties is missing or empty, add placeholder
		if !propsVal.Exists() || len(propsVal.Map()) == 0 {
			placeholderProp := `{"reason":{"type":"string","description":"Brief explanation of why you are calling this tool"}}`
			jsonStr, _ = sjson.SetRaw(jsonStr, propsPath, placeholderProp)
			if !reqVal.Exists() || len(reqVal.Array()) == 0 {
				jsonStr, _ = sjson.Set(jsonStr, reqPath, []string{"reason"})
			}
		}
	}

	return jsonStr
}

// Helper functions

// findPaths finds all dot-separated GJSON paths in jsonStr matching targetKey.
func findPaths(jsonStr string, targetKey string) []string {
	var result []string
	root := gjson.Parse(jsonStr)

	// Direct check for root target
	if targetKey == "type" && root.Get("type").Exists() {
		result = append(result, "type")
	}

	findPathsRecursive(root, "", targetKey, &result)
	return result
}

func findPathsRecursive(node gjson.Result, currentPath string, targetKey string, result *[]string) {
	if !node.IsObject() && !node.IsArray() {
		return
	}

	if node.IsObject() {
		node.ForEach(func(key, val gjson.Result) bool {
			k := key.String()
			// Don't recurse into properties map when searching for keywords
			if k == "properties" && targetKey != "properties" && targetKey != "properties.reason" {
				// But do search inside each property definition
				val.ForEach(func(propName, propVal gjson.Result) bool {
					escapedPropName := escapeGJSONPathKey(propName.String())
					propPath := joinPath(currentPath, "properties."+escapedPropName)
					if targetKey == "type" && propVal.Get("type").Exists() {
						*result = append(*result, joinPath(propPath, "type"))
					}
					findPathsRecursive(propVal, propPath, targetKey, result)
					return true
				})
				return true
			}

			path := joinPath(currentPath, escapeGJSONPathKey(k))
			if k == targetKey {
				*result = append(*result, path)
			}
			findPathsRecursive(val, path, targetKey, result)
			return true
		})
	} else if node.IsArray() {
		node.ForEach(func(idx, val gjson.Result) bool {
			path := joinPath(currentPath, idx.String())
			findPathsRecursive(val, path, targetKey, result)
			return true
		})
	}
}

// escapeGJSONPathKey escapes dots and colons in object keys for GJSON pathing.
func escapeGJSONPathKey(key string) string {
	// GJSON uses \. for literal dots in keys
	key = strings.ReplaceAll(key, "\\", "\\\\")
	key = strings.ReplaceAll(key, ".", "\\.")
	return key
}

// splitGJSONPath splits a GJSON path taking escaped dots into account.
func splitGJSONPath(path string) []string {
	if path == "" {
		return nil
	}
	var parts []string
	var current strings.Builder
	escaped := false

	for i := 0; i < len(path); i++ {
		ch := path[i]
		if escaped {
			current.WriteByte(ch)
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch == '.' {
			parts = append(parts, current.String())
			current.Reset()
			continue
		}
		current.WriteByte(ch)
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
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
	if suffix == "" {
		return base
	}
	return base + "." + suffix
}

func appendHint(currentDesc, hint string) string {
	if currentDesc == "" {
		return hint
	}
	if strings.Contains(currentDesc, hint) {
		return currentDesc
	}
	return currentDesc + " " + hint
}

func contains(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}

// gjsonPathFromJSONPointer converts #/$defs/MyType to $defs.MyType
func gjsonPathFromJSONPointer(pointer string) string {
	pointer = strings.TrimPrefix(pointer, "#/")
	parts := strings.Split(pointer, "/")
	return strings.Join(parts, ".")
}

// Regular expressions for schema parsing
var (
	enumRegex = regexp.MustCompile(`"enum"\s*:\s*\[([^\]]*)\]`)
)
