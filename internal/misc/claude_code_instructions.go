// Package misc provides miscellaneous utility functions and embedded data for the CLI Proxy API.
// This package contains general-purpose helpers and embedded resources that do not fit into
// more specific domain packages. It includes embedded instructional text for Claude Code-related operations.
package misc

import _ "embed"

// ClaudeCodeInstructions holds the content of the claude_code_instructions.txt file,
// which is embedded into the application binary at compile time. This variable
// contains specific instructions for Claude Code model interactions and code generation guidance.
//
//go:embed claude_code_instructions.txt
var ClaudeCodeInstructions string
