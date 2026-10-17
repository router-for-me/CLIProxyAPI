package util

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	applypatch "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/apply-patch"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ResponsesToolIdentity represents the resolved identity of a tool in OpenAI Responses format.
type ResponsesToolIdentity struct {
	Name       string
	Namespace  string
	Custom     bool
	ApplyPatch bool // Resolved from the winning original declaration, never from the upstream name.
	ToolSearch bool // Marks the function shim standing in for a client-executed tool_search declaration.
}

const (
	// ResponsesToolSearchType is the Responses tool type for deferred tool discovery.
	ResponsesToolSearchType = "tool_search"
	// ResponsesToolSearchExecutionClient marks a tool_search that the client executes itself.
	ResponsesToolSearchExecutionClient = "client"
	// ResponsesToolSearchFunctionName is the function name exposed to providers
	// that have no native tool_search support.
	ResponsesToolSearchFunctionName = "tool_search"
	// ResponsesClientToolSearchKey is the internal forwardMap key for the
	// client-executed tool_search shim. The leading NUL byte cannot appear in a
	// JSON identifier, preventing collisions with user-declared tool names.
	ResponsesClientToolSearchKey = "\x00client_tool_search"
	// ResponsesToolSearchCallType is the Responses item type for a tool_search invocation.
	ResponsesToolSearchCallType = "tool_search_call"
	// ResponsesToolSearchOutputType is the Responses input item carrying discovered tools.
	ResponsesToolSearchOutputType = "tool_search_output"
	// ResponsesDiscoveredToolPriority ranks tools loaded by tool_search_output after declared tools.
	ResponsesDiscoveredToolPriority = 2

	responsesToolSearchDefaultParameters = `{"type":"object","properties":{"query":{"type":"string","description":"Search query for deferred tools."},"limit":{"type":"number","description":"Maximum number of tools to return."}},"required":["query"],"additionalProperties":false}`
)

// ResponsesToolDescriptor is an internal representation of a tool declaration in a Responses request.
type ResponsesToolDescriptor struct {
	Name           string // Qualified name (e.g. "functions__exec" or "exec")
	LocalName      string // Local name without namespace (e.g. "exec")
	Namespace      string // Namespace if any (e.g. "functions")
	ToolType       string // "function", "custom", etc.
	Tool           gjson.Result
	SourcePriority int  // 0 for top-level tools, 1 for additional_tools, 2 for tool_search_output
	Direct         bool // true if declared directly, false if declared as namespace child
	Order          int  // original discovery order
	ClientSearch   bool // true for a client-executed tool_search declaration
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

func responsesToolSources(root gjson.Result, includeClientToolSearch bool) []struct {
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
		if includeClientToolSearch {
			input.ForEach(func(_, item gjson.Result) bool {
				if item.Get("type").String() == ResponsesToolSearchOutputType && IsResponsesClientToolSearchItem(item) {
					appendSource(item.Get("tools"), ResponsesDiscoveredToolPriority)
				}
				return true
			})
		}
	}
	return sources
}

// IsResponsesClientToolSearchDeclaration reports whether a tool declaration is
// a client-executed Responses tool_search.
func IsResponsesClientToolSearchDeclaration(tool gjson.Result) bool {
	return strings.TrimSpace(tool.Get("type").String()) == ResponsesToolSearchType &&
		strings.TrimSpace(tool.Get("execution").String()) == ResponsesToolSearchExecutionClient
}

// IsResponsesClientToolSearchItem reports whether a tool_search_call or
// tool_search_output input item belongs to client-executed tool search.
// Hosted server search explicitly sets execution to "server".
func IsResponsesClientToolSearchItem(item gjson.Result) bool {
	execution := strings.TrimSpace(item.Get("execution").String())
	return execution == "" || execution == ResponsesToolSearchExecutionClient
}

func responsesToolName(tool gjson.Result) string {
	if name := strings.TrimSpace(tool.Get("name").String()); name != "" {
		return name
	}
	return strings.TrimSpace(tool.Get("function.name").String())
}

// ResponsesToolDescription extracts the description from a tool or function object.
func ResponsesToolDescription(tool gjson.Result) string {
	if description := tool.Get("description").String(); description != "" {
		return description
	}
	return tool.Get("function.description").String()
}

