package responsestools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// RewriteRequest rewrites one decoded client request for a bridge route: the
// client-executed tool_search declaration becomes the ordinary search
// function, search calls and outputs become function items, deferred tools
// are pruned, and discovered tools are injected under budget. It reports
// whether the value changed.
func RewriteRequest(value any, policy RoutePolicy, contract *ToolContract, limits Limits) (bool, error) {
	if root, ok := value.(map[string]any); ok && policy.ClientSearch == ClientSearchBridge {
		if err := validateClientSearchRequest(root, contract); err != nil {
			return false, err
		}
	}
	changed, err := rewriteRequestValue(value, contract)
	if err != nil {
		return false, err
	}
	if root, ok := value.(map[string]any); ok && contract != nil && contract.ClientSearch {
		policyChanged, err := applyDeferredToolPolicy(root, contract, limits)
		if err != nil {
			return false, err
		}
		if policyChanged {
			changed = true
		}
	}
	_ = policy
	return changed, nil
}

func validateClientSearchRequest(root map[string]any, contract *ToolContract) error {
	if contract == nil {
		return nil
	}
	if contract.ServerSearch {
		return unprocessableError(ReasonUnsupportedProtocol, fmt.Errorf("server-executed tool search is not supported by the bridge route"))
	}
	if !contract.ClientSearch {
		return nil
	}
	input, ok := root["input"].([]any)
	if !ok {
		return nil
	}
	calls := make(map[string]struct{})
	outputs := make(map[string]struct{})
	for _, rawItem := range input {
		item, okItem := rawItem.(map[string]any)
		if !okItem {
			continue
		}
		switch stringField(item, "type") {
		case "tool_search_call":
			callID := strings.TrimSpace(stringField(item, "call_id"))
			if callID == "" {
				return unprocessableError(ReasonHistoryLink, fmt.Errorf("client search call is missing call_id"))
			}
			if _, exists := calls[callID]; exists {
				return unprocessableError(ReasonHistoryLink, fmt.Errorf("client search call_id is duplicated"))
			}
			if _, okArguments := item["arguments"].(map[string]any); !okArguments {
				return unprocessableError(ReasonHistoryLink, fmt.Errorf("client search arguments must be an object"))
			}
			calls[callID] = struct{}{}
		case "tool_search_output":
			callID := strings.TrimSpace(stringField(item, "call_id"))
			if callID == "" {
				return unprocessableError(ReasonHistoryLink, fmt.Errorf("client search output is missing call_id"))
			}
			if _, exists := calls[callID]; !exists {
				return unprocessableError(ReasonHistoryLink, fmt.Errorf("client search output has no preceding call"))
			}
			if _, exists := outputs[callID]; exists {
				return unprocessableError(ReasonHistoryLink, fmt.Errorf("client search output is duplicated"))
			}
			if _, okTools := item["tools"].([]any); !okTools {
				return unprocessableError(ReasonHistoryLink, fmt.Errorf("client search output tools must be an array"))
			}
			outputs[callID] = struct{}{}
		}
	}
	if len(calls) != len(outputs) {
		return unprocessableError(ReasonHistoryLink, fmt.Errorf("client search call has no matching output"))
	}
	return nil
}

func rewriteRequestValue(value any, contract *ToolContract) (bool, error) {
	switch typed := value.(type) {
	case map[string]any:
		changed := rewriteToolList(typed["tools"], contract)
		if rewriteToolChoice(typed["tool_choice"], contract) {
			changed = true
		}
		if input, ok := typed["input"].([]any); ok {
			for _, rawItem := range input {
				item, ok := rawItem.(map[string]any)
				if !ok {
					continue
				}
				switch stringField(item, "type") {
				case "additional_tools":
					if rewriteToolList(item["tools"], contract) {
						changed = true
					}
				case "tool_search_call":
					itemChanged, err := rewriteToolSearchCallForRequest(item, contract)
					if err != nil {
						return false, err
					}
					if itemChanged {
						changed = true
					}
				case "tool_search_output":
					itemChanged, err := rewriteToolSearchOutputForRequest(item, contract)
					if err != nil {
						return false, err
					}
					if itemChanged {
						changed = true
					}
				case "function_call", "custom_tool_call":
					if rewriteActivatedToolCallForRequest(item, contract) {
						changed = true
					}
				}
			}
		}
		return changed, nil
	case []any:
		changed := false
		for _, child := range typed {
			childChanged, err := rewriteRequestValue(child, contract)
			if err != nil {
				return false, err
			}
			if childChanged {
				changed = true
			}
		}
		return changed, nil
	default:
		return false, nil
	}
}

