package helps

import (
	"bytes"
	"fmt"
	"strings"

	kimiauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kimi"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ResolveKimiBaseURL resolves the upstream API base URL for Kimi API requests based on auth.
func ResolveKimiBaseURL(auth *cliproxyauth.Auth) string {
	if auth != nil {
		if auth.Attributes != nil {
			if raw := strings.TrimRight(strings.TrimSpace(auth.Attributes["base_url"]), "/"); raw != "" {
				return raw
			}
		}
		if auth.Metadata != nil {
			if raw, ok := auth.Metadata["base_url"].(string); ok && strings.TrimSpace(raw) != "" {
				return strings.TrimRight(strings.TrimSpace(raw), "/")
			}
		}
		if kimiauth.IsKimiAIAuth(auth) {
			return kimiauth.KimiAIAPIBaseURL
		}
	}
	return kimiauth.KimiAPIBaseURL
}

// ResolveKimiResponsesURL resolves the upstream URL for Kimi Responses API requests.
func ResolveKimiResponsesURL(auth *cliproxyauth.Auth) string {
	baseURL := ResolveKimiBaseURL(auth)
	if strings.HasSuffix(baseURL, "/v1") {
		return baseURL + "/responses"
	}
	return baseURL + "/v1/responses"
}

// ResolveKimiChatURL resolves the upstream URL for Kimi Chat Completions requests.
func ResolveKimiChatURL(auth *cliproxyauth.Auth) string {
	baseURL := ResolveKimiBaseURL(auth)
	if strings.HasSuffix(baseURL, "/v1") {
		return baseURL + "/chat/completions"
	}
	return baseURL + "/v1/chat/completions"
}

// ResolveKimiClaudeBaseURL resolves the base URL for Kimi Claude Messages delegation.
func ResolveKimiClaudeBaseURL(auth *cliproxyauth.Auth) string {
	baseURL := ResolveKimiBaseURL(auth)
	return strings.TrimSuffix(baseURL, "/v1")
}

func isResponsesToolCall(item gjson.Result) bool {
	t := strings.TrimSpace(item.Get("type").String())
	return t == "function_call" || t == "custom_tool_call"
}

func isResponsesToolOutput(item gjson.Result) bool {
	t := strings.TrimSpace(item.Get("type").String())
	return t == "function_call_output" || t == "custom_tool_call_output"
}

// NormalizeKimiResponsesInput normalizes the input array for Kimi Responses API requests.
// When parallel function calls are emitted, Kimi requires all corresponding tool outputs
// to follow immediately without intervening non-tool messages (e.g. developer/user messages).
// This function defers intervening non-tool items until all outputs for the current batch
// of function calls have been emitted, keeping the outputs contiguous.
func NormalizeKimiResponsesInput(body []byte) ([]byte, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body, nil
	}

	input := gjson.GetBytes(body, "input")
	if !input.Exists() || !input.IsArray() {
		return body, nil
	}

	items := input.Array()
	if len(items) == 0 {
		return body, nil
	}

	reordered := false
	result := make([]gjson.Result, 0, len(items))

	for i := 0; i < len(items); {
		if !isResponsesToolCall(items[i]) {
			result = append(result, items[i])
			i++
			continue
		}

		startCalls := i
		endCalls := i
		callIDs := make(map[string]int)
		callIDCount := 0
		for endCalls < len(items) && isResponsesToolCall(items[endCalls]) {
			callID := translatorcommon.ExtractResponsesCallID(items[endCalls])
			if callID != "" {
				callIDs[callID]++
				callIDCount++
			}
			endCalls++
		}

		result = append(result, items[startCalls:endCalls]...)

		if callIDCount == 0 {
			i = endCalls
			continue
		}

		needed := make(map[string]int, len(callIDs))
		for k, v := range callIDs {
			needed[k] = v
		}
		remainingNeeded := callIDCount
		lastMatchingIdx := -1

		for j := endCalls; j < len(items) && remainingNeeded > 0; j++ {
			it := items[j]
			if isResponsesToolCall(it) {
				break
			}
			if isResponsesToolOutput(it) {
				cid := translatorcommon.ExtractResponsesCallID(it)
				if count := needed[cid]; count > 0 {
					needed[cid]--
					remainingNeeded--
					lastMatchingIdx = j
				}
			}
		}

		if remainingNeeded == 0 && lastMatchingIdx >= endCalls {
			matchingOutputs := make([]gjson.Result, 0, callIDCount)
			interveningItems := make([]gjson.Result, 0)
			consumed := make(map[string]int, len(callIDs))
			for k, v := range callIDs {
				consumed[k] = v
			}

			for j := endCalls; j <= lastMatchingIdx; j++ {
				it := items[j]
				if isResponsesToolOutput(it) {
					cid := translatorcommon.ExtractResponsesCallID(it)
					if count := consumed[cid]; count > 0 {
						consumed[cid]--
						matchingOutputs = append(matchingOutputs, it)
						continue
					}
				}
				interveningItems = append(interveningItems, it)
			}

			if len(interveningItems) > 0 {
				reordered = true
			}

			result = append(result, matchingOutputs...)
			result = append(result, interveningItems...)

			i = lastMatchingIdx + 1
		} else {
			i = endCalls
		}
	}

	if !reordered {
		return body, nil
	}

	rebuilt := make([]string, 0, len(result))
	for _, it := range result {
		rebuilt = append(rebuilt, it.Raw)
	}

	updated, errSet := sjson.SetRawBytes(body, "input", []byte("["+strings.Join(rebuilt, ",")+"]"))
	if errSet != nil {
		return body, fmt.Errorf("normalize kimi responses input: %w", errSet)
	}
	return updated, nil
}

