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
	changed := false
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(stringField(tool, "type")), NamespaceToolType) {
			if children, okChildren := tool["tools"].([]any); okChildren {
				if CompleteToolSearchSchemas(children) {
					changed = true
				}
			}
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(stringField(tool, "type")), "tool_search") || IsServerExecutedItem(tool) {
			continue
		}
		schema, ok := tool["parameters"].(map[string]any)
		if !ok {
			continue
		}
		if completeToolSearchSchema(schema) {
			changed = true
		}
	}
	return changed
}

func completeToolSearchSchema(schema map[string]any) bool {
	properties, ok := schema["properties"].(map[string]any)
	if !ok || len(properties) == 0 {
		return false
	}
	required := stringSet(schema["required"])
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
		property, isObjectSchema := propertyValue.(map[string]any)
		if isObjectSchema {
			if _, hasNestedProperties := property["properties"].(map[string]any); hasNestedProperties {
				if completeToolSearchSchema(property) {
					nestedChanged = true
				}
			}
		}
		if required[key] {
			continue
		}
		if !isObjectSchema {
			properties[key] = map[string]any{
				"anyOf": []any{propertyValue, map[string]any{"type": "null"}},
			}
			continue
		}
		if _, alreadyNullable := nullableSchemaEntry(property); !alreadyNullable {
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
	}
	if !completionNeeded && strictAdditionalPropertiesFalse(schema) && !nestedChanged {
		return false
	}
	ordered := make([]any, 0, len(keys))
	for _, key := range keys {
		ordered = append(ordered, key)
	}
	schema["required"] = ordered
	schema["additionalProperties"] = false
	return true
}

func nullableSchemaEntry(schema map[string]any) (any, bool) {
	entries, ok := schema["anyOf"].([]any)
	if !ok {
		return nil, false
	}
	for _, entry := range entries {
		if candidate, okCandidate := entry.(map[string]any); okCandidate && stringField(candidate, "type") == "null" {
			return entry, true
		}
	}
	return nil, false
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

func strictAdditionalPropertiesFalse(schema map[string]any) bool {
	value, exists := schema["additionalProperties"]
	if !exists {
		return false
	}
	flag, ok := value.(bool)
	return ok && !flag
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
