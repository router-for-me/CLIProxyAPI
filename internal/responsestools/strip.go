package responsestools

import (
	"encoding/json"
	"fmt"
	"strings"
)

// StripCustomDeclarations removes future custom declarations for lossy
// compatibility. History conversion is separate: call ConvertCustomHistory
// after stripping. A forced custom choice fails instead of silently changing
// which tool the model must call.
func StripCustomDeclarations(value any) (bool, error) {
	switch typed := value.(type) {
	case map[string]any:
		changed := false
		if choice, ok := typed["tool_choice"].(map[string]any); ok {
			if customChoiceForced(choice, value) {
				return false, unprocessableError(ReasonCustomForcedCall, fmt.Errorf("tool_choice forces a stripped custom tool"))
			}
		}
		if tools, ok := typed["tools"].([]any); ok {
			filtered, stripped := stripCustomToolArray(tools)
			if stripped {
				typed["tools"] = filtered
				changed = true
			}
		}
		if input, ok := typed["input"].([]any); ok {
			for _, rawItem := range input {
				item, okItem := rawItem.(map[string]any)
				if !okItem || !IsToolDeclarationInput(item) {
					continue
				}
				if tools, okTools := item["tools"].([]any); okTools {
					filtered, stripped := stripCustomToolArray(tools)
					if stripped {
						item["tools"] = filtered
						changed = true
					}
				}
			}
		}
		return changed, nil
	case []any:
		changed := false
		for _, child := range typed {
			if childChanged, err := StripCustomDeclarations(child); err != nil {
				return false, err
			} else if childChanged {
				changed = true
			}
		}
		return changed, nil
	default:
		return false, nil
	}
}

func stripCustomToolArray(tools []any) ([]any, bool) {
	out := make([]any, 0, len(tools))
	changed := false
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			out = append(out, rawTool)
			continue
		}
		if stringsEqualFold(stringField(tool, "type"), "custom") {
			changed = true
			continue
		}
		if children, okChildren := tool["tools"].([]any); okChildren {
			if filtered, childChanged := stripCustomToolArray(children); childChanged {
				tool["tools"] = filtered
				changed = true
			}
		}
		out = append(out, rawTool)
	}
	return out, changed
}

func customChoiceForced(choice map[string]any, root any) bool {
	customNames := make(map[string]struct{})
	collectCustomNames(root, "", customNames)
	if len(customNames) == 0 {
		return false
	}
	if stringsEqualFold(stringField(choice, "type"), "custom") {
		return true
	}
	if name := stringField(choice, "name"); name != "" {
		if _, exists := customNames[name]; exists {
			return true
		}
	}
	if forced, ok := choice["tools"].([]any); ok {
		for _, rawTool := range forced {
			tool, okTool := rawTool.(map[string]any)
			if !okTool {
				continue
			}
			if stringsEqualFold(stringField(tool, "type"), "custom") {
				return true
			}
			if _, exists := customNames[stringField(tool, "name")]; exists {
				return true
			}
		}
	}
	return false
}

func collectCustomNames(value any, namespace string, names map[string]struct{}) {
	switch typed := value.(type) {
	case map[string]any:
		if tools, ok := typed["tools"].([]any); ok {
			current := namespace
			if stringsEqualFold(stringField(typed, "type"), NamespaceToolType) {
				name := stringField(typed, "name")
				if current != "" && name != "" {
					current = JoinNamespace(current, name)
				} else if name != "" {
					current = name
				}
			}
			for _, rawTool := range tools {
				tool, okTool := rawTool.(map[string]any)
				if !okTool {
					continue
				}
				if stringsEqualFold(strings.TrimSpace(stringField(tool, "type")), NamespaceToolType) {
					collectCustomNames(tool, current, names)
					continue
				}
				if !stringsEqualFold(strings.TrimSpace(stringField(tool, "type")), "custom") {
					continue
				}
				name := stringField(tool, "name")
				if name == "" {
					continue
				}
				names[name] = struct{}{}
				names[RawQualifiedToolName(current, name)] = struct{}{}
			}
		}
		if input, ok := typed["input"].([]any); ok {
			for _, rawItem := range input {
				if item, okItem := rawItem.(map[string]any); okItem && IsToolDeclarationInput(item) {
					collectCustomNames(item, namespace, names)
				}
			}
		}
	case []any:
		for _, child := range typed {
			collectCustomNames(child, namespace, names)
		}
	}
}

// ConvertCustomHistory keeps prior client-executed custom tool calls in the
// conversation when their declarations are stripped for a strict upstream:
// custom_tool_call becomes a function_call carrying {"input": original}, and
// custom_tool_call_output becomes a function_call_output. Non-string inputs
// fail instead of inventing a payload.
func ConvertCustomHistory(value any) (bool, error) {
	switch typed := value.(type) {
	case map[string]any:
		changed := false
		if input, ok := typed["input"].([]any); ok {
			for _, rawItem := range input {
				item, okItem := rawItem.(map[string]any)
				if !okItem {
					continue
				}
				switch stringField(item, "type") {
				case "custom_tool_call":
					toolInput, okInput := item["input"].(string)
					if !okInput {
						return false, unprocessableError(ReasonInvalidCustomInput, fmt.Errorf("custom history input is not a string"))
					}
					arguments, err := json.Marshal(map[string]string{CustomFunctionParameter: toolInput})
					if err != nil {
						return false, unprocessableError(ReasonInvalidCustomInput, err)
					}
					if _, err := ReidentifyItem(item, "function_call"); err != nil {
						return false, unprocessableError(ReasonHistoryLink, err)
					}
					item["arguments"] = string(arguments)
					delete(item, "input")
					if namespace := stringField(item, "namespace"); namespace != "" {
						item["name"] = RawQualifiedToolName(namespace, stringField(item, "name"))
						delete(item, "namespace")
					}
					changed = true
				case "custom_tool_call_output":
					if _, err := ReidentifyItem(item, "function_call_output"); err != nil {
						return false, unprocessableError(ReasonHistoryLink, err)
					}
					changed = true
				}
			}
		}
		return changed, nil
	case []any:
		changed := false
		for _, child := range typed {
			if childChanged, err := ConvertCustomHistory(child); err != nil {
				return false, err
			} else if childChanged {
				changed = true
			}
		}
		return changed, nil
	default:
		return false, nil
	}
}

func stringsEqualFold(left, right string) bool {
	if len(left) != len(right) && left != "" && right != "" {
		return strings.EqualFold(left, right)
	}
	return strings.EqualFold(left, right)
}
