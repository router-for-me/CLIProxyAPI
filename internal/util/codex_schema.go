// Package util provides utility functions for the CLI Proxy API server.
package util

import (
	"strconv"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// FixCodexToolSchemas fixes tool schemas in a Codex API request body.
// It adds missing "items" to array-type schemas which OpenAI's strict validation requires.
func FixCodexToolSchemas(body []byte) []byte {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body
	}

	for i, tool := range tools.Array() {
		if tool.Get("type").String() != "function" {
			continue
		}

		var params gjson.Result
		var setPath string

		// Support both Chat Completions (function.parameters) and Responses API (parameters)
		if tool.Get("function.parameters").Exists() {
			params = tool.Get("function.parameters")
			setPath = "tools." + strconv.Itoa(i) + ".function.parameters"
		} else if tool.Get("parameters").Exists() {
			params = tool.Get("parameters")
			setPath = "tools." + strconv.Itoa(i) + ".parameters"
		} else {
			continue
		}

		fixed := addMissingArrayItems(params.Raw)
		if fixed != params.Raw {
			body, _ = sjson.SetRawBytes(body, setPath, []byte(fixed))
		}
	}
	return body
}

// addMissingArrayItems adds a default "items" schema to arrays that are missing it.
func addMissingArrayItems(jsonStr string) string {
	paths := findArrayTypePaths(gjson.Parse(jsonStr), "")
	for _, p := range paths {
		itemsPath := p + ".items"
		if p == "" {
			itemsPath = "items"
		}
		items := gjson.Get(jsonStr, itemsPath)
		// Add items if missing or null
		if !items.Exists() || items.Type == gjson.Null {
			jsonStr, _ = sjson.SetRaw(jsonStr, itemsPath, `{}`)
		}
	}
	return jsonStr
}

// isArrayType checks if a node's type indicates an array (string or array containing "array").
func isArrayType(node gjson.Result) bool {
	typeVal := node.Get("type")
	if typeVal.IsArray() {
		for _, t := range typeVal.Array() {
			if t.String() == "array" {
				return true
			}
		}
		return false
	}
	return typeVal.String() == "array"
}

// findArrayTypePaths recursively finds all paths where type="array".
func findArrayTypePaths(node gjson.Result, path string) []string {
	var paths []string

	if node.IsObject() {
		if isArrayType(node) {
			paths = append(paths, path)
		}
		node.ForEach(func(key, value gjson.Result) bool {
			newPath := key.String()
			if path != "" {
				newPath = path + "." + key.String()
			}
			paths = append(paths, findArrayTypePaths(value, newPath)...)
			return true
		})
	} else if node.IsArray() {
		for i, elem := range node.Array() {
			newPath := strconv.Itoa(i)
			if path != "" {
				newPath = path + "." + strconv.Itoa(i)
			}
			paths = append(paths, findArrayTypePaths(elem, newPath)...)
		}
	}

	return paths
}
