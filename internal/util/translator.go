// Package util provides utility functions for the CLI Proxy API server.
// It includes helper functions for JSON manipulation, proxy configuration,
// and other common operations used across the application.
package util

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Walk recursively traverses a JSON structure to find all occurrences of a specific field.
// It builds paths to each occurrence and adds them to the provided paths slice.
//
// Parameters:
//   - value: The gjson.Result object to traverse
//   - path: The current path in the JSON structure (empty string for root)
//   - field: The field name to search for
//   - paths: Pointer to a slice where found paths will be stored
//
// The function works recursively, building dot-notation paths to each occurrence
// of the specified field throughout the JSON structure.
func Walk(value gjson.Result, path, field string, paths *[]string) {
	switch value.Type {
	case gjson.JSON:
		// For JSON objects and arrays, iterate through each child
		value.ForEach(func(key, val gjson.Result) bool {
			var childPath string
			// Escape special characters for gjson/sjson path syntax
			// . -> \.
			// * -> \*
			// ? -> \?
			keyStr := key.String()
			safeKey := escapeGJSONPathKey(keyStr)

			if path == "" {
				childPath = safeKey
			} else {
				childPath = path + "." + safeKey
			}
			if keyStr == field {
				*paths = append(*paths, childPath)
			}
			Walk(val, childPath, field, paths)
			return true
		})
	case gjson.String, gjson.Number, gjson.True, gjson.False, gjson.Null:
		// Terminal types - no further traversal needed
	}
}

// RenameKey renames a key in a JSON string by moving its value to a new key path
// and then deleting the old key path.
//
// Parameters:
//   - jsonStr: The JSON string to modify
//   - oldKeyPath: The dot-notation path to the key that should be renamed
//   - newKeyPath: The dot-notation path where the value should be moved to
//
// Returns:
//   - string: The modified JSON string with the key renamed
//   - error: An error if the operation fails
//
// The function performs the rename in two steps:
// 1. Sets the value at the new key path
// 2. Deletes the old key path
func RenameKey(jsonStr, oldKeyPath, newKeyPath string) (string, error) {
	value := gjson.Get(jsonStr, oldKeyPath)

	if !value.Exists() {
		return "", fmt.Errorf("old key '%s' does not exist", oldKeyPath)
	}

	interimJSON, errSet := sjson.SetRawBytes([]byte(jsonStr), newKeyPath, []byte(value.Raw))
	if errSet != nil {
		return "", fmt.Errorf("failed to set new key '%s': %w", newKeyPath, errSet)
	}

	finalJSON, errDelete := sjson.DeleteBytes(interimJSON, oldKeyPath)
	if errDelete != nil {
		return "", fmt.Errorf("failed to delete old key '%s': %w", oldKeyPath, errDelete)
	}

	return string(finalJSON), nil
}

