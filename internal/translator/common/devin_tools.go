package common

import (
	"regexp"
	"strings"
)

// IsDevinCodexAppAutomationUpdate reports whether a tool declaration corresponds
// to the automation_update method within the mcp__codex_app namespace.
func IsDevinCodexAppAutomationUpdate(namespace, toolName string) bool {
	cleanNamespace := strings.TrimSpace(namespace)
	cleanTool := strings.TrimSpace(toolName)
	if strings.EqualFold(cleanNamespace, "mcp__codex_app") && strings.EqualFold(cleanTool, "automation_update") {
		return true
	}
	if strings.EqualFold(cleanTool, "mcp__codex_app__automation_update") {
		return true
	}
	return false
}

const (
	execCommandTargetPhrase     = "returning output or a session ID for ongoing interaction"
	execCommandObfuscatedPhrase = "returning output or an session ID for ongoing interaction"
	writeStdinTargetPhrase      = "Writes characters to an existing unified exec session and returns recent output."
	writeStdinObfuscatedPhrase  = "Writes characters to a existing unified exec session and returns recent output."
)

var (
	execCommandRegex = regexp.MustCompile(`(?i)returning output or a session ID for ongoing interaction`)
	writeStdinRegex  = regexp.MustCompile(`(?i)Writes characters to an existing unified exec session and returns recent output(\.?)`)
)

// ObfuscateExecCommandDescription replaces "returning output or a session ID for ongoing interaction"
// with "returning output or an session ID for ongoing interaction".
func ObfuscateExecCommandDescription(desc string) string {
	if strings.Contains(desc, execCommandObfuscatedPhrase) {
		return desc
	}
	if strings.Contains(desc, execCommandTargetPhrase) {
		return strings.ReplaceAll(desc, execCommandTargetPhrase, execCommandObfuscatedPhrase)
	}
	return execCommandRegex.ReplaceAllString(desc, execCommandObfuscatedPhrase)
}

// ObfuscateWriteStdinDescription replaces "Writes characters to an existing unified exec session and returns recent output."
// with "Writes characters to a existing unified exec session and returns recent output.".
func ObfuscateWriteStdinDescription(desc string) string {
	if strings.Contains(desc, writeStdinObfuscatedPhrase) {
		return desc
	}
	if strings.Contains(desc, writeStdinTargetPhrase) {
		return strings.ReplaceAll(desc, writeStdinTargetPhrase, writeStdinObfuscatedPhrase)
	}
	return writeStdinRegex.ReplaceAllString(desc, "Writes characters to a existing unified exec session and returns recent output$1")
}

// SanitizeDevinToolDescription applies description obfuscation for Devin function tools.
func SanitizeDevinToolDescription(toolName, desc string) string {
	if desc == "" {
		return desc
	}
	cleanTool := strings.ToLower(strings.TrimSpace(toolName))
	if cleanTool == "exec_command" || strings.HasSuffix(cleanTool, "__exec_command") {
		desc = ObfuscateExecCommandDescription(desc)
	}
	if cleanTool == "write_stdin" || strings.HasSuffix(cleanTool, "__write_stdin") {
		desc = ObfuscateWriteStdinDescription(desc)
	}
	return desc
}
