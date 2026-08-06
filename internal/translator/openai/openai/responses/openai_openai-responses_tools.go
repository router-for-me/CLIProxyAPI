package responses

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func convertResponsesToolToOpenAIChatTools(tool gjson.Result) [][]byte {
	toolType := strings.TrimSpace(tool.Get("type").String())
	switch toolType {
	case "", "function":
		if tJSON, ok := convertResponsesFunctionToolToOpenAIChat(tool, ""); ok {
			return [][]byte{tJSON}
		}
	case "namespace":
		return convertResponsesNamespaceToolToOpenAIChat(tool)
	case "custom":
		if tJSON, ok := convertResponsesCustomToolToOpenAIChat(tool, ""); ok {
			return [][]byte{tJSON}
		}
	default:
		return nil
	}
	return nil
}

// mergeResponsesRequestChatTools converts every tool declaration in a Responses
// request into Chat Completions form, merging the top-level "tools" field with
// Codex Desktop (Responses Lite) "additional_tools" input items.
//
// Codex clients may deliver the same tool through both channels, and namespace
// qualification can collapse distinct declarations onto one Chat Completions
// name, so entries are deduplicated by function name. The first occurrence
// wins, which keeps the top-level "tools" definition authoritative over the
// "additional_tools" copy. Chat Completions requires tool names to be unique;
// strict upstreams reject the whole request otherwise.
func mergeResponsesRequestChatTools(root gjson.Result) [][]byte {
	var merged [][]byte
	seenToolNames := make(map[string]struct{})
	appendChatTools := func(tools gjson.Result) {
		if !tools.Exists() || !tools.IsArray() {
			return
		}
		tools.ForEach(func(_, tool gjson.Result) bool {
			for _, chatTool := range convertResponsesToolToOpenAIChatTools(tool) {
				name := gjson.GetBytes(chatTool, "function.name").String()
				if name != "" {
					if _, duplicate := seenToolNames[name]; duplicate {
						continue
					}
					seenToolNames[name] = struct{}{}
				}
				merged = append(merged, chatTool)
			}
			return true
		})
	}
	appendChatTools(root.Get("tools"))
	if input := root.Get("input"); input.Exists() && input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			if item.Get("type").String() == "additional_tools" {
				appendChatTools(item.Get("tools"))
			}
			return true
		})
	}
	return merged
}

// convertResponsesCustomToolToOpenAIChat maps a Responses freeform ("custom")
// tool onto a Chat Completions function tool with a single freeform "input"
// string, mirroring the function-based shape Codex uses for apply_patch.
func convertResponsesCustomToolToOpenAIChat(tool gjson.Result, overrideName string) ([]byte, bool) {
	name := strings.TrimSpace(overrideName)
	if name == "" {
		name = responsesToolName(tool)
	}
	if name == "" {
		return nil, false
	}
	chatTool := []byte(`{"type":"function","function":{"name":"","description":"","parameters":{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}}}`)
	chatTool, _ = sjson.SetBytes(chatTool, "function.name", name)
	if description := responsesToolDescription(tool); description != "" {
		chatTool, _ = sjson.SetBytes(chatTool, "function.description", description)
	}
	return chatTool, true
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
		switch strings.TrimSpace(child.Get("type").String()) {
		case "", "function":
			if tJSON, ok := convertResponsesFunctionToolToOpenAIChat(child, qualifiedName); ok {
				out = append(out, tJSON)
			}
		case "custom":
			if tJSON, ok := convertResponsesCustomToolToOpenAIChat(child, qualifiedName); ok {
				out = append(out, tJSON)
			}
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

// responsesToolOutputText flattens a tool output value that may be a plain
// string or an array of content parts ({"type":"input_text","text":...}) into
// a single text payload for a Chat Completions tool message.
func responsesToolOutputText(output gjson.Result) string {
	if output.Type == gjson.String {
		return output.String()
	}
	if output.IsArray() {
		var b strings.Builder
		output.ForEach(func(_, part gjson.Result) bool {
			if part.Type == gjson.String {
				b.WriteString(part.String())
				return true
			}
			if text := part.Get("text"); text.Exists() {
				b.WriteString(text.String())
			}
			return true
		})
		return b.String()
	}
	if output.Exists() {
		return output.Raw
	}
	return ""
}

// responsesCustomToolNames collects the names of freeform ("custom") tools
// declared in the original Responses request, both in the top-level "tools"
// field and in Codex Desktop "additional_tools" input items. Namespace child
// names use the qualified Chat Completions form.
func responsesCustomToolNames(requestRawJSON []byte) map[string]struct{} {
	names := make(map[string]struct{})
	var collect func(gjson.Result, string)
	collect = func(tools gjson.Result, namespaceName string) {
		if !tools.Exists() || !tools.IsArray() {
			return
		}
		tools.ForEach(func(_, tool gjson.Result) bool {
			switch strings.TrimSpace(tool.Get("type").String()) {
			case "custom":
				name := responsesToolName(tool)
				if namespaceName != "" {
					name = qualifyResponsesNamespaceToolName(namespaceName, name)
				}
				if name != "" {
					names[name] = struct{}{}
				}
			case "namespace":
				collect(tool.Get("tools"), strings.TrimSpace(tool.Get("name").String()))
			}
			return true
		})
	}
	root := gjson.ParseBytes(requestRawJSON)
	collect(root.Get("tools"), "")
	if input := root.Get("input"); input.Exists() && input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			if item.Get("type").String() == "additional_tools" {
				collect(item.Get("tools"), "")
			}
			return true
		})
	}
	return names
}

