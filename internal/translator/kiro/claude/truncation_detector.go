// Package claude provides truncation detection for Kiro tool call responses.
// When Kiro API reaches its output token limit, tool call JSON may be truncated,
// resulting in incomplete or unparseable input parameters.
package claude

import (
	"encoding/json"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
)

// TruncationInfo re-exports the common TruncationInfo for backward compatibility.
type TruncationInfo = common.TruncationInfo

// TruncationType constants re-exported for backward compatibility.
const (
	TruncationTypeNone             = common.TruncationTypeNone
	TruncationTypeEmptyInput       = common.TruncationTypeEmptyInput
	TruncationTypeInvalidJSON      = common.TruncationTypeInvalidJSON
	TruncationTypeMissingFields    = common.TruncationTypeMissingFields
	TruncationTypeIncompleteString = common.TruncationTypeIncompleteString
)

// KnownWriteTools lists tool names that typically write content and have a "content" field.
var KnownWriteTools = map[string]bool{
	"Write":              true,
	"write_to_file":      true,
	"fsWrite":            true,
	"create_file":        true,
	"edit_file":          true,
	"apply_diff":         true,
	"str_replace_editor": true,
	"insert":             true,
}

// KnownCommandTools lists tool names that execute commands.
var KnownCommandTools = map[string]bool{
	"Bash":           true,
	"execute":        true,
	"run_command":    true,
	"shell":          true,
	"terminal":       true,
	"execute_python": true,
}

// RequiredFieldsByTool maps tool names to their required field groups.
// Each outer element is a required group; each inner slice lists alternative field names (OR logic).
var RequiredFieldsByTool = map[string][][]string{
	"Write":              {{"file_path"}, {"content"}},
	"write_to_file":      {{"path"}, {"content"}},
	"fsWrite":            {{"path"}, {"content"}},
	"create_file":        {{"path"}, {"content"}},
	"edit_file":          {{"path"}},
	"apply_diff":         {{"path"}, {"diff"}},
	"str_replace_editor": {{"path"}, {"old_str"}, {"new_str"}},
	"Bash":               {{"cmd", "command"}},
	"execute":            {{"command"}},
	"run_command":        {{"command"}},
}

// DetectTruncation checks if the tool use input appears to be truncated.
// Delegates to common.DetectTruncation with Kiro-specific required fields and write tool names.
func DetectTruncation(toolName, toolUseID, rawInput string, parsedInput map[string]interface{}) common.TruncationInfo {
	return common.DetectTruncation(toolName, toolUseID, rawInput, parsedInput, RequiredFieldsByTool, KnownWriteTools)
}

// IsTruncated is a convenience function to check if a tool use appears truncated.
func IsTruncated(toolName, rawInput string, parsedInput map[string]interface{}) bool {
	info := DetectTruncation(toolName, "", rawInput, parsedInput)
	return info.IsTruncated
}

// GetTruncationSummary returns a short summary string for logging.
func GetTruncationSummary(info common.TruncationInfo) string {
	return common.GetTruncationSummary(info)
}

// SoftFailureMessage contains the message structure for a truncation soft failure.
type SoftFailureMessage struct {
	Status      string   // "incomplete" - not an error, just incomplete
	Reason      string   // Why the tool call was incomplete
	Guidance    []string // Step-by-step retry instructions
	Context     string   // Any context about what was received
	MaxLineHint int      // Suggested maximum lines per chunk
}

// BuildSoftFailureMessage creates a structured message for Claude when truncation is detected.
func BuildSoftFailureMessage(info common.TruncationInfo) SoftFailureMessage {
	msg := SoftFailureMessage{
		Status:      "incomplete",
		MaxLineHint: 300,
	}

	switch info.TruncationType {
	case TruncationTypeEmptyInput:
		msg.Reason = "Your tool call was too large and the input was completely lost during transmission."
		msg.MaxLineHint = 200
	case TruncationTypeInvalidJSON:
		msg.Reason = "Your tool call was truncated mid-transmission, resulting in incomplete JSON."
		msg.MaxLineHint = 250
	case TruncationTypeMissingFields:
		msg.Reason = "Your tool call was partially received but critical fields were cut off."
		msg.MaxLineHint = 300
	case TruncationTypeIncompleteString:
		msg.Reason = "Your tool call content was truncated - the full content did not arrive."
		msg.MaxLineHint = 350
	default:
		msg.Reason = "Your tool call was truncated by the API due to output size limits."
	}

	if len(info.ParsedFields) > 0 {
		var parts []string
		for k, v := range info.ParsedFields {
			if len(v) > 30 {
				v = v[:30] + "..."
			}
			parts = append(parts, k+"="+v)
		}
		msg.Context = "Received partial data: " + strings.Join(parts, ", ")
	}

	msg.Guidance = []string{
		"CONCLUSION: Split your output into smaller chunks and retry.",
		"",
		"REQUIRED APPROACH:",
		"1. For file writes: Write in chunks of ~" + formatInt(msg.MaxLineHint) + " lines maximum",
		"2. For new files: First create with initial chunk, then append remaining sections",
		"3. For edits: Make surgical, targeted changes - avoid rewriting entire files",
		"",
		"EXAMPLE (writing a 600-line file):",
		"  - Step 1: Write lines 1-300 (create file)",
		"  - Step 2: Append lines 301-600 (extend file)",
		"",
		"DO NOT attempt to write the full content again in a single call.",
		"The API has a hard output limit that cannot be bypassed.",
	}

	return msg
}

// formatInt converts an integer to string.
func formatInt(n int) string {
	if n == 0 {
		return "0"
	}
	result := ""
	for n > 0 {
		result = string(rune('0'+n%10)) + result
		n /= 10
	}
	return result
}

// BuildSoftFailureToolResult creates a tool_result content for Claude.
func BuildSoftFailureToolResult(info common.TruncationInfo) string {
	msg := BuildSoftFailureMessage(info)

	var sb strings.Builder
	sb.WriteString("TOOL_CALL_INCOMPLETE\n")
	sb.WriteString("status: ")
	sb.WriteString(msg.Status)
	sb.WriteString("\n")
	sb.WriteString("reason: ")
	sb.WriteString(msg.Reason)
	sb.WriteString("\n")

	if msg.Context != "" {
		sb.WriteString("context: ")
		sb.WriteString(msg.Context)
		sb.WriteString("\n")
	}

	sb.WriteString("\n")
	for _, line := range msg.Guidance {
		if line != "" {
			sb.WriteString(line)
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

// CreateTruncationToolResult creates a KiroToolUse that represents a soft failure.
func CreateTruncationToolResult(info common.TruncationInfo) KiroToolUse {
	return KiroToolUse{
		ToolUseID:      info.ToolUseID,
		Name:           info.ToolName,
		Input:          nil,
		IsTruncated:    true,
		TruncationInfo: &info,
	}
}

// BuildSoftFailureMessageJSON marshals the soft failure message to JSON for API responses.
func BuildSoftFailureMessageJSON(info common.TruncationInfo) []byte {
	msg := BuildSoftFailureMessage(info)
	result, _ := json.Marshal(msg)
	return result
}
