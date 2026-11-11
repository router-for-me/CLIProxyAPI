package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
)

// xAI executes these x_search subtools server-side but exposes their trace as
// client-style tool calls. Hide the trace so Responses clients do not execute it again.
type xaiInternalXSearchResponseFilter struct {
	enabled              bool
	clientDeclaredTools  map[xaiClientToolKey]struct{}
	droppedOutputIndexes map[int64]struct{}
	droppedItemIDs       map[string]struct{}
}

func newXAIInternalXSearchResponseFilter(enabled bool, clientDeclaredTools map[xaiClientToolKey]struct{}) *xaiInternalXSearchResponseFilter {
	filter := &xaiInternalXSearchResponseFilter{
		enabled:             enabled,
		clientDeclaredTools: clientDeclaredTools,
	}
	if enabled {
		filter.droppedOutputIndexes = make(map[int64]struct{})
		filter.droppedItemIDs = make(map[string]struct{})
	}
	return filter
}

func xaiToolsArrayHasNativeXSearch(tools ojson.Result) bool {
	if !tools.IsArray() {
		return false
	}
	for _, tool := range tools.Array() {
		if tool.Get("type").String() == "x_search" {
			return true
		}
	}
	return false
}

func xaiRequestHasNativeXSearch(body []byte) bool {
	if xaiToolsArrayHasNativeXSearch(ojson.GetBytesNoCopy(body, "tools")) {
		return true
	}
	input := ojson.GetBytesNoCopy(body, "input")
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		if item.Get("type").String() == "additional_tools" && xaiToolsArrayHasNativeXSearch(item.Get("tools")) {
			return true
		}
	}
	return false
}

func xaiRequestHasNativeXSearchDoc(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	if xaiToolsArrayHasNativeXSearch(doc.Get("tools")) {
		return true
	}
	input := doc.Get("input")
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		if item.Get("type").String() == "additional_tools" && xaiToolsArrayHasNativeXSearch(item.Get("tools")) {
			return true
		}
	}
	return false
}

// collectXAIClientDeclaredToolKeys records client-declared function/custom tools
// using the Responses post-restore identity (short name + optional namespace) and
// the effective upstream tool type after normalizeXAITool. Client custom tools
// are normalized to function before being sent to xAI, so keys use function for
// both declaration kinds. Must run before normalizeXAITools flattens namespace wrappers.
func collectXAIClientDeclaredToolKeys(body []byte) map[xaiClientToolKey]struct{} {
	return collectXAIClientDeclaredToolKeysResults(ojson.GetBytesNoCopy(body, "tools"), ojson.GetBytesNoCopy(body, "input"))
}

func collectXAIClientDeclaredToolKeysDoc(doc *ojson.Doc) map[xaiClientToolKey]struct{} {
	if doc == nil {
		return make(map[xaiClientToolKey]struct{})
	}
	return collectXAIClientDeclaredToolKeysResults(doc.Get("tools"), doc.Get("input"))
}

func collectXAIClientDeclaredToolKeysResults(toolsResult, inputResult ojson.Result) map[xaiClientToolKey]struct{} {
	keys := make(map[xaiClientToolKey]struct{})
	collect := func(tools ojson.Result) {
		if !tools.Exists() || !tools.IsArray() {
			return
		}
		for _, tool := range tools.Array() {
			switch toolType := strings.TrimSpace(tool.Get("type").String()); toolType {
			case xaiNamespaceToolType:
				namespaceName := strings.TrimSpace(tool.Get("name").String())
				if namespaceName == "" {
					continue
				}
				for _, nestedTool := range tool.Get("tools").Array() {
					nestedType := strings.TrimSpace(nestedTool.Get("type").String())
					if nestedType != xaiFunctionToolType && nestedType != xaiCustomToolType {
						continue
					}
					toolName := strings.TrimSpace(nestedTool.Get("name").String())
					if toolName == "" {
						continue
					}
					// normalizeXAITool converts custom → function before upstream send.
					keys[xaiClientToolKey{namespace: namespaceName, name: toolName, toolType: xaiEffectiveDeclaredToolType(nestedType)}] = struct{}{}
				}
			case xaiFunctionToolType, xaiCustomToolType:
				toolName := strings.TrimSpace(tool.Get("name").String())
				if toolName == "" {
					continue
				}
				// normalizeXAITool converts custom → function before upstream send.
				keys[xaiClientToolKey{namespace: "", name: toolName, toolType: xaiEffectiveDeclaredToolType(toolType)}] = struct{}{}
			}
		}
	}
	collect(toolsResult)
	if inputResult.Exists() && inputResult.IsArray() {
		for _, item := range inputResult.Array() {
			if item.Get("type").String() == "additional_tools" {
				collect(item.Get("tools"))
			}
		}
	}
	return keys
}

// xaiEffectiveDeclaredToolType returns the tool type actually sent upstream
// after normalizeXAITool. Client custom tools are rewritten to function.
func xaiEffectiveDeclaredToolType(toolType string) string {
	if strings.TrimSpace(toolType) == xaiCustomToolType {
		return xaiFunctionToolType
	}
	return strings.TrimSpace(toolType)
}

func xaiIsInternalXSearchToolName(name string) bool {
	switch strings.TrimSpace(name) {
	case "x_user_search", "x_semantic_search", "x_keyword_search", "x_thread_fetch":
		return true
	default:
		return false
	}
}

// xaiResponseCallDeclaredType maps a Responses output call type to the effective
// upstream tool declaration kind used when matching client-declared tools.
// Client custom tools are normalized to function before upstream send, so only
// function_call can match a client-declared same-name tool; custom_tool_call
// remains the internal X Search trace shape.
func xaiResponseCallDeclaredType(itemType string) string {
	switch strings.TrimSpace(itemType) {
	case "function_call":
		return xaiFunctionToolType
	case "custom_tool_call":
		return xaiCustomToolType
	default:
		return ""
	}
}

// xaiIsInternalXSearchCallID reports whether call_id matches the evidenced xAI
// X Search server-side trace prefix (xs_call...), as observed in Responses traffic
// for native x_search subtools (see issue #4282 / PR #4284 fixtures).
func xaiIsInternalXSearchCallID(callID string) bool {
	return strings.HasPrefix(strings.TrimSpace(callID), "xs_call")
}

