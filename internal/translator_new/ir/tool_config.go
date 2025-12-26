// Package ir provides intermediate representation types for the translator system.
//
// @file Tool call normalization configuration
// @description Contains mappings for parameter synonyms, defaults, and sanitizers.
//
//	Fixes common model mistakes and incompatibilities with client expectations.
package ir

import (
	"encoding/json"
	"strings"
)

// ParameterSynonyms maps parameter names to their synonyms.
// When a model returns a parameter name that doesn't exist in the schema,
// we check if it's a known synonym and remap it to the expected name.
var ParameterSynonyms = map[string][]string{
	// File path variations
	"path":             {"target_file", "file_path", "filename", "target_directory"},
	"paths":            {"target_file", "file_path", "path", "filename"},
	"target_file":      {"file_path", "path", "filename"},
	"file_path":        {"target_file", "path", "filename"},
	"filename":         {"target_file", "file_path", "path"},
	"target_directory": {"path", "directory", "dir_path", "dir"},
	"directory":        {"target_directory", "path", "dir_path", "dir"},

	// Content variations
	"content":  {"contents", "code", "text", "body"},
	"contents": {"content", "code", "text", "body"},
	"code":     {"content", "contents", "text"},
	"text":     {"content", "contents", "code"},
	"body":     {"content", "contents"},

	// Boolean flags
	"background":    {"is_background"},
	"is_background": {"background"},

	// Command variations
	"command":       {"cmd", "shell_command", "script"},
	"cmd":           {"command", "shell_command"},
	"shell_command": {"command", "cmd"},

	// String manipulation
	"old_string":  {"old_text", "search", "find", "pattern"},
	"new_string":  {"new_text", "replace", "replacement"},
	"old_text":    {"old_string", "search", "find"},
	"new_text":    {"new_string", "replace", "replacement"},
	"search":      {"old_string", "old_text", "find", "pattern"},
	"replace":     {"new_string", "new_text", "replacement"},
	"replacement": {"new_string", "new_text", "replace"},
}

// ToolDefaults defines default values for commonly missing required parameters.
// When a model forgets to include a required parameter, we add the default value.
var ToolDefaults = map[string]map[string]interface{}{
	"run_terminal_cmd": {
		"is_background": false, // Default to foreground execution
	},
}

// StringTypeParameters lists parameter names that typically expect string values.
// Used to detect when model sends array but schema expects string.
var StringTypeParameters = map[string]bool{
	"target_file":      true,
	"file_path":        true,
	"path":             true,
	"target_directory": true,
	"command":          true,
	"content":          true,
	"contents":         true,
	"old_string":       true,
	"new_string":       true,
	"query":            true,
	"pattern":          true,
	"explanation":      true,
	"search_term":      true,
	"target_notebook":  true,
	"url":              true,
	"text":             true,
	"title":            true,
	"description":      true,
}

// ToolArgsSanitizers contains tool-specific argument sanitization functions.
// These fix known incompatibilities between model outputs and client expectations.
// Key: tool name, Value: sanitizer function that modifies args map in place and returns true if changed.
var ToolArgsSanitizers = map[string]func(args map[string]interface{}) bool{
	"grep": sanitizeGrepArgs,
}

// SanitizeGrepContextParams fixes grep -A/-B/-C conflict in any JSON args.
// Cursor error: "Cannot specify both 'context' (-C) and 'context_before' (-B) or 'context_after' (-A)"
// This is called for ALL tool calls to handle streaming where tool name may be unknown.
func SanitizeGrepContextParams(argsJSON string) string {
	if argsJSON == "" || argsJSON == "{}" {
		return argsJSON
	}
	// Quick check: if no -C key, nothing to fix
	if !hasGrepContextConflict(argsJSON) {
		return argsJSON
	}

	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return argsJSON
	}

	if !sanitizeGrepArgs(args) {
		return argsJSON
	}

	out, err := json.Marshal(args)
	if err != nil {
		return argsJSON
	}
	return string(out)
}

// hasGrepContextConflict quickly checks if JSON has potential -A/-B/-C conflict.
func hasGrepContextConflict(argsJSON string) bool {
	hasC := strings.Contains(argsJSON, `"-C"`)
	hasAorB := strings.Contains(argsJSON, `"-A"`) || strings.Contains(argsJSON, `"-B"`)
	return hasC && hasAorB
}

// sanitizeGrepArgs fixes grep tool arguments to avoid Cursor validation errors.
func sanitizeGrepArgs(args map[string]interface{}) bool {
	cVal, cExists := args["-C"]
	_, aExists := args["-A"]
	_, bExists := args["-B"]

	if !cExists || (!aExists && !bExists) {
		return false // No conflict
	}

	// Helper to check if value is effectively zero
	isZero := func(v interface{}) bool {
		switch val := v.(type) {
		case float64:
			return val == 0
		case int:
			return val == 0
		case int64:
			return val == 0
		}
		return false
	}

	// If -C is non-zero, it takes precedence - remove -A and -B
	if !isZero(cVal) {
		delete(args, "-A")
		delete(args, "-B")
		return true
	}

	// -C is zero - remove it, keep -A/-B
	delete(args, "-C")
	return true
}
