// Package misc provides miscellaneous utility functions and embedded data for the CLI Proxy API.
// This package contains general-purpose helpers and embedded resources that do not fit into
// more specific domain packages. It includes embedded instructional text for agent-related operations.
package misc

import _ "embed"

// ClaudeCodeInstructions holds the content of the claude_code_instructions.txt file.
//
//go:embed claude_code_instructions.txt
var ClaudeCodeInstructions string

// GPT5AnthropicAgentInstructions holds the GPT-5 behavior patch for Anthropic agent requests.
//
//go:embed gpt5_anthropic_agent_instructions.txt
var GPT5AnthropicAgentInstructions string