// xaiIsInternalXSearchCall reports whether an output item is an xAI server-side
// X Search subtool trace that should be hidden from Responses clients.
//
// Evidence from xAI Responses traffic (issue #4282 / PR #4284):
//   - native x_search subtools are emitted as custom_tool_call items named
//     x_user_search / x_semantic_search / x_keyword_search / x_thread_fetch
//   - those traces commonly use call_id values prefixed with "xs_call"
//
// Client tools that share a short name are preserved only when the response call
// kind matches the effective upstream declaration type. Because normalizeXAITool
// rewrites client custom → function, a client custom x_keyword_search is keyed as
// function and therefore preserves function_call while still filtering genuine
// internal custom_tool_call / xs_call* traces. Namespaced restored client tools
// are never treated as internal.
func xaiIsInternalXSearchCall(item ojson.Result, clientDeclaredTools map[xaiClientToolKey]struct{}) bool {
	itemType := strings.TrimSpace(item.Get("type").String())
	declaredType := xaiResponseCallDeclaredType(itemType)
	if declaredType == "" {
		return false
	}
	name := strings.TrimSpace(item.Get("name").String())
	if !xaiIsInternalXSearchToolName(name) {
		return false
	}
	namespace := strings.TrimSpace(item.Get("namespace").String())
	// Namespaced calls are restored client tools, never xAI internal X Search traces.
	if namespace != "" {
		return false
	}
	// Evidenced internal call_id prefix always identifies server-side X Search traces,
	// even when a client tool reuses the same short name.
	if xaiIsInternalXSearchCallID(item.Get("call_id").String()) {
		return true
	}
	// Preserve only client tools whose effective upstream declaration kind matches
	// this call type (function_call ↔ function after custom normalization).
	if _, declared := clientDeclaredTools[xaiClientToolKey{namespace: namespace, name: name, toolType: declaredType}]; declared {
		return false
	}
	return true
}

func (f *xaiInternalXSearchResponseFilter) apply(eventData []byte) []byte {
	if f == nil || !f.enabled || len(eventData) == 0 || !ojson.ValidBytes(eventData) {
		return eventData
	}

	if item := ojson.GetBytesNoCopy(eventData, "item"); xaiIsInternalXSearchCall(item, f.clientDeclaredTools) {
		f.recordDroppedItem(eventData, item)
		return nil
	}

	eventData = f.filterCompletedOutput(eventData)
	if f.referencesDroppedItem(eventData) {
		return nil
	}
	return f.compactOutputIndex(eventData)
}

func (f *xaiInternalXSearchResponseFilter) recordDroppedItem(eventData []byte, item ojson.Result) {
	if outputIndex := ojson.GetBytesNoCopy(eventData, "output_index"); outputIndex.Exists() {
		f.droppedOutputIndexes[outputIndex.Int()] = struct{}{}
	}
	for _, path := range []string{"id", "call_id"} {
		if id := strings.TrimSpace(item.Get(path).String()); id != "" {
			f.droppedItemIDs[strings.Clone(id)] = struct{}{}
		}
	}
}

func (f *xaiInternalXSearchResponseFilter) referencesDroppedItem(eventData []byte) bool {
	if outputIndex := ojson.GetBytesNoCopy(eventData, "output_index"); outputIndex.Exists() {
		if _, dropped := f.droppedOutputIndexes[outputIndex.Int()]; dropped {
			return true
		}
	}
	for _, path := range []string{"item_id", "call_id"} {
		id := strings.TrimSpace(ojson.GetBytesNoCopy(eventData, path).String())
		if _, dropped := f.droppedItemIDs[id]; id != "" && dropped {
			return true
		}
	}
	return false
}

func (f *xaiInternalXSearchResponseFilter) compactOutputIndex(eventData []byte) []byte {
	outputIndex := ojson.GetBytesNoCopy(eventData, "output_index")
	if !outputIndex.Exists() {
		return eventData
	}
	original := outputIndex.Int()
	removedBefore := int64(0)
	for dropped := range f.droppedOutputIndexes {
		if dropped < original {
			removedBefore++
		}
	}
	if removedBefore == 0 {
		return eventData
	}
	updated, errSet := ojson.SetBytes(eventData, "output_index", original-removedBefore)
	if errSet != nil {
		return eventData
	}
	return updated
}

func (f *xaiInternalXSearchResponseFilter) filterCompletedOutput(eventData []byte) []byte {
	output := ojson.GetBytesNoCopy(eventData, "response.output")
	if !output.IsArray() {
		return eventData
	}
	var clientDeclaredTools map[xaiClientToolKey]struct{}
	if f != nil {
		clientDeclaredTools = f.clientDeclaredTools
	}
	items := make([]json.RawMessage, 0, len(output.Array()))
	changed := false
	for _, item := range output.Array() {
		if xaiIsInternalXSearchCall(item, clientDeclaredTools) {
			changed = true
			continue
		}
		items = append(items, json.RawMessage(item.Raw))
	}
	if !changed {
		return eventData
	}
	rawOutput, errMarshal := json.Marshal(items)
	if errMarshal != nil {
		return eventData
	}
	updated, errSet := ojson.SetRawBytes(eventData, "response.output", rawOutput)
	if errSet != nil {
		return eventData
	}
	return updated
}

func normalizeXAIInputNamespaceToolCalls(body []byte) []byte {
	return normalizeXAIInputNamespaceToolCallsWithFold(body, xaiShouldFoldNamespaceTools(body, false))
}

func normalizeXAIInputNamespaceToolCallsDoc(doc *ojson.Doc) bool {
	return normalizeXAIInputNamespaceToolCallsWithFoldDoc(doc, xaiShouldFoldNamespaceToolsDoc(doc, false))
}

func normalizeXAIInputNamespaceToolCallsWithFold(body []byte, shouldFold bool) []byte {
	if !ojson.ValidBytes(body) {
		return body
	}
	doc := ojson.NewDoc(body)
	if !normalizeXAIInputNamespaceToolCallsWithFoldDoc(doc, shouldFold) {
		return body
	}
	return doc.Bytes()
}

func normalizeXAIInputNamespaceToolCallsWithFoldDoc(doc *ojson.Doc, shouldFold bool) bool {
	if doc == nil {
		return false
	}
	input := doc.Get("input")
	if !input.Exists() || !input.IsArray() {
		return false
	}
	changed := false
	for index, item := range input.Array() {
		if item.Get("type").String() != "function_call" {
			continue
		}
		namespaceName := strings.TrimSpace(item.Get("namespace").String())
		toolName := strings.TrimSpace(item.Get("name").String())
		if namespaceName == "" {
			continue
		}
		qualifiedName := qualifyXAINamespaceToolName(namespaceName, toolName)
		var isFolded bool
		if xaiHasFunctionToolNamedDoc(doc, namespaceName) {
			isFolded = true
		} else if xaiHasFunctionToolNamedDoc(doc, qualifiedName) {
			isFolded = false
		} else {
			isFolded = shouldFold
		}
		if isFolded {
			namePath := fmt.Sprintf("input.%d.name", index)
			namespacePath := fmt.Sprintf("input.%d.namespace", index)
			argsPath := fmt.Sprintf("input.%d.arguments", index)

			dispatcherArgs := map[string]any{
				"name": toolName,
			}
			if rawArgs := item.Get("arguments").String(); rawArgs != "" {
				if ojson.Valid(rawArgs) {
					dispatcherArgs["arguments"] = json.RawMessage(rawArgs)
				} else {
					dispatcherArgs["arguments"] = rawArgs
				}
			}
			encodedArgs, errMarshal := json.Marshal(dispatcherArgs)
			if errMarshal != nil {
				continue
			}

			if errSet := doc.Set(namePath, namespaceName); errSet != nil {
				continue
			}
			if errSet := doc.Set(argsPath, string(encodedArgs)); errSet != nil {
				continue
			}
			if errDelete := doc.Delete(namespacePath); errDelete != nil {
				continue
			}
			changed = true
			continue
		}

		if qualifiedName == "" {
			continue
		}
		namePath := fmt.Sprintf("input.%d.name", index)
		namespacePath := fmt.Sprintf("input.%d.namespace", index)
		if errSet := doc.Set(namePath, qualifiedName); errSet != nil {
			continue
		}
		if errDelete := doc.Delete(namespacePath); errDelete != nil {
			continue
		}
		changed = true
	}
	return changed
}

