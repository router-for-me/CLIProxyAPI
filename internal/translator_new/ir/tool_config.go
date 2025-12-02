// Package ir provides intermediate representation types for the translator system.
//
// This file contains configuration for tool call normalization.
// These mappings help fix common model mistakes where parameter names
// don't match the expected schema.
//
// TODO: In the future, this could be loaded from external config (YAML/JSON)
// to allow customization without code changes.
package ir

// ParameterSynonyms maps parameter names to their synonyms.
// When a model returns a parameter name that doesn't exist in the schema,
// we check if it's a known synonym and remap it to the expected name.
//
// Key: parameter name that model might send
// Value: list of possible expected names in schema (checked in order)
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
//
// Key: tool name
// Value: map of parameter name -> default value
var ToolDefaults = map[string]map[string]interface{}{
	"run_terminal_cmd": {
		"is_background": false, // Default to foreground execution
	},
}

// StringTypeParameters lists parameter names that typically expect string values.
// Used to detect when model sends array but schema expects string.
// This is a fallback when schema type information is not available.
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
