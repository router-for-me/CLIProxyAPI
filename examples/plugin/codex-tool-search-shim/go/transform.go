package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

const toolSearchName = "tool_search"

func rewriteRequestBody(body []byte) ([]byte, bool, error) {
	return rewriteRequestBodyWithPolicy(
		body,
		requestPolicy{bridge: true, prune: true, bridgeKnown: true},
		extractToolCatalog(body),
	)
}

func rewriteRequestBodyWithPolicy(body []byte, policy requestPolicy, catalog *toolCatalog) ([]byte, bool, error) {
	if !policy.bridge {
		return body, false, nil
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' && trimmed[0] != '[' {
		return body, false, nil
	}
	// Skip the parse for the common request that carries neither deferred tool
	// metadata nor a namespaced tool list.
	if !bytes.Contains(trimmed, []byte(toolSearchName)) &&
		!bytes.Contains(trimmed, []byte(namespaceToolType)) {
		return body, false, nil
	}

	value, okValue := decodeJSONValue(body)
	if !okValue {
		return body, false, nil
	}
	changed := rewriteRequestValue(value, policy, catalog)
	if root, ok := value.(map[string]any); ok && policy.prune {
		if applyDeferredToolPolicyWithCatalog(root, catalog) {
			changed = true
		}
	}
	if !changed {
		return body, false, nil
	}
	out, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return body, false, errMarshal
	}
	return out, true, nil
}

func rewriteResponseBody(body []byte) ([]byte, bool, error) {
	return rewriteResponseBodyWithPolicy(body, nil, true)
}

func rewriteResponseBodyWithPolicy(body []byte, catalog *toolCatalog, bridge bool) ([]byte, bool, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return body, false, nil
	}
	// Every rewriteable response item is a function_call, so text deltas and
	// other hot stream chunks leave without a JSON parse.
	if !bytes.Contains(trimmed, []byte("function_call")) {
		return body, false, nil
	}

	if trimmed[0] == '{' || trimmed[0] == '[' {
		return rewriteJSONBodyWithPolicy(body, catalog, bridge)
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) || bytes.HasPrefix(trimmed, []byte("event:")) {
		return rewriteSSEBodyWithPolicy(body, catalog, bridge)
	}
	return body, false, nil
}

func rewriteJSONBodyWithPolicy(body []byte, catalog *toolCatalog, bridge bool) ([]byte, bool, error) {
	var value any
	if errUnmarshal := json.Unmarshal(body, &value); errUnmarshal != nil {
		return body, false, nil
	}
	if !rewriteValueWithPolicy(value, catalog, bridge) {
		return body, false, nil
	}
	out, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return body, false, errMarshal
	}
	return out, true, nil
}

func rewriteSSEBodyWithPolicy(body []byte, catalog *toolCatalog, bridge bool) ([]byte, bool, error) {
	lines := bytes.SplitAfter(body, []byte("\n"))
	changed := false
	for index, line := range lines {
		content := bytes.TrimSuffix(line, []byte("\n"))
		if !bytes.HasPrefix(content, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(content[len("data:"):])
		if len(payload) == 0 || payload[0] != '{' && payload[0] != '[' {
			continue
		}
		rewritten, lineChanged, errRewrite := rewriteJSONBodyWithPolicy(payload, catalog, bridge)
		if errRewrite != nil {
			return body, false, errRewrite
		}
		if !lineChanged {
			continue
		}
		suffix := []byte("\n")
		if bytes.HasSuffix(line, suffix) {
			lines[index] = append(append([]byte("data:"), rewritten...), suffix...)
		} else {
			lines[index] = append([]byte("data:"), rewritten...)
		}
		changed = true
	}
	if !changed {
		return body, false, nil
	}
	return bytes.Join(lines, nil), true, nil
}

func rewriteRequestValue(value any, policy requestPolicy, catalog *toolCatalog) bool {
	switch typed := value.(type) {
	case map[string]any:
		changed := rewriteToolList(typed["tools"], policy)
		if policy.bridge && rewriteToolChoice(typed["tool_choice"]) {
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
					if rewriteToolList(item["tools"], policy) {
						changed = true
					}
				case "tool_search_call":
					if policy.bridge && rewriteToolSearchCallForRequest(item) {
						changed = true
					}
				case "tool_search_output":
					if policy.bridge && rewriteToolSearchOutputForRequest(item, catalog) {
						changed = true
					}
				}
			}
		}
		return changed
	case []any:
		changed := false
		for _, child := range typed {
			if rewriteRequestValue(child, policy, catalog) {
				changed = true
			}
		}
		return changed
	default:
		return false
	}
}

func rewriteToolList(value any, policy requestPolicy) bool {
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
		if stringField(tool, "type") == namespaceToolType {
			if rewriteToolList(tool["tools"], policy) {
				changed = true
			}
			continue
		}
		if policy.bridge && rewriteToolSearchDeclaration(tool) {
			changed = true
		}
	}
	return changed
}