type xaiNamespaceRestorer struct {
	refs              map[string]xaiNamespaceToolRef
	dispatcherItemIDs map[string]string
}

func newXAINamespaceRestorer(refs map[string]xaiNamespaceToolRef) *xaiNamespaceRestorer {
	return &xaiNamespaceRestorer{
		refs:              refs,
		dispatcherItemIDs: make(map[string]string),
	}
}

func (r *xaiNamespaceRestorer) restore(data []byte) []byte {
	if r == nil || len(r.refs) == 0 || len(data) == 0 || !ojson.ValidBytes(data) {
		return data
	}
	eventType := ojson.GetBytesNoCopy(data, "type").String()
	switch eventType {
	case "response.output_item.added":
		item := ojson.GetBytesNoCopy(data, "item")
		if item.Get("type").String() == "function_call" {
			name := strings.TrimSpace(item.Get("name").String())
			itemID := strings.TrimSpace(item.Get("id").String())
			if ref, ok := r.refs[name]; ok && ref.isDispatcher {
				if itemID != "" {
					r.dispatcherItemIDs[strings.Clone(itemID)] = ref.namespace
				}
				data, _ = ojson.SetBytes(data, "item.namespace", ref.namespace)
			}
		}
		return data

	case "response.function_call_arguments.done":
		itemID := strings.TrimSpace(ojson.GetBytesNoCopy(data, "item_id").String())
		if namespaceName, isDisp := r.dispatcherItemIDs[itemID]; isDisp {
			rawArgs := ojson.GetBytesNoCopy(data, "arguments").String()
			if _, childArgs, ok := unwrapXAIDispatcherArguments(rawArgs, namespaceName, r.refs); ok {
				updated, errSet := ojson.SetBytes(data, "arguments", string(childArgs))
				if errSet == nil {
					data = updated
				}
			}
		}
		return data

	default:
		doc := ojson.NewDoc(data)
		changed := r.restoreAtPathDoc(doc, "item")
		output := doc.Get("response.output")
		if output.Exists() && output.IsArray() {
			for index := range output.Array() {
				if r.restoreAtPathDoc(doc, fmt.Sprintf("response.output.%d", index)) {
					changed = true
				}
			}
		}
		if !changed {
			return data
		}
		return doc.Bytes()
	}
}

func (r *xaiNamespaceRestorer) restoreAtPath(data []byte, path string) []byte {
	doc := ojson.NewDoc(data)
	if !r.restoreAtPathDoc(doc, path) {
		return data
	}
	return doc.Bytes()
}

func (r *xaiNamespaceRestorer) restoreAtPathDoc(doc *ojson.Doc, path string) bool {
	if doc == nil || doc.Get(path+".type").String() != "function_call" {
		return false
	}
	qualifiedName := strings.TrimSpace(doc.Get(path + ".name").String())
	ref, ok := r.refs[qualifiedName]
	if !ok {
		return false
	}
	if ref.isDispatcher {
		rawArgs := doc.Get(path + ".arguments").String()
		childName, childArgs, unwrapped := unwrapXAIDispatcherArguments(rawArgs, ref.namespace, r.refs)
		if !unwrapped && childName == "" {
			childName = ref.name
		}
		if errSet := doc.Set(path+".namespace", ref.namespace); errSet != nil {
			return false
		}
		if childName != "" {
			_ = doc.Set(path+".name", childName)
		}
		if len(childArgs) > 0 {
			_ = doc.Set(path+".arguments", string(childArgs))
		}
		return true
	}

	if errSet := doc.Set(path+".name", ref.name); errSet != nil {
		return false
	}
	if errSet := doc.Set(path+".namespace", ref.namespace); errSet != nil {
		return false
	}
	return true
}

func unwrapXAIDispatcherArguments(rawArgs string, namespaceName string, refs map[string]xaiNamespaceToolRef) (string, []byte, bool) {
	if !ojson.Valid(rawArgs) {
		return "", nil, false
	}
	argsParsed := ojson.Parse(rawArgs)
	nameField := argsParsed.Get("name")
	if !nameField.Exists() || nameField.Type != ojson.String {
		return "", nil, false
	}
	childName := strings.TrimSpace(nameField.String())
	if childName == "" {
		return "", nil, false
	}

	if namespaceName != "" {
		if dispRef, exists := refs[namespaceName]; exists && dispRef.isDispatcher && len(dispRef.children) > 0 {
			if _, isChild := dispRef.children[childName]; !isChild {
				return "", nil, false
			}
		} else {
			qualified := qualifyXAINamespaceToolName(namespaceName, childName)
			if ref, exists := refs[qualified]; exists && ref.isDispatcher {
				return "", nil, false
			}
		}
	} else {
		isChildOfDispatcher := false
		for _, ref := range refs {
			if ref.isDispatcher && (ref.name == childName || ref.namespace == childName) {
				isChildOfDispatcher = true
				break
			}
		}
		if !isChildOfDispatcher && !argsParsed.Get("arguments").Exists() {
			return "", nil, false
		}
	}

	var childArgs []byte
	if argsField := argsParsed.Get("arguments"); argsField.Exists() {
		if argsField.Type == ojson.String {
			childArgs = []byte(argsField.String())
		} else {
			childArgs = []byte(argsField.Raw)
		}
	} else {
		cleaned, errDel := ojson.DeleteBytes([]byte(rawArgs), "name")
		if errDel == nil && len(cleaned) > 0 && string(cleaned) != "{}" {
			childArgs = cleaned
		} else {
			childArgs = []byte("{}")
		}
	}
	if len(childArgs) == 0 {
		childArgs = []byte("{}")
	}
	return childName, childArgs, true
}

func restoreXAINamespaceToolCalls(data []byte, refs map[string]xaiNamespaceToolRef) []byte {
	restorer := newXAINamespaceRestorer(refs)
	return restorer.restore(data)
}

