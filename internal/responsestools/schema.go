package responsestools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// SchemaBudget bounds one strict schema expansion.
type SchemaBudget struct {
	MaxBytes int
	MaxNodes int
	MaxDepth int
}

// BudgetFromLimits derives the schema budget from route limits.
func BudgetFromLimits(limits Limits) SchemaBudget {
	return SchemaBudget{
		MaxBytes: limits.MaxSchemaExpansionBytes,
		MaxNodes: limits.MaxSchemaExpansionNodes,
		MaxDepth: limits.MaxDepth,
	}
}

// CompleteToolSearchSchemas rewrites client-executed tool_search declarations
// so every declared property is also required. Properties that were optional
// widen to accept null, which keeps them optional for the model while
// satisfying strict-mode validation. Type and execution stay untouched so the
// client still receives a locally resolved tool_search_call. Other tool kinds
// are never modified.
func CompleteToolSearchSchemas(tools []any) bool {
	changed, _, _, _ := CompleteToolSearchSchemasWithSyntheticNulls(tools)
	return changed
}

// CompleteToolSearchSchemasWithSyntheticNulls completes client search schemas
// and returns paths whose nullability was added by this call.
func CompleteToolSearchSchemasWithSyntheticNulls(tools []any) (bool, map[string]struct{}, bool, error) {
	changed := false
	var synthetic map[string]struct{}
	seen := false
	var visitErr error
	var visit func([]any)
	visit = func(entries []any) {
		for _, rawTool := range entries {
			if visitErr != nil {
				return
			}
			tool, ok := rawTool.(map[string]any)
			if !ok {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(stringField(tool, "type")), NamespaceToolType) {
				if children, okChildren := tool["tools"].([]any); okChildren {
					visit(children)
				}
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(stringField(tool, "type")), "tool_search") || IsServerExecutedItem(tool) {
				continue
			}
			seen = true
			candidates := make(map[string]struct{})
			if schema, okSchema := tool["parameters"].(map[string]any); okSchema {
				schemaChanged, err := completeToolSearchSchemaAt(schema, nil, candidates)
				if err != nil {
					visitErr = err
					return
				}
				if schemaChanged {
					changed = true
				}
			}
			if synthetic == nil {
				synthetic = candidates
				continue
			}
			for path := range synthetic {
				if _, exists := candidates[path]; !exists {
					delete(synthetic, path)
				}
			}
		}
	}
	visit(tools)
	if synthetic == nil {
		synthetic = make(map[string]struct{})
	}
	return changed, synthetic, seen, visitErr
}

func completeToolSearchSchema(schema map[string]any) bool {
	changed, err := completeToolSearchSchemaAt(schema, nil, nil)
	return changed && err == nil
}

func completeToolSearchSchemaAt(schema map[string]any, path []string, synthetic map[string]struct{}) (bool, error) {
	properties, ok := schema["properties"].(map[string]any)
	if !ok || len(properties) == 0 {
		return false, nil
	}
	requiredItems := []any(nil)
	if rawRequired, exists := schema["required"]; exists {
		var ok bool
		requiredItems, ok = rawRequired.([]any)
		if !ok {
			return false, schemaError("invalid_required")
		}
	}
	required := make(map[string]bool, len(requiredItems))
	for _, item := range requiredItems {
		name, ok := item.(string)
		if !ok {
			return false, schemaError("invalid_required")
		}
		required[name] = true
	}
	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	completionNeeded := false
	for _, key := range keys {
		if !required[key] {
			completionNeeded = true
			break
		}
	}
	nestedChanged := false
	for _, key := range keys {
		propertyValue, okProperty := properties[key]
		if !okProperty {
			continue
		}
		childPath := append(append([]string(nil), path...), key)
		property, isObjectSchema := propertyValue.(map[string]any)
		if isObjectSchema {
			if _, hasNestedProperties := property["properties"].(map[string]any); hasNestedProperties {
				childChanged, err := completeToolSearchSchemaAt(property, childPath, synthetic)
				if err != nil {
					return false, err
				}
				if childChanged {
					nestedChanged = true
				}
			}
		}
		if required[key] {
			continue
		}
		switch nullability := schemaNullability(propertyValue); nullability {
		case nullAllowed:
			continue
		case nullUnknown:
			return false, schemaError("nullable_schema_unprovable")
		}
		if synthetic != nil {
			synthetic[jsonPointer(childPath)] = struct{}{}
		}
		if !isObjectSchema {
			properties[key] = map[string]any{
				"anyOf": []any{propertyValue, map[string]any{"type": "null"}},
			}
			continue
		}
		original := make(map[string]any, len(property)+1)
		for nestedKey, value := range property {
			original[nestedKey] = value
		}
		replacement := map[string]any{"anyOf": []any{original, map[string]any{"type": "null"}}}
		if description, exists := property["description"]; exists {
			replacement["description"] = description
		}
		properties[key] = replacement
	}
	if !completionNeeded && !nestedChanged {
		return false, nil
	}
	ordered := append([]any(nil), requiredItems...)
	for _, key := range keys {
		if !required[key] {
			ordered = append(ordered, key)
			required[key] = true
		}
	}
	schema["required"] = ordered
	return true, nil
}