const kimiCustomToolInput = "input"

var kimiCustomToolParameters = []byte(`{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}`)

// NormalizeKimiResponsesCustomTools adapts Responses custom tools to the
// function shape accepted by Kimi while preserving the client-facing name.
// apply_patch remains owned by the existing apply-patch bridge.
func NormalizeKimiResponsesCustomTools(body []byte) ([]byte, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body, fmt.Errorf("invalid Responses request JSON")
	}
	winners := util.CollectResponsesToolWinners(gjson.ParseBytes(body))
	seenTools := make(map[string]struct{})
	var tools [][]byte
	if declared := gjson.GetBytes(body, "tools"); declared.IsArray() {
		tools = append(tools, flattenKimiResponsesToolArray(declared, "", seenTools, winners)...)
	}
	input := gjson.GetBytes(body, "input").Array()
	var remainingInput [][]byte
	for _, item := range input {
		if item.Get("type").String() != "additional_tools" {
			remainingInput = append(remainingInput, []byte(item.Raw))
			continue
		}
		if item.Get("tools").IsArray() {
			tools = append(tools, flattenKimiResponsesToolArray(item.Get("tools"), "", seenTools, winners)...)
		}
	}
	if gjson.GetBytes(body, "input").IsArray() {
		body, _ = sjson.SetRawBytes(body, "input", translatorcommon.JoinRawArray(remainingInput))
	}
	if gjson.GetBytes(body, "tools").Exists() || len(tools) > 0 {
		body, _ = sjson.SetRawBytes(body, "tools", translatorcommon.JoinRawArray(tools))
	}

	// Kimi's Responses client uses function calls for every executable tool.
	// Give custom and ordinary function calls valid IDs and qualify namespaced tools.
	callIDs := make(map[string]string)
	var unmatchedCalls []string
	for index, item := range gjson.GetBytes(body, "input").Array() {
		typ := item.Get("type").String()
		if typ != "custom_tool_call" && typ != "function_call" {
			continue
		}
		path := fmt.Sprintf("input.%d", index)
		raw := []byte(item.Raw)
		name := strings.TrimSpace(item.Get("name").String())
		namespace := strings.TrimSpace(item.Get("namespace").String())
		if namespace != "" && name != "" {
			name = util.QualifyResponsesNamespaceToolName(namespace, name)
			raw, _ = sjson.SetBytes(raw, "name", name)
		}
		raw, _ = sjson.DeleteBytes(raw, "namespace")
		callID := strings.TrimSpace(item.Get("call_id").String())
		if callID == "" {
			callID = kimiGeneratedResponsesCallID(item, index)
			raw, _ = sjson.SetBytes(raw, "call_id", callID)
		}
		callIDs[strings.TrimSpace(item.Get("call_id").String())] = callID
		unmatchedCalls = append(unmatchedCalls, callID)
		if typ == "custom_tool_call" {
			if item.Get("input").Type != gjson.String {
				return nil, fmt.Errorf("Kimi custom tool history input must be a string")
			}
			raw, _ = sjson.SetBytes(raw, "type", "function_call")
			raw, _ = sjson.SetBytes(raw, "arguments", kimiCustomToolArguments(item.Get("input").String()))
			raw, _ = sjson.DeleteBytes(raw, "input")
			raw = normalizeKimiResponsesUpstreamItemID(raw, "function_call")
		}
		body, _ = sjson.SetRawBytes(body, path, raw)
	}

	for index, item := range gjson.GetBytes(body, "input").Array() {
		typ := item.Get("type").String()
		if typ != "custom_tool_call_output" && typ != "function_call_output" {
			continue
		}
		path := fmt.Sprintf("input.%d", index)
		raw := []byte(item.Raw)
		callID := strings.TrimSpace(item.Get("call_id").String())
		if callID == "" {
			if len(unmatchedCalls) != 1 {
				return nil, fmt.Errorf("Kimi tool history output is missing a call_id and has %d unmatched calls", len(unmatchedCalls))
			}
			callID = unmatchedCalls[0]
			raw, _ = sjson.SetBytes(raw, "call_id", callID)
		} else if upstreamCallID, ok := callIDs[callID]; ok {
			callID = upstreamCallID
			raw, _ = sjson.SetBytes(raw, "call_id", callID)
		}
		if len(unmatchedCalls) > 0 {
			for callIndex, unmatched := range unmatchedCalls {
				if unmatched == callID {
					unmatchedCalls = append(unmatchedCalls[:callIndex], unmatchedCalls[callIndex+1:]...)
					break
				}
			}
		}
		raw, _ = sjson.SetBytes(raw, "type", "function_call_output")
		raw, _ = sjson.DeleteBytes(raw, "id")
		body, _ = sjson.SetRawBytes(body, path, raw)
	}
	if choice := gjson.GetBytes(body, "tool_choice"); choice.IsObject() {
		body, _ = sjson.SetRawBytes(body, "tool_choice", normalizeKimiCustomToolChoice(choice))
	}
	return body, nil
}