// restoreXAIClientWebSearchName rewrites the given alias
// back to the original client tool name "web_search" across event items and
// completed outputs. Only unnamespaced tool calls are restored so namespaced
// tools (e.g. acme.clientfn_web_search) are preserved untouched.
func restoreXAIClientWebSearchName(data []byte, alias string) []byte {
	if alias == "" || !bytes.Contains(data, []byte(alias)) {
		return data
	}
	if !ojson.ValidBytes(data) {
		return data
	}
	doc := ojson.NewDoc(data)
	changed := false

	// Check item (e.g. response.output_item.added / response.output_item.done)
	if strings.TrimSpace(doc.Get("item.namespace").String()) == "" {
		if strings.TrimSpace(doc.Get("item.name").String()) == alias {
			if doc.Set("item.name", xaiWebSearchToolType) == nil {
				changed = true
			}
		}
		if strings.TrimSpace(doc.Get("item.function.name").String()) == alias {
			if doc.Set("item.function.name", xaiWebSearchToolType) == nil {
				changed = true
			}
		}
	}

	// Check response.output array (e.g. response.completed / response.incomplete)
	respOutput := doc.Get("response.output")
	if respOutput.IsArray() {
		for idx, item := range respOutput.Array() {
			if strings.TrimSpace(item.Get("namespace").String()) != "" {
				continue
			}
			if strings.TrimSpace(item.Get("name").String()) == alias {
				if doc.Set(fmt.Sprintf("response.output.%d.name", idx), xaiWebSearchToolType) == nil {
					changed = true
				}
			}
			if strings.TrimSpace(item.Get("function.name").String()) == alias {
				if doc.Set(fmt.Sprintf("response.output.%d.function.name", idx), xaiWebSearchToolType) == nil {
					changed = true
				}
			}
		}
	}

	// Check top-level output array (non-stream responses translated)
	topOutput := doc.Get("output")
	if topOutput.IsArray() {
		for idx, item := range topOutput.Array() {
			if strings.TrimSpace(item.Get("namespace").String()) != "" {
				continue
			}
			if strings.TrimSpace(item.Get("name").String()) == alias {
				if doc.Set(fmt.Sprintf("output.%d.name", idx), xaiWebSearchToolType) == nil {
					changed = true
				}
			}
			if strings.TrimSpace(item.Get("function.name").String()) == alias {
				if doc.Set(fmt.Sprintf("output.%d.function.name", idx), xaiWebSearchToolType) == nil {
					changed = true
				}
			}
		}
	}

	// Check top-level name
	if strings.TrimSpace(doc.Get("namespace").String()) == "" {
		if strings.TrimSpace(doc.Get("name").String()) == alias {
			if doc.Set("name", xaiWebSearchToolType) == nil {
				changed = true
			}
		}
	}

	if !changed {
		return data
	}
	return doc.Bytes()
}

// normalizeXAIObjectRootUnionBranchTypes makes untyped root union branches
// explicitly object-only when the parameter root already permits only objects.
// This preserves the original schema semantics while satisfying xAI validation.
func normalizeXAIObjectRootUnionBranchTypes(tool []byte) ([]byte, bool, bool) {
	doc := ojson.NewDoc(tool)
	changed, ok := normalizeXAIObjectRootUnionBranchTypesDoc(doc)
	if !ok {
		return tool, false, false
	}
	if !changed {
		return tool, false, true
	}
	return doc.Bytes(), true, true
}

func normalizeXAIObjectRootUnionBranchTypesDoc(doc *ojson.Doc) (bool, bool) {
	if doc == nil {
		return false, false
	}
	parameters := doc.Get("parameters")
	rootType := parameters.Get("type")
	if rootType.Type != ojson.String || rootType.String() != "object" {
		return false, true
	}

	var paths []string
	for _, unionName := range []string{"anyOf", "oneOf"} {
		union := parameters.Get(unionName)
		if !union.IsArray() {
			continue
		}
		for index, branch := range union.Array() {
			if !branch.IsObject() || branch.Get("type").Exists() || branch.Get("$ref").Exists() {
				continue
			}
			paths = append(paths, fmt.Sprintf("parameters.%s.%d.type", unionName, index))
		}
	}
	if len(paths) == 0 {
		return false, true
	}
	for _, p := range paths {
		if errSet := doc.Set(p, "object"); errSet != nil {
			return false, false
		}
	}
	return true, true
}

func xaiSchemaTypeIsObjectOnly(schemaType ojson.Result) bool {
	if schemaType.Type == ojson.String {
		return strings.EqualFold(strings.TrimSpace(schemaType.String()), "object")
	}
	if !schemaType.IsArray() {
		return false
	}
	types := schemaType.Array()
	if len(types) == 0 {
		return false
	}
	for _, schemaTypeItem := range types {
		if schemaTypeItem.Type != ojson.String || !strings.EqualFold(strings.TrimSpace(schemaTypeItem.String()), "object") {
			return false
		}
	}
	return true
}

func isXAICodexAppAutomationUpdate(toolName, namespaceName string) bool {
	cleanNamespace := strings.TrimPrefix(strings.TrimSpace(namespaceName), "mcp__")
	cleanTool := strings.TrimPrefix(strings.TrimSpace(toolName), "mcp__")
	if strings.EqualFold(cleanTool, xaiAutomationUpdateToolName) && (strings.EqualFold(cleanNamespace, xaiCodexAppNamespaceName) || strings.EqualFold(cleanNamespace, "codex_apps")) {
		return true
	}
	if strings.EqualFold(cleanTool, xaiCodexAppNamespaceName+"__"+xaiAutomationUpdateToolName) || strings.EqualFold(cleanTool, "codex_apps__"+xaiAutomationUpdateToolName) {
		return true
	}
	return false
}

// xaiFunctionParametersNeedSimplification reports whether a function tool, or
// a custom tool normalized to a function, has a schema that xAI cannot accept.
func xaiFunctionParametersNeedSimplification(tool ojson.Result, namespaceName string) bool {
	return xaiFunctionParametersNeedSimplificationFields(tool.Get("type").String(), tool.Get("name").String(), tool.Get("parameters"), namespaceName)
}

func xaiFunctionParametersNeedSimplificationFields(toolType, toolName string, parameters ojson.Result, namespaceName string) bool {
	toolType = strings.TrimSpace(toolType)
	isFunction := strings.EqualFold(toolType, xaiFunctionToolType)
	isNormalizedCustom := strings.EqualFold(toolType, xaiCustomToolType)
	if !isFunction && !isNormalizedCustom {
		return false
	}

	toolName = strings.TrimSpace(toolName)
	if isFunction && isXAICodexAppAutomationUpdate(toolName, namespaceName) {
		return true
	}

	for _, unionName := range []string{"anyOf", "oneOf"} {
		union := parameters.Get(unionName)
		if !union.IsArray() {
			continue
		}
		for _, branch := range union.Array() {
			if branch.Get("$ref").Exists() || !xaiSchemaTypeIsObjectOnly(branch.Get("type")) {
				return true
			}
		}
	}
	return false
}

func sanitizeXAIInputEncryptedContent(body []byte) []byte {
	return sanitizeXAIInputEncryptedContentWithOptions(body, false)
}

func sanitizeXAIInputEncryptedContentDoc(doc *ojson.Doc) bool {
	return sanitizeXAIInputEncryptedContentWithOptionsDoc(doc, false)
}

func sanitizeXAIInputEncryptedContentForBaseURL(body []byte, baseURL string) []byte {
	return sanitizeXAIInputEncryptedContentWithOptions(body, isOpenRouterUpstreamURL(baseURL))
}

func sanitizeXAIInputEncryptedContentForBaseURLDoc(doc *ojson.Doc, baseURL string) bool {
	return sanitizeXAIInputEncryptedContentWithOptionsDoc(doc, isOpenRouterUpstreamURL(baseURL))
}

func sanitizeXAIInputEncryptedContentWithOptions(body []byte, allowRouterSuffix bool) []byte {
	doc := ojson.NewDoc(body)
	if !sanitizeXAIInputEncryptedContentWithOptionsDoc(doc, allowRouterSuffix) {
		return body
	}
	return doc.Bytes()
}

