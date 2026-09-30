package responsestools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ActiveToolArraysBytes measures the serialized size of every tool array the
// upstream will receive: top-level tools plus declaration-bearing inputs. The
// budget covers retained declarations, not Go heap.
func ActiveToolArraysBytes(root map[string]any) (int, error) {
	total := 0
	if tools, exists := root["tools"]; exists {
		encoded, err := json.Marshal(tools)
		if err != nil {
			return 0, err
		}
		total += len(encoded)
	}
	input, ok := root["input"].([]any)
	if !ok {
		return total, nil
	}
	for _, rawItem := range input {
		item, okItem := rawItem.(map[string]any)
		if !okItem || !IsToolDeclarationInput(item) {
			continue
		}
		tools, exists := item["tools"]
		if !exists {
			continue
		}
		encoded, err := json.Marshal(tools)
		if err != nil {
			return 0, err
		}
		total += len(encoded)
	}
	return total, nil
}

// EnforceActiveToolBudget rejects a raw request whose active tool
// declarations already exceed the budget.
func EnforceActiveToolBudget(body []byte, limits Limits) error {
	value, ok := decodeValue(body)
	if !ok {
		return nil
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	size, err := ActiveToolArraysBytes(root)
	if err != nil {
		return err
	}
	if size > limits.MaxActiveToolBytes {
		return budgetError(ReasonDeclarationBudget, fmt.Errorf("request declarations do not fit: %d > %d", size, limits.MaxActiveToolBytes))
	}
	return nil
}

func applyDeferredToolPolicy(root map[string]any, contract *ToolContract, limits Limits) (bool, error) {
	if contract == nil {
		return false, nil
	}
	changed := ensureTopLevelToolSearch(root, contract)
	if pruneRequestDeclarations(root, contract) {
		changed = true
	}
	injected, err := injectDiscoveredTools(root, contract, limits)
	if err != nil {
		return changed, err
	}
	if injected {
		changed = true
	}
	return changed, nil
}

// ensureTopLevelToolSearch advertises the ordinary tool_search function the
// upstream can call. It only runs when the request has something to search:
// either the client already speaks the tool_search protocol or it declares
// deferred tools that this rewrite is about to move into the index.
func ensureTopLevelToolSearch(root map[string]any, contract *ToolContract) bool {
	if contract == nil || !contract.ClientSearch {
		return false
	}
	tools, ok := root["tools"].([]any)
	if !ok {
		if _, exists := root["tools"]; exists {
			return false
		}
		tools = make([]any, 0, 1)
	}
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		if toolType := stringField(tool, "type"); toolType == "tool_search" && !IsServerExecutedItem(tool) {
			return false
		}
		if toolType := stringField(tool, "type"); toolType == "function" && stringField(tool, "name") == contract.SearchAlias {
			return false
		}
	}
	root["tools"] = append(tools, map[string]any{
		"type":        "function",
		"name":        contract.SearchAlias,
		"description": "Search deferred tools by keyword before calling one.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string"},
			},
			"required": []any{"query"},
		},
	})
	return true
}

func pruneRequestDeclarations(root map[string]any, contract *ToolContract) bool {
	if contract == nil || !contract.ClientSearch {
		return false
	}
	changed := false
	if tools, ok := root["tools"].([]any); ok {
		retained := make([]any, 0, len(tools))
		for _, rawTool := range tools {
			tool, okTool := rawTool.(map[string]any)
			if okTool && deferredLoading(tool) && toolTypeIsCallable(stringField(tool, "type")) {
				changed = true
				continue
			}
			retained = append(retained, rawTool)
		}
		if len(retained) != len(tools) {
			if len(retained) > 0 {
				root["tools"] = retained
			} else {
				delete(root, "tools")
			}
		}
		if pruneDeferredNamespaceTools(root["tools"], contract) {
			changed = true
		}
	}
	if input, ok := root["input"].([]any); ok {
		for _, rawItem := range input {
			item, ok := rawItem.(map[string]any)
			if !ok || stringField(item, "type") != "additional_tools" {
				continue
			}
			if tools, okTools := item["tools"].([]any); okTools {
				retained := make([]any, 0, len(tools))
				for _, rawTool := range tools {
					tool, okTool := rawTool.(map[string]any)
					if okTool && deferredLoading(tool) && toolTypeIsCallable(stringField(tool, "type")) {
						changed = true
						continue
					}
					if okTool && stringField(tool, "type") == "function" && stringField(tool, "name") == contract.SearchAlias {
						changed = true
						continue
					}
					retained = append(retained, rawTool)
				}
				if len(retained) != len(tools) {
					if len(retained) > 0 {
						item["tools"] = retained
					} else {
						delete(item, "tools")
					}
				}
			}
			if pruneDeferredNamespaceTools(item["tools"], contract) {
				changed = true
			}
		}
	}
	return changed
}

func hasNamespaceChildren(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		if stringField(typed, "type") == NamespaceToolType {
			if children, ok := typed["tools"].([]any); ok && len(children) > 0 {
				return true
			}
		}
		for _, child := range typed {
			if hasNamespaceChildren(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if hasNamespaceChildren(child) {
				return true
			}
		}
	}
	return false
}

