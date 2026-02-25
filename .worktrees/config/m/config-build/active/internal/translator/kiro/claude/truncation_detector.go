// Package claude provides truncation detection for Kiro tool call responses.
// When Kiro API reaches its output token limit, tool call JSON may be truncated,
// resulting in incomplete or unparseable input parameters.
package claude

import (
	"encoding/json"
	"strings"

	log "github.com/sirupsen/logrus"
)

// TruncationInfo contains details about detected truncation in a tool use event.
type TruncationInfo struct {
	IsTruncated    bool              // Whether truncation was detected
	TruncationType string            // Type of truncation detected
	ToolName       string            // Name of the truncated tool
	ToolUseID      string            // ID of the truncated tool use
	RawInput       string            // The raw (possibly truncated) input string
	ParsedFields   map[string]string // Fields that were successfully parsed before truncation
	ErrorMessage   string            // Human-readable error message
}

// TruncationType constants for different truncation scenarios
const (
	TruncationTypeNone             = ""                  // No truncation detected
	TruncationTypeEmptyInput       = "empty_input"       // No input data received at all
	TruncationTypeInvalidJSON      = "invalid_json"      // JSON is syntactically invalid (truncated mid-value)
	TruncationTypeMissingFields    = "missing_fields"    // JSON parsed but critical fields are missing
	TruncationTypeIncompleteString = "incomplete_string" // String value was cut off mid-content
)

// KnownWriteTools lists tool names that typically write content and have a "content" field.
// These tools are checked for content field truncation specifically.
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

// RequiredFieldsByTool maps tool names to their required fields.
// If any of these fields are missing, the tool input is considered truncated.
var RequiredFieldsByTool = map[string][]string{
	"Write":              {"file_path", "content"},
	"write_to_file":      {"path", "content"},
	"fsWrite":            {"path", "content"},
	"create_file":        {"path", "content"},
	"edit_file":          {"path"},
	"apply_diff":         {"path", "diff"},
	"str_replace_editor": {"path", "old_str", "new_str"},
	"Bash":               {"command"},
	"execute":            {"command"},
	"run_command":        {"command"},
}

// DetectTruncation checks if the tool use input appears to be truncated.
// It returns detailed information about the truncation status and type.
func DetectTruncation(toolName, toolUseID, rawInput string, parsedInput map[string]interface{}) TruncationInfo {
	info := TruncationInfo{
		ToolName:     toolName,
		ToolUseID:    toolUseID,
		RawInput:     rawInput,
		ParsedFields: make(map[string]string),
	}

	// Scenario 1: Empty input buffer - no data received at all
	if strings.TrimSpace(rawInput) == "" {
		info.IsTruncated = true
		info.TruncationType = TruncationTypeEmptyInput
		info.ErrorMessage = "Tool input was completely empty - API response may have been truncated before tool parameters were transmitted"
		log.Warnf("kiro: truncation detected [%s] for tool %s (ID: %s): empty input buffer",
			info.TruncationType, toolName, toolUseID)
		return info
	}

	// Scenario 2: JSON parse failure - syntactically invalid JSON
	if parsedInput == nil || len(parsedInput) == 0 {
		// Check if the raw input looks like truncated JSON
		if looksLikeTruncatedJSON(rawInput) {
			info.IsTruncated = true
			info.TruncationType = TruncationTypeInvalidJSON
			info.ParsedFields = extractPartialFields(rawInput)
			info.ErrorMessage = buildTruncationErrorMessage(toolName, info.TruncationType, info.ParsedFields, rawInput)
			log.Warnf("kiro: truncation detected [%s] for tool %s (ID: %s): JSON parse failed, raw length=%d bytes",
				info.TruncationType, toolName, toolUseID, len(rawInput))
			return info
		}
	}

	// Scenario 3: JSON parsed but critical fields are missing
	if parsedInput != nil {
		requiredFields, hasRequirements := RequiredFieldsByTool[toolName]
		if hasRequirements {
			missingFields := findMissingRequiredFields(parsedInput, requiredFields)
			if len(missingFields) > 0 {
				info.IsTruncated = true
				info.TruncationType = TruncationTypeMissingFields
				info.ParsedFields = extractParsedFieldNames(parsedInput)
				info.ErrorMessage = buildMissingFieldsErrorMessage(toolName, missingFields, info.ParsedFields)
				log.Warnf("kiro: truncation detected [%s] for tool %s (ID: %s): missing required fields: %v",
					info.TruncationType, toolName, toolUseID, missingFields)
				return info
			}
		}

		// Scenario 4: Check for incomplete string values (very short content for write tools)
		if isWriteTool(toolName) {
			if contentTruncation := detectContentTruncation(parsedInput, rawInput); contentTruncation != "" {
				info.IsTruncated = true
				info.TruncationType = TruncationTypeIncompleteString
				info.ParsedFields = extractParsedFieldNames(parsedInput)
				info.ErrorMessage = contentTruncation
				log.Warnf("kiro: truncation detected [%s] for tool %s (ID: %s): %s",
					info.TruncationType, toolName, toolUseID, contentTruncation)
				return info
			}
		}
	}

	// No truncation detected
	info.IsTruncated = false
	info.TruncationType = TruncationTypeNone
	return info
}

