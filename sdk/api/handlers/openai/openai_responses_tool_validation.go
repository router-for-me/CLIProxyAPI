package openai

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

func validateOpenAIResponsesToolsForChatTranslation(requestRawJSON []byte) error {
	tools := gjson.GetBytes(requestRawJSON, "tools")
	if !tools.Exists() {
		return nil
	}
	if !tools.IsArray() {
		return fmt.Errorf("tools must be an array")
	}

	seenNames := map[string]struct{}{}
	var validationErr error
	tools.ForEach(func(_, tool gjson.Result) bool {
		toolType := strings.TrimSpace(tool.Get("type").String())
		switch toolType {
		case "", "function":
			name := responsesValidationToolName(tool)
			if name == "" {
				validationErr = fmt.Errorf("function tool name must not be empty")
				return false
			}
			if err := recordResponsesValidationToolName(seenNames, name); err != nil {
				validationErr = err
				return false
			}
		case "namespace":
			if err := validateResponsesNamespaceTool(seenNames, tool); err != nil {
				validationErr = err
				return false
			}
		default:
			validationErr = fmt.Errorf("unsupported tool type %q", toolType)
			return false
		}
		return true
	})
	return validationErr
}

func validateResponsesNamespaceTool(seenNames map[string]struct{}, tool gjson.Result) error {
	namespaceName := strings.TrimSpace(tool.Get("name").String())
	if namespaceName == "" {
		return fmt.Errorf("namespace tool name must not be empty")
	}
	if !strings.HasPrefix(namespaceName, "mcp__") && strings.Contains(namespaceName, "__") {
		return fmt.Errorf("namespace tool name must not contain __")
	}
	children := tool.Get("tools")
	if !children.Exists() || !children.IsArray() {
		return fmt.Errorf("namespace tool %q must contain a tools array", namespaceName)
	}

	childNames := map[string]struct{}{}
	var validationErr error
	children.ForEach(func(_, child gjson.Result) bool {
		childType := strings.TrimSpace(child.Get("type").String())
		if childType != "" && childType != "function" {
			validationErr = fmt.Errorf("namespace child tool type must be function")
			return false
		}
		childName := responsesValidationToolName(child)
		if childName == "" {
			validationErr = fmt.Errorf("namespace child tool name must not be empty")
			return false
		}
		if strings.HasPrefix(childName, namespaceName+"__") {
			validationErr = fmt.Errorf("namespace child tool name must not be pre-qualified")
			return false
		}
		if err := recordResponsesValidationToolName(childNames, childName); err != nil {
			validationErr = fmt.Errorf("duplicate namespace child tool name %q", childName)
			return false
		}
		qualifiedName := qualifyResponsesValidationNamespaceToolName(namespaceName, childName)
		if err := recordResponsesValidationToolName(seenNames, qualifiedName); err != nil {
			validationErr = err
			return false
		}
		return true
	})
	return validationErr
}

func responsesValidationToolName(tool gjson.Result) string {
	if name := strings.TrimSpace(tool.Get("name").String()); name != "" {
		return name
	}
	return strings.TrimSpace(tool.Get("function.name").String())
}

func qualifyResponsesValidationNamespaceToolName(namespaceName, childName string) string {
	childName = strings.TrimSpace(childName)
	if childName == "" || namespaceName == "" || strings.HasPrefix(childName, "mcp__") {
		return childName
	}
	if strings.HasPrefix(childName, namespaceName) {
		return childName
	}
	if strings.HasSuffix(namespaceName, "__") {
		return namespaceName + childName
	}
	return namespaceName + "__" + childName
}

func recordResponsesValidationToolName(seenNames map[string]struct{}, name string) error {
	if _, exists := seenNames[name]; exists {
		return fmt.Errorf("duplicate tool name %q", name)
	}
	seenNames[name] = struct{}{}
	return nil
}