func pruneDeferredNamespaceTools(value any, contract *ToolContract) bool {
	switch typed := value.(type) {
	case map[string]any:
		changed := false
		if stringField(typed, "type") == NamespaceToolType {
			if children, ok := typed["tools"].([]any); ok {
				retained, entries := pruneNamespaceChildren(children, strings.TrimSpace(stringField(typed, "name")))
				if len(entries) > 0 {
					typed[DeferredToolsKey] = appendExistingDeferredEntries(typed[DeferredToolsKey], entries)
					if len(retained) > 0 {
						typed["tools"] = retained
					} else {
						delete(typed, "tools")
					}
					changed = true
				}
			}
		}
		for key, child := range typed {
			if key == DeferredToolsKey {
				continue
			}
			if pruneDeferredNamespaceTools(child, contract) {
				changed = true
			}
		}
		return changed
	case []any:
		changed := false
		for _, child := range typed {
			if pruneDeferredNamespaceTools(child, contract) {
				changed = true
			}
		}
		return changed
	default:
		return false
	}
}

func pruneNamespaceChildren(children []any, namespace string) ([]any, []any) {
	retained := make([]any, 0, len(children))
	entries := make([]any, 0)
	for _, rawChild := range children {
		child, ok := rawChild.(map[string]any)
		if !ok {
			retained = append(retained, rawChild)
			continue
		}
		if stringField(child, "type") == NamespaceToolType {
			nested, okNested := child["tools"].([]any)
			if !okNested {
				retained = append(retained, rawChild)
				continue
			}
			nestedNamespace := JoinNamespace(namespace, stringField(child, "name"))
			nestedRetained, nestedEntries := pruneNamespaceChildren(nested, nestedNamespace)
			entries = append(entries, nestedEntries...)
			if len(nestedRetained) > 0 {
				if len(nestedEntries) > 0 {
					child["tools"] = nestedRetained
					child[DeferredToolsKey] = appendExistingDeferredEntries(child[DeferredToolsKey], nestedEntries)
				}
				retained = append(retained, child)
			} else if len(nestedEntries) == 0 {
				retained = append(retained, child)
			}
			continue
		}
		childType := strings.TrimSpace(stringField(child, "type"))
		switch childType {
		case "function", "custom", "":
		default:
			retained = append(retained, rawChild)
			continue
		}
		name := strings.TrimSpace(stringField(child, "name"))
		if name == "" {
			retained = append(retained, rawChild)
			continue
		}
		// Only prune when this rewrite round actually handles the deferred
		// protocol. Callers gate on contract.ClientSearch; re-checking it here
		// would couple pruning to catalog state that tests construct directly.
		if !deferredLoading(child) {
			retained = append(retained, rawChild)
			continue
		}
		entries = append(entries, map[string]any{
			"namespace": namespace,
			"name":      name,
			"type":      childType,
		})
	}
	return retained, entries
}

func appendExistingDeferredEntries(existing any, additions []any) []any {
	out := make([]any, 0)
	if values, ok := existing.([]any); ok {
		out = append(out, values...)
	}
	return append(out, additions...)
}