func rewriteToolList(value any, contract *ToolContract) bool {
	tools, ok := value.([]any)
	if !ok {
		return false
	}
	changed := false
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		if stringField(tool, "type") == NamespaceToolType {
			if rewriteToolList(tool["tools"], contract) {
				changed = true
			}
			continue
		}
		if rewriteToolSearchDeclaration(tool, contract) {
			changed = true
		}
	}
	return changed
}

// RewriteToolChoice converts a tool_search choice into the ordinary function
// the upstream receives. Both the dedicated choice type and allowed_tools
// entries are rejected by chat-shaped upstreams.
func rewriteToolChoice(value any, contract *ToolContract) bool {
	choice, ok := value.(map[string]any)
	if !ok || contract == nil || !contract.ClientSearch {
		return false
	}
	alias := contract.SearchAlias
	changed := false
	if stringField(choice, "type") == "tool_search" {
		choice["type"] = "function"
		choice["name"] = alias
		changed = true
	} else if rewriteDiscoveredFunctionChoice(choice, contract) {
		changed = true
	}
	tools, okTools := choice["tools"].([]any)
	if !okTools {
		return changed
	}
	for _, rawTool := range tools {
		tool, okTool := rawTool.(map[string]any)
		if !okTool {
			continue
		}
		if stringField(tool, "type") == "tool_search" {
			tool["type"] = "function"
			tool["name"] = alias
			changed = true
			continue
		}
		if rewriteDiscoveredFunctionChoice(tool, contract) {
			changed = true
		}
	}
	return changed
}

func rewriteDiscoveredFunctionChoice(choice map[string]any, contract *ToolContract) bool {
	if contract == nil || stringField(choice, "type") != "function" {
		return false
	}
	name := strings.TrimSpace(stringField(choice, "name"))
	namespace := strings.TrimSpace(stringField(choice, "namespace"))
	if name == "" {
		return false
	}
	lookupName := name
	if namespace != "" {
		lookupName = RawQualifiedToolName(namespace, name)
	}
	identity, ok := contract.Resolve(lookupName)
	if !ok || identity.Kind != ToolKindFunction {
		return false
	}
	if _, discovered := contract.Discovered[identity]; !discovered {
		return false
	}
	alias, ok := contract.AliasByID[identity]
	if !ok || alias == "" || (name == alias && namespace == "") {
		return false
	}
	choice["name"] = alias
	delete(choice, "namespace")
	return true
}

func rewriteToolSearchDeclaration(tool map[string]any, contract *ToolContract) bool {
	if stringField(tool, "type") != "tool_search" || IsServerExecutedItem(tool) {
		return false
	}
	tool["type"] = "function"
	tool["name"] = ToolSearchName
	if contract != nil && contract.SearchAlias != "" {
		tool["name"] = contract.SearchAlias
	}
	if _, exists := tool["parameters"]; !exists {
		tool["parameters"] = map[string]any{}
	}
	delete(tool, "execution")
	return true
}

// rewriteToolSearchCallForRequest turns one replayed client search call back
// into the ordinary function item the bridged upstream expects. The type change
// and the id namespace move together, so the call keeps replaying as one
// identity instead of a function call carrying a search id.
func rewriteToolSearchCallForRequest(item map[string]any, contract *ToolContract) (bool, error) {
	if IsServerExecutedItem(item) || stringField(item, "call_id") == "" {
		return false, nil
	}
	arguments, ok := JSONArgumentsString(item["arguments"])
	if !ok {
		return false, nil
	}
	if _, err := ReidentifyItem(item, "function_call"); err != nil {
		return false, unprocessableError(ReasonHistoryLink, err)
	}
	item["name"] = ToolSearchName
	if contract != nil && contract.SearchAlias != "" {
		item["name"] = contract.SearchAlias
	}
	item["arguments"] = arguments
	delete(item, "execution")
	return true, nil
}