func sanitizeXAIInputEncryptedContentWithOptionsDoc(doc *ojson.Doc, allowRouterSuffix bool) bool {
	if doc == nil {
		return false
	}
	input := doc.Get("input")
	if !input.Exists() || !input.IsArray() {
		return false
	}
	items := make([]json.RawMessage, 0, len(input.Array()))
	changed := false
	dropCount := 0
	firstReason := ""
	firstItemType := ""
	for _, item := range input.Array() {
		itemType := strings.TrimSpace(item.Get("type").String())
		if itemType != "reasoning" && itemType != "compaction" {
			items = append(items, json.RawMessage(item.Raw))
			continue
		}
		encryptedContent := item.Get("encrypted_content")
		if !encryptedContent.Exists() {
			items = append(items, json.RawMessage(item.Raw))
			continue
		}
		reason := ""
		rewrittenSig := ""
		switch encryptedContent.Type {
		case ojson.String:
			rawSig := encryptedContent.String()
			if info, err := signature.InspectGrokEncryptedContent(rawSig); err != nil {
				reason = err.Error()
			} else if info.RouterSuffix != "" && !allowRouterSuffix {
				rewrittenSig = stripRouterSuffix(rawSig, info.RouterSuffix)
			}
		case ojson.Null:
			reason = "encrypted_content is null"
		default:
			reason = fmt.Sprintf("encrypted_content must be a string, got %s", encryptedContent.Type.String())
		}
		if reason == "" {
			if rewrittenSig != "" {
				if next, errSet := ojson.SetBytes([]byte(item.Raw), "encrypted_content", rewrittenSig); errSet == nil {
					items = append(items, json.RawMessage(next))
					changed = true
					continue
				}
			}
			items = append(items, json.RawMessage(item.Raw))
			continue
		}

		if itemType == "compaction" {
			changed = true
			dropCount++
			if firstReason == "" {
				firstReason = reason
				firstItemType = itemType
			}
			continue
		}

		next, err := ojson.DeleteBytes([]byte(item.Raw), "encrypted_content")
		if err != nil {
			items = append(items, json.RawMessage(item.Raw))
			continue
		}
		items = append(items, json.RawMessage(next))
		changed = true
		dropCount++
		if firstReason == "" {
			firstReason = reason
			firstItemType = itemType
		}
	}
	if !changed {
		return false
	}
	rawInput, err := json.Marshal(items)
	if err != nil {
		return false
	}
	if errSet := doc.SetRawBytes("input", rawInput); errSet != nil {
		return false
	}
	if dropCount > 0 {
		log.WithFields(log.Fields{
			"component":       "xai_encrypted_content_sanitizer",
			"dropped":         dropCount,
			"first_item_type": firstItemType,
			"first_reason":    firstReason,
		}).Debug("xai executor: removed invalid encrypted_content before upstream")
	}
	mergeAdjacentXAIInputReasoningSummariesDoc(doc)
	return true
}

func normalizeXAIInputReasoningItems(body []byte) []byte {
	doc := ojson.NewDoc(body)
	if !normalizeXAIInputReasoningItemsDoc(doc) {
		return body
	}
	return doc.Bytes()
}

func normalizeXAIInputReasoningItemsDoc(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	input := doc.Get("input")
	if !input.Exists() || !input.IsArray() {
		return false
	}

	var deletePaths []string
	for i, item := range input.Array() {
		if item.Get("type").String() != "reasoning" {
			continue
		}
		if content := item.Get("content"); content.Exists() && content.Type == ojson.Null {
			deletePaths = append(deletePaths, fmt.Sprintf("input.%d.content", i))
		}
		if encryptedContent := item.Get("encrypted_content"); encryptedContent.Exists() && encryptedContent.Type == ojson.Null {
			deletePaths = append(deletePaths, fmt.Sprintf("input.%d.encrypted_content", i))
		}
	}
	changed := false
	for _, p := range deletePaths {
		if errDel := doc.Delete(p); errDel != nil {
			return false
		}
		changed = true
	}
	if mergeAdjacentXAIInputReasoningSummariesDoc(doc) {
		changed = true
	}
	return changed
}

func mergeAdjacentXAIInputReasoningSummaries(body []byte) []byte {
	doc := ojson.NewDoc(body)
	if !mergeAdjacentXAIInputReasoningSummariesDoc(doc) {
		return body
	}
	return doc.Bytes()
}

func mergeAdjacentXAIInputReasoningSummariesDoc(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	input := doc.Get("input")
	if !input.Exists() || !input.IsArray() {
		return false
	}

	changed := false
	items := make([]json.RawMessage, 0, len(input.Array()))
	for _, item := range input.Array() {
		if len(items) > 0 && canMergeXAIReasoningSummary(items[len(items)-1], item) {
			merged, ok := appendXAIReasoningSummary(items[len(items)-1], item.Get("summary").Array())
			if ok {
				items[len(items)-1] = json.RawMessage(merged)
				changed = true
				continue
			}
		}
		items = append(items, json.RawMessage(item.Raw))
	}
	if !changed {
		return false
	}

	rawInput, errMarshal := json.Marshal(items)
	if errMarshal != nil {
		return false
	}
	return doc.SetRawBytes("input", rawInput) == nil
}

func canMergeXAIReasoningSummary(previous json.RawMessage, current ojson.Result) bool {
	previousItem := util.ParseGJSONBytesNoCopy(previous)
	if previousItem.Get("type").String() != "reasoning" || current.Get("type").String() != "reasoning" {
		return false
	}
	if !previousItem.Get("summary").IsArray() || !current.Get("summary").IsArray() {
		return false
	}
	if len(current.Get("summary").Array()) == 0 {
		return false
	}
	for name := range current.Map() {
		if name != "type" && name != "summary" {
			return false
		}
	}
	return true
}

func appendXAIReasoningSummary(previous json.RawMessage, currentSummary []ojson.Result) ([]byte, bool) {
	summary := ojson.GetBytesNoCopy(previous, "summary")
	if !summary.IsArray() {
		return previous, false
	}
	nextIndex := len(summary.Array())
	doc := ojson.NewDoc(previous)
	for i, item := range currentSummary {
		if errSet := doc.SetRawBytes(fmt.Sprintf("summary.%d", nextIndex+i), []byte(item.Raw)); errSet != nil {
			return previous, false
		}
	}
	return doc.Bytes(), true
}

func xaiNormalizeReasoningSummaryEventLine(line []byte, eventName string) []byte {
	if eventName == "" && bytes.HasPrefix(line, xaiEventTag) {
		eventName = strings.TrimSpace(string(line[len(xaiEventTag):]))
	}
	eventName = xaiNormalizeReasoningSummaryEventName(eventName)
	if eventName == "" {
		return bytes.Clone(line)
	}
	return []byte("event: " + eventName)
}

func xaiNormalizeReasoningSummaryEventName(eventName string) string {
	switch eventName {
	case "response.reasoning_text.delta":
		return "response.reasoning_summary_text.delta"
	case "response.reasoning_text.done":
		return "response.reasoning_summary_part.done"
	default:
		return eventName
	}
}