func flattenKimiResponsesToolArray(tools gjson.Result, namespace string, seen map[string]struct{}, winners map[string]util.ResponsesToolDescriptor) [][]byte {
	items := make([][]byte, 0, len(tools.Array()))
	for _, tool := range tools.Array() {
		raw := []byte(tool.Raw)
		if tool.Get("type").String() == "namespace" {
			name := strings.TrimSpace(tool.Get("name").String())
			childNamespace := util.QualifyResponsesNamespaceToolName(namespace, name)
			children := tool.Get("tools")
			if !children.IsArray() {
				children = tool.Get("children")
			}
			if children.IsArray() {
				items = append(items, flattenKimiResponsesToolArray(children, childNamespace, seen, winners)...)
			}
			continue
		}
		name := kimiResponsesToolName(tool)
		qualifiedName := util.QualifyResponsesNamespaceToolName(namespace, name)
		if winner, ok := winners[qualifiedName]; ok && winner.Tool.Index != tool.Index {
			continue
		}
		if qualifiedName != "" {
			if _, exists := seen[qualifiedName]; exists {
				continue
			}
			seen[qualifiedName] = struct{}{}
		}
		if qualifiedName != "" && qualifiedName != name {
			raw, _ = sjson.SetBytes(raw, "name", qualifiedName)
		}
		toolType := strings.TrimSpace(tool.Get("type").String())
		if toolType == "" || toolType == "function" || toolType == "custom" {
			raw, _ = sjson.SetBytes(raw, "type", "function")
			raw, _ = sjson.SetBytes(raw, "strict", false)
		}
		if toolType == "custom" {
			raw, _ = sjson.SetRawBytes(raw, "parameters", kimiCustomToolParameters)
			raw, _ = sjson.DeleteBytes(raw, "format")
		}
		if nested := tool.Get("function"); nested.IsObject() {
			for _, field := range []string{"name", "description", "parameters", "strict"} {
				if value := nested.Get(field); value.Exists() && !gjson.GetBytes(raw, field).Exists() {
					raw, _ = sjson.SetRawBytes(raw, field, []byte(value.Raw))
				}
			}
			raw, _ = sjson.DeleteBytes(raw, "function")
		}
		if qualifiedName != "" {
			raw, _ = sjson.SetBytes(raw, "name", qualifiedName)
		}
		items = append(items, raw)
	}
	return items
}

