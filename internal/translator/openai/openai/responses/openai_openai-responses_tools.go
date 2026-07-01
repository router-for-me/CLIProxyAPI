package responses

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func ValidateOpenAIResponsesToolsForChatTranslation(requestRawJSON []byte) error {
	return validateOpenAIResponsesTools(requestRawJSON, true)
}

func ValidateOpenAIResponsesNamespaceTools(requestRawJSON []byte) error {
	return validateOpenAIResponsesTools(requestRawJSON, false)
}

func validateOpenAIResponsesTools(requestRawJSON []byte, strict bool) error {
	if !gjson.ValidBytes(requestRawJSON) {
		return fmt.Errorf("invalid JSON payload")
	}

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
			name := responsesToolName(tool)
			if name == "" {
				validationErr = fmt.Errorf("function tool name must not be empty")
				return false
			}
			if err := recordResponsesToolName(seenNames, name); err != nil {
				validationErr = err
				return false
			}
		case "namespace":
			if err := validateResponsesNamespaceTool(seenNames, tool, strict); err != nil {
				validationErr = err
				return false
			}
		default:
			if strict {
				validationErr = fmt.Errorf("unsupported tool type %q", toolType)
				return false
			}
		}
		return true
	})
	return validationErr
}

func validateResponsesNamespaceTool(seenNames map[string]struct{}, tool gjson.Result, strict bool) error {
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
			if strict {
				validationErr = fmt.Errorf("namespace child tool type must be function")
				return false
			}
			return true
		}
		childName := responsesToolName(child)
		if childName == "" {
			validationErr = fmt.Errorf("namespace child tool name must not be empty")
			return false
		}
		if strings.HasPrefix(childName, namespaceName+"__") {
			validationErr = fmt.Errorf("namespace child tool name must not be pre-qualified")
			return false
		}
		if err := recordResponsesToolName(childNames, childName); err != nil {
			validationErr = fmt.Errorf("duplicate namespace child tool name %q", childName)
			return false
		}
		qualifiedName := qualifyResponsesNamespaceToolName(namespaceName, childName)
		if err := recordResponsesToolName(seenNames, qualifiedName); err != nil {
			validationErr = err
			return false
		}
		return true
	})
	return validationErr
}

func recordResponsesToolName(seenNames map[string]struct{}, name string) error {
	if _, exists := seenNames[name]; exists {
		return fmt.Errorf("duplicate tool name %q", name)
	}
	seenNames[name] = struct{}{}
	return nil
}

func convertResponsesToolToOpenAIChatTools(tool gjson.Result) [][]byte {
	toolType := strings.TrimSpace(tool.Get("type").String())
	switch toolType {
	case "", "function":
		if tJSON, ok := convertResponsesFunctionToolToOpenAIChat(tool, ""); ok {
			return [][]byte{tJSON}
		}
	case "namespace":
		return convertResponsesNamespaceToolToOpenAIChat(tool)
	default:
		return nil
	}
	return nil
}

func convertResponsesNamespaceToolToOpenAIChat(tool gjson.Result) [][]byte {
	namespaceName := strings.TrimSpace(tool.Get("name").String())
	children := tool.Get("tools")
	if !children.Exists() || !children.IsArray() {
		return nil
	}

	var out [][]byte
	children.ForEach(func(_, child gjson.Result) bool {
		childName := responsesToolName(child)
		qualifiedName := qualifyResponsesNamespaceToolName(namespaceName, childName)
		if tJSON, ok := convertResponsesFunctionToolToOpenAIChat(child, qualifiedName); ok {
			out = append(out, tJSON)
		}
		return true
	})
	return out
}