func xaiNormalizeReasoningSummaryData(eventData []byte) []byte {
	if len(eventData) == 0 || !ojson.ValidBytes(eventData) {
		return eventData
	}

	doc := ojson.NewDoc(eventData)
	changed := false
	switch doc.Get("type").String() {
	case "response.reasoning_text.delta":
		if doc.Set("type", "response.reasoning_summary_text.delta") == nil {
			changed = true
		}
		if xaiNormalizeReasoningSummaryIndexDoc(doc) {
			changed = true
		}
	case "response.reasoning_text.done":
		if doc.Set("type", "response.reasoning_summary_part.done") == nil {
			changed = true
		}
		if doc.Set("part.type", "summary_text") == nil {
			changed = true
		}
		if text := doc.Get("text"); text.Exists() {
			if doc.Set("part.text", text.String()) == nil {
				changed = true
			}
		}
		if doc.Delete("text") == nil {
			changed = true
		}
		if xaiNormalizeReasoningSummaryIndexDoc(doc) {
			changed = true
		}
	case "response.content_part.added":
		if doc.Get("part.type").String() == "reasoning_text" {
			if doc.Set("type", "response.reasoning_summary_part.added") == nil {
				changed = true
			}
			if doc.Set("part.type", "summary_text") == nil {
				changed = true
			}
			if xaiNormalizeReasoningSummaryIndexDoc(doc) {
				changed = true
			}
		}
	case "response.content_part.done":
		if doc.Get("part.type").String() == "reasoning_text" {
			if doc.Set("type", "response.reasoning_summary_part.done") == nil {
				changed = true
			}
			if doc.Set("part.type", "summary_text") == nil {
				changed = true
			}
			if xaiNormalizeReasoningSummaryIndexDoc(doc) {
				changed = true
			}
		}
	}

	if item := doc.Get("item"); item.Exists() && item.Type == ojson.JSON {
		updatedItem := xaiNormalizeReasoningOutputItem([]byte(item.Raw))
		if !bytes.Equal(updatedItem, []byte(item.Raw)) {
			if doc.SetRawBytes("item", updatedItem) == nil {
				changed = true
			}
		}
	}
	if output := doc.Get("response.output"); output.IsArray() {
		updatedOutput, outputChanged := xaiNormalizeReasoningOutputItems(output.Array())
		if outputChanged {
			if doc.SetRawBytes("response.output", updatedOutput) == nil {
				changed = true
			}
		}
	}

	if !changed {
		return eventData
	}
	return doc.Bytes()
}

func xaiNormalizeReasoningSummaryDataEvents(eventData []byte) [][]byte {
	if len(eventData) == 0 || !ojson.ValidBytes(eventData) {
		return [][]byte{eventData}
	}
	if ojson.GetBytesNoCopy(eventData, "type").String() != "response.reasoning_text.done" {
		return [][]byte{xaiNormalizeReasoningSummaryData(eventData)}
	}

	textDoneDoc := ojson.NewDoc(eventData)
	_ = textDoneDoc.Set("type", "response.reasoning_summary_text.done")
	xaiNormalizeReasoningSummaryIndexDoc(textDoneDoc)
	textDone := textDoneDoc.Bytes()
	partDone := xaiNormalizeReasoningSummaryData(eventData)
	return [][]byte{textDone, partDone}
}

func xaiNormalizeReasoningSummaryIndex(eventData []byte) []byte {
	doc := ojson.NewDoc(eventData)
	if !xaiNormalizeReasoningSummaryIndexDoc(doc) {
		return eventData
	}
	return doc.Bytes()
}

func xaiNormalizeReasoningSummaryIndexDoc(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	changed := false
	contentIndex := doc.Get("content_index")
	if contentIndex.Exists() && contentIndex.Raw != "" && !doc.Get("summary_index").Exists() {
		if doc.SetRawBytes("summary_index", []byte(contentIndex.Raw)) == nil {
			changed = true
		}
	}
	if doc.Delete("content_index") == nil {
		changed = true
	}
	return changed
}

func xaiNormalizeReasoningOutputItems(items []ojson.Result) ([]byte, bool) {
	var buf bytes.Buffer
	buf.WriteByte('[')
	changed := false
	for i, item := range items {
		if i > 0 {
			buf.WriteByte(',')
		}
		updatedItem := xaiNormalizeReasoningOutputItem([]byte(item.Raw))
		if !bytes.Equal(updatedItem, []byte(item.Raw)) {
			changed = true
		}
		buf.Write(updatedItem)
	}
	buf.WriteByte(']')
	return buf.Bytes(), changed
}

func xaiNormalizeReasoningOutputItem(item []byte) []byte {
	if !ojson.ValidBytes(item) || ojson.GetBytesNoCopy(item, "type").String() != "reasoning" {
		return item
	}

	doc := ojson.NewDoc(item)
	changed := false
	if summary := doc.Get("summary"); summary.IsArray() {
		updatedSummary, summaryChanged := xaiNormalizeReasoningSummaryItems(summary.Array())
		if summaryChanged {
			if doc.SetRawBytes("summary", updatedSummary) == nil {
				changed = true
			}
		}
	}

	content := doc.Get("content")
	if !content.IsArray() {
		if !changed {
			return item
		}
		return doc.Bytes()
	}

	summaryItems := make([]ojson.Result, 0, len(content.Array()))
	for _, part := range content.Array() {
		if part.Get("type").String() == "reasoning_text" {
			summaryItems = append(summaryItems, part)
		}
	}
	if len(summaryItems) == 0 {
		if !changed {
			return item
		}
		return doc.Bytes()
	}

	updatedSummary, _ := xaiNormalizeReasoningSummaryItems(summaryItems)
	_ = doc.SetRawBytes("summary", updatedSummary)
	_ = doc.Delete("content")
	return doc.Bytes()
}

func xaiNormalizeReasoningSummaryItems(items []ojson.Result) ([]byte, bool) {
	var buf bytes.Buffer
	buf.WriteByte('[')
	changed := false
	for i, item := range items {
		if i > 0 {
			buf.WriteByte(',')
		}
		itemRaw := []byte(item.Raw)
		if item.Get("type").String() == "reasoning_text" {
			var errSet error
			itemRaw, errSet = ojson.SetBytes(itemRaw, "type", "summary_text")
			if errSet == nil {
				changed = true
			}
		}
		buf.Write(itemRaw)
	}
	buf.WriteByte(']')
	return buf.Bytes(), changed
}

func xaiCollectOutputItemDone(eventData []byte, outputItemsByIndex map[int64][]byte, outputItemsFallback *[][]byte) {
	itemResult := ojson.GetBytesNoCopy(eventData, "item")
	if !itemResult.Exists() || itemResult.Type != ojson.JSON {
		return
	}
	outputIndexResult := ojson.GetBytesNoCopy(eventData, "output_index")
	if outputIndexResult.Exists() {
		outputItemsByIndex[outputIndexResult.Int()] = []byte(itemResult.Raw)
		return
	}
	*outputItemsFallback = append(*outputItemsFallback, []byte(itemResult.Raw))
}