func responsesToolDescription(tool gjson.Result) string {
	return ResponsesToolDescription(tool)
}

// ResponsesToolParameters extracts the schema/parameters from a tool or function object.
func ResponsesToolParameters(tool gjson.Result) gjson.Result {
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

func responsesToolParameters(tool gjson.Result) gjson.Result {
	return ResponsesToolParameters(tool)
}

// CollectResponsesToolDescriptors extracts all tool descriptors from a Responses request root.
func CollectResponsesToolDescriptors(root gjson.Result) []ResponsesToolDescriptor {
	return collectResponsesToolDescriptors(root, false)
}

func collectResponsesToolDescriptors(root gjson.Result, includeClientToolSearch bool) []ResponsesToolDescriptor {
	var descriptors []ResponsesToolDescriptor
	appendDescriptor := func(tool gjson.Result, name, localName, namespace string, toolType string, sourcePriority int, direct bool, clientSearch bool) {
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
			ClientSearch:   clientSearch,
		})
	}
	appendNamespaceChildren := func(namespaceTool gjson.Result, sourcePriority int) {
		namespaceName := strings.TrimSpace(namespaceTool.Get("name").String())
		children := namespaceTool.Get("tools")
		if !children.Exists() || !children.IsArray() {
			children = namespaceTool.Get("children")
		}
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
				appendDescriptor(child, qualifiedName, childName, namespaceName, "function", sourcePriority, false, false)
			case "custom":
				appendDescriptor(child, qualifiedName, childName, namespaceName, "custom", sourcePriority, false, false)
			}
			return true
		})
	}
	for _, source := range responsesToolSources(root, includeClientToolSearch) {
		source.tools.ForEach(func(_, tool gjson.Result) bool {
			toolType := strings.TrimSpace(tool.Get("type").String())
			switch toolType {
			case "", "function":
				name := responsesToolName(tool)
				appendDescriptor(tool, name, name, "", "function", source.priority, true, false)
			case "custom":
				name := responsesToolName(tool)
				appendDescriptor(tool, name, name, "", "custom", source.priority, true, false)
			case "namespace":
				appendNamespaceChildren(tool, source.priority)
			case ResponsesToolSearchType:
				if includeClientToolSearch && source.priority < ResponsesDiscoveredToolPriority && IsResponsesClientToolSearchDeclaration(tool) {
					appendDescriptor(tool, ResponsesToolSearchFunctionName, ResponsesToolSearchFunctionName, "", ResponsesToolSearchType, source.priority, true, true)
				}
			}
			return true
		})
	}

	if !includeClientToolSearch || !responsesHasClientToolSearch(descriptors) {
		return descriptors
	}
	kept := make([]ResponsesToolDescriptor, 0, len(descriptors))
	for _, descriptor := range descriptors {
		if !descriptor.ClientSearch && descriptor.SourcePriority != ResponsesDiscoveredToolPriority && descriptor.Tool.Get("defer_loading").Bool() {
			continue
		}
		descriptor.Order = len(kept)
		kept = append(kept, descriptor)
	}
	return kept
}

func responsesHasClientToolSearch(descriptors []ResponsesToolDescriptor) bool {
	for i := range descriptors {
		if descriptors[i].ClientSearch {
			return true
		}
	}
	return false
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
	return collectResponsesToolWinnersFromDescriptors(CollectResponsesToolDescriptors(root))
}

