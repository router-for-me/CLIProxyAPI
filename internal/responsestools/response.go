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
	owners := snapshotResponseItemOwners(items)
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
	if changed {
		if err := checkRewrittenResponseItemIDs(items, owners); err != nil {
			return changed, err
		}
	}
	return changed, nil
}

// snapshotResponseItemOwners records what every output item was before the
// rewrite, so the converted ids can be checked against the items they still
// have to describe.
func snapshotResponseItemOwners(items []any) map[int]ItemIDOwner {
	owners := make(map[int]ItemIDOwner, len(items))
	for index, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		id, isText := item["id"].(string)
		if !isText || id == "" {
			continue
		}
		owners[index] = ItemIDOwner{
			WireID:   id,
			WireType: strings.TrimSpace(stringField(item, "type")),
			CallID:   strings.TrimSpace(stringField(item, "call_id")),
		}
	}
	return owners
}

// checkRewrittenResponseItemIDs refuses a rewritten output in which two items
// would reach the client under one id. Items the rewrite left alone are
// registered too, so a migrated id cannot land on an id the client already
// knows.
func checkRewrittenResponseItemIDs(items []any, owners map[int]ItemIDOwner) error {
	var registry ItemIDRegistry
	for index, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		id, isText := item["id"].(string)
		if !isText || id == "" {
			continue
		}
		owner, known := owners[index]
		if !known {
			owner = ItemIDOwner{
				WireID:   id,
				WireType: strings.TrimSpace(stringField(item, "type")),
				CallID:   strings.TrimSpace(stringField(item, "call_id")),
			}
		}
		if err := registry.Register(id, owner); err != nil {
			return upstreamError(ReasonUpstreamContract, fmt.Errorf(
				"response output item %d would share one client item id with another item", index))
		}
	}
	return nil
}

func rewriteResponseItem(item map[string]any, contract *ToolContract, bridge *CustomBridge) bool {
	changed, _ := rewriteResponseItemChecked(item, contract, bridge)
	return changed
}

func rewriteResponseItemChecked(item map[string]any, contract *ToolContract, bridge *CustomBridge) (bool, error) {
	if isOrdinaryToolSearchCall(item, contract) {
		if err := rewriteToolSearchItemChecked(item); err != nil {
			return false, err
		}
		restoreSyntheticSearchNulls(item, contract)
		return true, nil
	}
	if stringField(item, "type") == "tool_search_call" {
		return restoreSyntheticSearchNulls(item, contract), nil
	}
	if bridge != nil && stringField(item, "type") == "function_call" {
		identity, isCustomAlias := bridge.ResolveWireAlias(stringField(item, "name"))
		if isCustomAlias && identity.Kind == ToolKindCustom {
			return bridge.restoreCustomResponseItemChecked(item, identity)
		}
	}
	return RestoreFunctionCallIdentityChecked(item, contract)
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

// rewriteToolSearchItemChecked turns one bridged function call back into the
// client search item. The upstream minted the id in the function_call
// namespace and the client stores it as given, so it has to leave in the
// namespace the new type requires.
func rewriteToolSearchItemChecked(item map[string]any) error {
	if err := requireBridgedItemID(item); err != nil {
		return err
	}
	arguments := item["arguments"]
	switch encoded := arguments.(type) {
	case string:
		trimmed := bytes.TrimSpace([]byte(encoded))
		if len(trimmed) == 0 {
			arguments = map[string]any{}
		} else {
			if err := json.Unmarshal(trimmed, &arguments); err != nil {
				return upstreamError(ReasonUpstreamContract, fmt.Errorf("search arguments are not valid JSON"))
			}
		}
	case nil:
		arguments = map[string]any{}
	}
	if _, err := ReidentifyItem(item, "tool_search_call"); err != nil {
		return upstreamError(ReasonUpstreamContract, err)
	}
	item["arguments"] = arguments
	item["execution"] = "client"
	delete(item, "name")
	delete(item, "namespace")
	return nil
}

// requireBridgedItemID rejects a bridged response item that carries no usable
// id. The proxy cannot mint a replacement: any id it invented would be a new
// identity the client stores and replays on the next turn.
func requireBridgedItemID(item map[string]any) error {
	rawID, exists := item["id"]
	if !exists {
		return upstreamError(ReasonUpstreamContract, fmt.Errorf("bridged output item has no id"))
	}
	if id, isText := rawID.(string); !isText || strings.TrimSpace(id) == "" {
		return upstreamError(ReasonUpstreamContract, fmt.Errorf("bridged output item has an empty id"))
	}
	return nil
}

// RestoreFunctionCallIdentity maps one upstream function call back to its
// canonical namespaced identity where a failure and a no-op are equivalent.
// Production paths use the checked form so an unbridgeable kind change fails
// instead of silently disappearing.
func RestoreFunctionCallIdentity(item map[string]any, contract *ToolContract) bool {
	changed, _ := RestoreFunctionCallIdentityChecked(item, contract)
	return changed
}

// RestoreFunctionCallIdentityChecked restores the name and namespace of one
// upstream function call. It deliberately does not change the item type: a
// name that resolves to a custom identity has no bridge to justify turning a
// function call into a custom call, and silently doing so would hand the
// client a protocol the upstream never produced.
func RestoreFunctionCallIdentityChecked(item map[string]any, contract *ToolContract) (bool, error) {
	itemType := stringField(item, "type")
	if (itemType != "function_call" && itemType != "custom_tool_call") || contract == nil {
		return false, nil
	}
	if strings.TrimSpace(stringField(item, "namespace")) != "" {
		return false, nil
	}
	name := strings.TrimSpace(stringField(item, "name"))
	identity, ok := contract.Resolve(name)
	if !ok {
		return false, nil
	}
	wantType := "function_call"
	if identity.Kind == ToolKindCustom {
		wantType = "custom_tool_call"
	}
	if wantType != itemType {
		return false, upstreamError(ReasonUpstreamContract,
			fmt.Errorf("output item resolved to %q without an explicit custom bridge", wantType))
	}
	item["name"] = identity.Name
	if identity.Namespace != "" {
		item["namespace"] = identity.Namespace
	}
	return true, nil
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