// injectDiscoveredTools promotes the latest discovery round in full and older
// rounds newest-first while the activation budget holds. Older rounds are
// admitted atomically. History items are never deleted.
func injectDiscoveredTools(root map[string]any, contract *ToolContract, limits Limits) (bool, error) {
	if contract == nil || len(contract.Discovered) == 0 {
		return false, nil
	}
	originalTools, _ := root["tools"].([]any)
	workingRoot := make(map[string]any, len(root))
	for key, value := range root {
		workingRoot[key] = value
	}
	removePromotedDiscoveries(workingRoot, contract)
	tools, _ := workingRoot["tools"].([]any)
	_, toolsPresent := workingRoot["tools"]
	tools = append([]any(nil), tools...)
	removedPromoted := len(tools) != len(originalTools)
	if !toolsPresent {
		delete(workingRoot, "tools")
	}
	existing := make(map[string]struct{}, len(tools))
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		switch strings.TrimSpace(stringField(tool, "type")) {
		case "function", "custom", "":
			if name := strings.TrimSpace(stringField(tool, "name")); name != "" {
				// The bridge's search entry point is not a client
				// declaration, so it must not block a discovered tool whose
				// own name collides with it.
				if name == contract.SearchAlias {
					continue
				}
				existing[name] = struct{}{}
			}
		}
	}
	if toolsPresent {
		workingRoot["tools"] = tools
	}
	if size, err := ActiveToolArraysBytes(workingRoot); err != nil {
		return false, err
	} else if size > limits.MaxActiveToolBytes {
		return false, budgetError(ReasonDeclarationBudget, fmt.Errorf("eager declarations do not fit: %d > %d", size, limits.MaxActiveToolBytes))
	}

	identities := make([]ToolIdentity, 0, len(contract.Declarations))
	for identity := range contract.Declarations {
		identities = append(identities, identity)
	}
	sort.Slice(identities, func(left, right int) bool {
		leftRound := contract.DiscoveredRound[identities[left]]
		rightRound := contract.DiscoveredRound[identities[right]]
		if leftRound != rightRound {
			return leftRound > rightRound
		}
		if identities[left].Namespace != identities[right].Namespace {
			return identities[left].Namespace < identities[right].Namespace
		}
		if identities[left].Name != identities[right].Name {
			return identities[left].Name < identities[right].Name
		}
		return identities[left].Kind < identities[right].Kind
	})
	changed := removedPromoted
	for start := 0; start < len(identities); {
		end := start + 1
		round := contract.DiscoveredRound[identities[start]]
		for end < len(identities) && contract.DiscoveredRound[identities[end]] == round {
			end++
		}
		groupTools := make([]any, 0, end-start)
		groupNames := make([]string, 0, 2*(end-start))
		for _, identity := range identities[start:end] {
			raw, ok := contract.Declarations[identity]
			if !ok {
				continue
			}
			name := contract.AliasByID[identity]
			if name == "" {
				name = RawQualifiedToolName(identity.Namespace, identity.Name)
			}
			rawName := RawQualifiedToolName(identity.Namespace, identity.Name)
			// Skip the bridge's own search entry point. A discovered tool that
			// merely shares the default "tool_search" spelling receives a
			// distinct alias during activation and must still be injected.
			if name == "" || (contract.SearchAlias != "" && name == contract.SearchAlias) {
				continue
			}
			if _, exists := existing[name]; exists {
				continue
			}
			if _, exists := existing[rawName]; exists {
				continue
			}
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.UseNumber()
			var tool map[string]any
			if err := decoder.Decode(&tool); err != nil {
				if round == contract.LatestRound {
					return false, unprocessableError(ReasonAmbiguousIdentity, fmt.Errorf("latest discovery contains an invalid tool declaration"))
				}
				groupTools = nil
				break
			}
			delete(tool, "defer_loading")
			if identity.Kind == ToolKindCustom {
				// Keep the original custom identity intact until the custom bridge
				// rewrites it with the single canonical wire alias.
				if identity.Namespace != "" {
					tool["namespace"] = identity.Namespace
				}
			} else {
				if strings.TrimSpace(stringField(tool, "type")) == "" {
					tool["type"] = "function"
				}
				tool["name"] = name
				delete(tool, "namespace")
			}
			groupTools = append(groupTools, tool)
			groupNames = append(groupNames, name)
			if rawName != contract.SearchAlias {
				groupNames = append(groupNames, rawName)
			}
		}
		if len(groupTools) > 0 {
			candidateTools := append(append([]any(nil), tools...), groupTools...)
			candidateRoot := make(map[string]any, len(workingRoot)+1)
			for key, value := range workingRoot {
				candidateRoot[key] = value
			}
			candidateRoot["tools"] = candidateTools
			size, err := ActiveToolArraysBytes(candidateRoot)
			if err != nil {
				return false, err
			}
			if size > limits.MaxActiveToolBytes {
				if round == contract.LatestRound {
					return false, budgetError(ReasonDeclarationBudget, fmt.Errorf("latest discovery does not fit: %d > %d", size, limits.MaxActiveToolBytes))
				}
				start = end
				continue
			}
			tools = candidateTools
			workingRoot["tools"] = tools
			toolsPresent = true
			for _, name := range groupNames {
				existing[name] = struct{}{}
			}
			changed = true
		}
		start = end
	}
	if toolsPresent {
		root["tools"] = tools
	} else {
		delete(root, "tools")
	}
	return changed, nil
}

func removePromotedDiscoveries(root map[string]any, contract *ToolContract) {
	tools, ok := root["tools"].([]any)
	if !ok {
		return
	}
	retained := make([]any, 0, len(tools))
	for _, rawTool := range tools {
		tool, okTool := rawTool.(map[string]any)
		name := ""
		if okTool {
			name = stringField(tool, "name")
		}
		identity, knownAlias := contract.IDByAlias[name]
		_, discovered := contract.Discovered[identity]
		if okTool && IsPromotedToolName(name) && knownAlias && discovered {
			continue
		}
		retained = append(retained, rawTool)
	}
	if len(retained) != len(tools) {
		if len(retained) > 0 {
			root["tools"] = retained
		} else {
			delete(root, "tools")
		}
	}
}

func toolTypeIsCallable(value string) bool {
	switch strings.TrimSpace(value) {
	case "", "function", "custom":
		return true
	default:
		return false
	}
}

// ActiveToolArraysBytesOf measures the tool arrays of one raw wire body. It
// decodes failures return 0 so the guard never blocks on unparseable frames.
func ActiveToolArraysBytesOf(body []byte) int {
	value, ok := decodeValue(body)
	if !ok {
		return 0
	}
	root, ok := value.(map[string]any)
	if !ok {
		return 0
	}
	size, err := ActiveToolArraysBytes(root)
	if err != nil {
		return 0
	}
	return size
}