func jsonPointer(path []string) string {
	var builder strings.Builder
	for _, segment := range path {
		segment = strings.ReplaceAll(segment, "~", "~0")
		segment = strings.ReplaceAll(segment, "/", "~1")
		builder.WriteByte('/')
		builder.WriteString(segment)
	}
	return builder.String()
}

type nullability uint8

const (
	nullUnknown nullability = iota
	nullDenied
	nullAllowed
)

func schemaNullability(value any) nullability {
	switch schema := value.(type) {
	case bool:
		if schema {
			return nullAllowed
		}
		return nullDenied
	case map[string]any:
		result := nullAllowed
		if schema["nullable"] == true {
			result = nullAllowed
		} else if typeValue, hasType := schema["type"]; hasType {
			switch types := typeValue.(type) {
			case string:
				if types == "null" {
					result = nullAllowed
				} else {
					result = nullDenied
				}
			case []any:
				matched := false
				for _, item := range types {
					if item == "null" {
						matched = true
						break
					}
				}
				if matched {
					result = nullAllowed
				} else {
					result = nullDenied
				}
			default:
				result = nullUnknown
			}
		}
		if _, exists := schema["$ref"]; exists {
			result = combineNullability(result, nullUnknown)
		}
		if constant, exists := schema["const"]; exists {
			constraint := nullDenied
			if constant == nil {
				constraint = nullAllowed
			}
			result = combineNullability(result, constraint)
		}
		if values, ok := schema["enum"].([]any); ok {
			constraint := nullDenied
			for _, candidate := range values {
				if candidate == nil {
					constraint = nullAllowed
					break
				}
			}
			result = combineNullability(result, constraint)
		}
		if entries, ok := schema["anyOf"].([]any); ok {
			constraint := unionNullability(entries)
			result = combineNullability(result, constraint)
		}
		if entries, ok := schema["oneOf"].([]any); ok {
			constraint := oneOfNullability(entries)
			result = combineNullability(result, constraint)
		}
		if entries, ok := schema["allOf"].([]any); ok {
			constraint := intersectionNullability(entries)
			result = combineNullability(result, constraint)
		}
		if not, exists := schema["not"]; exists {
			constraint := negateNullability(schemaNullability(not))
			result = combineNullability(result, constraint)
		}
		return result
	default:
		return nullUnknown
	}
}

func combineNullability(left, right nullability) nullability {
	if left == nullDenied || right == nullDenied {
		return nullDenied
	}
	if left == nullAllowed && right == nullAllowed {
		return nullAllowed
	}
	return nullUnknown
}

func unionNullability(entries []any) nullability {
	unknown := false
	for _, entry := range entries {
		switch schemaNullability(entry) {
		case nullAllowed:
			return nullAllowed
		case nullUnknown:
			unknown = true
		}
	}
	if unknown {
		return nullUnknown
	}
	return nullDenied
}

func oneOfNullability(entries []any) nullability {
	matches := 0
	for _, entry := range entries {
		switch schemaNullability(entry) {
		case nullAllowed:
			matches++
		case nullUnknown:
			return nullUnknown
		}
	}
	if matches == 1 {
		return nullAllowed
	}
	return nullDenied
}

func intersectionNullability(entries []any) nullability {
	unknown := false
	for _, entry := range entries {
		switch schemaNullability(entry) {
		case nullDenied:
			return nullDenied
		case nullUnknown:
			unknown = true
		}
	}
	if unknown {
		return nullUnknown
	}
	return nullAllowed
}

func negateNullability(value nullability) nullability {
	switch value {
	case nullAllowed:
		return nullDenied
	case nullDenied:
		return nullAllowed
	default:
		return nullUnknown
	}
}