// looksLikeTruncatedJSON checks if the raw string appears to be truncated JSON.
func looksLikeTruncatedJSON(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false
	}

	// Must start with { to be considered JSON
	if !strings.HasPrefix(trimmed, "{") {
		return false
	}

	// Count brackets to detect imbalance
	openBraces := strings.Count(trimmed, "{")
	closeBraces := strings.Count(trimmed, "}")
	openBrackets := strings.Count(trimmed, "[")
	closeBrackets := strings.Count(trimmed, "]")

	// Bracket imbalance suggests truncation
	if openBraces > closeBraces || openBrackets > closeBrackets {
		return true
	}

	// Check for obvious truncation patterns
	// - Ends with a quote but no closing brace
	// - Ends with a colon (mid key-value)
	// - Ends with a comma (mid object/array)
	lastChar := trimmed[len(trimmed)-1]
	if lastChar != '}' && lastChar != ']' {
		// Check if it's not a complete simple value
		if lastChar == '"' || lastChar == ':' || lastChar == ',' {
			return true
		}
	}

	// Check for unclosed strings (odd number of unescaped quotes)
	inString := false
	escaped := false
	for i := 0; i < len(trimmed); i++ {
		c := trimmed[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' {
			escaped = true
			continue
		}
		if c == '"' {
			inString = !inString
		}
	}
	if inString {
		return true // Unclosed string
	}

	return false
}

// extractPartialFields attempts to extract any field names from malformed JSON.
// This helps provide context about what was received before truncation.
func extractPartialFields(raw string) map[string]string {
	fields := make(map[string]string)

	// Simple pattern matching for "key": "value" or "key": value patterns
	// This works even with truncated JSON
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "{") {
		return fields
	}

	// Remove opening brace
	content := strings.TrimPrefix(trimmed, "{")

	// Split by comma (rough parsing)
	parts := strings.Split(content, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if colonIdx := strings.Index(part, ":"); colonIdx > 0 {
			key := strings.TrimSpace(part[:colonIdx])
			key = strings.Trim(key, `"`)
			value := strings.TrimSpace(part[colonIdx+1:])

			// Truncate long values for display
			if len(value) > 50 {
				value = value[:50] + "..."
			}
			fields[key] = value
		}
	}

	return fields
}

// extractParsedFieldNames returns the field names from a successfully parsed map.
func extractParsedFieldNames(parsed map[string]interface{}) map[string]string {
	fields := make(map[string]string)
	for key, val := range parsed {
		switch v := val.(type) {
		case string:
			if len(v) > 50 {
				fields[key] = v[:50] + "..."
			} else {
				fields[key] = v
			}
		case nil:
			fields[key] = "<null>"
		default:
			// For complex types, just indicate presence
			fields[key] = "<present>"
		}
	}
	return fields
}

// findMissingRequiredFields checks which required fields are missing from the parsed input.
func findMissingRequiredFields(parsed map[string]interface{}, required []string) []string {
	var missing []string
	for _, field := range required {
		if _, exists := parsed[field]; !exists {
			missing = append(missing, field)
		}
	}
	return missing
}

// isWriteTool checks if the tool is a known write/file operation tool.
func isWriteTool(toolName string) bool {
	return KnownWriteTools[toolName]
}

// detectContentTruncation checks if the content field appears truncated for write tools.
func detectContentTruncation(parsed map[string]interface{}, rawInput string) string {
	// Check for content field
	content, hasContent := parsed["content"]
	if !hasContent {
		return ""
	}

	contentStr, isString := content.(string)
	if !isString {
		return ""
	}

	// Heuristic: if raw input is very large but content is suspiciously short,
	// it might indicate truncation during JSON repair
	if len(rawInput) > 1000 && len(contentStr) < 100 {
		return "content field appears suspiciously short compared to raw input size"
	}

	// Check for code blocks that appear to be cut off
	if strings.Contains(contentStr, "```") {
		openFences := strings.Count(contentStr, "```")
		if openFences%2 != 0 {
			return "content contains unclosed code fence (```) suggesting truncation"
		}
	}

	return ""
}