func normalizeKimiCustomToolChoice(choice gjson.Result) []byte {
	raw := []byte(choice.Raw)
	if choice.Get("type").String() == "custom" {
		raw, _ = sjson.SetBytes(raw, "type", "function")
	}
	name := strings.TrimSpace(choice.Get("name").String())
	namespace := strings.TrimSpace(choice.Get("namespace").String())
	function := choice.Get("function")
	if name == "" && function.IsObject() {
		name = strings.TrimSpace(function.Get("name").String())
		if namespace == "" {
			namespace = strings.TrimSpace(function.Get("namespace").String())
		}
	}
	if namespace != "" && name != "" {
		name = util.QualifyResponsesNamespaceToolName(namespace, name)
	}
	if name != "" {
		raw, _ = sjson.SetBytes(raw, "name", name)
	}
	raw, _ = sjson.DeleteBytes(raw, "namespace")
	raw, _ = sjson.DeleteBytes(raw, "function")
	for index, child := range choice.Get("tools").Array() {
		raw, _ = sjson.SetRawBytes(raw, fmt.Sprintf("tools.%d", index), normalizeKimiCustomToolChoice(child))
	}
	return raw
}

func kimiGeneratedResponsesCallID(item gjson.Result, index int) string {
	return kimiResponsesCallIDFromItemID(strings.TrimSpace(item.Get("id").String()), index)
}

func kimiResponsesCallIDFromItemID(itemID string, index int) string {
	itemID = strings.TrimPrefix(itemID, "ctc_")
	itemID = strings.TrimPrefix(itemID, "fc_")
	if itemID == "" {
		return fmt.Sprintf("call_kimi_%d", index)
	}
	return "call_kimi_" + itemID
}

func kimiResponsesToolName(tool gjson.Result) string {
	if name := strings.TrimSpace(tool.Get("name").String()); name != "" {
		return name
	}
	return strings.TrimSpace(tool.Get("function.name").String())
}

func kimiCustomToolArguments(input string) string {
	raw := []byte(`{"input":""}`)
	raw, _ = sjson.SetBytes(raw, kimiCustomToolInput, input)
	return string(raw)
}

func normalizeKimiResponsesUpstreamItemID(raw []byte, itemType string) []byte {
	id := gjson.GetBytes(raw, "id").String()
	if id == "" {
		return raw
	}
	if itemType == "function_call" && strings.HasPrefix(id, "ctc_") {
		raw, _ = sjson.SetBytes(raw, "id", "fc_"+strings.TrimPrefix(id, "ctc_"))
	}
	if itemType == "function_call_output" && strings.HasPrefix(id, "ctco_") {
		raw, _ = sjson.SetBytes(raw, "id", "fco_"+strings.TrimPrefix(id, "ctco_"))
	}
	return raw
}

func normalizeKimiResponsesClientItemID(raw []byte, itemType string) []byte {
	id := gjson.GetBytes(raw, "id").String()
	if id == "" {
		return raw
	}
	if itemType == "custom_tool_call" && strings.HasPrefix(id, "fc_") {
		raw, _ = sjson.SetBytes(raw, "id", "ctc_"+strings.TrimPrefix(id, "fc_"))
	}
	if itemType == "custom_tool_call_output" && strings.HasPrefix(id, "fco_") {
		raw, _ = sjson.SetBytes(raw, "id", "ctco_"+strings.TrimPrefix(id, "fco_"))
	}
	return raw
}

// KimiResponsesCustomToolState restores namespace metadata to function calls
// that were flattened while adapting custom tools for Kimi.
type KimiResponsesCustomToolState struct {
	customByName  map[string]util.ResponsesToolDescriptor
	customByLocal map[string]util.ResponsesToolDescriptor
	ambiguous     map[string]bool
	calls         map[string]*kimiCustomCall
	eventLine     []byte
	lastSequence  int
}