func stringSet(value any) map[string]bool {
	out := map[string]bool{}
	items, ok := value.([]any)
	if !ok {
		return out
	}
	for _, item := range items {
		if name, okName := item.(string); okName {
			out[name] = true
		}
	}
	return out
}

var localRefPattern = regexp.MustCompile(`^#/(\$defs|definitions)/([^/]+)$`)

// InlineLocalRefs expands local schema references under a strict policy:
// sibling keys of $ref keep their conjunctive meaning (conflicts fall back to
// allOf, unrepresentable merges fail), and recursive, external, missing, or
// unsupported references are rejected instead of being silently deleted.
func InlineLocalRefs(tools []any, budget SchemaBudget) (bool, error) {
	changed := false
	for _, rawTool := range tools {
		toolChanged, err := inlineToolSchemaRef(rawTool, 1, budget)
		if err != nil {
			return changed, err
		}
		changed = changed || toolChanged
	}
	return changed, nil
}

func inlineToolSchemaRef(rawTool any, depth int, budget SchemaBudget) (bool, error) {
	tool, ok := rawTool.(map[string]any)
	if !ok {
		return false, nil
	}
	if depth > budget.MaxDepth {
		return false, schemaError("schema_depth_exceeded")
	}
	changed := false
	if function, okFunction := tool["function"].(map[string]any); okFunction {
		if parameters, exists := function["parameters"]; exists {
			resolved, didChange, err := inlineSchemaRefs(parameters, depth, budget)
			if err != nil {
				return false, err
			}
			if didChange {
				function["parameters"] = resolved
				changed = true
			}
		}
	} else if parameters, exists := tool["parameters"]; exists {
		resolved, didChange, err := inlineSchemaRefs(parameters, depth, budget)
		if err != nil {
			return false, err
		}
		if didChange {
			tool["parameters"] = resolved
			changed = true
		}
	}
	if toolType, _ := tool["type"].(string); strings.EqualFold(strings.TrimSpace(toolType), NamespaceToolType) {
		if children, okChildren := tool["tools"].([]any); okChildren {
			for _, child := range children {
				childChanged, err := inlineToolSchemaRef(child, depth+1, budget)
				if err != nil {
					return false, err
				}
				changed = changed || childChanged
			}
		}
	}
	return changed, nil
}

func inlineSchemaRefs(schema any, depth int, budget SchemaBudget) (any, bool, error) {
	root, ok := schema.(map[string]any)
	if !ok || !schemaContainsLocalRef(root) {
		return schema, false, nil
	}
	encodedRoot, err := json.Marshal(root)
	if err != nil {
		return schema, false, schemaError("schema_encoding_failed")
	}
	if len(encodedRoot) > budget.MaxBytes {
		return schema, false, schemaError("schema_bytes_exceeded")
	}
	state := refExpansionState{budget: budget}
	resolved, err := inlineLocalRefs(root, root, nil, depth, &state)
	if err != nil {
		return schema, false, err
	}
	encoded, err := json.Marshal(resolved)
	if err != nil {
		return schema, false, schemaError("schema_encoding_failed")
	}
	if len(encoded) > budget.MaxBytes {
		return schema, false, schemaError("schema_bytes_exceeded")
	}
	return resolved, true, nil
}

type refExpansionState struct {
	budget SchemaBudget
	nodes  int
	bytes  int
}

func (state *refExpansionState) consume() error {
	state.nodes++
	if state.nodes > state.budget.MaxNodes {
		return schemaError("schema_nodes_exceeded")
	}
	return nil
}

