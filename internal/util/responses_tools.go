package util

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ResponsesToolIdentity represents the resolved identity of a tool in OpenAI Responses format.
type ResponsesToolIdentity struct {
	Name      string
	Namespace string
	Custom    bool
}

// ResponsesToolDescriptor is an internal representation of a tool declaration in a Responses request.
type ResponsesToolDescriptor struct {
	Name           string // Qualified name (e.g. "functions__exec" or "exec")
	LocalName      string // Local name without namespace (e.g. "exec")
	Namespace      string // Namespace if any (e.g. "functions")
	ToolType       string // "function", "custom", etc.
	Tool           gjson.Result
	SourcePriority int  // 0 for top-level tools, 1 for additional_tools
	Direct         bool // true if declared directly, false if declared as namespace child
	Order          int  // original discovery order
}

// QualifyResponsesNamespaceToolName qualifies a child tool name with its namespace.
func QualifyResponsesNamespaceToolName(namespaceName, childName string) string {
	childName = strings.TrimSpace(childName)
	namespaceName = strings.TrimSpace(namespaceName)
	if childName == "" || namespaceName == "" || strings.HasPrefix(childName, "mcp__") {
		return childName
	}
	if childName == namespaceName || strings.HasPrefix(childName, namespaceName+"__") {
		return childName
	}
	if strings.HasSuffix(namespaceName, "__") {
		return namespaceName + childName
	}
	return namespaceName + "__" + childName
}

func responsesToolSources(root gjson.Result) []struct {
	tools    gjson.Result
	priority int
} {
	var sources []struct {
		tools    gjson.Result
		priority int
	}
	appendSource := func(tools gjson.Result, priority int) {
		if tools.Exists() && tools.IsArray() {
			sources = append(sources, struct {
				tools    gjson.Result
				priority int
			}{tools: tools, priority: priority})
		}
	}
	appendSource(root.Get("tools"), 0)
	if input := root.Get("input"); input.Exists() && input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			if item.Get("type").String() == "additional_tools" {
				appendSource(item.Get("tools"), 1)
			}
			return true
		})
	}
	return sources
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

// CollectResponsesToolDescriptors extracts all tool descriptors from a Responses request root.
func CollectResponsesToolDescriptors(root gjson.Result) []ResponsesToolDescriptor {
	var descriptors []ResponsesToolDescriptor
	appendDescriptor := func(tool gjson.Result, name, localName, namespace string, toolType string, sourcePriority int, direct bool) {
		if name == "" {
			return
		}
		descriptors = append(descriptors, ResponsesToolDescriptor{
			Name:           name,
			LocalName:      localName,
			Namespace:      namespace,
			ToolType:       toolType,
			Tool:           tool,
			SourcePriority: sourcePriority,
			Direct:         direct,
			Order:          len(descriptors),
		})
	}
	appendNamespaceChildren := func(namespaceTool gjson.Result, sourcePriority int) {
		namespaceName := strings.TrimSpace(namespaceTool.Get("name").String())
		children := namespaceTool.Get("tools")
		if !children.Exists() || !children.IsArray() {
			return
		}
		children.ForEach(func(_, child gjson.Result) bool {
			childName := responsesToolName(child)
			if childName == "" {
				return true
			}
			qualifiedName := QualifyResponsesNamespaceToolName(namespaceName, childName)
			switch strings.TrimSpace(child.Get("type").String()) {
			case "", "function":
				appendDescriptor(child, qualifiedName, childName, namespaceName, "function", sourcePriority, false)
			case "custom":
				appendDescriptor(child, qualifiedName, childName, namespaceName, "custom", sourcePriority, false)
			}
			return true
		})
	}
	for _, source := range responsesToolSources(root) {
		source.tools.ForEach(func(_, tool gjson.Result) bool {
			toolType := strings.TrimSpace(tool.Get("type").String())
			switch toolType {
			case "", "function":
				name := responsesToolName(tool)
				appendDescriptor(tool, name, name, "", "function", source.priority, true)
			case "custom":
				name := responsesToolName(tool)
				appendDescriptor(tool, name, name, "", "custom", source.priority, true)
			case "namespace":
				appendNamespaceChildren(tool, source.priority)
			}
			return true
		})
	}
	return descriptors
}