// rewriteToolSearchOutputForRequest converts the matching discovery result into
// the function output the upstream reads, moving its id with its type.
func rewriteToolSearchOutputForRequest(item map[string]any, contract *ToolContract) (bool, error) {
	if IsServerExecutedItem(item) || stringField(item, "call_id") == "" {
		return false, nil
	}
	manifest := CompactToolSearchManifest(item["tools"], contract)
	encoded, err := json.Marshal(map[string]any{"tools": manifest})
	if err != nil {
		return false, nil
	}
	if _, err := ReidentifyItem(item, "function_call_output"); err != nil {
		return false, unprocessableError(ReasonHistoryLink, err)
	}
	item["output"] = string(encoded)
	delete(item, "execution")
	delete(item, "status")
	delete(item, "tools")
	return true, nil
}

// rewriteActivatedToolCallForRequest aligns replayed tool history with the
// flat alias used by the active declaration. It only touches identities that
// were explicitly deferred or discovered in this request, so eager tools and
// unknown calls keep their original names.
func rewriteActivatedToolCallForRequest(item map[string]any, contract *ToolContract) bool {
	if contract == nil {
		return false
	}
	itemType := strings.TrimSpace(stringField(item, "type"))
	if itemType != "function_call" && itemType != "custom_tool_call" {
		return false
	}
	name := strings.TrimSpace(stringField(item, "name"))
	namespace := strings.TrimSpace(stringField(item, "namespace"))
	if name == "" {
		return false
	}
	identity := ToolIdentity{
		Namespace: namespace,
		Name:      name,
		Kind:      NormalizeToolKind(itemType),
	}
	_, discovered := contract.Discovered[identity]
	if !contract.Deferred[identity] && !discovered {
		return false
	}
	alias := contract.AliasByID[identity]
	if alias == "" || alias == name {
		return false
	}
	item["name"] = alias
	if namespace != "" {
		delete(item, "namespace")
	}
	return true
}

// CompactToolSearchManifest reduces one discovery round to the name-only
// manifest carried by the upstream function_call_output.
func CompactToolSearchManifest(value any, contract *ToolContract) []any {
	entries := make([]any, 0)
	var collect func(any, string)
	collect = func(current any, inheritedNamespace string) {
		switch typed := current.(type) {
		case map[string]any:
			switch strings.TrimSpace(stringField(typed, "type")) {
			case NamespaceToolType:
				namespace := strings.TrimSpace(stringField(typed, "name"))
				if inheritedNamespace != "" && namespace != "" {
					namespace = JoinNamespace(inheritedNamespace, namespace)
				} else if inheritedNamespace != "" {
					namespace = inheritedNamespace
				}
				collect(typed["tools"], namespace)
			case "function", "custom", "":
				name := strings.TrimSpace(stringField(typed, "name"))
				if name == "" {
					return
				}
				namespace := strings.TrimSpace(stringField(typed, "namespace"))
				if namespace == "" {
					namespace = inheritedNamespace
				}
				flatName := RawQualifiedToolName(namespace, name)
				if flatName == "" {
					return
				}
				kind := NormalizeToolKind(stringField(typed, "type"))
				if contract != nil {
					if alias := contract.AliasByID[ToolIdentity{Namespace: namespace, Name: name, Kind: kind}]; alias != "" {
						flatName = alias
					}
				}
				entries = append(entries, map[string]any{"name": flatName})
			}
		case []any:
			for _, child := range typed {
				collect(child, inheritedNamespace)
			}
		}
	}
	collect(value, "")
	return entries
}

// JSONArgumentsString normalizes tool call arguments to the JSON string the
// upstream function_call items carry.
func JSONArgumentsString(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		trimmed := bytes.TrimSpace([]byte(typed))
		if len(trimmed) == 0 {
			return "{}", true
		}
		var decoded any
		if err := json.Unmarshal(trimmed, &decoded); err != nil {
			return "", false
		}
		return string(trimmed), true
	case nil:
		return "{}", true
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return "", false
		}
		return string(encoded), true
	}
}