type kimiCustomCall struct {
	descriptor   util.ResponsesToolDescriptor
	itemID       string
	clientItemID string
	callID       string
	outputIndex  int
	arguments    string
	inputEmitted bool
}

// NewKimiResponsesCustomToolState builds a response adapter from the original
// client declarations. Ordinary function tools are never classified as custom.
func NewKimiResponsesCustomToolState(original []byte) *KimiResponsesCustomToolState {
	state := &KimiResponsesCustomToolState{
		customByName:  make(map[string]util.ResponsesToolDescriptor),
		customByLocal: make(map[string]util.ResponsesToolDescriptor),
		ambiguous:     make(map[string]bool),
		calls:         make(map[string]*kimiCustomCall),
	}
	root := gjson.ParseBytes(original)
	for name, descriptor := range util.CollectResponsesToolWinners(root) {
		if descriptor.ToolType != "custom" && descriptor.Namespace == "" {
			continue
		}
		state.customByName[name] = descriptor
		local := descriptor.LocalName
		if previous, ok := state.customByLocal[local]; ok && previous.Name != descriptor.Name {
			state.ambiguous[local] = true
			delete(state.customByLocal, local)
			continue
		}
		if !state.ambiguous[local] {
			state.customByLocal[local] = descriptor
		}
	}
	return state
}

func (s *KimiResponsesCustomToolState) Active() bool {
	return s != nil && len(s.customByName) > 0
}

// Kimi receives all custom tools as functions. Restore them to the client
// custom-tool contract after Kimi responds. apply_patch remains owned by the
// existing apply-patch bridge.
func isKimiNativeCustomToolDescriptor(descriptor util.ResponsesToolDescriptor) bool {
	return descriptor.ToolType == "custom" &&
		!strings.EqualFold(descriptor.LocalName, "apply_patch")
}

func (s *KimiResponsesCustomToolState) resolve(name, namespace string) (util.ResponsesToolDescriptor, bool) {
	if s == nil {
		return util.ResponsesToolDescriptor{}, false
	}
	qualified := util.QualifyResponsesNamespaceToolName(namespace, name)
	if descriptor, ok := s.customByName[qualified]; ok {
		return descriptor, true
	}
	if namespace == "" {
		if descriptor, ok := s.customByName[name]; ok {
			return descriptor, true
		}
		if !s.ambiguous[name] {
			if descriptor, ok := s.customByLocal[name]; ok {
				return descriptor, true
			}
		}
	}
	return util.ResponsesToolDescriptor{}, false
}

func (s *KimiResponsesCustomToolState) rememberCall(root, item gjson.Result, descriptor util.ResponsesToolDescriptor) *kimiCustomCall {
	itemID := item.Get("id").String()
	if itemID == "" {
		itemID = root.Get("item_id").String()
	}
	callID := item.Get("call_id").String()
	if callID == "" {
		callID = root.Get("call_id").String()
	}
	outputIndex := int(root.Get("output_index").Int())
	if item.Get("output_index").Exists() {
		outputIndex = int(item.Get("output_index").Int())
	}
	var call *kimiCustomCall
	for _, key := range []string{itemID, root.Get("item_id").String(), callID, fmt.Sprintf("index:%d", outputIndex)} {
		if key != "" {
			call = s.calls[key]
			if call != nil {
				break
			}
		}
	}
	if call == nil {
		call = &kimiCustomCall{descriptor: descriptor, itemID: itemID, callID: callID, outputIndex: outputIndex}
	}
	call.descriptor = descriptor
	if itemID != "" {
		call.itemID = itemID
		s.calls[itemID] = call
	}
	call.clientItemID = itemID
	if isKimiNativeCustomToolDescriptor(descriptor) && strings.HasPrefix(itemID, "fc_") {
		call.clientItemID = "ctc_" + strings.TrimPrefix(itemID, "fc_")
	}
	if callID != "" {
		call.callID = callID
		s.calls[callID] = call
	} else {
		call.callID = kimiResponsesCallIDFromItemID(itemID, outputIndex)
	}
	s.calls[fmt.Sprintf("index:%d", outputIndex)] = call
	return call
}