func responsesToolDescriptorPrecedes(left, right ResponsesToolDescriptor) bool {
	if left.SourcePriority != right.SourcePriority {
		return left.SourcePriority < right.SourcePriority
	}
	if left.Direct != right.Direct {
		return left.Direct
	}
	return left.Order < right.Order
}

// CollectResponsesToolWinners collects deduplicated winning descriptors for each qualified tool name.
func CollectResponsesToolWinners(root gjson.Result) map[string]ResponsesToolDescriptor {
	winners := map[string]ResponsesToolDescriptor{}
	for _, descriptor := range CollectResponsesToolDescriptors(root) {
		current, exists := winners[descriptor.Name]
		if !exists || responsesToolDescriptorPrecedes(descriptor, current) {
			winners[descriptor.Name] = descriptor
		}
	}
	return winners
}

func sanitizeResponsesToolNames(names []string) map[string]string {
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
			mapped = disambiguateResponsesSanitizedName(base, name, used)
		}
		out[name] = mapped
		used[mapped] = name
	}
	return out
}

func disambiguateResponsesSanitizedName(base, original string, used map[string]string) string {
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

// BuildGeminiFunctionDeclarations builds Gemini function declarations, forward name mapping, and reverse identity mapping.
func BuildGeminiFunctionDeclarations(root gjson.Result) ([][]byte, map[string]string, map[string]ResponsesToolIdentity) {
	descriptors := CollectResponsesToolDescriptors(root)
	winners := CollectResponsesToolWinners(root)

	seenNames := make(map[string]struct{})
	var winningList []ResponsesToolDescriptor
	for _, descriptor := range descriptors {
		winner, ok := winners[descriptor.Name]
		if !ok || winner.Order != descriptor.Order {
			continue
		}
		if _, seen := seenNames[descriptor.Name]; seen {
			continue
		}
		seenNames[descriptor.Name] = struct{}{}
		winningList = append(winningList, descriptor)
	}

	if len(winningList) == 0 {
		return nil, nil, nil
	}

	qualifiedNames := make([]string, 0, len(winningList))
	for _, desc := range winningList {
		qualifiedNames = append(qualifiedNames, desc.Name)
	}
	sanitizedMap := sanitizeResponsesToolNames(qualifiedNames)

	forwardMap := make(map[string]string, len(winningList)*2)
	reverseMap := make(map[string]ResponsesToolIdentity, len(winningList)*2)
	var declarations [][]byte

	for _, desc := range winningList {
		geminiName := desc.Name
		if mapped, ok := sanitizedMap[desc.Name]; ok && mapped != "" {
			geminiName = mapped
		} else {
			geminiName = SanitizeFunctionName(desc.Name)
		}

		forwardMap[desc.Name] = geminiName
		if desc.LocalName != "" && desc.LocalName != desc.Name {
			if _, exists := forwardMap[desc.LocalName]; !exists {
				forwardMap[desc.LocalName] = geminiName
			}
		}

		identity := ResponsesToolIdentity{
			Name:      desc.LocalName,
			Namespace: desc.Namespace,
			Custom:    desc.ToolType == "custom",
		}
		reverseMap[geminiName] = identity
		if desc.Name != geminiName {
			reverseMap[desc.Name] = identity
		}

		funcDecl := []byte(`{"name":"","description":"","parametersJsonSchema":{}}`)
		funcDecl, _ = sjson.SetBytes(funcDecl, "name", geminiName)
		if descStr := responsesToolDescription(desc.Tool); descStr != "" {
			funcDecl, _ = sjson.SetBytes(funcDecl, "description", descStr)
		}

		if desc.ToolType == "custom" {
			funcDecl, _ = sjson.SetRawBytes(funcDecl, "parametersJsonSchema", []byte(`{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}`))
		} else {
			params := responsesToolParameters(desc.Tool)
			if params.Exists() {
				funcDecl, _ = sjson.SetRawBytes(funcDecl, "parametersJsonSchema", []byte(CleanJSONSchemaForGemini(params.Raw)))
			}
		}
		declarations = append(declarations, funcDecl)
	}

	return declarations, forwardMap, reverseMap
}

// ResponsesToolReverseIdentityMap builds a Gemini function name -> ResponsesToolIdentity map from a Responses request raw JSON.
func ResponsesToolReverseIdentityMap(rawJSON []byte) map[string]ResponsesToolIdentity {
	if len(rawJSON) == 0 || !gjson.ValidBytes(rawJSON) {
		return nil
	}
	root := gjson.ParseBytes(rawJSON)
	if req := root.Get("request"); req.Exists() && (req.Get("model").Exists() || req.Get("input").Exists() || req.Get("tools").Exists()) {
		root = req
	}
	_, _, reverseMap := BuildGeminiFunctionDeclarations(root)
	return reverseMap
}

// MapResponsesToolName returns the mapped Gemini function name if present in forwardMap, else sanitized name.
func MapResponsesToolName(forwardMap map[string]string, name string) string {
	if mapped, ok := forwardMap[name]; ok && mapped != "" {
		return mapped
	}
	return SanitizeFunctionName(name)
}

// ConvertResponsesToolChoiceToGemini translates Responses tool_choice into Gemini functionCallingConfig JSON.
func ConvertResponsesToolChoiceToGemini(toolChoice gjson.Result, forwardMap map[string]string) ([]byte, bool) {
	if !toolChoice.Exists() {
		return nil, false
	}
	mode := ""
	var allowedNames []string
	if toolChoice.Type == gjson.String {
		switch strings.ToLower(strings.TrimSpace(toolChoice.String())) {
		case "none":
			mode = "NONE"
		case "auto":
			mode = "AUTO"
		case "required", "any":
			mode = "ANY"
		}
	} else if toolChoice.IsObject() {
		toolType := strings.ToLower(strings.TrimSpace(toolChoice.Get("type").String()))
		switch toolType {
		case "none":
			mode = "NONE"
		case "auto":
			mode = "AUTO"
		case "required", "any":
			mode = "ANY"
		case "function", "custom", "tool", "":
			mode = "ANY"
			name := strings.TrimSpace(toolChoice.Get("name").String())
			if name == "" {
				name = strings.TrimSpace(toolChoice.Get("function.name").String())
			}
			if name == "" {
				name = strings.TrimSpace(toolChoice.Get("custom.name").String())
			}
			namespace := strings.TrimSpace(toolChoice.Get("namespace").String())
			if namespace == "" {
				namespace = strings.TrimSpace(toolChoice.Get("function.namespace").String())
			}
			if namespace == "" {
				namespace = strings.TrimSpace(toolChoice.Get("custom.namespace").String())
			}
			if namespace != "" {
				name = QualifyResponsesNamespaceToolName(namespace, name)
			}
			if name != "" {
				geminiName := MapResponsesToolName(forwardMap, name)
				allowedNames = append(allowedNames, geminiName)
			}
		}
	}
	if mode == "" {
		return nil, false
	}
	cfg := []byte(`{"mode":""}`)
	cfg, _ = sjson.SetBytes(cfg, "mode", mode)
	if len(allowedNames) > 0 {
		cfg, _ = sjson.SetBytes(cfg, "allowedFunctionNames", allowedNames)
	}
	return cfg, true
}

// UnwrapResponsesCustomToolInput extracts the raw input string from custom tool arguments JSON or plain string.
func UnwrapResponsesCustomToolInput(arguments string) string {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" || arguments == "{}" {
		return ""
	}
	if gjson.Valid(arguments) {
		parsed := gjson.Parse(arguments)
		if v := parsed.Get("input"); v.Exists() {
			if v.Type == gjson.String {
				return v.String()
			}
			return v.Raw
		}
		if parsed.Type == gjson.String {
			return parsed.String()
		}
	}
	return arguments
}
