package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var errActiveToolBudget = errors.New("active tool budget exceeded")

func activeToolArraysBytes(root map[string]any) (int, error) {
	total := 0
	if tools, exists := root["tools"]; exists {
		encoded, errMarshal := json.Marshal(tools)
		if errMarshal != nil {
			return 0, errMarshal
		}
		total += len(encoded)
	}
	input, ok := root["input"].([]any)
	if !ok {
		return total, nil
	}
	for _, rawItem := range input {
		item, okItem := rawItem.(map[string]any)
		if !okItem || stringField(item, "type") != "additional_tools" {
			continue
		}
		tools, exists := item["tools"]
		if !exists {
			continue
		}
		encoded, errMarshal := json.Marshal(tools)
		if errMarshal != nil {
			return 0, errMarshal
		}
		total += len(encoded)
	}
	return total, nil
}

func enforceActiveToolBudget(body []byte, budget pluginBudget) error {
	value, ok := decodeJSONValue(body)
	if !ok {
		return nil
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	size, errSize := activeToolArraysBytes(root)
	if errSize != nil {
		return errSize
	}
	if size > budget.MaxToolBytes {
		return fmt.Errorf("%w: request declarations do not fit", errActiveToolBudget)
	}
	return nil
}

func removePromotedDiscoveries(root map[string]any, catalog *toolCatalog) {
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
		identity, knownAlias := catalog.idByAlias[name]
		_, discovered := catalog.discovered[identity]
		if okTool && isPromotedToolName(name) && knownAlias && discovered {
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

func isPromotedToolName(name string) bool {
	const prefix = "cts_"
	const hashLength = 48
	if len(name) != len(prefix)+hashLength || !strings.HasPrefix(name, prefix) {
		return false
	}
	for _, char := range name[len(prefix):] {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