func (s *KimiResponsesCustomToolState) findCall(root, item gjson.Result) *kimiCustomCall {
	for _, key := range []string{root.Get("item_id").String(), item.Get("id").String(), root.Get("call_id").String(), item.Get("call_id").String(), fmt.Sprintf("index:%d", root.Get("output_index").Int())} {
		if key != "" {
			if call := s.calls[key]; call != nil {
				return call
			}
		}
	}
	return nil
}

func (s *KimiResponsesCustomToolState) restoreFunctionItem(raw []byte, descriptor util.ResponsesToolDescriptor, itemType string) []byte {
	customTool := isKimiNativeCustomToolDescriptor(descriptor)
	clientType := itemType
	if customTool {
		switch itemType {
		case "function_call":
			clientType = "custom_tool_call"
		case "function_call_output":
			clientType = "custom_tool_call_output"
		}
	}
	raw, _ = sjson.SetBytes(raw, "type", clientType)
	if customTool {
		raw = normalizeKimiResponsesClientItemID(raw, clientType)
	}
	if itemType == "function_call" {
		// Flattened names belong only to the Kimi upstream. Restore the exact
		// declaration identity for custom tools and ordinary functions alike.
		raw, _ = sjson.SetBytes(raw, "name", descriptor.LocalName)
		if descriptor.Namespace != "" {
			raw, _ = sjson.SetBytes(raw, "namespace", descriptor.Namespace)
		} else {
			raw, _ = sjson.DeleteBytes(raw, "namespace")
		}
	}
	if clientType == "custom_tool_call" {
		if strings.TrimSpace(gjson.GetBytes(raw, "call_id").String()) == "" {
			callID := kimiGeneratedResponsesCallID(gjson.ParseBytes(raw), int(gjson.GetBytes(raw, "output_index").Int()))
			raw, _ = sjson.SetBytes(raw, "call_id", callID)
		}
		arguments := gjson.GetBytes(raw, "arguments")
		if arguments.Exists() {
			raw, _ = sjson.SetBytes(raw, "input", kimiCustomToolInputFromArguments(arguments))
		}
		raw, _ = sjson.DeleteBytes(raw, "arguments")
	}
	return raw
}

func kimiCustomToolInputFromArguments(arguments gjson.Result) string {
	if arguments.Type == gjson.String {
		if parsed, ok := gjson.Parse(arguments.String()).Value().(map[string]any); ok {
			if input, exists := parsed[kimiCustomToolInput]; exists {
				if value, ok := input.(string); ok {
					return value
				}
				return gjson.Get(arguments.String(), kimiCustomToolInput).Raw
			}
		}
		return arguments.String()
	}
	if input := arguments.Get(kimiCustomToolInput); input.Exists() {
		if input.Type == gjson.String {
			return input.String()
		}
		return input.Raw
	}
	if arguments.Exists() {
		return arguments.Raw
	}
	return ""
}

func (s *KimiResponsesCustomToolState) restoreResponseOutput(raw []byte, path string) []byte {
	items := gjson.GetBytes(raw, path)
	if !items.IsArray() {
		return raw
	}
	for index, item := range items.Array() {
		itemType := item.Get("type").String()
		if itemType != "function_call" && itemType != "function_call_output" {
			continue
		}
		descriptor, ok := s.resolve(item.Get("name").String(), item.Get("namespace").String())
		if !ok {
			continue
		}
		if itemType == "function_call_output" && isKimiNativeCustomToolDescriptor(descriptor) {
			continue
		}
		itemRaw := []byte(item.Raw)
		if itemType == "function_call" && isKimiNativeCustomToolDescriptor(descriptor) {
			itemRaw = ensureKimiResponsesCallIdentity(itemRaw, index)
		}
		converted := s.restoreFunctionItem(itemRaw, descriptor, itemType)
		raw, _ = sjson.SetRawBytes(raw, fmt.Sprintf("%s.%d", path, index), converted)
	}
	return raw
}

func ensureKimiResponsesCallIdentity(raw []byte, index int) []byte {
	if strings.TrimSpace(gjson.GetBytes(raw, "id").String()) == "" {
		raw, _ = sjson.SetBytes(raw, "id", fmt.Sprintf("fc_kimi_%d", index))
	}
	if strings.TrimSpace(gjson.GetBytes(raw, "call_id").String()) == "" {
		raw, _ = sjson.SetBytes(raw, "call_id", kimiResponsesCallIDFromItemID(gjson.GetBytes(raw, "id").String(), index))
	}
	return raw
}