// buildTruncationErrorMessage creates a human-readable error message for truncation.
func buildTruncationErrorMessage(toolName, truncationType string, parsedFields map[string]string, rawInput string) string {
	var sb strings.Builder
	sb.WriteString("Tool input was truncated by the API. ")

	switch truncationType {
	case TruncationTypeEmptyInput:
		sb.WriteString("No input data was received.")
	case TruncationTypeInvalidJSON:
		sb.WriteString("JSON was cut off mid-transmission. ")
		if len(parsedFields) > 0 {
			sb.WriteString("Partial fields received: ")
			first := true
			for k := range parsedFields {
				if !first {
					sb.WriteString(", ")
				}
				sb.WriteString(k)
				first = false
			}
		}
	case TruncationTypeMissingFields:
		sb.WriteString("Required fields are missing from the input.")
	case TruncationTypeIncompleteString:
		sb.WriteString("Content appears to be shortened or incomplete.")
	}

	sb.WriteString(" Received ")
	sb.WriteString(string(rune(len(rawInput))))
	sb.WriteString(" bytes. Please retry with smaller content chunks.")

	return sb.String()
}

// buildMissingFieldsErrorMessage creates an error message for missing required fields.
func buildMissingFieldsErrorMessage(toolName string, missingFields []string, parsedFields map[string]string) string {
	var sb strings.Builder
	sb.WriteString("Tool '")
	sb.WriteString(toolName)
	sb.WriteString("' is missing required fields: ")
	sb.WriteString(strings.Join(missingFields, ", "))
	sb.WriteString(". Fields received: ")

	first := true
	for k := range parsedFields {
		if !first {
			sb.WriteString(", ")
		}
		sb.WriteString(k)
		first = false
	}

	sb.WriteString(". This usually indicates the API response was truncated.")
	return sb.String()
}

// IsTruncated is a convenience function to check if a tool use appears truncated.
func IsTruncated(toolName, rawInput string, parsedInput map[string]interface{}) bool {
	info := DetectTruncation(toolName, "", rawInput, parsedInput)
	return info.IsTruncated
}

// GetTruncationSummary returns a short summary string for logging.
func GetTruncationSummary(info TruncationInfo) string {
	if !info.IsTruncated {
		return ""
	}

	result, _ := json.Marshal(map[string]interface{}{
		"tool":           info.ToolName,
		"type":           info.TruncationType,
		"parsed_fields":  info.ParsedFields,
		"raw_input_size": len(info.RawInput),
	})
	return string(result)
}

// SoftFailureMessage contains the message structure for a truncation soft failure.
// This is returned to Claude as a tool_result to guide retry behavior.
type SoftFailureMessage struct {
	Status      string   // "incomplete" - not an error, just incomplete
	Reason      string   // Why the tool call was incomplete
	Guidance    []string // Step-by-step retry instructions
	Context     string   // Any context about what was received
	MaxLineHint int      // Suggested maximum lines per chunk
}

// BuildSoftFailureMessage creates a structured message for Claude when truncation is detected.
// This follows the "soft failure" pattern:
// - For Claude: Clear explanation of what happened and how to fix
// - For User: Hidden or minimized (appears as normal processing)
//
// Key principle: "Conclusion First"
// 1. First state what happened (incomplete)
// 2. Then explain how to fix (chunked approach)
// 3. Provide specific guidance (line limits)
func BuildSoftFailureMessage(info TruncationInfo) SoftFailureMessage {
	msg := SoftFailureMessage{
		Status:      "incomplete",
		MaxLineHint: 300, // Conservative default
	}

	// Build reason based on truncation type
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

	// Build context from parsed fields
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

	// Build retry guidance - CRITICAL: Conclusion first approach
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

// formatInt converts an integer to string (helper to avoid strconv import)
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
// This is what Claude will see when a tool call is truncated.
// Returns a string that should be used as the tool_result content.
func BuildSoftFailureToolResult(info TruncationInfo) string {
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
// Instead of returning the truncated tool_use, we return a tool with a special
// error result that guides Claude to retry with smaller chunks.
//
// This is the key mechanism for "soft failure":
// - stop_reason remains "tool_use" so Claude continues
// - The tool_result content explains the issue and how to fix it
// - Claude will read this and adjust its approach
func CreateTruncationToolResult(info TruncationInfo) KiroToolUse {
	// We create a pseudo tool_use that represents the failed attempt
	// The executor will convert this to a tool_result with the guidance message
	return KiroToolUse{
		ToolUseID:      info.ToolUseID,
		Name:           info.ToolName,
		Input:          nil, // No input since it was truncated
		IsTruncated:    true,
		TruncationInfo: &info,
	}
}