func convertResponsesFunctionToolToOpenAIChat(tool gjson.Result, overrideName string) ([]byte, bool) {
	name := strings.TrimSpace(overrideName)
	if name == "" {
		name = responsesToolName(tool)
	}
	if name == "" {
		return nil, false
	}

	chatTool := []byte(`{"type":"function","function":{"name":"","description":"","parameters":{}}}`)
	chatTool, _ = sjson.SetBytes(chatTool, "function.name", name)
	if description := responsesToolDescription(tool); description != "" {
		chatTool, _ = sjson.SetBytes(chatTool, "function.description", description)
	}
	if parameters := responsesToolParameters(tool); parameters.Exists() {
		chatTool, _ = sjson.SetRawBytes(chatTool, "function.parameters", []byte(parameters.Raw))
	}
	return chatTool, true
}

func responsesToolName(tool gjson.Result) string {
	if name := strings.TrimSpace(tool.Get("name").String()); name != "" {
		return name
	}
	return strings.TrimSpace(tool.Get("function.name").String())
}

func responsesToolDescription(tool gjson.Result) string {
	if description := tool.Get("description").String(); description != "" {
		return description
	}
	return tool.Get("function.description").String()
}

func responsesToolParameters(tool gjson.Result) gjson.Result {
	for _, path := range []string{
		"parameters",
		"parametersJsonSchema",
		"input_schema",
		"function.parameters",
		"function.parametersJsonSchema",
	} {
		if parameters := tool.Get(path); parameters.Exists() {
			return parameters
		}
	}
	return gjson.Result{}
}

func qualifyResponsesNamespaceToolName(namespaceName, childName string) string {
	childName = strings.TrimSpace(childName)
	if childName == "" || namespaceName == "" || strings.HasPrefix(childName, "mcp__") {
		return childName
	}
	if strings.HasPrefix(childName, namespaceName+"__") {
		return childName
	}
	if strings.HasSuffix(namespaceName, "__") {
		return namespaceName + childName
	}
	return namespaceName + "__" + childName
}

func splitResponsesQualifiedFunctionCallFromRequest(requestRawJSON []byte, qualifiedName string) (name, namespace string) {
	qualifiedName = strings.TrimSpace(qualifiedName)
	if qualifiedName == "" {
		return "", ""
	}

	tools := gjson.GetBytes(requestRawJSON, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return qualifiedName, ""
	}

	var bestNamespace string
	var bestChild string
	tools.ForEach(func(_, tool gjson.Result) bool {
		if strings.TrimSpace(tool.Get("type").String()) != "namespace" {
			return true
		}
		namespaceName := strings.TrimSpace(tool.Get("name").String())
		if namespaceName == "" {
			return true
		}
		children := tool.Get("tools")
		if !children.Exists() || !children.IsArray() {
			return true
		}
		children.ForEach(func(_, child gjson.Result) bool {
			childName := responsesToolName(child)
			if childName == "" {
				return true
			}
			if qualifyResponsesNamespaceToolName(namespaceName, childName) == qualifiedName {
				bestNamespace = namespaceName
				bestChild = childName
			}
			return true
		})
		return true
	})

	if bestNamespace == "" || bestChild == "" {
		return qualifiedName, ""
	}
	return bestChild, bestNamespace
}

func pickRequestJSON(originalRequestRawJSON, requestRawJSON []byte) []byte {
	if len(originalRequestRawJSON) > 0 && gjson.ValidBytes(originalRequestRawJSON) {
		return originalRequestRawJSON
	}
	if len(requestRawJSON) > 0 && gjson.ValidBytes(requestRawJSON) {
		return requestRawJSON
	}
	return nil
}

func applyResponsesFunctionCallNamespaceFields(item []byte, requestRawJSON []byte, qualifiedName string, itemPath string) []byte {
	name, namespace := splitResponsesQualifiedFunctionCallFromRequest(requestRawJSON, qualifiedName)
	namePath := "name"
	namespacePath := "namespace"
	if itemPath != "" {
		namePath = itemPath + ".name"
		namespacePath = itemPath + ".namespace"
	}
	item, _ = sjson.SetBytes(item, namePath, name)
	if namespace != "" {
		item, _ = sjson.SetBytes(item, namespacePath, namespace)
	} else {
		item, _ = sjson.DeleteBytes(item, namespacePath)
	}
	return item
}