// TransformNonStream restores custom tool output items in a completed response.
func (s *KimiResponsesCustomToolState) TransformNonStream(response []byte) ([]byte, error) {
	if !s.Active() {
		return response, nil
	}
	if !gjson.ValidBytes(response) {
		return nil, fmt.Errorf("invalid Kimi Responses response JSON")
	}
	if gjson.GetBytes(response, "output").IsArray() {
		response = s.restoreResponseOutput(response, "output")
	}
	if gjson.GetBytes(response, "response.output").IsArray() {
		response = s.restoreResponseOutput(response, "response.output")
	}
	return response, nil
}

func (s *KimiResponsesCustomToolState) setSequence(raw []byte) []byte {
	sequence := int(gjson.GetBytes(raw, "sequence_number").Int())
	if sequence <= s.lastSequence {
		sequence = s.lastSequence + 1
	}
	s.lastSequence = sequence
	raw, _ = sjson.SetBytes(raw, "sequence_number", sequence)
	return raw
}

func (s *KimiResponsesCustomToolState) streamPayload(payload []byte) [][]byte {
	data := append([]byte("data: "), payload...)
	data = append(data, '\n', '\n')
	eventName := gjson.GetBytes(payload, "type").String()
	event := []byte("event: " + eventName)
	s.eventLine = nil
	return [][]byte{event, data}
}

func (s *KimiResponsesCustomToolState) customInputEvents(root, item gjson.Result, call *kimiCustomCall, arguments gjson.Result) [][]byte {
	if call == nil || call.inputEmitted || !isKimiNativeCustomToolDescriptor(call.descriptor) {
		return nil
	}
	if arguments.Type == gjson.String {
		call.arguments = arguments.String()
	} else if arguments.Exists() {
		call.arguments = arguments.Raw
	}
	input := kimiCustomToolInputFromArguments(gjson.Parse(call.arguments))
	call.inputEmitted = true
	if call.itemID == "" {
		call.itemID = item.Get("id").String()
	}
	if call.clientItemID == "" {
		call.clientItemID = call.itemID
		if strings.HasPrefix(call.clientItemID, "fc_") {
			call.clientItemID = "ctc_" + strings.TrimPrefix(call.clientItemID, "fc_")
		}
	}
	if call.callID == "" {
		call.callID = item.Get("call_id").String()
	}
	if item.Get("output_index").Exists() {
		call.outputIndex = int(item.Get("output_index").Int())
	} else if root.Get("output_index").Exists() {
		call.outputIndex = int(root.Get("output_index").Int())
	}
	makeEvent := func(eventType, field, value string) []byte {
		event := []byte(`{"type":""}`)
		event, _ = sjson.SetBytes(event, "type", eventType)
		if call.clientItemID != "" {
			event, _ = sjson.SetBytes(event, "item_id", call.clientItemID)
		}
		if call.callID != "" {
			event, _ = sjson.SetBytes(event, "call_id", call.callID)
		}
		event, _ = sjson.SetBytes(event, "output_index", call.outputIndex)
		event, _ = sjson.SetBytes(event, field, value)
		return s.setSequence(event)
	}
	frames := make([][]byte, 0, 4)
	frames = append(frames, s.streamPayload(makeEvent("response.custom_tool_call_input.delta", "delta", input))...)
	frames = append(frames, s.streamPayload(makeEvent("response.custom_tool_call_input.done", "input", input))...)
	return frames
}

func (s *KimiResponsesCustomToolState) streamOriginal(line []byte) [][]byte {
	if s.eventLine == nil {
		return [][]byte{bytes.Clone(line)}
	}
	event := bytes.Clone(s.eventLine)
	s.eventLine = nil
	return [][]byte{event, bytes.Clone(line)}
}

