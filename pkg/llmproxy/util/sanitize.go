// Package util provides utility functions for the CLI Proxy API server.
// It includes helper functions for JSON manipulation, proxy configuration,
// and other common operations used across the application.
package util

import (
	"bytes"
	"encoding/json"
)

// SanitizedToolNameMap returns a reverse lookup map from sanitized tool names
// to their original names when sanitization is required.
//
// The returned map uses the sanitized tool name as the key and the original
// tool name as the value. If no tool names need sanitization, nil is returned.
func SanitizedToolNameMap(raw []byte) map[string]string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}

	var payload struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || len(payload.Tools) == 0 {
		return nil
	}

	var mappings map[string]string
	for _, tool := range payload.Tools {
		original := tool.Name
		if original == "" {
			continue
		}

		sanitized := SanitizeFunctionName(original)
		if sanitized == original {
			continue
		}
		if mappings == nil {
			mappings = make(map[string]string)
		}
		if _, exists := mappings[sanitized]; exists {
			continue
		}
		mappings[sanitized] = original
	}

	if len(mappings) == 0 {
		return nil
	}
	return mappings
}

// RestoreSanitizedToolName maps a sanitized tool name back to its original
// form when the mapping is known. Unknown names pass through unchanged.
func RestoreSanitizedToolName(mapping map[string]string, name string) string {
	if name == "" || len(mapping) == 0 {
		return name
	}
	if original, ok := mapping[name]; ok {
		return original
	}
	return name
}