// rewriteToolChoice converts a tool_search choice into the ordinary function the
// upstream receives. Both the dedicated choice type and the allowed_tools
// entries are rejected by chat-shaped upstreams.
func rewriteToolChoice(value any) bool {
	choice, ok := value.(map[string]any)
	if !ok {
		return false
	}
	changed := false
	if stringField(choice, "type") == "tool_search" {
		choice["type"] = "function"
		choice["name"] = toolSearchName
		changed = true
	}
	tools, okTools := choice["tools"].([]any)
	if !okTools {
		return changed
	}
	for _, rawTool := range tools {
		tool, okTool := rawTool.(map[string]any)
		if !okTool || stringField(tool, "type") != "tool_search" {
			continue
		}
		tool["type"] = "function"
		tool["name"] = toolSearchName
		changed = true
	}
	return changed
}

func rewriteToolSearchDeclaration(tool map[string]any) bool {
	if stringField(tool, "type") != "tool_search" {
		return false
	}
	tool["type"] = "function"
	tool["name"] = toolSearchName
	if _, exists := tool["parameters"]; !exists {
		tool["parameters"] = map[string]any{}
	}
	delete(tool, "execution")
	return true
}

func rewriteToolSearchCallForRequest(item map[string]any) bool {
	if isServerExecutedItem(item) || stringField(item, "call_id") == "" {
		return false
	}
	arguments, ok := jsonArgumentsString(item["arguments"])
	if !ok {
		return false
	}
	item["type"] = "function_call"
	item["name"] = toolSearchName
	item["arguments"] = arguments
	delete(item, "execution")
	return true
}

func rewriteToolSearchOutputForRequest(item map[string]any, catalog *toolCatalog) bool {
	if isServerExecutedItem(item) || stringField(item, "call_id") == "" {
		return false
	}
	tools := item["tools"]
	manifest := compactToolSearchManifest(tools)
	encodedTools, errMarshal := json.Marshal(map[string]any{"tools": manifest})
	if errMarshal != nil {
		return false
	}
	item["type"] = "function_call_output"
	item["output"] = string(encodedTools)
	delete(item, "execution")
	delete(item, "status")
	delete(item, "tools")
	return true
}

func compactToolSearchManifest(value any) []any {
	entries := make([]any, 0)
	var collect func(any, string)
	collect = func(current any, inheritedNamespace string) {
		switch typed := current.(type) {
		case map[string]any:
			switch strings.TrimSpace(stringField(typed, "type")) {
			case namespaceToolType:
				namespace := strings.TrimSpace(stringField(typed, "name"))
				if inheritedNamespace != "" && namespace != "" {
					namespace = joinNamespace(inheritedNamespace, namespace)
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
				flatName := rawQualifiedToolName(namespace, name)
				if flatName == "" {
					return
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

func jsonArgumentsString(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		trimmed := bytes.TrimSpace([]byte(typed))
		if len(trimmed) == 0 {
			return "{}", true
		}
		var decoded any
		if errUnmarshal := json.Unmarshal(trimmed, &decoded); errUnmarshal != nil {
			return "", false
		}
		return string(trimmed), true
	case nil:
		return "{}", true
	default:
		encoded, errMarshal := json.Marshal(typed)
		if errMarshal != nil {
			return "", false
		}
		return string(encoded), true
	}
}

func rewriteValue(value any) bool {
	return rewriteValueWithPolicy(value, nil, true)
}

func rewriteValueWithPolicy(value any, catalog *toolCatalog, bridge bool) bool {
	changed := false
	switch typed := value.(type) {
	case map[string]any:
		if bridge && isOrdinaryToolSearchCall(typed) {
			rewriteToolSearchItem(typed)
			return true
		}
		if bridge && restoreFunctionCallIdentity(typed, catalog) {
			changed = true
		}
		for _, child := range typed {
			if rewriteValueWithPolicy(child, catalog, bridge) {
				changed = true
			}
		}
	case []any:
		for _, child := range typed {
			if rewriteValueWithPolicy(child, catalog, bridge) {
				changed = true
			}
		}
	}
	return changed
}

func isOrdinaryToolSearchCall(item map[string]any) bool {
	if stringField(item, "type") != "function_call" {
		return false
	}
	name, ok := item["name"].(string)
	return ok && name == toolSearchName
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
		if errUnmarshal := json.Unmarshal(trimmed, &decoded); errUnmarshal != nil {
			return
		}
		item["arguments"] = decoded
	case nil:
		item["arguments"] = map[string]any{}
	}
}

func stringField(value map[string]any, key string) string {
	text, _ := value[key].(string)
	return text
}

// isServerExecutedItem reports whether an item is owned by an upstream that
// implements tool search itself. Items that omit execution are treated as
// client-executed, which is what Responses-compatible clients emit.
func isServerExecutedItem(item map[string]any) bool {
	return strings.EqualFold(strings.TrimSpace(stringField(item, "execution")), "server")
}

func validateToolSearchCall(item map[string]any) error {
	if item["type"] != "tool_search_call" {
		return fmt.Errorf("unexpected type %v", item["type"])
	}
	if item["execution"] != "client" {
		return fmt.Errorf("unexpected execution %v", item["execution"])
	}
	if _, exists := item["name"]; exists {
		return fmt.Errorf("tool_search name must be removed")
	}
	return nil
}