// Stream restores namespaces on function calls while retaining SSE framing.
func (s *KimiResponsesCustomToolState) Stream(line []byte) ([][]byte, error) {
	if !s.Active() {
		return [][]byte{line}, nil
	}
	if bytes.HasPrefix(line, []byte("event:")) {
		s.eventLine = bytes.Clone(line)
		return nil, nil
	}
	if !bytes.HasPrefix(line, []byte("data:")) {
		return [][]byte{line}, nil
	}
	payload := bytes.TrimSpace(line[len("data:"):])
	if bytes.Equal(payload, []byte("[DONE]")) {
		s.eventLine = nil
		return [][]byte{line}, nil
	}
	if !gjson.ValidBytes(payload) {
		return s.streamOriginal(line), nil
	}
	root := gjson.ParseBytes(payload)
	if sequence := int(root.Get("sequence_number").Int()); sequence > s.lastSequence {
		s.lastSequence = sequence
	}
	kind := root.Get("type").String()
	item := root.Get("item")
	if kind == "response.output_item.added" || kind == "response.output_item.done" {
		if !item.Exists() {
			return s.streamOriginal(line), nil
		}
		descriptor, ok := s.resolve(item.Get("name").String(), item.Get("namespace").String())
		if !ok {
			return s.streamOriginal(line), nil
		}
		itemRaw := []byte(item.Raw)
		if isKimiNativeCustomToolDescriptor(descriptor) {
			itemRaw = ensureKimiResponsesCallIdentity(itemRaw, int(root.Get("output_index").Int()))
		}
		item = gjson.ParseBytes(itemRaw)
		call := s.rememberCall(root, item, descriptor)
		var frames [][]byte
		if kind == "response.output_item.done" && isKimiNativeCustomToolDescriptor(descriptor) {
			frames = append(frames, s.customInputEvents(root, item, call, item.Get("arguments"))...)
		}
		converted := s.restoreFunctionItem([]byte(item.Raw), descriptor, "function_call")
		payload, _ = sjson.SetRawBytes(payload, "item", converted)
		payload = s.setSequence(payload)
		return append(frames, s.streamPayload(payload)...), nil
	}
	if kind == "response.function_call_arguments.delta" || kind == "response.function_call_arguments.done" {
		call := s.findCall(root, item)
		if call == nil {
			if descriptor, ok := s.resolve(root.Get("name").String(), root.Get("namespace").String()); ok {
				call = s.rememberCall(root, item, descriptor)
			}
		}
		if call == nil {
			return s.streamOriginal(line), nil
		}
		if kind == "response.function_call_arguments.delta" {
			if isKimiNativeCustomToolDescriptor(call.descriptor) {
				call.arguments += root.Get("delta").String()
				s.eventLine = nil
				return nil, nil
			}
			return s.streamOriginal(line), nil
		}
		if isKimiNativeCustomToolDescriptor(call.descriptor) {
			return s.customInputEvents(root, item, call, root.Get("arguments")), nil
		}
		return s.streamOriginal(line), nil
	}
	if strings.HasPrefix(kind, "response.") {
		if gjson.GetBytes(payload, "response.output").IsArray() {
			var frames [][]byte
			for index, outputItem := range gjson.GetBytes(payload, "response.output").Array() {
				if outputItem.Get("type").String() != "function_call" {
					continue
				}
				descriptor, ok := s.resolve(outputItem.Get("name").String(), outputItem.Get("namespace").String())
				if !ok || !isKimiNativeCustomToolDescriptor(descriptor) {
					continue
				}
				outputItem = gjson.ParseBytes(ensureKimiResponsesCallIdentity([]byte(outputItem.Raw), index))
				itemRootRaw, _ := sjson.SetBytes([]byte(`{}`), "output_index", index)
				call := s.rememberCall(gjson.ParseBytes(itemRootRaw), outputItem, descriptor)
				frames = append(frames, s.customInputEvents(gjson.ParseBytes(itemRootRaw), outputItem, call, outputItem.Get("arguments"))...)
			}
			updated := s.restoreResponseOutput(payload, "response.output")
			if bytes.Equal(updated, payload) {
				return append(frames, s.streamOriginal(line)...), nil
			}
			payload = s.setSequence(updated)
			return append(frames, s.streamPayload(payload)...), nil
		} else {
			return s.streamOriginal(line), nil
		}
	}
	return s.streamOriginal(line), nil
}

// FinishStream flushes no additional data. Kimi's terminal output item carries
// the complete argument snapshot when it omits function_call_arguments.done.
func (s *KimiResponsesCustomToolState) FinishStream() ([][]byte, error) {
	if s != nil {
		s.eventLine = nil
	}
	return nil, nil
}