func responsesSingleCustomToolName(requestRawJSON []byte) (string, bool) {
	customToolNames := responsesCustomToolNames(requestRawJSON)
	if len(customToolNames) != 1 {
		return "", false
	}

	// Count the tools actually emitted, which are deduplicated by name, so a
	// tool delivered through both "tools" and "additional_tools" still counts
	// once and freeform unwrapping stays enabled.
	toolCount := len(mergeResponsesRequestChatTools(gjson.ParseBytes(requestRawJSON)))
	for name := range customToolNames {
		return name, toolCount == 1
	}
	return "", false
}

// unwrapCustomToolInput extracts the freeform input from the {"input": "..."}
// function-call arguments produced for a converted custom tool; it falls back
// to the raw arguments when the wrapper is absent.
func unwrapCustomToolInput(arguments string) string {
	if v := gjson.Get(arguments, "input"); v.Exists() {
		if v.Type == gjson.String {
			return v.String()
		}
		return v.Raw
	}
	return arguments
}

func qualifyResponsesNamespaceToolName(namespaceName, childName string) string {
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

// resolveResponsesQualifiedToolIdentity maps an emitted Chat Completions
// function name back to the Responses declaration that produced it.
//
// Declarations are walked in the same order mergeResponsesRequestChatTools
// uses, and the first one producing the name wins, so reverse translation
// reports the identity of the declaration that actually survived the merge. A
// flat top-level tool named "editor__apply_patch" therefore stays flat even
// when a later namespace declares a child qualifying to the same name.
func resolveResponsesQualifiedToolIdentity(root gjson.Result, qualifiedName string) (name, namespace string, found bool) {
	scan := func(tools gjson.Result) {
		if found || !tools.Exists() || !tools.IsArray() {
			return
		}
		tools.ForEach(func(_, tool gjson.Result) bool {
			switch strings.TrimSpace(tool.Get("type").String()) {
			case "namespace":
				namespaceName := strings.TrimSpace(tool.Get("name").String())
				children := tool.Get("tools")
				if namespaceName == "" || !children.Exists() || !children.IsArray() {
					return true
				}
				children.ForEach(func(_, child gjson.Result) bool {
					switch strings.TrimSpace(child.Get("type").String()) {
					case "", "function", "custom":
					default:
						return true
					}
					childName := responsesToolName(child)
					if childName == "" {
						return true
					}
					if qualifyResponsesNamespaceToolName(namespaceName, childName) == qualifiedName {
						name, namespace, found = childName, namespaceName, true
						return false
					}
					return true
				})
			case "", "function", "custom":
				if responsesToolName(tool) == qualifiedName {
					name, namespace, found = qualifiedName, "", true
					return false
				}
			}
			return !found
		})
	}

	scan(root.Get("tools"))
	if !found {
		if input := root.Get("input"); input.Exists() && input.IsArray() {
			input.ForEach(func(_, item gjson.Result) bool {
				if item.Get("type").String() == "additional_tools" {
					scan(item.Get("tools"))
				}
				return !found
			})
		}
	}
	return name, namespace, found
}

func splitResponsesQualifiedFunctionCallFromRequest(requestRawJSON []byte, qualifiedName string) (name, namespace string) {
	qualifiedName = strings.TrimSpace(qualifiedName)
	if qualifiedName == "" {
		return "", ""
	}

	if resolvedName, resolvedNamespace, ok := resolveResponsesQualifiedToolIdentity(gjson.ParseBytes(requestRawJSON), qualifiedName); ok {
		return resolvedName, resolvedNamespace
	}
	return qualifiedName, ""
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