// FixJSON converts non-standard JSON that uses single quotes for strings into
// RFC 8259-compliant JSON by converting those single-quoted strings to
// double-quoted strings with proper escaping.
//
// Examples:
//
//	{'a': 1, 'b': '2'}      => {"a": 1, "b": "2"}
//	{"t": 'He said "hi"'} => {"t": "He said \"hi\""}
//
// Rules:
//   - Existing double-quoted JSON strings are preserved as-is.
//   - Single-quoted strings are converted to double-quoted strings.
//   - Inside converted strings, any double quote is escaped (\").
//   - Common backslash escapes (\n, \r, \t, \b, \f, \\) are preserved.
//   - \' inside single-quoted strings becomes a literal ' in the output (no
//     escaping needed inside double quotes).
//   - Unicode escapes (\uXXXX) inside single-quoted strings are forwarded.
//   - The function does not attempt to fix other non-JSON features beyond quotes.
func FixJSON(input string) string {
	var out bytes.Buffer

	inDouble := false
	inSingle := false
	escaped := false // applies within the current string state

	// Helper to write a rune, escaping double quotes when inside a converted
	// single-quoted string (which becomes a double-quoted string in output).
	writeConverted := func(r rune) {
		if r == '"' {
			out.WriteByte('\\')
			out.WriteByte('"')
			return
		}
		out.WriteRune(r)
	}

	runes := []rune(input)
	for i := 0; i < len(runes); i++ {
		r := runes[i]

		if inDouble {
			out.WriteRune(r)
			if escaped {
				// end of escape sequence in a standard JSON string
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == '"' {
				inDouble = false
			}
			continue
		}

		if inSingle {
			if escaped {
				// Handle common escape sequences after a backslash within a
				// single-quoted string
				escaped = false
				switch r {
				case 'n', 'r', 't', 'b', 'f', '/', '"':
					// Keep the backslash and the character (except for '"' which
					// rarely appears, but if it does, keep as \" to remain valid)
					out.WriteByte('\\')
					out.WriteRune(r)
				case '\\':
					out.WriteByte('\\')
					out.WriteByte('\\')
				case '\'':
					// \' inside single-quoted becomes a literal '
					out.WriteRune('\'')
				case 'u':
					// Forward \uXXXX if possible
					out.WriteByte('\\')
					out.WriteByte('u')
					// Copy up to next 4 hex digits if present
					for k := 0; k < 4 && i+1 < len(runes); k++ {
						peek := runes[i+1]
						// simple hex check
						if (peek >= '0' && peek <= '9') || (peek >= 'a' && peek <= 'f') || (peek >= 'A' && peek <= 'F') {
							out.WriteRune(peek)
							i++
						} else {
							break
						}
					}
				default:
					// Unknown escape: preserve the backslash and the char
					out.WriteByte('\\')
					out.WriteRune(r)
				}
				continue
			}

			if r == '\\' { // start escape sequence
				escaped = true
				continue
			}
			if r == '\'' { // end of single-quoted string
				out.WriteByte('"')
				inSingle = false
				continue
			}
			// regular char inside converted string; escape double quotes
			writeConverted(r)
			continue
		}

		// Outside any string
		if r == '"' {
			inDouble = true
			out.WriteRune(r)
			continue
		}
		if r == '\'' { // start of non-standard single-quoted string
			inSingle = true
			out.WriteByte('"')
			continue
		}
		out.WriteRune(r)
	}

	// If input ended while still inside a single-quoted string, close it to
	// produce the best-effort valid JSON.
	if inSingle {
		out.WriteByte('"')
	}

	return out.String()
}

func CanonicalToolName(name string) string {
	canonical := strings.TrimSpace(name)
	canonical = strings.TrimLeft(canonical, "_")
	return strings.ToLower(canonical)
}

// ToolNameMapFromClaudeRequest returns a canonical-name -> original-name map extracted from a Claude request.
// It is used to restore exact tool name casing for clients that require strict tool name matching (e.g. Claude Code).
func ToolNameMapFromClaudeRequest(rawJSON []byte) map[string]string {
	if len(rawJSON) == 0 || !gjson.ValidBytes(rawJSON) {
		return nil
	}

	tools := gjson.GetBytes(rawJSON, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return nil
	}

	toolResults := tools.Array()
	out := make(map[string]string, len(toolResults))
	tools.ForEach(func(_, tool gjson.Result) bool {
		name := strings.TrimSpace(tool.Get("name").String())
		if name == "" {
			name = strings.TrimSpace(tool.Get("function.name").String())
		}
		if name == "" {
			return true
		}
		key := CanonicalToolName(name)
		if key == "" {
			return true
		}
		if _, exists := out[key]; !exists {
			out[key] = name
		}
		return true
	})

	if len(out) == 0 {
		return nil
	}
	return out
}

func MapToolName(toolNameMap map[string]string, name string) string {
	if name == "" || toolNameMap == nil {
		return name
	}
	if mapped, ok := toolNameMap[CanonicalToolName(name)]; ok && mapped != "" {
		return mapped
	}
	return name
}

// MapClaudeToolName restores the client-facing tool name while preferring an exact
// declared name over canonical (case/leading-underscore-insensitive) restoration.
// This prevents distinct tools such as "Read" and "read" from being collapsed to
// the first declaration when the upstream already returned an exact name.
func MapClaudeToolName(toolNameMap map[string]string, originalRequestRawJSON []byte, name string) string {
	if name == "" {
		return name
	}
	if len(originalRequestRawJSON) > 0 && gjson.ValidBytes(originalRequestRawJSON) {
		tools := gjson.GetBytes(originalRequestRawJSON, "tools")
		if tools.IsArray() {
			canonical := CanonicalToolName(name)
			canonicalNames := map[string]bool{}
			for _, tool := range tools.Array() {
				declared := strings.TrimSpace(tool.Get("name").String())
				if declared == "" {
					declared = strings.TrimSpace(tool.Get("function.name").String())
				}
				if declared == name {
					return name
				}
				if declared != "" && CanonicalToolName(declared) == canonical {
					canonicalNames[declared] = true
				}
			}
			if len(canonicalNames) > 1 {
				return name
			}
		}
	}
	return MapToolName(toolNameMap, name)
}

// SanitizedFunctionNameMap builds an original-name → sanitized-name map from request tools.
// Exact duplicate names share a mapping. Distinct names that sanitize to the same value receive
// deterministic hash suffixes so every declaration remains addressable within the 64-byte limit.
func SanitizedFunctionNameMap(rawJSON []byte) map[string]string {
	names := functionNamesFromRequest(rawJSON)
	if len(names) == 0 {
		return nil
	}

	uniqueNames := make(map[string]struct{}, len(names))
	baseCounts := make(map[string]int, len(names))
	for _, name := range names {
		if name == "" {
			continue
		}
		if _, exists := uniqueNames[name]; exists {
			continue
		}
		uniqueNames[name] = struct{}{}
		baseCounts[SanitizeFunctionName(name)]++
	}

	sortedNames := make([]string, 0, len(uniqueNames))
	for name := range uniqueNames {
		sortedNames = append(sortedNames, name)
	}
	sort.Strings(sortedNames)

	out := make(map[string]string, len(sortedNames))
	used := make(map[string]string, len(sortedNames))
	for _, name := range sortedNames {
		base := SanitizeFunctionName(name)
		mapped := base
		_, baseUsed := used[base]
		if baseCounts[base] > 1 || baseUsed {
			mapped = disambiguateSanitizedFunctionName(base, name, used)
		}
		out[name] = mapped
		used[mapped] = name
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// MapSanitizedFunctionName returns the request-specific sanitized name when available.
func MapSanitizedFunctionName(nameMap map[string]string, name string) string {
	if mapped := nameMap[name]; mapped != "" {
		return mapped
	}
	return SanitizeFunctionName(name)
}

// DisambiguatedToolNameMap builds a sanitized-name → original-name map using the
// same collision-aware mapping as SanitizedFunctionNameMap.
func DisambiguatedToolNameMap(rawJSON []byte) map[string]string {
	forward := SanitizedFunctionNameMap(rawJSON)
	if len(forward) == 0 {
		return nil
	}

	out := make(map[string]string, len(forward))
	for original, sanitized := range forward {
		if sanitized != original {
			out[sanitized] = original
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// SanitizedToolNameMap builds the legacy sanitized-name → original-name map from
// top-level Claude-style tools. Collision-aware translators should use
// DisambiguatedToolNameMap instead.
func SanitizedToolNameMap(rawJSON []byte) map[string]string {
	if len(rawJSON) == 0 || !gjson.ValidBytes(rawJSON) {
		return nil
	}
	tools := gjson.GetBytes(rawJSON, "tools")
	if !tools.IsArray() {
		return nil
	}

	out := make(map[string]string)
	tools.ForEach(func(_, tool gjson.Result) bool {
		name := strings.TrimSpace(tool.Get("name").String())
		if name == "" {
			return true
		}
		sanitized := SanitizeFunctionName(name)
		if sanitized == name {
			return true
		}
		if existing, exists := out[sanitized]; !exists {
			out[sanitized] = name
		} else {
			log.Warnf("sanitized tool name collision: %q and %q both map to %q, keeping first", existing, name, sanitized)
		}
		return true
	})
	if len(out) == 0 {
		return nil
	}
	return out
}

func functionNamesFromRequest(rawJSON []byte) []string {
	if len(rawJSON) == 0 || !gjson.ValidBytes(rawJSON) {
		return nil
	}
	tools := gjson.GetBytes(rawJSON, "tools")
	if !tools.IsArray() {
		return nil
	}

	names := make([]string, 0, len(tools.Array()))
	var collectTool func(gjson.Result)
	collectDeclarations := func(declarations gjson.Result) {
		if !declarations.IsArray() {
			return
		}
		declarations.ForEach(func(_, declaration gjson.Result) bool {
			if name := declaration.Get("name").String(); name != "" {
				names = append(names, name)
			}
			return true
		})
	}
	collectTool = func(tool gjson.Result) {
		if nestedTools := tool.Get("tools"); nestedTools.IsArray() {
			nestedTools.ForEach(func(_, nestedTool gjson.Result) bool {
				collectTool(nestedTool)
				return true
			})
			return
		}
		hasDeclarations := false
		if declarations := tool.Get("functionDeclarations"); declarations.IsArray() {
			collectDeclarations(declarations)
			hasDeclarations = true
		}
		if declarations := tool.Get("function_declarations"); declarations.IsArray() {
			collectDeclarations(declarations)
			hasDeclarations = true
		}
		if hasDeclarations {
			return
		}
		if name := tool.Get("function.name").String(); name != "" {
			names = append(names, name)
			return
		}
		if name := tool.Get("name").String(); name != "" {
			names = append(names, name)
		}
	}
	tools.ForEach(func(_, tool gjson.Result) bool {
		collectTool(tool)
		return true
	})
	return names
}

func disambiguateSanitizedFunctionName(base, original string, used map[string]string) string {
	for attempt := 0; ; attempt++ {
		digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", original, attempt)))
		suffix := "_" + hex.EncodeToString(digest[:6])
		prefix := base
		if maxPrefix := 64 - len(suffix); len(prefix) > maxPrefix {
			prefix = prefix[:maxPrefix]
		}
		candidate := prefix + suffix
		if _, exists := used[candidate]; !exists {
			return candidate
		}
	}
}

// DeduplicateFunctionDeclarations removes duplicate named declarations while preserving order.
func DeduplicateFunctionDeclarations(raw []byte) []byte {
	result := gjson.ParseBytes(raw)
	if !result.IsArray() {
		return raw
	}

	seen := make(map[string]struct{}, len(result.Array()))
	parts := make([]string, 0, len(result.Array()))
	for _, declaration := range result.Array() {
		name := declaration.Get("name").String()
		if name != "" {
			if _, exists := seen[name]; exists {
				continue
			}
			seen[name] = struct{}{}
		}
		parts = append(parts, declaration.Raw)
	}
	return []byte("[" + strings.Join(parts, ",") + "]")
}

// RestoreSanitizedToolName looks up a sanitized function name in the provided map
// and returns the original client-facing name. If no mapping exists, it returns
// the sanitized name unchanged.
func RestoreSanitizedToolName(toolNameMap map[string]string, sanitizedName string) string {
	if sanitizedName == "" || toolNameMap == nil {
		return sanitizedName
	}
	if original, ok := toolNameMap[sanitizedName]; ok {
		return original
	}
	return sanitizedName
}

type claudeObjectSchemaInfo struct {
	properties           map[string][]gjson.Result
	required             map[string]bool
	hasProperties        bool
	allowsDynamic        bool
	forbidsDynamic       bool
	hasPatternProperties bool
	complex              bool
	unsafeComplex        bool
	declaresObject       bool
	branchInfos          []claudeObjectSchemaInfo
}

func claudeSchemaAllowsDynamic(schema gjson.Result) bool {
	// patternProperties permit matching keys even when additionalProperties is false
	// (the latter only restricts keys matching no pattern), so pattern permission is
	// never cancelled by an additionalProperties: false sibling.
	if schema.Get("patternProperties").IsObject() && len(schema.Get("patternProperties").Map()) > 0 {
		return true
	}
	additionalProperties := schema.Get("additionalProperties")
	if additionalProperties.Exists() && additionalProperties.Type != gjson.False {
		return true
	}
	return false
}

func claudeSchemaHasPatternProperties(schema gjson.Result) bool {
	return schema.Get("patternProperties").IsObject() && len(schema.Get("patternProperties").Map()) > 0
}

func claudeSchemaForbidsDynamic(schema gjson.Result) bool {
	additionalProperties := schema.Get("additionalProperties")
	return additionalProperties.Exists() && additionalProperties.Type == gjson.False
}

func claudeResolveLocalRef(root gjson.Result, ref string) (gjson.Result, bool) {
	if !strings.HasPrefix(ref, "#/") {
		return gjson.Result{}, false
	}
	current := root
	for _, segment := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		segment = strings.ReplaceAll(strings.ReplaceAll(segment, "~1", "/"), "~0", "~")
		next, ok := current.Map()[segment]
		if !ok {
			if current.IsArray() {
				index, errAtoi := strconv.Atoi(segment)
				if errAtoi != nil || index < 0 || index >= len(current.Array()) {
					return gjson.Result{}, false
				}
				next = current.Array()[index]
			} else {
				return gjson.Result{}, false
			}
		}
		current = next
	}
	return current, true
}

func analyzeClaudeObjectSchema(schema gjson.Result, root gjson.Result) claudeObjectSchemaInfo {
	return analyzeClaudeObjectSchemaDepth(schema, root, 0, map[string]bool{})
}

func claudeSchemaDeclaresObject(schema gjson.Result) bool {
	typeValue := schema.Get("type")
	if typeValue.String() == "object" {
		return true
	}
	if typeValue.IsArray() {
		for _, item := range typeValue.Array() {
			if item.String() == "object" {
				return true
			}
		}
	}
	return false
}

func claudeUnionBranches(schema gjson.Result) ([]gjson.Result, bool) {
	var branches []gjson.Result
	seen := map[string]bool{}
	for _, keyword := range []string{"anyOf", "oneOf"} {
		union := schema.Get(keyword)
		if !union.Exists() {
			continue
		}
		if !union.IsArray() {
			return nil, false
		}
		for _, branch := range union.Array() {
			branchType := branch.Get("type")
			if branchType.String() == "null" {
				continue
			}
			if branchType.IsArray() && len(branchType.Array()) == 1 && branchType.Array()[0].String() == "null" {
				continue
			}
			if seen[branch.Raw] {
				continue
			}
			seen[branch.Raw] = true
			branches = append(branches, branch)
		}
	}
	return branches, true
}

var claudeAnalyzedSchemaKeywords = map[string]bool{
	"type": true, "description": true, "title": true, "properties": true,
	"required": true, "items": true, "additionalProperties": true,
	"patternProperties": true, "anyOf": true, "oneOf": true, "allOf": true,
	"$ref": true, "$defs": true, "definitions": true, "nullable": true,
	"minLength": true, "maxLength": true, "minimum": true, "maximum": true,
	"exclusiveMinimum": true, "exclusiveMaximum": true, "multipleOf": true,
	"pattern": true, "format": true, "default": true, "examples": true,
	"minItems": true, "maxItems": true, "maxProperties": true,
	"$schema": true, "$id": true, "id": true,
	"$comment": true, "deprecated": true, "readOnly": true, "writeOnly": true,
	"example": true, "contentEncoding": true, "contentMediaType": true,
}

func claudeSchemaHasUnanalyzedKeyword(schema gjson.Result) bool {
	for key := range schema.Map() {
		if strings.HasPrefix(key, "x-") {
			continue
		}
		if !claudeAnalyzedSchemaKeywords[key] {
			return true
		}
	}
	return false
}

func analyzeClaudeObjectSchemaDepth(schema gjson.Result, root gjson.Result, depth int, visitedRefs map[string]bool) claudeObjectSchemaInfo {
	info := claudeObjectSchemaInfo{properties: map[string][]gjson.Result{}, required: map[string]bool{}}
	if depth > 16 || !schema.Exists() || !schema.IsObject() {
		info.complex = true
		return info
	}
	if claudeSchemaDeclaresObject(schema) {
		info.declaresObject = true
	}
	if claudeSchemaHasUnanalyzedKeyword(schema) {
		// Any keyword outside the explicitly analyzed set (enum, const, contains,
		// conditionals, unevaluated*, $dynamicRef, vendor applicators, ...) may
		// constrain legal values in ways this analyzer does not model. Such schemas
		// are unsafe-complex (unlike multi-branch unions, whose branches are
		// collected): no destructive cleaning may run on them at all, even when a
		// union keyword is also present.
		info.complex = true
		info.unsafeComplex = true
		return info
	}
	if schema.Get("anyOf").Exists() || schema.Get("oneOf").Exists() {
		branches, okBranches := claudeUnionBranches(schema)
		if !okBranches || len(branches) == 0 {
			info.complex = true
			return info
		}
		if len(branches) > 1 {
			// Multi-branch unions stay complex so this level never normalizes arguments,
			// but collect every branch's declared properties so child recursion and
			// placeholder protection still see the full original declaration. The
			// per-branch infos are kept separately because union branches are
			// alternatives: their dynamic-property policies must not be combined as
			// simultaneous constraints.
			for _, branch := range branches {
				branchInfo := analyzeClaudeObjectSchemaDepth(branch, root, depth+1, visitedRefs)
				info.branchInfos = append(info.branchInfos, branchInfo)
				mergeClaudeObjectSchemaInfo(&info, branchInfo)
			}
			if claudeSchemaAllowsDynamic(schema) {
				info.allowsDynamic = true
			}
			if claudeSchemaForbidsDynamic(schema) {
				info.forbidsDynamic = true
			}
			if claudeSchemaHasPatternProperties(schema) {
				info.hasPatternProperties = true
			}
			for _, requiredName := range schema.Get("required").Array() {
				info.required[requiredName.String()] = true
			}
			props := schema.Get("properties")
			if props.IsObject() {
				info.hasProperties = true
				info.declaresObject = true
				for key, value := range props.Map() {
					info.properties[key] = appendUniqueSchema(info.properties[key], value)
				}
			}
			// Sibling $ref/allOf applicators still constrain this node alongside the
			// union; collect them too or their declared child properties would be
			// invisible to protection and child recursion.
			if ref := schema.Get("$ref"); ref.Exists() {
				refValue := ref.String()
				if !visitedRefs[refValue] {
					if target, ok := claudeResolveLocalRef(root, refValue); ok {
						visitedRefs[refValue] = true
						resolved := analyzeClaudeObjectSchemaDepth(target, root, depth+1, visitedRefs)
						delete(visitedRefs, refValue)
						mergeClaudeObjectSchemaInfo(&info, resolved)
					}
				}
			}
			if allOf := schema.Get("allOf"); allOf.IsArray() {
				for _, branch := range allOf.Array() {
					branchInfo := analyzeClaudeObjectSchemaDepth(branch, root, depth+1, visitedRefs)
					mergeClaudeObjectSchemaInfo(&info, branchInfo)
				}
			}
			info.complex = true
			return info
		}
		branchInfo := analyzeClaudeObjectSchemaDepth(branches[0], root, depth+1, visitedRefs)
		mergeClaudeObjectSchemaInfo(&info, branchInfo)
	}
	if ref := schema.Get("$ref"); ref.Exists() {
		refValue := ref.String()
		if visitedRefs[refValue] {
			info.complex = true
			return info
		}
		target, ok := claudeResolveLocalRef(root, refValue)
		if !ok {
			info.complex = true
			return info
		}
		visitedRefs[refValue] = true
		resolved := analyzeClaudeObjectSchemaDepth(target, root, depth+1, visitedRefs)
		delete(visitedRefs, refValue)
		mergeClaudeObjectSchemaInfo(&info, resolved)
	}
	if allOf := schema.Get("allOf"); allOf.IsArray() {
		for _, branch := range allOf.Array() {
			branchInfo := analyzeClaudeObjectSchemaDepth(branch, root, depth+1, visitedRefs)
			mergeClaudeObjectSchemaInfo(&info, branchInfo)
		}
	} else if allOf.Exists() {
		info.complex = true
	}
	if claudeSchemaAllowsDynamic(schema) {
		info.allowsDynamic = true
	}
	if claudeSchemaForbidsDynamic(schema) {
		info.forbidsDynamic = true
	}
	if claudeSchemaHasPatternProperties(schema) {
		info.hasPatternProperties = true
	}
	props := schema.Get("properties")
	if props.Exists() {
		if !props.IsObject() {
			info.complex = true
		} else {
			info.hasProperties = true
			info.declaresObject = true
			for key, value := range props.Map() {
				info.properties[key] = appendUniqueSchema(info.properties[key], value)
			}
		}
	}
	for _, requiredName := range schema.Get("required").Array() {
		info.required[requiredName.String()] = true
	}
	return info
}

func appendUniqueSchema(schemas []gjson.Result, value gjson.Result) []gjson.Result {
	for _, existing := range schemas {
		if existing.Raw == value.Raw {
			return schemas
		}
	}
	return append(schemas, value)
}

func mergeClaudeObjectSchemaInfo(dst *claudeObjectSchemaInfo, src claudeObjectSchemaInfo) {
	if dst.properties == nil {
		dst.properties = map[string][]gjson.Result{}
	}
	if dst.required == nil {
		dst.required = map[string]bool{}
	}
	if src.hasProperties {
		dst.hasProperties = true
		for key, values := range src.properties {
			for _, value := range values {
				dst.properties[key] = appendUniqueSchema(dst.properties[key], value)
			}
		}
	}
	for name := range src.required {
		dst.required[name] = true
	}
	dst.branchInfos = append(dst.branchInfos, src.branchInfos...)
	dst.allowsDynamic = dst.allowsDynamic || src.allowsDynamic
	dst.forbidsDynamic = dst.forbidsDynamic || src.forbidsDynamic
	dst.hasPatternProperties = dst.hasPatternProperties || src.hasPatternProperties
	dst.complex = dst.complex || src.complex
	dst.unsafeComplex = dst.unsafeComplex || src.unsafeComplex
	dst.declaresObject = dst.declaresObject || src.declaresObject
}

func findClaudeToolSchema(originalRequestRawJSON []byte, toolName string) (gjson.Result, gjson.Result, bool) {
	if toolName == "" || len(originalRequestRawJSON) == 0 || !gjson.ValidBytes(originalRequestRawJSON) {
		return gjson.Result{}, gjson.Result{}, false
	}
	root := gjson.ParseBytes(originalRequestRawJSON)
	tools := root.Get("tools")
	if !tools.IsArray() {
		return gjson.Result{}, root, false
	}
	type candidate struct {
		name   string
		schema gjson.Result
		found  bool
	}
	var exact []candidate
	canonicalToolName := CanonicalToolName(toolName)
	canonicalMatches := map[string][]candidate{}
	for _, tool := range tools.Array() {
		name := strings.TrimSpace(tool.Get("name").String())
		if name == "" {
			name = strings.TrimSpace(tool.Get("function.name").String())
		}
		if name == "" {
			continue
		}
		schema := tool.Get("input_schema")
		if !schema.Exists() {
			schema = tool.Get("parameters")
		}
		if !schema.Exists() {
			schema = tool.Get("function.parameters")
		}
		c := candidate{name: name, schema: schema, found: true}
		if name == toolName {
			exact = append(exact, c)
		}
		if CanonicalToolName(name) == canonicalToolName {
			canonicalMatches[name] = append(canonicalMatches[name], c)
		}
	}
	if len(exact) == 1 {
		return exact[0].schema, root, true
	}
	if len(exact) > 1 {
		firstRaw := exact[0].schema.Raw
		identical := true
		for _, other := range exact[1:] {
			if other.schema.Raw != firstRaw {
				identical = false
				break
			}
		}
		if identical {
			return exact[0].schema, root, true
		}
		return gjson.Result{}, root, false
	}
	if len(canonicalMatches) == 1 {
		for _, matches := range canonicalMatches {
			if len(matches) == 1 {
				return matches[0].schema, root, true
			}
			firstRaw := matches[0].schema.Raw
			identical := true
			for _, other := range matches[1:] {
				if other.schema.Raw != firstRaw {
					identical = false
					break
				}
			}
			if identical {
				return matches[0].schema, root, true
			}
		}
	}
	return gjson.Result{}, root, false
}

// ClaudeToolIsParameterless reports whether the tool was declared in the original
// Claude request with an empty or missing object schema. Such tools had a placeholder
// "reason" property injected for Gemini/Antigravity schema validation, so any returned
// arguments must be normalized to "{}" before they reach the Claude client. Schemas that
// allow dynamic additional or pattern properties are never parameterless.
func ClaudeToolIsParameterless(toolName string, originalRequestRawJSON []byte) bool {
	schema, root, found := findClaudeToolSchema(originalRequestRawJSON, toolName)
	if !found {
		return false
	}
	if !schema.Exists() {
		return true
	}
	_ = root
	info := analyzeClaudeObjectSchema(schema, schema)
	if info.complex || claudeInfoAnyBranchAllowsDynamic(info) || !info.declaresObject {
		return false
	}
	return len(info.required) == 0 && (!info.hasProperties || len(info.properties) == 0)
}

func resolveClaudeArrayItemsSchema(schema gjson.Result, root gjson.Result, depth int, visitedRefs map[string]bool) ([]gjson.Result, bool) {
	if depth > 16 || !schema.Exists() || !schema.IsObject() {
		return nil, false
	}
	var items []gjson.Result
	deterministic := true
	collect := func(candidate gjson.Result) {
		items = appendUniqueSchema(items, candidate)
	}
	if ref := schema.Get("$ref"); ref.Exists() {
		refValue := ref.String()
		if visitedRefs[refValue] {
			return nil, false
		}
		target, ok := claudeResolveLocalRef(root, refValue)
		if !ok {
			return nil, false
		}
		visitedRefs[refValue] = true
		targetItems, targetOK := resolveClaudeArrayItemsSchema(target, root, depth+1, visitedRefs)
		delete(visitedRefs, refValue)
		if !targetOK {
			deterministic = false
		} else {
			for _, item := range targetItems {
				collect(item)
			}
		}
	}
	if schema.Get("anyOf").Exists() || schema.Get("oneOf").Exists() {
		branches, okBranches := claudeUnionBranches(schema)
		if !okBranches || len(branches) != 1 {
			deterministic = false
		} else {
			branchItems, branchOK := resolveClaudeArrayItemsSchema(branches[0], root, depth+1, visitedRefs)
			if !branchOK {
				deterministic = false
			} else {
				for _, item := range branchItems {
					collect(item)
				}
			}
		}
	}
	if allOf := schema.Get("allOf"); allOf.IsArray() {
		for _, branch := range allOf.Array() {
			branchItems, branchOK := resolveClaudeArrayItemsSchema(branch, root, depth+1, visitedRefs)
			if !branchOK {
				deterministic = false
				continue
			}
			for _, item := range branchItems {
				collect(item)
			}
		}
	} else if allOf.Exists() {
		deterministic = false
	}
	if directItems := schema.Get("items"); directItems.Exists() {
		collect(directItems)
	}
	return items, deterministic
}

func claudeDeclaredPropertyPlaceholders(info claudeObjectSchemaInfo) (bool, bool) {
	_, reasonDeclared := info.properties["reason"]
	_, underscoreDeclared := info.properties["_"]
	return reasonDeclared, underscoreDeclared
}

func claudeArraySchemaUnsafe(schema gjson.Result, root gjson.Result) bool {
	return analyzeClaudeObjectSchema(schema, root).unsafeComplex
}

func cleanClaudeToolArray(argsJSON string, schemas []gjson.Result, root gjson.Result, path string) string {
	for _, schema := range schemas {
		if claudeArraySchemaUnsafe(schema, root) {
			// Value-constraining keywords on the array itself (e.g. enum of whole
			// arrays) are not analyzed; keep the array untouched.
			return argsJSON
		}
	}
	var itemSchemas []gjson.Result
	deterministic := true
	for _, schema := range schemas {
		resolvedItems, okItems := resolveClaudeArrayItemsSchema(schema, root, 0, map[string]bool{})
		if !okItems {
			deterministic = false
			continue
		}
		if len(resolvedItems) == 0 {
			// A candidate without an items schema (e.g. free-form "{}") accepts
			// arbitrary elements; it must stay in the candidate set so element
			// cleaning sees the free alternative instead of only strict siblings.
			itemSchemas = appendUniqueSchema(itemSchemas, schema)
			continue
		}
		for _, item := range resolvedItems {
			itemSchemas = appendUniqueSchema(itemSchemas, item)
		}
	}
	if !deterministic || len(itemSchemas) == 0 {
		return argsJSON
	}
	cleaned := argsJSON
	elementCount := len(gjson.Get(cleaned, path).Array())
	for index := 0; index < elementCount; index++ {
		elementPath := joinPath(path, strconv.Itoa(index))
		element := gjson.Get(cleaned, elementPath)
		if element.IsObject() {
			cleaned = cleanClaudeToolObject(cleaned, itemSchemas, root, elementPath, false)
		} else if element.IsArray() {
			cleaned = cleanClaudeToolArray(cleaned, itemSchemas, root, elementPath)
		}
	}
	return cleaned
}

func cleanClaudeToolObject(argsJSON string, schemas []gjson.Result, root gjson.Result, path string, topLevel bool) string {
	argsResult := gjson.Get(argsJSON, path)
	if path == "" {
		argsResult = gjson.Parse(argsJSON)
	}
	if !argsResult.IsObject() || len(schemas) == 0 {
		return argsJSON
	}
	merged := claudeObjectSchemaInfo{properties: map[string][]gjson.Result{}}
	for _, schema := range schemas {
		mergeClaudeObjectSchemaInfo(&merged, analyzeClaudeObjectSchema(schema, root))
	}
	if merged.complex || !merged.declaresObject {
		return argsJSON
	}
	isEmptySchema := len(merged.required) == 0 && (!merged.hasProperties || len(merged.properties) == 0)
	allSchemasDeclareObject := true
	for _, schema := range schemas {
		if !analyzeClaudeObjectSchema(schema, root).declaresObject {
			allSchemasDeclareObject = false
			break
		}
	}
	if isEmptySchema && allSchemasDeclareObject && !claudeInfoAnyBranchAllowsDynamic(merged) && !claudeOriginalsAnyBranchAllowsDynamic(schemas, root) && !claudeOriginalsAnyCandidateAcceptsImplicitly(schemas, root, argsResult) {
		if len(argsResult.Map()) == 0 {
			return argsJSON
		}
		if topLevel {
			return "{}"
		}
		updated, _ := sjson.SetRaw(argsJSON, path, "{}")
		return updated
	}
	keys := make([]string, 0, len(merged.properties))
	for key := range merged.properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	cleaned := argsJSON
	for _, key := range keys {
		propertySchemas := merged.properties[key]
		childPath := joinPath(path, escapeGJSONPathKey(key))
		child := gjson.Get(cleaned, childPath)
		if !child.Exists() {
			continue
		}
		currentForBranch := gjson.Parse(cleaned)
		if path != "" {
			currentForBranch = gjson.Get(cleaned, path)
		}
		if claudeParentBranchAcceptsChild(merged, key, currentForBranch) || claudeOriginalsMayPermitChild(schemas, root, key, currentForBranch) {
			continue
		}
		if child.IsObject() {
			cleaned = cleanClaudeToolObject(cleaned, propertySchemas, root, childPath, false)
		} else if child.IsArray() {
			cleaned = cleanClaudeToolArray(cleaned, propertySchemas, root, childPath)
		}
	}
	if claudeInfoAnyBranchAllowsDynamic(merged) {
		return cleaned
	}
	current := gjson.Parse(cleaned)
	if path != "" {
		current = gjson.Get(cleaned, path)
	}
	if !current.IsObject() {
		return cleaned
	}
	reasonDeclared, underscoreDeclared := claudeDeclaredPropertyPlaceholders(merged)
	for _, placeholder := range []string{"reason", "_"} {
		if placeholder == "reason" && reasonDeclared {
			continue
		}
		if placeholder == "_" && underscoreDeclared {
			continue
		}
		if merged.required[placeholder] {
			continue
		}
		if claudeInfoAnyBranchAllowsDynamic(merged) || claudeOriginalsAnyBranchAllowsDynamic(schemas, root) {
			continue
		}
		if claudeParentBranchAcceptsChild(merged, placeholder, current) {
			continue
		}
		if claudeOriginalsAnyCandidateAcceptsImplicitly(schemas, root, current) {
			continue
		}
		if _, ok := current.Map()[placeholder]; ok {
			placeholderPath := joinPath(path, placeholder)
			cleaned, _ = sjson.Delete(cleaned, placeholderPath)
		}
	}
	parsedCleaned := gjson.Parse(cleaned)
	if path != "" {
		parsedCleaned = gjson.Get(cleaned, path)
	}
	if parsedCleaned.IsObject() && len(parsedCleaned.Map()) == 0 && path == "" {
		return "{}"
	}
	return cleaned
}

func findTranslatedClaudeToolSchemas(translatedRequestRawJSON []byte, toolName string, originalRequestRawJSON []byte) []gjson.Result {
	if toolName == "" || len(translatedRequestRawJSON) == 0 || !gjson.ValidBytes(translatedRequestRawJSON) {
		return nil
	}
	root := gjson.ParseBytes(translatedRequestRawJSON)
	type namedSchema struct {
		name   string
		schema gjson.Result
	}
	var named []namedSchema
	collectNamed := func(tools gjson.Result) {
		if !tools.IsArray() {
			return
		}
		for _, toolGroup := range tools.Array() {
			decls := toolGroup.Get("functionDeclarations")
			if !decls.IsArray() {
				continue
			}
			for _, decl := range decls.Array() {
				name := strings.TrimSpace(decl.Get("name").String())
				if name == "" {
					continue
				}
				schema := decl.Get("parametersJsonSchema")
				if !schema.Exists() {
					schema = decl.Get("parameters")
				}
				if schema.Exists() && schema.IsObject() {
					named = append(named, namedSchema{name: name, schema: schema})
				}
			}
		}
	}
	collectNamed(root.Get("tools"))
	collectNamed(root.Get("request.tools"))
	if len(named) == 0 {
		return nil
	}
	canonicalToolName := CanonicalToolName(toolName)
	sanitizedToolName := SanitizeFunctionName(toolName)
	// Request translation renames colliding tool names via the request-wide
	// disambiguated map (hash suffixes); the response name is the restored
	// client name, so match the actually-sent name when it can be derived.
	sentName := ""
	if forward := SanitizedFunctionNameMap(originalRequestRawJSON); forward != nil {
		sentName = forward[toolName]
	}
	var exact []gjson.Result
	canonicalByName := map[string][]gjson.Result{}
	for _, entry := range named {
		if entry.name == toolName || (sentName != "" && entry.name == sentName) {
			exact = append(exact, entry.schema)
		}
		if CanonicalToolName(entry.name) == canonicalToolName || entry.name == sanitizedToolName || SanitizeFunctionName(entry.name) == sanitizedToolName {
			canonicalByName[entry.name] = append(canonicalByName[entry.name], entry.schema)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	if len(canonicalByName) == 1 {
		for _, schemas := range canonicalByName {
			return schemas
		}
	}
	return nil
}

func claudeOriginalDeclaresPropertyDeep(schema gjson.Result, root gjson.Result, property string, depth int, visitedRefs map[string]bool) bool {
	if depth > 16 || !schema.Exists() || !schema.IsObject() {
		return false
	}
	if ref := schema.Get("$ref"); ref.Exists() {
		refValue := ref.String()
		if !visitedRefs[refValue] {
			if target, ok := claudeResolveLocalRef(root, refValue); ok {
				visitedRefs[refValue] = true
				declared := claudeOriginalDeclaresPropertyDeep(target, root, property, depth+1, visitedRefs)
				delete(visitedRefs, refValue)
				if declared {
					return true
				}
			}
		}
	}
	if _, ok := schema.Get("properties").Map()[property]; ok {
		return true
	}
	for _, requiredName := range schema.Get("required").Array() {
		if requiredName.String() == property {
			return true
		}
	}
	for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
		for _, branch := range schema.Get(keyword).Array() {
			if claudeOriginalDeclaresPropertyDeep(branch, root, property, depth+1, visitedRefs) {
				return true
			}
		}
	}
	return false
}

func claudeInfoAnyBranchAllowsDynamic(info claudeObjectSchemaInfo) bool {
	if info.hasPatternProperties {
		return true
	}
	if len(info.branchInfos) == 0 {
		return info.allowsDynamic && !info.forbidsDynamic
	}
	for _, branch := range info.branchInfos {
		if claudeInfoAnyBranchAllowsDynamic(branch) {
			return true
		}
	}
	return info.allowsDynamic && !info.forbidsDynamic
}

func claudeBranchMayAccept(info claudeObjectSchemaInfo, key string, current gjson.Result) bool {
	// patternProperties permit matching keys regardless of additionalProperties.
	if info.hasPatternProperties {
		return true
	}
	// A branch that does not explicitly forbid additional properties accepts the
	// key by JSON Schema default. To avoid treating every unconstrained sibling
	// branch as permission (which would mask genuine placeholder cleaning), this
	// implicit permission only counts when the current object matches the branch
	// by carrying every property the branch requires.
	if !info.forbidsDynamic {
		matches := true
		for requiredName := range info.required {
			if _, ok := current.Map()[requiredName]; !ok {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	if info.allowsDynamic && !info.forbidsDynamic {
		return true
	}
	for _, branch := range info.branchInfos {
		_, branchDeclaresKey := branch.properties[key]
		if branchDeclaresKey {
			// This branch constrains the key with its own explicit schema,
			// which is already part of the collected candidates.
			continue
		}
		if claudeBranchMayAccept(branch, key, current) {
			return true
		}
	}
	return false
}

func claudeParentBranchAcceptsChild(originalInfo claudeObjectSchemaInfo, key string, current gjson.Result) bool {
	if len(originalInfo.branchInfos) == 0 {
		return false
	}
	for _, branch := range originalInfo.branchInfos {
		_, branchDeclaresKey := branch.properties[key]
		if branchDeclaresKey {
			continue
		}
		if claudeBranchMayAccept(branch, key, current) {
			return true
		}
	}
	return false
}

func claudeCandidateMayPermitChild(info claudeObjectSchemaInfo, key string, current gjson.Result) bool {
	if _, declaresKey := info.properties[key]; declaresKey {
		return false
	}
	if info.hasPatternProperties {
		return true
	}
	if info.forbidsDynamic {
		return false
	}
	for requiredName := range info.required {
		if _, ok := current.Map()[requiredName]; !ok {
			return false
		}
	}
	return true
}

func claudeOriginalsMayPermitChild(originals []gjson.Result, root gjson.Result, key string, current gjson.Result) bool {
	if len(originals) <= 1 {
		return false
	}
	for _, original := range originals {
		info := analyzeClaudeObjectSchema(original, root)
		if info.complex {
			continue
		}
		if claudeCandidateMayPermitChild(info, key, current) {
			return true
		}
	}
	return false
}

func claudeOriginalsAnyCandidateAcceptsImplicitly(originals []gjson.Result, root gjson.Result, current gjson.Result) bool {
	if len(originals) <= 1 {
		return false
	}
	for _, original := range originals {
		info := analyzeClaudeObjectSchema(original, root)
		if info.complex || info.forbidsDynamic {
			continue
		}
		matches := true
		for requiredName := range info.required {
			if _, ok := current.Map()[requiredName]; !ok {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
}

func claudeOriginalsAnyBranchAllowsDynamic(originals []gjson.Result, root gjson.Result) bool {
	for _, original := range originals {
		if info := analyzeClaudeObjectSchema(original, root); claudeInfoAnyBranchAllowsDynamic(info) {
			return true
		}
	}
	return false
}

func mergedClaudeOriginalInfo(originals []gjson.Result, root gjson.Result) claudeObjectSchemaInfo {
	merged := claudeObjectSchemaInfo{properties: map[string][]gjson.Result{}}
	for _, original := range originals {
		mergeClaudeObjectSchemaInfo(&merged, analyzeClaudeObjectSchema(original, root))
	}
	return merged
}

func cleanClaudeToolArgumentsByTranslatedSchema(argsJSON string, translatedSchemas []gjson.Result, originals []gjson.Result, root gjson.Result, path string, topLevel bool) string {
	current := gjson.Parse(argsJSON)
	if path != "" {
		current = gjson.Get(argsJSON, path)
	}
	if !current.IsObject() || len(translatedSchemas) == 0 || len(originals) == 0 {
		return argsJSON
	}
	for _, original := range originals {
		if !original.IsObject() {
			return argsJSON
		}
	}
	originalInfo := mergedClaudeOriginalInfo(originals, root)
	if originalInfo.unsafeComplex {
		return argsJSON
	}
	if originalInfo.complex {
		// Multi-branch unions in the original cannot be analyzed declaratively; fall through
		// and rely on the translated schema, which records the actually-sent branches.
		hasUnion := false
		for _, original := range originals {
			if original.Get("anyOf").IsArray() || original.Get("oneOf").IsArray() {
				hasUnion = true
			}
		}
		if !hasUnion {
			return argsJSON
		}
	}
	cleaned := argsJSON
	// Recurse into declared children using matching translated child schemas.
	if originalInfo.hasProperties {
		keys := make([]string, 0, len(originalInfo.properties))
		for key := range originalInfo.properties {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			childPath := joinPath(path, escapeGJSONPathKey(key))
			child := gjson.Get(cleaned, childPath)
			if !child.Exists() {
				continue
			}
			var translatedChildren []gjson.Result
			for _, translated := range translatedSchemas {
				translatedInfo := analyzeClaudeObjectSchema(translated, translated)
				for _, childSchema := range translatedInfo.properties[key] {
					translatedChildren = appendUniqueSchema(translatedChildren, childSchema)
				}
			}
			if len(translatedChildren) == 0 {
				continue
			}
			originalChildren := originalInfo.properties[key]
			currentForBranch := gjson.Parse(cleaned)
			if path != "" {
				currentForBranch = gjson.Get(cleaned, path)
			}
			if claudeParentBranchAcceptsChild(originalInfo, key, currentForBranch) || claudeOriginalsMayPermitChild(originals, root, key, currentForBranch) {
				// Another alternative branch/candidate may accept this key without an
				// explicit property schema (dynamic, pattern, or implicit-open matched
				// permission); the collected explicit candidates do not constrain it
				// then, so skip destructive recursion into this subtree.
				continue
			}
			if child.IsObject() && len(originalChildren) > 0 {
				cleaned = cleanClaudeToolArgumentsByTranslatedSchema(cleaned, translatedChildren, originalChildren, root, childPath, false)
			} else if child.IsArray() && len(originalChildren) > 0 {
				cleaned = cleanClaudeTranslatedArray(cleaned, translatedChildren, originalChildren, root, childPath)
			}
		}
	}
	currentAfterChildren := gjson.Parse(cleaned)
	if path != "" {
		currentAfterChildren = gjson.Get(cleaned, path)
	}
	if !currentAfterChildren.IsObject() {
		return cleaned
	}
	for _, placeholder := range []string{"reason", "_"} {
		if _, declared := originalInfo.properties[placeholder]; declared {
			continue
		}
		if originalInfo.required[placeholder] {
			continue
		}
		if claudeInfoAnyBranchAllowsDynamic(originalInfo) || claudeOriginalsAnyBranchAllowsDynamic(originals, root) {
			// Some alternative branch (or candidate schema) accepts dynamic keys, so
			// this value may be legitimate user data under that branch even though
			// another branch received an injected placeholder. Never delete it.
			continue
		}
		if claudeOriginalsAnyCandidateAcceptsImplicitly(originals, root, currentAfterChildren) {
			// Alternative candidate child schemas extracted from different union
			// branches: one candidate that the current object matches accepts the
			// key implicitly, so the value may be legitimate under that candidate.
			continue
		}
		if claudeParentBranchAcceptsChild(originalInfo, placeholder, currentAfterChildren) {
			// An alternative branch that the current object matches (it carries all
			// of the branch's required keys) accepts this key by implicit-open or
			// pattern permission, so the value may be legitimate under that branch.
			continue
		}
		declaredDeep := false
		for _, original := range originals {
			if claudeOriginalDeclaresPropertyDeep(original, root, placeholder, 0, map[string]bool{}) {
				declaredDeep = true
				break
			}
		}
		if declaredDeep {
			continue
		}
		if _, ok := currentAfterChildren.Map()[placeholder]; !ok {
			continue
		}
		injected := false
		for _, translated := range translatedSchemas {
			translatedInfo := analyzeClaudeObjectSchema(translated, translated)
			placeholderSchemas, declaredTranslated := translatedInfo.properties[placeholder]
			if !declaredTranslated {
				continue
			}
			if placeholder == "reason" {
				for _, placeholderSchema := range placeholderSchemas {
					required := false
					for _, req := range translated.Get("required").Array() {
						if req.String() == "reason" {
							required = true
						}
					}
					if required || placeholderSchema.Get("description").String() == placeholderReasonDescription {
						injected = true
					}
				}
				// Also check required on union branches flattened into the analyzed info.
				if !injected {
					for _, req := range translated.Get("required").Array() {
						if req.String() == "reason" {
							injected = true
						}
					}
				}
			} else {
				for _, req := range translated.Get("required").Array() {
					if req.String() == "_" {
						injected = true
					}
				}
				// The "_" placeholder is also marked required by addEmptySchemaPlaceholder;
				// when branch flattening drops required, its boolean type is the fallback signal
				// only for nested objects. At the top level, the Antigravity executor wraps tool
				// schemas before its second sanitization pass, which can also inject "_" after
				// translation; that post-translation injection is handled below by re-running
				// the same schema cleaning on the translated schema.
				if !injected && !topLevel {
					for _, placeholderSchema := range placeholderSchemas {
						if placeholderSchema.Get("type").String() == "boolean" {
							injected = true
						}
					}
				}
			}
		}
		// Union branches: analyzeClaudeObjectSchema only resolves single-branch unions,
		// so also scan raw union branches. A required placeholder alone does not prove
		// injection (the original may legitimately require it in a branch, which the
		// deep-declaration check above already excluded); here the original declares the
		// field nowhere, so a required occurrence in a sent branch is the injected one.
		if !injected {
			for _, translated := range translatedSchemas {
				for _, keyword := range []string{"anyOf", "oneOf"} {
					for _, branch := range translated.Get(keyword).Array() {
						branchInfo := analyzeClaudeObjectSchema(branch, branch)
						if _, declaredBranch := branchInfo.properties[placeholder]; !declaredBranch {
							continue
						}
						for _, req := range branch.Get("required").Array() {
							if req.String() == placeholder {
								injected = true
							}
						}
					}
				}
			}
		}
		// The Antigravity executor re-sanitizes tool schemas after translation (wrapping
		// them so top-level objects become nested), which can inject placeholders that the
		// translated request seen here does not yet contain. Re-running the same cleaning
		// on the translated schema reproduces that final sent schema; if the placeholder
		// appears there as required, it was injected post-translation. This simulation is
		// only applied to closed objects (additionalProperties: false): open objects may
		// legitimately carry the key as user data on routes without executor injection,
		// and deleting based on a simulated injection would lose real arguments there.
		if !injected && topLevel && originalInfo.forbidsDynamic {
			for _, translated := range translatedSchemas {
				// Mirror the executor's cleanNestedSchema wrapping: the schema is nested
				// under a wrapper key before cleaning, so its top-level object counts as
				// nested and can receive the "_" placeholder.
				wrapped, errWrap := sjson.SetRaw("{}", "schema", translated.Raw)
				if errWrap != nil {
					continue
				}
				recleaned := gjson.Get(CleanJSONSchemaForAntigravity(wrapped), "schema")
				if !recleaned.Exists() {
					continue
				}
				recleanedInfo := analyzeClaudeObjectSchema(recleaned, recleaned)
				if _, declaredRecleaned := recleanedInfo.properties[placeholder]; !declaredRecleaned {
					continue
				}
				for _, req := range recleaned.Get("required").Array() {
					if req.String() == placeholder {
						injected = true
					}
				}
			}
		}
		if injected {
			cleaned, _ = sjson.Delete(cleaned, joinPath(path, placeholder))
		}
	}
	// Parameterless tools: if the original declared no properties and no candidate
	// schema (including alternative union branches that supplied this child) allows
	// dynamic data, any remaining keys can only be injected placeholders; normalize
	// to "{}". A free-form alternative candidate ("{}" or any candidate that does
	// not declare an object at all) accepts arbitrary data, so the whole-object
	// normalization must not run when one is present.
	allCandidatesDeclareObject := true
	for _, original := range originals {
		if !analyzeClaudeObjectSchema(original, root).declaresObject {
			allCandidatesDeclareObject = false
			break
		}
	}
	currentForNormalize := gjson.Parse(cleaned)
	if path != "" {
		currentForNormalize = gjson.Get(cleaned, path)
	}
	if !originalInfo.complex && allCandidatesDeclareObject && !claudeInfoAnyBranchAllowsDynamic(originalInfo) && !claudeOriginalsAnyBranchAllowsDynamic(originals, root) && !claudeOriginalsAnyCandidateAcceptsImplicitly(originals, root, currentForNormalize) && originalInfo.declaresObject && len(originalInfo.required) == 0 && (!originalInfo.hasProperties || len(originalInfo.properties) == 0) {
		if topLevel {
			return "{}"
		}
		updated, _ := sjson.SetRaw(cleaned, path, "{}")
		return updated
	}
	parsedCleaned := gjson.Parse(cleaned)
	if path != "" {
		parsedCleaned = gjson.Get(cleaned, path)
	}
	if parsedCleaned.IsObject() && len(parsedCleaned.Map()) == 0 && path == "" {
		return "{}"
	}
	_ = root
	return cleaned
}

func cleanClaudeTranslatedArray(argsJSON string, translatedArraySchemas []gjson.Result, originalArraySchemas []gjson.Result, root gjson.Result, path string) string {
	for _, schema := range originalArraySchemas {
		if claudeArraySchemaUnsafe(schema, root) {
			return argsJSON
		}
	}
	var translatedItems []gjson.Result
	var originalItems []gjson.Result
	deterministic := true
	for _, schema := range translatedArraySchemas {
		items, okItems := resolveClaudeArrayItemsSchema(schema, schema, 0, map[string]bool{})
		if !okItems {
			deterministic = false
		}
		for _, item := range items {
			translatedItems = appendUniqueSchema(translatedItems, item)
		}
	}
	for _, schema := range originalArraySchemas {
		items, okItems := resolveClaudeArrayItemsSchema(schema, root, 0, map[string]bool{})
		if !okItems {
			deterministic = false
		}
		if okItems && len(items) == 0 {
			originalItems = appendUniqueSchema(originalItems, schema)
			continue
		}
		for _, item := range items {
			originalItems = appendUniqueSchema(originalItems, item)
		}
	}
	if !deterministic || len(translatedItems) == 0 || len(originalItems) == 0 {
		return argsJSON
	}
	cleaned := argsJSON
	elementCount := len(gjson.Get(cleaned, path).Array())
	for index := 0; index < elementCount; index++ {
		elementPath := joinPath(path, strconv.Itoa(index))
		element := gjson.Get(cleaned, elementPath)
		if element.IsObject() {
			cleaned = cleanClaudeToolArgumentsByTranslatedSchema(cleaned, translatedItems, originalItems, root, elementPath, false)
		} else if element.IsArray() {
			cleaned = cleanClaudeTranslatedArray(cleaned, translatedItems, originalItems, root, elementPath)
		}
	}
	return cleaned
}

// CleanClaudeToolArgumentsWithTranslated strips placeholder arguments ("reason" or "_")
// only when the translated (actually-sent) tool schema shows that the placeholder was
// injected for that object (present in the translated schema with the placeholder
// description or marked required, while absent from the original declaration). When no
// translated schema is available it falls back to the original-schema inference used by
// CleanClaudeToolArguments. Parameterless tools are still normalized to "{}" because a
// strict Claude client rejects any argument for them.
func CleanClaudeToolArgumentsWithTranslated(rawArgs string, toolName string, originalRequestRawJSON []byte, translatedRequestRawJSON []byte) string {
	if translatedSchemas := findTranslatedClaudeToolSchemas(translatedRequestRawJSON, toolName, originalRequestRawJSON); len(translatedSchemas) > 0 {
		rawArgs = strings.TrimSpace(rawArgs)
		if rawArgs == "" || rawArgs == "{}" {
			return "{}"
		}
		if !gjson.Valid(rawArgs) || !gjson.Parse(rawArgs).IsObject() {
			return rawArgs
		}
		schema, _, found := findClaudeToolSchema(originalRequestRawJSON, toolName)
		if !found {
			return rawArgs
		}
		if !schema.Exists() {
			return "{}"
		}
		return cleanClaudeToolArgumentsByTranslatedSchema(rawArgs, translatedSchemas, []gjson.Result{schema}, schema, "", true)
	}
	return CleanClaudeToolArguments(rawArgs, toolName, originalRequestRawJSON)
}

// CleanClaudeToolArguments strips placeholder arguments ("reason" or "_") injected during
// schema normalization for empty object schemas when returning responses to Claude clients.
// If the tool was declared with empty or missing properties, the arguments are normalized
// to "{}". Placeholder fields are also stripped recursively from declared nested object
// properties when they were not declared there. Free-form nested schemas that do not declare
// an object type or properties are preserved because no placeholder is injected for them.
// Tools that legitimately declared those fields or use union schemas that cannot be resolved
// deterministically are left untouched. Deterministic allOf, single-branch unions, and local
// $ref schemas are resolved within the tool schema scope, matching request translation.
func CleanClaudeToolArguments(rawArgs string, toolName string, originalRequestRawJSON []byte) string {
	rawArgs = strings.TrimSpace(rawArgs)
	if rawArgs == "" || rawArgs == "{}" {
		return "{}"
	}
	if !gjson.Valid(rawArgs) {
		return rawArgs
	}
	schema, root, found := findClaudeToolSchema(originalRequestRawJSON, toolName)
	if !found {
		return rawArgs
	}
	if !schema.Exists() {
		return "{}"
	}
	argsResult := gjson.Parse(rawArgs)
	if !argsResult.IsObject() {
		return rawArgs
	}
	_ = root
	info := analyzeClaudeObjectSchema(schema, schema)
	if info.complex || !info.declaresObject {
		return rawArgs
	}
	if len(info.required) == 0 && (!info.hasProperties || len(info.properties) == 0) && !claudeInfoAnyBranchAllowsDynamic(info) {
		return "{}"
	}
	return cleanClaudeToolObject(rawArgs, []gjson.Result{schema}, schema, "", true)
}