func collectResponsesToolWinnersFromDescriptors(descriptors []ResponsesToolDescriptor) map[string]ResponsesToolDescriptor {
	winners := map[string]ResponsesToolDescriptor{}
	for _, descriptor := range descriptors {
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

type responsesToolWinnerKey struct {
	name         string
	clientSearch bool
}

// BuildGeminiFunctionDeclarations builds Gemini function declarations, forward name mapping, and reverse identity mapping.
func BuildGeminiFunctionDeclarations(root gjson.Result) ([][]byte, map[string]string, map[string]ResponsesToolIdentity) {
	descriptors := collectResponsesToolDescriptors(root, true)
	winners := make(map[responsesToolWinnerKey]ResponsesToolDescriptor, len(descriptors))
	for _, descriptor := range descriptors {
		key := responsesToolWinnerKey{name: descriptor.Name, clientSearch: descriptor.ClientSearch}
		current, exists := winners[key]
		if !exists || responsesToolDescriptorPrecedes(descriptor, current) {
			winners[key] = descriptor
		}
	}

	seenKeys := make(map[responsesToolWinnerKey]struct{}, len(winners))
	var winningList []ResponsesToolDescriptor
	for _, descriptor := range descriptors {
		key := responsesToolWinnerKey{name: descriptor.Name, clientSearch: descriptor.ClientSearch}
		winner, ok := winners[key]
		if !ok || winner.Order != descriptor.Order {
			continue
		}
		if _, seen := seenKeys[key]; seen {
			continue
		}
		seenKeys[key] = struct{}{}
		winningList = append(winningList, descriptor)
	}

	hasClientSearchHistory := false
	if include := root.Get("input"); include.Exists() && include.IsArray() {
		include.ForEach(func(_, item gjson.Result) bool {
			typ := item.Get("type").String()
			if (typ == ResponsesToolSearchCallType || typ == ResponsesToolSearchOutputType) && IsResponsesClientToolSearchItem(item) {
				hasClientSearchHistory = true
				return false
			}
			return true
		})
	}

	if len(winningList) == 0 {
		if !hasClientSearchHistory {
			return nil, nil, nil
		}
		forwardMap := map[string]string{
			ResponsesClientToolSearchKey:    ResponsesToolSearchFunctionName,
			ResponsesToolSearchFunctionName: ResponsesToolSearchFunctionName,
		}
		reverseMap := map[string]ResponsesToolIdentity{
			ResponsesToolSearchFunctionName: {
				Name:       ResponsesToolSearchFunctionName,
				ToolSearch: true,
			},
		}
		return nil, forwardMap, reverseMap
	}

	hasClientSearch := false
	userOwnsExactToolSearch := false
	qualifiedNames := make([]string, 0, len(winningList))
	for _, desc := range winningList {
		if desc.ClientSearch {
			hasClientSearch = true
			continue
		}
		if desc.Name == ResponsesToolSearchFunctionName {
			userOwnsExactToolSearch = true
		}
		qualifiedNames = append(qualifiedNames, desc.Name)
	}
	sanitizedMap := sanitizeResponsesToolNames(qualifiedNames)
	if sanitizedMap == nil {
		sanitizedMap = make(map[string]string, len(winningList))
	}

	clientSearchGeminiName := ""
	if hasClientSearch || hasClientSearchHistory {
		used := make(map[string]string, len(sanitizedMap)+1)
		for origName, mappedName := range sanitizedMap {
			used[mappedName] = origName
		}
		if !userOwnsExactToolSearch {
			// Reserve "tool_search" for the client tool_search shim and
			// disambiguate any non-exact tool (e.g. "tool.search") that sanitized to it.
			if collidingOrig, taken := used[ResponsesToolSearchFunctionName]; taken && collidingOrig != ResponsesToolSearchFunctionName {
				used[ResponsesToolSearchFunctionName] = ResponsesClientToolSearchKey
				newMapped := disambiguateResponsesSanitizedName(ResponsesToolSearchFunctionName, collidingOrig, used)
				sanitizedMap[collidingOrig] = newMapped
				used[newMapped] = collidingOrig
			} else {
				used[ResponsesToolSearchFunctionName] = ResponsesClientToolSearchKey
			}
			clientSearchGeminiName = ResponsesToolSearchFunctionName
		} else {
			clientSearchGeminiName = disambiguateResponsesSanitizedName(ResponsesToolSearchFunctionName, ResponsesClientToolSearchKey, used)
			used[clientSearchGeminiName] = ResponsesClientToolSearchKey
		}
	}

	forwardMap := make(map[string]string, len(winningList)*2+1)
	reverseMap := make(map[string]ResponsesToolIdentity, len(winningList)*2+1)
	if clientSearchGeminiName != "" {
		forwardMap[ResponsesClientToolSearchKey] = clientSearchGeminiName
		forwardMap[clientSearchGeminiName] = clientSearchGeminiName
		if !userOwnsExactToolSearch {
			forwardMap[ResponsesToolSearchFunctionName] = clientSearchGeminiName
		}
		reverseMap[clientSearchGeminiName] = ResponsesToolIdentity{
			Name:       ResponsesToolSearchFunctionName,
			ToolSearch: true,
		}
	}
	var declarations [][]byte

	for _, desc := range winningList {
		geminiName := desc.Name
		if desc.ClientSearch {
			geminiName = clientSearchGeminiName
		} else if mapped, ok := sanitizedMap[desc.Name]; ok && mapped != "" {
			geminiName = mapped
		} else {
			geminiName = SanitizeFunctionName(desc.Name)
		}

		if !desc.ClientSearch {
			forwardMap[desc.Name] = geminiName
		}
		forwardMap[geminiName] = geminiName

		identity := ResponsesToolIdentity{
			Name:       desc.LocalName,
			Namespace:  desc.Namespace,
			Custom:     desc.ToolType == "custom",
			ApplyPatch: applypatch.IsCustomTool(desc.Tool),
			ToolSearch: desc.ClientSearch,
		}
		reverseMap[geminiName] = identity
		if !desc.ClientSearch && desc.Name != geminiName {
			reverseMap[desc.Name] = identity
		}

		funcDecl := []byte(`{"name":"","description":"","parametersJsonSchema":{}}`)
		funcDecl, _ = sjson.SetBytes(funcDecl, "name", geminiName)
		if descStr := responsesToolDescription(desc.Tool); descStr != "" {
			funcDecl, _ = sjson.SetBytes(funcDecl, "description", descStr)
		}

		if applypatch.IsCustomTool(desc.Tool) {
			funcDecl, _ = sjson.SetBytes(funcDecl, "description", applypatch.Description(desc.Tool))
			funcDecl, _ = sjson.SetRawBytes(funcDecl, "parametersJsonSchema", applypatch.Parameters())
		} else if desc.ToolType == "custom" {
			funcDecl, _ = sjson.SetRawBytes(funcDecl, "parametersJsonSchema", []byte(`{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}`))
		} else if desc.ClientSearch {
			funcDecl, _ = sjson.SetRawBytes(funcDecl, "parametersJsonSchema", buildGeminiToolSearchParametersSchema(desc.Tool))
		} else {
			params := responsesToolParameters(desc.Tool)
			if params.Exists() {
				funcDecl, _ = sjson.SetRawBytes(funcDecl, "parametersJsonSchema", []byte(CleanJSONSchemaForGeminiJSONSchema(params.Raw)))
			}
		}
		declarations = append(declarations, funcDecl)
	}

	// Populate local-name aliases after all direct/qualified names are registered
	// so a namespace child's local name never shadows a direct tool or the search shim.
	for _, desc := range winningList {
		if !desc.ClientSearch && desc.LocalName != "" && desc.LocalName != desc.Name {
			if _, exists := forwardMap[desc.LocalName]; !exists {
				forwardMap[desc.LocalName] = forwardMap[desc.Name]
			}
		}
	}

	return declarations, forwardMap, reverseMap
}

func buildGeminiToolSearchParametersSchema(tool gjson.Result) []byte {
	if params := responsesToolParameters(tool); params.Exists() && params.IsObject() {
		cleaned := CleanJSONSchemaForGeminiJSONSchema(params.Raw)
		if gjson.Valid(cleaned) && gjson.Parse(cleaned).IsObject() {
			return []byte(cleaned)
		}
	}
	return []byte(responsesToolSearchDefaultParameters)
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

// MapResponsesClientToolSearchName returns the mapped Gemini function name for
// the client-executed tool_search shim.
func MapResponsesClientToolSearchName(forwardMap map[string]string) string {
	if mapped, ok := forwardMap[ResponsesClientToolSearchKey]; ok && mapped != "" {
		return mapped
	}
	return ResponsesToolSearchFunctionName
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
		case ResponsesToolSearchType:
			mode = "ANY"
			allowedNames = append(allowedNames, MapResponsesClientToolSearchName(forwardMap))
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