func xaiPatchCompletedOutput(eventData []byte, outputItemsByIndex map[int64][]byte, outputItemsFallback [][]byte) []byte {
	eventData = helps.EnsureResponsesUsageDetails(eventData)
	outputResult := ojson.GetBytesNoCopy(eventData, "response.output")
	shouldPatchOutput := (!outputResult.Exists() || !outputResult.IsArray() || len(outputResult.Array()) == 0) && (len(outputItemsByIndex) > 0 || len(outputItemsFallback) > 0)
	if !shouldPatchOutput {
		return eventData
	}

	indexes := make([]int64, 0, len(outputItemsByIndex))
	for idx := range outputItemsByIndex {
		indexes = append(indexes, idx)
	}
	sort.Slice(indexes, func(i, j int) bool {
		return indexes[i] < indexes[j]
	})

	outputArray := []byte("[]")
	var buf bytes.Buffer
	buf.WriteByte('[')
	wrote := false
	for _, idx := range indexes {
		if wrote {
			buf.WriteByte(',')
		}
		buf.Write(outputItemsByIndex[idx])
		wrote = true
	}
	for _, item := range outputItemsFallback {
		if wrote {
			buf.WriteByte(',')
		}
		buf.Write(item)
		wrote = true
	}
	buf.WriteByte(']')
	if wrote {
		outputArray = buf.Bytes()
	}

	patched, _ := ojson.SetRawBytes(eventData, "response.output", outputArray)
	return patched
}

// xaiFreeUsageExhaustedCooldown is the free-tier rolling window advertised by
// cli-chat-proxy ("Usage resets over a rolling 24-hour window").
const xaiFreeUsageExhaustedCooldown = 24 * time.Hour

const (
	xaiEmptyStreamMessage      = "xai stream error: empty upstream stream"
	xaiIncompleteStreamMessage = "xai stream error: stream closed before response.completed or response.incomplete"
)

type xaiRequestScopedStreamError struct {
	statusErr
}

func (xaiRequestScopedStreamError) IsRequestScoped() bool {
	return true
}

func newXAIEmptyStreamError() xaiRequestScopedStreamError {
	return xaiRequestScopedStreamError{statusErr: statusErr{
		code: http.StatusBadGateway,
		msg:  xaiEmptyStreamMessage,
	}}
}

func newXAIIncompleteStreamError() xaiRequestScopedStreamError {
	return xaiRequestScopedStreamError{statusErr: statusErr{
		code: http.StatusBadGateway,
		msg:  xaiIncompleteStreamMessage,
	}}
}

func xaiTerminalFailureErr(eventName string, eventData []byte) (statusErr, bool) {
	doc, ok := xaiTerminalFailureDoc(eventName, eventData)
	if !ok || doc == nil {
		return statusErr{}, false
	}
	status := xaiTerminalFailureStatus(eventData, doc)
	return xaiStatusErr(status, doc.Bytes()), true
}

func xaiTerminalFailureDoc(eventName string, eventData []byte) (*ojson.Doc, bool) {
	trimmed := bytes.TrimSpace(eventData)
	if len(trimmed) == 0 {
		return nil, false
	}
	if !ojson.ValidBytes(trimmed) {
		if strings.EqualFold(strings.TrimSpace(eventName), "error") {
			doc := ojson.NewDocString(`{"error":{}}`)
			_ = doc.Set("error.message", string(trimmed))
			return doc, true
		}
		return nil, false
	}

	eventType := strings.TrimSpace(ojson.GetBytesNoCopy(trimmed, "type").String())
	if eventType == "" {
		normalizedEventName := strings.TrimSpace(eventName)
		if normalizedEventName == "error" || normalizedEventName == "response.failed" {
			eventType = normalizedEventName
		} else if errField := ojson.GetBytesNoCopy(trimmed, "error"); errField.Exists() && errField.Type != ojson.Null && !ojson.GetBytesNoCopy(trimmed, "output").Exists() {
			if errField.Type == ojson.JSON || strings.TrimSpace(errField.String()) != "" {
				eventType = "error"
			}
		}
	}

	var doc *ojson.Doc
	switch eventType {
	case "error":
		doc = xaiTerminalErrorDoc(trimmed, "error")
		if doc == nil {
			doc = xaiTerminalErrorDoc(trimmed, "body.error")
		}
		if doc == nil {
			doc = xaiTerminalTopLevelErrorDoc(trimmed)
		}
	case "response.failed":
		doc = xaiTerminalErrorDoc(trimmed, "response.error")
		if doc == nil {
			doc = xaiTerminalErrorDoc(trimmed, "error")
		}
	default:
		return nil, false
	}

	if doc == nil {
		doc = ojson.NewDocString(`{"error":{"message":"upstream stream failed without error details"}}`)
	}
	if seq := ojson.GetBytesNoCopy(trimmed, "sequence_number"); seq.Exists() && seq.Type == ojson.Number {
		_ = doc.Set("sequence_number", seq.Int())
	}
	return doc, true
}

func xaiTerminalErrorDoc(eventData []byte, path string) *ojson.Doc {
	errorResult := ojson.GetBytesNoCopy(eventData, path)
	if !errorResult.Exists() || errorResult.Type == ojson.Null {
		return nil
	}

	doc := ojson.NewDocString(`{"error":{}}`)
	hasField := false
	if errorResult.Type == ojson.JSON {
		if errorResult.IsObject() && len(errorResult.Map()) > 0 {
			_ = doc.SetRawBytes("error", []byte(errorResult.Raw))
			hasField = true
		}
	} else if message := strings.TrimSpace(errorResult.String()); message != "" {
		_ = doc.Set("error.message", message)
		hasField = true
	}

	if strings.TrimSpace(doc.Get("error.code").String()) == "" {
		for _, codePath := range []string{"response.error.code", "code", "body.error.code"} {
			if code := strings.TrimSpace(ojson.GetBytesNoCopy(eventData, codePath).String()); code != "" {
				_ = doc.Set("error.code", code)
				hasField = true
				break
			}
		}
	}
	if strings.TrimSpace(doc.Get("error.type").String()) == "" {
		for _, typePath := range []string{"response.error.type", "error_type", "body.error.type"} {
			if errType := strings.TrimSpace(ojson.GetBytesNoCopy(eventData, typePath).String()); errType != "" {
				_ = doc.Set("error.type", errType)
				hasField = true
				break
			}
		}
	}
	if strings.TrimSpace(doc.Get("error.param").String()) == "" {
		for _, paramPath := range []string{"response.error.param", "param", "body.error.param"} {
			if param := strings.TrimSpace(ojson.GetBytesNoCopy(eventData, paramPath).String()); param != "" {
				_ = doc.Set("error.param", param)
				hasField = true
				break
			}
		}
	}
	if strings.TrimSpace(doc.Get("error.message").String()) == "" {
		for _, msgPath := range []string{"response.error.message", "message", "body.error.message"} {
			if message := strings.TrimSpace(ojson.GetBytesNoCopy(eventData, msgPath).String()); message != "" {
				_ = doc.Set("error.message", message)
				hasField = true
				break
			}
		}
	}
	if strings.TrimSpace(doc.Get("error.message").String()) == "" {
		if code := strings.TrimSpace(doc.Get("error.code").String()); code != "" {
			_ = doc.Set("error.message", code)
			hasField = true
		} else if errType := strings.TrimSpace(doc.Get("error.type").String()); errType != "" {
			_ = doc.Set("error.message", errType)
			hasField = true
		}
	}
	if !hasField {
		return nil
	}
	return doc
}

