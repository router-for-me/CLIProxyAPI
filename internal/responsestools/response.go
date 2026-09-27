package responsestools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// RewriteResponseBody restores client tool semantics in one decoded upstream
// response body: ordinary search calls become tool_search_call items and
// bridged aliases resolve to their canonical identities. Locations outside
// confirmed Responses payload shapes are never scanned.
func RewriteResponseBody(value any, contract *ToolContract, bridge *CustomBridge) bool {
	changed, _ := RewriteResponseBodyChecked(value, contract, bridge)
	return changed
}

// RewriteResponseBodyChecked restores client tool semantics and propagates
// malformed upstream custom arguments instead of returning them as ordinary
// function calls.
func RewriteResponseBodyChecked(value any, contract *ToolContract, bridge *CustomBridge) (bool, error) {
	return rewriteResponseLocation(value, contract, bridge)
}

func rewriteResponseLocation(value any, contract *ToolContract, bridge *CustomBridge) (bool, error) {
	switch typed := value.(type) {
	case map[string]any:
		itemType := stringField(typed, "type")
		if itemType == "function_call" || itemType == "custom_tool_call" || itemType == "tool_search_call" {
			return rewriteResponseItemChecked(typed, contract, bridge)
		}
		if strings.HasPrefix(itemType, "response.") {
			if item, ok := typed["item"].(map[string]any); ok {
				return rewriteResponseItemChecked(item, contract, bridge)
			}
		}
		if response, ok := typed["response"].(map[string]any); ok {
			return rewriteResponseLocation(response, contract, bridge)
		}
		if output, ok := typed["output"].([]any); ok {
			return rewriteResponseItems(output, contract, bridge)
		}
	case []any:
		return rewriteResponseItems(typed, contract, bridge)
	}
	return false, nil
}

func rewriteResponseItems(items []any, contract *ToolContract, bridge *CustomBridge) (bool, error) {
	changed := false
	for _, rawItem := range items {
		if item, ok := rawItem.(map[string]any); ok {
			itemChanged, err := rewriteResponseItemChecked(item, contract, bridge)
			if err != nil {
				return changed, err
			}
			if itemChanged {
				changed = true
			}
		}
	}
	return changed, nil
}

func rewriteResponseItem(item map[string]any, contract *ToolContract, bridge *CustomBridge) bool {
	changed, _ := rewriteResponseItemChecked(item, contract, bridge)
	return changed
}

func rewriteResponseItemChecked(item map[string]any, contract *ToolContract, bridge *CustomBridge) (bool, error) {
	if isOrdinaryToolSearchCall(item, contract) {
		rewriteToolSearchItem(item)
		restoreSyntheticSearchNulls(item, contract)
		return true, nil
	}
	if stringField(item, "type") == "tool_search_call" {
		return restoreSyntheticSearchNulls(item, contract), nil
	}
	if bridge != nil && stringField(item, "type") == "function_call" {
		identity, isCustomAlias := bridge.ResolveWireAlias(stringField(item, "name"))
		if isCustomAlias && identity.Kind == ToolKindCustom {
			arguments, ok := item["arguments"].(string)
			if !ok {
				return false, upstreamError(ReasonUpstreamContract, fmt.Errorf("custom function_call arguments are not a string"))
			}
			input, err := UnpackCustomArguments(arguments)
			if err != nil {
				return false, upstreamError(ReasonUpstreamContract, fmt.Errorf("invalid custom function_call arguments: %w", err))
			}
			item["type"] = "custom_tool_call"
			item["name"] = identity.Name
			if identity.Namespace != "" {
				item["namespace"] = identity.Namespace
			} else {
				delete(item, "namespace")
			}
			item["input"] = input
			delete(item, "arguments")
			return true, nil
		}
	}
	return restoreFunctionCallIdentity(item, contract), nil
}

func restoreSyntheticSearchNulls(item map[string]any, contract *ToolContract) bool {
	arguments, ok := item["arguments"].(map[string]any)
	if !ok {
		return false
	}
	return restoreSyntheticSearchArgumentNulls(arguments, contract)
}

