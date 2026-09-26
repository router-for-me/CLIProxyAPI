package responsestools

import (
	"bytes"
	"encoding/json"
	"strings"
)

// RewriteResponseBody restores client tool semantics in one decoded upstream
// response body: ordinary search calls become tool_search_call items and
// bridged aliases resolve to their canonical identities. Locations outside
// confirmed Responses payload shapes are never scanned.
func RewriteResponseBody(value any, contract *ToolContract, bridge *CustomBridge) bool {
	return rewriteResponseLocation(value, contract, bridge)
}

func rewriteResponseLocation(value any, contract *ToolContract, bridge *CustomBridge) bool {
	switch typed := value.(type) {
	case map[string]any:
		itemType := stringField(typed, "type")
		if itemType == "function_call" || itemType == "custom_tool_call" || itemType == "tool_search_call" {
			return rewriteResponseItem(typed, contract, bridge)
		}
		if strings.HasPrefix(itemType, "response.") {
			if item, ok := typed["item"].(map[string]any); ok {
				return rewriteResponseItem(item, contract, bridge)
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
	return false
}

func rewriteResponseItems(items []any, contract *ToolContract, bridge *CustomBridge) bool {
	changed := false
	for _, rawItem := range items {
		if item, ok := rawItem.(map[string]any); ok && rewriteResponseItem(item, contract, bridge) {
			changed = true
		}
	}
	return changed
}

func rewriteResponseItem(item map[string]any, contract *ToolContract, bridge *CustomBridge) bool {
	if isOrdinaryToolSearchCall(item, contract) {
		rewriteToolSearchItem(item)
		return true
	}
	if bridge != nil && bridge.RestoreCustomResponseItem(item) {
		return true
	}
	return restoreFunctionCallIdentity(item, contract)
}

func isOrdinaryToolSearchCall(item map[string]any, contract *ToolContract) bool {
	if stringField(item, "type") != "function_call" {
		return false
	}
	name, ok := item["name"].(string)
	expected := ToolSearchName
	if contract != nil && contract.SearchAlias != "" {
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