func xaiTerminalTopLevelErrorDoc(eventData []byte) *ojson.Doc {
	message := strings.TrimSpace(ojson.GetBytesNoCopy(eventData, "message").String())
	code := strings.TrimSpace(ojson.GetBytesNoCopy(eventData, "code").String())
	errorType := strings.TrimSpace(ojson.GetBytesNoCopy(eventData, "error_type").String())
	param := strings.TrimSpace(ojson.GetBytesNoCopy(eventData, "param").String())
	if message == "" && code == "" && errorType == "" && param == "" {
		return nil
	}

	doc := ojson.NewDocString(`{"error":{}}`)
	if message != "" {
		_ = doc.Set("error.message", message)
	}
	if code != "" {
		_ = doc.Set("error.code", code)
	}
	if errorType != "" {
		_ = doc.Set("error.type", errorType)
	}
	if param != "" {
		_ = doc.Set("error.param", param)
	}
	if strings.TrimSpace(doc.Get("error.message").String()) == "" {
		if code != "" {
			_ = doc.Set("error.message", code)
		} else if errorType != "" {
			_ = doc.Set("error.message", errorType)
		}
	}
	return doc
}

func xaiTerminalFailureStatus(eventData []byte, doc *ojson.Doc) int {
	for _, path := range []string{"status", "status_code", "error.status", "error.status_code", "response.error.status", "response.error.status_code", "body.status", "body.error.status"} {
		res := ojson.GetBytesNoCopy(eventData, path)
		switch res.Type {
		case ojson.Number:
			if status := int(res.Int()); status >= 400 && status <= 599 {
				return status
			}
		case ojson.String:
			if status, errAtoi := strconv.Atoi(strings.TrimSpace(res.String())); errAtoi == nil && status >= 400 && status <= 599 {
				return status
			}
		}
	}

	errorType := strings.ToLower(strings.TrimSpace(doc.Get("error.type").String()))
	errorCode := strings.ToLower(strings.TrimSpace(doc.Get("error.code").String()))
	errorMsg := strings.ToLower(strings.TrimSpace(doc.Get("error.message").String()))
	if status, errAtoi := strconv.Atoi(errorCode); errAtoi == nil && status >= 400 && status <= 599 {
		return status
	}

	switch {
	case errorType == "authentication_error",
		errorCode == "invalid_api_key",
		errorCode == "unauthorized",
		strings.HasPrefix(errorCode, "unauthenticated"),
		strings.Contains(errorCode, "bad-credentials"),
		strings.Contains(errorMsg, "access token could not be validated"):
		return http.StatusUnauthorized
	case errorType == "permission_error",
		errorCode == "forbidden",
		errorCode == "permission_denied",
		errorCode == "permission-denied",
		strings.HasPrefix(errorCode, "permission_denied:"),
		strings.HasPrefix(errorCode, "permission-denied:"):
		return http.StatusForbidden
	case errorType == "not_found_error",
		errorCode == "not_found",
		errorCode == "model_not_found":
		return http.StatusNotFound
	case errorType == "rate_limit_error",
		errorCode == "rate_limit_exceeded",
		errorCode == "rate_limit",
		strings.Contains(errorCode, "free-usage-exhausted"),
		strings.Contains(errorCode, "resource_exhausted"),
		strings.Contains(errorCode, "resource-exhausted"),
		strings.Contains(errorMsg, "free-usage-exhausted"),
		strings.Contains(errorMsg, "included free usage"):
		return http.StatusTooManyRequests
	case errorCode == "cyber_policy",
		errorCode == "content_policy_violation",
		errorType == "invalid_request_error",
		errorType == "invalid_request",
		errorType == "bad_request_error",
		errorType == "image_generation_user_error",
		errorCode == "invalid_value",
		errorCode == "invalid_request_error",
		errorCode == "invalid_argument",
		errorCode == "invalid-argument",
		errorCode == "context_length_exceeded",
		errorCode == "context_too_large":
		return http.StatusBadRequest
	case errorType == "service_unavailable_error",
		errorCode == "server_is_overloaded",
		errorCode == "service_unavailable":
		return http.StatusServiceUnavailable
	default:
		return http.StatusBadGateway
	}
}

// xaiStatusErr normalizes upstream xAI error bodies for conductor behavior:
//   - credential invalidation (403 bad-credentials) is remapped to 401 so the
//     existing OAuth refresh-once-and-retry path runs instead of payment cooldown
//   - free-tier exhaustion (subscription:free-usage-exhausted) carries a 24h
//     RetryAfter hint for auth cooldown / account rotation
//
// Generic 429s stay without an explicit retry hint so conductor backoff still applies.
func xaiStatusErr(code int, body []byte) statusErr {
	err := statusErr{code: code, msg: string(body)}
	if len(body) == 0 {
		return err
	}
	if code == http.StatusForbidden && isXAIBadCredentialsBody(body) {
		// Upstream returns 403 for invalidated OAuth access tokens. Map to 401 so
		// tryRefreshAfterUnauthorized / MarkResult unauthorized handling applies.
		err.code = http.StatusUnauthorized
		return err
	}
	if code != http.StatusTooManyRequests {
		return err
	}
	codeStr := strings.ToLower(ojson.GetBytesNoCopy(body, "code").String())
	if codeStr == "" {
		codeStr = strings.ToLower(ojson.GetBytesNoCopy(body, "error.code").String())
	}
	msg := strings.ToLower(ojson.GetBytesNoCopy(body, "error.message").String())
	if msg == "" {
		msg = strings.ToLower(ojson.GetBytesNoCopy(body, "error").String())
	}
	if msg == "" {
		msg = strings.ToLower(string(body))
	}
	if strings.Contains(codeStr, "free-usage-exhausted") ||
		strings.Contains(msg, "free-usage-exhausted") ||
		strings.Contains(msg, "included free usage") {
		d := xaiFreeUsageExhaustedCooldown
		err.retryAfter = &d
	}
	return err
}

// isXAIBadCredentialsBody reports whether an xAI error body indicates an
// invalidated/unusable OAuth access token rather than a generic permission or
// payment failure. HTTP and websocket payloads both use this helper, so nested
// error.code / error.message shapes are checked as well as flat bodies.
func isXAIBadCredentialsBody(body []byte) bool {
	for _, path := range []string{"code", "error.code", "body.error.code"} {
		if strings.Contains(strings.ToLower(ojson.GetBytesNoCopy(body, path).String()), "bad-credentials") {
			return true
		}
	}
	for _, path := range []string{"error", "error.message", "message", "body.error", "body.error.message"} {
		msg := strings.ToLower(ojson.GetBytesNoCopy(body, path).String())
		if strings.Contains(msg, "access token could not be validated") {
			return true
		}
	}
	raw := strings.ToLower(string(body))
	return strings.Contains(raw, "bad-credentials") ||
		strings.Contains(raw, "access token could not be validated")
}