func restoreSyntheticSearchArgumentNulls(arguments map[string]any, contract *ToolContract) bool {
	if contract == nil || len(contract.SearchSyntheticNulls) == 0 {
		return false
	}
	changed := false
	for pointer := range contract.SearchSyntheticNulls {
		if !strings.HasPrefix(pointer, "/") {
			continue
		}
		segments := strings.Split(pointer[1:], "/")
		for index := range segments {
			segments[index] = decodeJSONPointerToken(segments[index])
		}
		if restoreSyntheticNullPath(arguments, segments) {
			changed = true
		}
	}
	return changed
}

func restoreSyntheticNullPath(value any, path []string) bool {
	if len(path) == 0 {
		return false
	}
	switch typed := value.(type) {
	case map[string]any:
		child, exists := typed[path[0]]
		if !exists {
			return false
		}
		if len(path) == 1 {
			if child == nil {
				delete(typed, path[0])
				return true
			}
			return false
		}
		return restoreSyntheticNullPath(child, path[1:])
	case []any:
		changed := false
		if path[0] == "*" {
			for _, child := range typed {
				if restoreSyntheticNullPath(child, path[1:]) {
					changed = true
				}
			}
		}
		return changed
	default:
		return false
	}
}

func isOrdinaryToolSearchCall(item map[string]any, contract *ToolContract) bool {
	if stringField(item, "type") != "function_call" || contract == nil || !contract.SearchBridged {
		return false
	}
	name, ok := item["name"].(string)
	expected := ToolSearchName
	if contract.SearchAlias != "" {
		expected = contract.SearchAlias
	}
	return ok && name == expected
}

func rewriteToolSearchItem(item map[string]any) {
	item["type"] = "tool_search_call"
	item["execution"] = "client"
	delete(item, "name")
	delete(item, "namespace")

	switch arguments := item["arguments"].(type) {
	case string:
		trimmed := bytes.TrimSpace([]byte(arguments))
		if len(trimmed) == 0 {
			item["arguments"] = map[string]any{}
			return
		}
		var decoded any
		if err := json.Unmarshal(trimmed, &decoded); err != nil {
			return
		}
		item["arguments"] = decoded
	case nil:
		item["arguments"] = map[string]any{}
	}
}

// RestoreFunctionCallIdentity maps one upstream function call back to its
// canonical namespaced identity. Eager tools, unknown calls, and ambiguous
// names are left untouched: only explicitly deferred or discovered identities
// restore.
func restoreFunctionCallIdentity(item map[string]any, contract *ToolContract) bool {
	return RestoreFunctionCallIdentity(item, contract)
}

// RestoreFunctionCallIdentity is the exported form used by the attempt and
// stream paths.
func RestoreFunctionCallIdentity(item map[string]any, contract *ToolContract) bool {
	itemType := stringField(item, "type")
	if (itemType != "function_call" && itemType != "custom_tool_call") || contract == nil {
		return false
	}
	if strings.TrimSpace(stringField(item, "namespace")) != "" {
		return false
	}
	name := strings.TrimSpace(stringField(item, "name"))
	identity, ok := contract.Resolve(name)
	if !ok {
		return false
	}
	item["name"] = identity.Name
	if identity.Namespace != "" {
		item["namespace"] = identity.Namespace
	}
	if identity.Kind == ToolKindCustom {
		item["type"] = "custom_tool_call"
	} else {
		item["type"] = "function_call"
	}
	return true
}

// ValidateToolSearchCall asserts the client-visible shape of a restored
// search item for tests and the outbound guard.
func ValidateToolSearchCall(item map[string]any) error {
	if item["type"] != "tool_search_call" {
		return unprocessableError(ReasonUpstreamContract, jsonErrorf("unexpected type %v", item["type"]))
	}
	if item["execution"] != "client" {
		return unprocessableError(ReasonUpstreamContract, jsonErrorf("unexpected execution %v", item["execution"]))
	}
	if _, exists := item["name"]; exists {
		return unprocessableError(ReasonUpstreamContract, jsonErrorf("tool_search name must be removed"))
	}
	return nil
}