func schemaContainsLocalRef(node any) bool {
	switch typed := node.(type) {
	case map[string]any:
		if _, ok := typed["$ref"].(string); ok {
			return true
		}
		for _, value := range typed {
			if schemaContainsLocalRef(value) {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if schemaContainsLocalRef(item) {
				return true
			}
		}
	}
	return false
}

func inlineLocalRefs(node any, root map[string]any, stack []string, depth int, state *refExpansionState) (any, error) {
	if err := state.consume(); err != nil {
		return nil, err
	}
	if depth > state.budget.MaxDepth {
		return nil, schemaError("schema_depth_exceeded")
	}
	switch typed := node.(type) {
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			resolved, err := inlineLocalRefs(item, root, stack, depth+1, state)
			if err != nil {
				return nil, err
			}
			out = append(out, resolved)
		}
		return out, nil
	case map[string]any:
		if ref, ok := typed["$ref"].(string); ok {
			match := localRefPattern.FindStringSubmatch(ref)
			if match == nil {
				return nil, schemaError("unsupported_schema_reference")
			}
			if containsString(stack, ref) {
				return nil, schemaError("recursive_schema_reference")
			}
			target := localRefTarget(root, match[1], decodeJSONPointerToken(match[2]))
			if target == nil {
				return nil, schemaError("missing_schema_reference")
			}
			encodedTarget, err := json.Marshal(target)
			if err != nil {
				return nil, schemaError("schema_encoding_failed")
			}
			if len(encodedTarget) > state.budget.MaxBytes-state.bytes {
				return nil, schemaError("schema_bytes_exceeded")
			}
			state.bytes += len(encodedTarget)
			rest := copyWithoutKey(typed, "$ref")
			expanded, err := inlineLocalRefs(target, root, append(append([]string(nil), stack...), ref), depth+1, state)
			if err != nil {
				return nil, err
			}
			resolvedRest, err := inlineLocalRefs(rest, root, stack, depth+1, state)
			if err != nil {
				return nil, err
			}
			return mergeRefSiblings(expanded, resolvedRest)
		}
		out := make(map[string]any, len(typed))
		for key, value := range typed {
			if key == "$defs" || key == "definitions" {
				continue
			}
			resolved, err := inlineLocalRefs(value, root, stack, depth+1, state)
			if err != nil {
				return nil, err
			}
			out[key] = resolved
		}
		return out, nil
	default:
		return node, nil
	}
}

// mergeRefSiblings preserves the conjunctive meaning of keys sitting next to
// $ref: non-conflicting keys merge directly, while conflicting keys fall back
// to an allOf conjunction. Shapes that cannot be expressed fail instead of
// silently dropping either side.
func mergeRefSiblings(expanded, rest any) (any, error) {
	expandedMap, okExpanded := expanded.(map[string]any)
	restMap, okRest := rest.(map[string]any)
	if !okExpanded || !okRest {
		return nil, schemaError("invalid_schema_reference")
	}
	if len(restMap) == 0 {
		return expandedMap, nil
	}
	merged := make(map[string]any, len(expandedMap)+len(restMap))
	for key, value := range expandedMap {
		merged[key] = value
	}
	conflict := false
	for key, value := range restMap {
		if previous, exists := merged[key]; exists && !jsonEqual(previous, value) {
			conflict = true
			break
		}
		merged[key] = value
	}
	if !conflict {
		return merged, nil
	}
	// Conflicting siblings keep both sides through allOf, which the expanded
	// schema supports. Anything else would delete a constraint.
	return map[string]any{"allOf": []any{expandedMap, restMap}}, nil
}

func jsonEqual(left, right any) bool {
	leftBytes, errLeft := json.Marshal(left)
	rightBytes, errRight := json.Marshal(right)
	if errLeft != nil || errRight != nil {
		return false
	}
	return bytes.Equal(leftBytes, rightBytes)
}

func schemaError(code string) *ToolCompatibilityError {
	return unprocessableError(ReasonSchemaPolicy, fmt.Errorf("strict schema policy: %s", code))
}

// ValidateToolArrayDepth rejects namespace nesting deeper than the budget
// before any expansion or copy happens.
func ValidateToolArrayDepth(tools []any, depth int, maxDepth int) error {
	if depth > maxDepth {
		return schemaError("schema_depth_exceeded")
	}
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(stringField(tool, "type")), NamespaceToolType) {
			children, okChildren := tool["tools"].([]any)
			if !okChildren {
				continue
			}
			if err := ValidateToolArrayDepth(children, depth+1, maxDepth); err != nil {
				return err
			}
		}
	}
	return nil
}

func decodeJSONPointerToken(token string) string {
	token = strings.ReplaceAll(token, "~1", "/")
	return strings.ReplaceAll(token, "~0", "~")
}

func localRefTarget(root map[string]any, container, name string) map[string]any {
	definitions, ok := root[container].(map[string]any)
	if !ok {
		return nil
	}
	target, _ := definitions[name].(map[string]any)
	return target
}

func copyWithoutKey(source map[string]any, key string) map[string]any {
	out := make(map[string]any, len(source))
	for existingKey, value := range source {
		if existingKey == key {
			continue
		}
		out[existingKey] = value
	}
	return out
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
