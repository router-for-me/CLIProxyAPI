// Package common provides shared validation and sanitization utilities
// for tool calls across all translator implementations.
package common

import (
	"encoding/json"
	"strings"
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

// TruncationType constants for different truncation scenarios.
const (
	TruncationTypeNone             = ""                  // No truncation detected
	TruncationTypeEmptyInput       = "empty_input"       // No input data received at all
	TruncationTypeInvalidJSON      = "invalid_json"      // JSON is syntactically invalid (truncated mid-value)
	TruncationTypeMissingFields    = "missing_fields"    // JSON parsed but critical fields are missing
	TruncationTypeIncompleteString = "incomplete_string" // String value was cut off mid-content
)

// DetectTruncation checks if the tool use input appears to be truncated.
// It returns detailed information about the truncation status and type.
//
// Parameters:
//   - toolName: name of the tool being called
//   - toolUseID: ID of the tool use event
//   - rawInput: the raw JSON string received for the tool input
//   - parsedInput: the parsed input map (nil if JSON parsing failed)
//   - requiredFields: maps tool names to required field groups (each group is
//     a slice of alternative field names; the group is satisfied when ANY
//     alternative exists). Pass nil to skip required-field checks.
//   - writeToolNames: set of tool names considered "write tools" for content
//     truncation checks. Pass nil to skip content truncation checks.
func DetectTruncation(toolName, toolUseID, rawInput string, parsedInput map[string]interface{}, requiredFields map[string][][]string, writeToolNames map[string]bool) TruncationInfo {
	info := TruncationInfo{
		ToolName:     toolName,
		ToolUseID:    toolUseID,
		RawInput:     rawInput,
		ParsedFields: make(map[string]string),
	}

	// Scenario 1: Empty input buffer - only flag as truncation if tool has required fields
	if strings.TrimSpace(rawInput) == "" {
		if requiredFields != nil {
			if _, hasRequirements := requiredFields[toolName]; hasRequirements {
				info.IsTruncated = true
				info.TruncationType = TruncationTypeEmptyInput
				info.ErrorMessage = "Tool input was completely empty - API response may have been truncated before tool parameters were transmitted"
				return info
			}
		}
		return info
	}

	// Scenario 2: JSON parse failure - syntactically invalid JSON
	if parsedInput == nil || len(parsedInput) == 0 {
		if LooksLikeTruncatedJSON(rawInput) {
			info.IsTruncated = true
			info.TruncationType = TruncationTypeInvalidJSON
			info.ParsedFields = ExtractPartialFields(rawInput)
			info.ErrorMessage = "Tool input was truncated mid-transmission: invalid JSON (" + truncateString(rawInput, 80) + ")"
			return info
		}
	}

	// Scenario 3: JSON parsed but critical fields are missing
	if parsedInput != nil && requiredFields != nil {
		requiredGroups, hasRequirements := requiredFields[toolName]
		if hasRequirements {
			missingFields := FindMissingRequiredFields(parsedInput, requiredGroups)
			if len(missingFields) > 0 {
				info.IsTruncated = true
				info.TruncationType = TruncationTypeMissingFields
				info.ParsedFields = ExtractParsedFieldNames(parsedInput)
				info.ErrorMessage = "Tool '" + toolName + "' is missing required fields: " + strings.Join(missingFields, ", ")
				return info
			}
		}
	}

	// Scenario 4: Check for incomplete string values (very short content for write tools)
	if parsedInput != nil && writeToolNames != nil {
		if writeToolNames[toolName] {
			if contentTruncation := DetectContentTruncation(parsedInput, rawInput); contentTruncation != "" {
				info.IsTruncated = true
				info.TruncationType = TruncationTypeIncompleteString
				info.ParsedFields = ExtractParsedFieldNames(parsedInput)
				info.ErrorMessage = contentTruncation
				return info
			}
		}
	}

	// No truncation detected
	info.IsTruncated = false
	info.TruncationType = TruncationTypeNone
	return info
}

// LooksLikeTruncatedJSON checks if the raw string appears to be truncated JSON.
func LooksLikeTruncatedJSON(raw string) bool {
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
	lastChar := trimmed[len(trimmed)-1]
	if lastChar != '}' && lastChar != ']' {
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
		return true
	}

	return false
}

// ExtractPartialFields attempts to extract field names from malformed JSON.
func ExtractPartialFields(raw string) map[string]string {
	fields := make(map[string]string)

	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "{") {
		return fields
	}

	content := strings.TrimPrefix(trimmed, "{")
	parts := strings.Split(content, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if colonIdx := strings.Index(part, ":"); colonIdx > 0 {
			key := strings.TrimSpace(part[:colonIdx])
			key = strings.Trim(key, `"`)
			value := strings.TrimSpace(part[colonIdx+1:])
			if len(value) > 50 {
				value = value[:50] + "..."
			}
			fields[key] = value
		}
	}

	return fields
}

// ExtractParsedFieldNames returns the field names from a successfully parsed map.
func ExtractParsedFieldNames(parsed map[string]interface{}) map[string]string {
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
			fields[key] = "<present>"
		}
	}
	return fields
}

// FindMissingRequiredFields checks which required field groups are unsatisfied.
// Each group is a slice of alternative field names; the group is satisfied when ANY alternative exists.
// Returns the list of unsatisfied groups (represented by their alternatives joined with "/").
func FindMissingRequiredFields(parsed map[string]interface{}, requiredGroups [][]string) []string {
	var missing []string
	for _, group := range requiredGroups {
		satisfied := false
		for _, field := range group {
			if _, exists := parsed[field]; exists {
				satisfied = true
				break
			}
		}
		if !satisfied {
			missing = append(missing, strings.Join(group, "/"))
		}
	}
	return missing
}

// DetectContentTruncation checks if the content field appears truncated for write tools.
func DetectContentTruncation(parsed map[string]interface{}, rawInput string) string {
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

// IsTruncated is a convenience function to check if a tool use appears truncated.
func IsTruncated(toolName, rawInput string, parsedInput map[string]interface{}, requiredFields map[string][][]string, writeToolNames map[string]bool) bool {
	info := DetectTruncation(toolName, "", rawInput, parsedInput, requiredFields, writeToolNames)
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
